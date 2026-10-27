package aliyun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alibabacloud-go/tea/dara"
	"github.com/aliyun/credentials-go/credentials"
)

type redirectClient struct {
	url    string
	client *http.Client
}

func (c redirectClient) Call(r *http.Request, _ *http.Transport) (*http.Response, error) {
	u, _ := url.Parse(c.url)
	r.URL.Scheme = u.Scheme
	r.URL.Host = u.Host
	return c.client.Do(r)
}
func testSDK(t *testing.T, h http.Handler) (*SDK, func()) {
	t.Helper()
	server := httptest.NewServer(h)
	cred, err := credentials.NewCredential(&credentials.Config{Type: dara.String("sts"), AccessKeyId: dara.String("synthetic-id"), AccessKeySecret: dara.String("synthetic-secret"), SecurityToken: dara.String("synthetic-sts")})
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := NewSDKWithCredential("cn-hangzhou", cred)
	if err != nil {
		t.Fatal(err)
	}
	sdk.ess.HttpClient = redirectClient{server.URL, server.Client()}
	return sdk, server.Close
}
func TestSDKSignSTSAndIdempotentRetry(t *testing.T) {
	var calls atomic.Int32
	var tokens []string
	sdk, closeServer := testSDK(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Header.Get("Authorization") == "" {
			t.Error("missing SDK signature")
		}
		if r.Header.Get("x-acs-security-token") != "synthetic-sts" && r.Form.Get("SecurityToken") != "synthetic-sts" {
			t.Error("missing STS token")
		}
		tokens = append(tokens, r.Form.Get("ClientToken"))
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"Code":"Throttling","RequestId":"request-test","Message":"synthetic-secret"}`)
			return
		}
		_, _ = io.WriteString(w, `{"RequestId":"request-test"}`)
	}))
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out map[string]any
	if err := sdk.Call(ctx, "ess", "ScaleWithAdjustment", map[string]string{"ClientToken": "same-token"}, &out); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || tokens[0] != tokens[1] {
		t.Fatal(tokens)
	}
}
func TestSDKRejectsMalformedBodies(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"body":null}`} {
		t.Run(body, func(t *testing.T) {
			sdk, closeServer := testSDK(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
			defer closeServer()
			var out map[string]any
			if err := sdk.Call(context.Background(), "ess", "DescribeScalingInstances", nil, &out); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}
func TestSDKCancellation(t *testing.T) {
	sdk, closeServer := testSDK(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer closeServer()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var out map[string]any
	if err := sdk.Call(ctx, "ess", "DescribeScalingGroups", nil, &out); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestErrorsNeverIncludeCredentialMessage(t *testing.T) {
	err := classify(dara.NewSDKError(map[string]interface{}{"code": "Forbidden", "message": "synthetic-secret", "data": map[string]interface{}{"RequestId": "request-test"}}))
	if strings.Contains(err.Error(), "synthetic-secret") || IsRetryable(err) {
		t.Fatal(err)
	}
	if !IsAmbiguous(classify(io.ErrUnexpectedEOF)) {
		t.Fatal("lost response must remain ambiguous")
	}
	_, _ = json.Marshal(err)
}
func TestOfficialESSEndpoints(t *testing.T) {
	for _, region := range []string{"cn-hangzhou", "cn-beijing", "cn-shanghai"} {
		if endpoint("ess", region) != "ess.aliyuncs.com" {
			t.Fatal(region)
		}
	}
	if endpoint("ess", "synthetic-region-1") != "ess.synthetic-region-1.aliyuncs.com" {
		t.Fatal("regional endpoint")
	}
}

func TestRejectsMalformedRegionBeforeSigning(t *testing.T) {
	cred, err := credentials.NewCredential(&credentials.Config{Type: dara.String("access_key"), AccessKeyId: dara.String("synthetic-id"), AccessKeySecret: dara.String("synthetic-secret")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSDKWithCredential("invalid?endpoint", cred)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "InvalidRegion" {
		t.Fatalf("expected InvalidRegion, got %v", err)
	}
}
