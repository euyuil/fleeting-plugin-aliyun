package acceptance

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type gateTransport func(*http.Request) (*http.Response, error)

func (f gateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func gateRequest(t *testing.T, g *ResponseGate, action, fields string) net.Conn {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(g.CA())
	c, err := tls.Dial("tcp", g.Address(), &tls.Config{RootCAs: roots, ServerName: g.endpoint, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, err = fmt.Fprintf(c, "POST / HTTP/1.1\r\nHost: %s\r\nx-acs-action: %s\r\nAuthorization: synthetic-signature\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: %d\r\n\r\n%s", g.endpoint, action, len(fields), fields)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const selectedIncrease = "ScalingGroupId=asg-test&AdjustmentType=TotalCapacity&AdjustmentValue=1&SyncActivity=false&ClientToken=token-test"

func TestResponseGateLocalDropCannotProvePeerExit(t *testing.T) {
	g, err := newResponseGate("cn-hangzhou", "asg-test", gateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"RequestId":"request-test"}`))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	c := gateRequest(t, g, "ScaleWithAdjustment", selectedIncrease)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := g.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	g.DropHeld()
	var b [1]byte
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := c.Read(b[:]); n != 0 || err == nil {
		t.Fatal("local drop unexpectedly delivered data", n, err)
	}
	proofCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if _, err := g.WaitDisconnect(proofCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("local drop was treated as peer exit", err)
	}
	if receipt, _ := g.Receipt(); !receipt.ClientClosedAt.IsZero() {
		t.Fatal(receipt)
	}
}

func TestResponseGateRejectsUntrustedUpstreamTLS(t *testing.T) {
	var reached atomic.Bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer upstream.Close()
	g, err := NewResponseGate("cn-hangzhou", "asg-test")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	c := gateRequest(t, g, "ScaleWithAdjustment", selectedIncrease)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || response.StatusCode != 502 || reached.Load() {
		t.Fatal("unverified TLS endpoint received the signed request", err)
	}
	_ = response.Body.Close()
	if receipt, err := g.Receipt(); err == nil || receipt.Held {
		t.Fatal("unverified upstream accepted", receipt, err)
	}
}

func TestResponseGateHoldsAllHTTPBytesUntilPeerExitAndRejectsReplay(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			var calls atomic.Int32
			g, err := newResponseGate("cn-hangzhou", "asg-test", gateTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if r.URL.Scheme != "https" || r.URL.Host != "ess.aliyuncs.com" || r.Host != "ess.aliyuncs.com" || r.Header.Get("Authorization") != "synthetic-signature" || string(body) != selectedIncrease {
					t.Error("signed upstream request changed")
				}
				data := []byte(`{"RequestId":"request-test"}`)
				header := make(http.Header)
				if compressed {
					var buffer bytes.Buffer
					writer := gzip.NewWriter(&buffer)
					_, _ = writer.Write(data)
					_ = writer.Close()
					data = buffer.Bytes()
					header.Set("Content-Encoding", "gzip")
				}
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(bytes.NewReader(data))}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			c := gateRequest(t, g, "ScaleWithAdjustment", selectedIncrease)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			receipt, err := g.Wait(ctx)
			if err != nil || !receipt.Held || receipt.ResponseDelivered || receipt.Forwarded != 1 || receipt.RequestID != "request-test" || receipt.GroupID != "asg-test" || receipt.ClientToken != "token-test" || !receipt.ClientClosedAt.IsZero() {
				t.Fatal(receipt, err)
			}
			_ = c.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
			var b [1]byte
			n, readErr := c.Read(b[:])
			var networkError net.Error
			if n != 0 || !errors.As(readErr, &networkError) || !networkError.Timeout() {
				t.Fatal("gate delivered HTTP bytes", n, readErr)
			}
			_ = c.SetReadDeadline(time.Time{})
			closedAt := time.Now()
			_ = c.Close()
			receipt, err = g.WaitDisconnect(ctx)
			if err != nil || receipt.ClientClosedAt.Before(closedAt) || receipt.ResponseDelivered {
				t.Fatal(receipt, err)
			}
			g.DropHeld()
			replay := gateRequest(t, g, "ScaleWithAdjustment", selectedIncrease)
			response, err := http.ReadResponse(bufio.NewReader(replay), nil)
			if err != nil || response.StatusCode != 409 || calls.Load() != 1 {
				t.Fatal("replay reached upstream", calls.Load(), err)
			}
			_ = response.Body.Close()
			if _, err = g.Receipt(); err == nil {
				t.Fatal("replay did not invalidate the evidence")
			}
		})
	}
}

func TestResponseGateForwardsOtherCallsAndRejectsUnsuccessfulAcceptance(t *testing.T) {
	for _, mode := range []string{"read", "forbidden", "malformed", "wrong-group"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			g, err := newResponseGate("cn-hangzhou", "asg-test", gateTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				status, body := 200, `{"RequestId":"request-test"}`
				if mode == "forbidden" {
					status, body = 403, `{"Code":"Forbidden","RequestId":"request-test"}`
				}
				if mode == "malformed" {
					body = `{}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			action, fields := "ScaleWithAdjustment", selectedIncrease
			if mode == "read" {
				action = "DescribeScalingGroups"
			}
			if mode == "wrong-group" {
				fields = strings.ReplaceAll(fields, "asg-test", "asg-foreign")
			}
			c := gateRequest(t, g, action, fields)
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			response, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			receipt, gateErr := g.Receipt()
			if mode == "read" {
				if response.StatusCode != 200 || gateErr != nil || receipt.Held || receipt.Forwarded != 0 {
					t.Fatal(receipt, gateErr)
				}
			} else if gateErr == nil || receipt.Held {
				t.Fatal("invalid cloud result was accepted", receipt, gateErr)
			}
			if mode == "wrong-group" && calls.Load() != 0 {
				t.Fatal("foreign expansion was forwarded")
			}
		})
	}
}

func TestResponseGateRejectsEndpointInjection(t *testing.T) {
	for _, region := range []string{"", "https://example.com", "../example", "valid-1.evil.com", "valid-1\n"} {
		if g, err := NewResponseGate(region, "asg-test"); err == nil {
			g.Close()
			t.Fatal("invalid region accepted")
		}
	}
}
