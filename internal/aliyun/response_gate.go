//go:build responsegate

package aliyun

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ConfigureESSResponseGate changes only the ESS dial destination. The SDK keeps
// its official URL, Host, SNI, signing, parsing and retry behavior. Neither this
// method nor its trust override exists in ordinary release builds.
func (s *SDK) ConfigureESSResponseGate(region, address string, ca []byte) error {
	host, port, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || n < 1 || n > 65535 || ip == nil || !ip.IsLoopback() {
		return errors.New("response gate must use a numeric loopback address and port")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return errors.New("system certificate roots are unavailable")
	}
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("response gate certificate is invalid")
	}
	ess := endpoint("ess", region)
	proxy := s.transport.Proxy
	s.transport.Proxy = func(r *http.Request) (*url.URL, error) {
		if r.URL.Hostname() == ess {
			return nil, nil
		}
		if proxy == nil {
			return nil, nil
		}
		return proxy(r)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	s.transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == net.JoinHostPort(ess, "443") {
			addr = address
		}
		return dialer.DialContext(ctx, network, addr)
	}
	s.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return nil
}
