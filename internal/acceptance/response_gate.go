package acceptance

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type HeldIncrease struct {
	ReceivedAt        time.Time `json:"received_at"`
	AcceptedAt        time.Time `json:"accepted_at"`
	GroupID           string    `json:"scaling_group_id"`
	ClientToken       string    `json:"client_token"`
	RequestID         string    `json:"request_id"`
	HTTPStatus        int       `json:"upstream_http_status"`
	ClientClosedAt    time.Time `json:"client_closed_at"`
	Forwarded         int       `json:"scale_to_one_forwarded"`
	Held              bool      `json:"response_held"`
	ResponseDelivered bool      `json:"response_delivered"`
}

// ResponseGate forwards signed requests unchanged to verified official HTTPS.
// Its only fault is withholding one successful expansion's entire HTTP response
// on a hijacked downstream connection. Credentials and raw requests stay in memory.
type ResponseGate struct {
	mu           sync.Mutex
	group        string
	transport    http.RoundTripper
	server       *http.Server
	address      string
	ca           []byte
	held         net.Conn
	receipt      HeldIncrease
	err          error
	accepted     chan struct{}
	disconnected chan struct{}
	dropped      chan struct{}
	dropOnce     sync.Once
}

func NewResponseGate(group string) (*ResponseGate, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 15 * time.Second
	return newResponseGate(group, transport)
}

func newResponseGate(group string, transport http.RoundTripper) (*ResponseGate, error) {
	if group == "" {
		return nil, errors.New("response gate group is required")
	}
	caKeyPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	serial := func() *big.Int { n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)); return n }
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: serial(), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caKeyPub, caKey)
	if err != nil {
		return nil, err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	leaf := &x509.Certificate{SerialNumber: serial(), DNSNames: []string{"ess.aliyuncs.com"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, caKey)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g := &ResponseGate{group: group, transport: transport, address: listener.Addr().String(), ca: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), accepted: make(chan struct{}), disconnected: make(chan struct{}), dropped: make(chan struct{})}
	g.server = &http.Server{Handler: g, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	tlsListener := tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, Certificates: []tls.Certificate{{Certificate: [][]byte{der, caDER}, PrivateKey: key}}})
	go func() { _ = g.server.Serve(tlsListener) }()
	return g, nil
}

func (g *ResponseGate) Address() string { return g.address }
func (g *ResponseGate) CA() []byte      { return bytes.Clone(g.ca) }
func (g *ResponseGate) Receipt() (HeldIncrease, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.receipt, g.err
}
func (g *ResponseGate) Wait(ctx context.Context) (HeldIncrease, error) {
	select {
	case <-ctx.Done():
		return HeldIncrease{}, ctx.Err()
	case <-g.accepted:
		return g.Receipt()
	}
}
func (g *ResponseGate) WaitDisconnect(ctx context.Context) (HeldIncrease, error) {
	select {
	case <-ctx.Done():
		return HeldIncrease{}, ctx.Err()
	case <-g.disconnected:
		return g.Receipt()
	}
}
func (g *ResponseGate) DropHeld() {
	g.dropOnce.Do(func() {
		g.mu.Lock()
		connection := g.held
		g.receipt.Held = false
		close(g.dropped)
		g.mu.Unlock()
		if connection != nil {
			_ = connection.Close()
		}
	})
}
func (g *ResponseGate) Close() error {
	g.DropHeld()
	if t, ok := g.transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	return g.server.Close()
}
func (g *ResponseGate) fail(message string) {
	g.mu.Lock()
	if g.err == nil {
		g.err = errors.New(message)
	}
	g.mu.Unlock()
}

func (g *ResponseGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	if (r.Host != "ess.aliyuncs.com" && r.Host != "ess.aliyuncs.com:443") || r.Method != "POST" || r.URL.Path != "/" || r.Header.Get("Expect") != "" {
		g.fail("response gate received an unexpected host, method or path")
		http.Error(w, "invalid gate request", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (128<<10)+1))
	if err != nil || len(body) > 128<<10 {
		g.fail("response gate request body is incomplete or too large")
		http.Error(w, "invalid gate body", http.StatusBadRequest)
		return
	}
	fields := r.URL.Query()
	form, err := url.ParseQuery(string(body))
	if err != nil {
		g.fail("response gate request form is invalid")
		return
	}
	for k, values := range form {
		for _, value := range values {
			fields.Add(k, value)
		}
	}
	action := r.Header.Get("x-acs-action")
	if action == "" {
		action = fields.Get("Action")
	}
	selected := action == "ScaleWithAdjustment" && fields.Get("AdjustmentValue") == "1"
	if selected {
		g.mu.Lock()
		g.receipt.Forwarded++
		valid := g.receipt.Forwarded == 1 && fields.Get("ScalingGroupId") == g.group && fields.Get("AdjustmentType") == "TotalCapacity" && fields.Get("SyncActivity") == "false" && fields.Get("ClientToken") != ""
		g.mu.Unlock()
		if !valid {
			g.fail("response gate rejected another or mismatched expansion")
			http.Error(w, "invalid expansion", http.StatusConflict)
			return
		}
	}
	forward := r.Clone(r.Context())
	forward.URL.Scheme, forward.URL.Host = "https", "ess.aliyuncs.com"
	forward.RequestURI = ""
	forward.Body = io.NopCloser(bytes.NewReader(body))
	response, err := g.transport.RoundTrip(forward)
	if err != nil {
		g.fail("verified official ESS forwarding failed")
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if !selected {
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		return
	}
	var responseBody io.Reader = response.Body
	if response.Header.Get("Content-Encoding") == "gzip" {
		reader, err := gzip.NewReader(response.Body)
		if err != nil {
			g.fail("expansion response compression is invalid")
			return
		}
		defer reader.Close()
		responseBody = reader
	} else if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		g.fail("expansion response encoding is unsupported")
		return
	}
	result, err := io.ReadAll(io.LimitReader(responseBody, (1<<20)+1))
	var success struct{ RequestId, Code string }
	if err != nil || len(result) > 1<<20 || json.Unmarshal(result, &success) != nil || response.StatusCode != http.StatusOK || success.RequestId == "" || success.Code != "" {
		g.fail("expansion did not return a complete successful official response")
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		g.fail("response gate requires HTTP/1 connection ownership")
		return
	}
	connection, _, err := hijacker.Hijack()
	if err != nil {
		g.fail("response gate could not take ownership before delivery")
		return
	}
	g.mu.Lock()
	g.held = connection
	g.receipt = HeldIncrease{ReceivedAt: receivedAt, AcceptedAt: time.Now().UTC(), GroupID: g.group, ClientToken: fields.Get("ClientToken"), RequestID: success.RequestId, HTTPStatus: response.StatusCode, Forwarded: g.receipt.Forwarded, Held: true}
	g.mu.Unlock()
	close(g.accepted)
	var buffer [1]byte
	n, readErr := connection.Read(buffer[:])
	select {
	case <-g.dropped:
	default:
		g.mu.Lock()
		if n != 0 || readErr == nil {
			g.err = errors.New("client sent unexpected data while its response was held")
		}
		g.receipt.ClientClosedAt = time.Now().UTC()
		g.mu.Unlock()
		close(g.disconnected)
	}
	_ = connection.Close()
}
