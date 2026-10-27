// Package aliyun contains the small ESS/ECS boundary used by the provider.
package aliyun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
	"github.com/aliyun/credentials-go/credentials"
)

// API is the signed API boundary. Tests replace this boundary, not the provider.
type API interface {
	Call(context.Context, string, string, map[string]string, any) error
}

type Error struct {
	Code, RequestID string
	Retryable       bool
}

func (e *Error) Error() string {
	return fmt.Sprintf("Alibaba Cloud API error: code=%s request_id=%s", e.Code, e.RequestID)
}
func IsRetryable(err error) bool { var e *Error; return errors.As(err, &e) && e.Retryable }
func IsAmbiguous(err error) bool {
	var e *Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || (errors.As(err, &e) && e.Code == "TransportError")
}

type SDK struct {
	ess, ecs  *openapi.Client
	transport *http.Transport
}

func NewSDK(region string) (*SDK, error) {
	cred, err := credentials.NewCredential(nil)
	if err != nil {
		return nil, &Error{Code: "CredentialConfiguration"}
	}
	return NewSDKWithCredential(region, cred)
}

var regionPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

func NewSDKWithCredential(region string, cred credentials.Credential) (*SDK, error) {
	if cred == nil {
		return nil, &Error{Code: "CredentialConfiguration"}
	}
	if !regionPattern.MatchString(region) {
		return nil, &Error{Code: "InvalidRegion"}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	s := &SDK{transport: transport}
	owned := ownedHTTP{client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for service, dst := range map[string]**openapi.Client{"ess": &s.ess, "ecs": &s.ecs} {
		endpoint := endpoint(service, region)
		c, err := openapi.NewClient(&openapi.Config{RegionId: &region, Endpoint: &endpoint, Credential: safeCredential{cred}})
		if err != nil {
			return nil, &Error{Code: "SDKConfiguration"}
		}
		c.HttpClient = owned
		*dst = c
	}
	return s, nil
}

// Call retries transient failures. Mutation callers must supply a stable
// idempotency token or otherwise establish that their request is safe to replay.
func (s *SDK) Call(ctx context.Context, service, action string, query map[string]string, out any) error {
	c, version := s.ess, "2014-08-28"
	if service == "ecs" {
		c, version = s.ecs, "2014-05-26"
	} else if service != "ess" {
		return errors.New("unsupported API service")
	}
	params := &openapi.Params{Action: &action, Version: &version, Protocol: dara.String("HTTPS"), Pathname: dara.String("/"), Method: dara.String("POST"), AuthType: dara.String("AK"), Style: dara.String("RPC"), ReqBodyType: dara.String("formData"), BodyType: dara.String("json")}
	q := map[string]*string{}
	for k, v := range query {
		q[k] = dara.String(v)
	}
	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := c.CallApiWithCtx(ctx, params, &openapi.OpenApiRequest{Query: q}, &dara.RuntimeOptions{Autoretry: dara.Bool(false), ConnectTimeout: dara.Int(10000), ReadTimeout: dara.Int(30000)})
		if err == nil {
			body, ok := result["body"].(map[string]interface{})
			if !ok || len(body) == 0 {
				return &Error{Code: "TransportError", Retryable: true}
			}
			b, e := json.Marshal(body)
			if e != nil {
				return &Error{Code: "TransportError", Retryable: true}
			}
			if e = json.Unmarshal(b, out); e != nil {
				return &Error{Code: "TransportError", Retryable: true}
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		safe := classify(err)
		if !IsRetryable(safe) || attempt == 3 {
			return safe
		}
		delay := time.Duration(100*(1<<attempt)+rand.IntN(100)) * time.Millisecond
		if err := Wait(ctx, delay); err != nil {
			return err
		}
	}
	return errors.New("retry budget exhausted")
}

// classify deliberately excludes SDK messages and URLs, which can contain credentials.
func classify(err error) error {
	var safe *Error
	if errors.As(err, &safe) {
		return safe
	}
	var wire struct {
		Code, RequestID string
		Data            json.RawMessage
	}
	b, _ := json.Marshal(err)
	_ = json.Unmarshal(b, &wire)
	if wire.Code == "" {
		var n net.Error
		if errors.As(err, &n) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &Error{Code: "TransportError", Retryable: true}
		}
		return &Error{Code: "TransportError", Retryable: true}
	}
	var data map[string]any
	_ = json.Unmarshal(wire.Data, &data)
	if data == nil {
		var encoded string
		if json.Unmarshal(wire.Data, &encoded) == nil {
			_ = json.Unmarshal([]byte(encoded), &data)
		}
	}
	if wire.RequestID == "" {
		if id, ok := data["RequestId"].(string); ok {
			wire.RequestID = id
		}
	}
	retry := strings.HasPrefix(wire.Code, "Throttling") || strings.HasPrefix(wire.Code, "InternalError") || strings.Contains(wire.Code, "ScalingActivityInProgress") || wire.Code == "ServiceUnavailable" || wire.Code == "IncorrectScalingGroupStatus"
	return &Error{Code: wire.Code, RequestID: wire.RequestID, Retryable: retry}
}
func Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type safeCredential struct{ credentials.Credential }

func (c safeCredential) GetCredential() (*credentials.CredentialModel, error) {
	value, err := c.Credential.GetCredential()
	if err != nil {
		return nil, &Error{Code: "CredentialsUnavailable"}
	}
	return value, nil
}

type ownedHTTP struct{ client *http.Client }

func (h ownedHTTP) Call(request *http.Request, _ *http.Transport) (*http.Response, error) {
	return h.client.Do(request)
}
func (s *SDK) Close() error {
	if s.transport != nil {
		s.transport.CloseIdleConnections()
	}
	return nil
}
