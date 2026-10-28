//go:build responsegate

package acceptance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alibabacloud-go/tea/dara"
	"github.com/aliyun/credentials-go/credentials"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func TestResponseGateRetainsRealSDKSigningAndBlocksItsReturn(t *testing.T) {
	for region, host := range map[string]string{"cn-hangzhou": "ess.aliyuncs.com", "eu-central-1": "ess.eu-central-1.aliyuncs.com"} {
		t.Run(region, func(t *testing.T) {
			testResponseGateSDKRegion(t, region, host)
		})
	}
}

func testResponseGateSDKRegion(t *testing.T, region, host string) {
	var upstream atomic.Int32
	g, err := newResponseGate(region, "asg-test", gateTransport(func(r *http.Request) (*http.Response, error) {
		upstream.Add(1)
		if r.URL.Host != host || r.Header.Get("Authorization") == "" {
			t.Error("official-host SDK signature was lost")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"RequestId":"request-test"}`))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	cred, err := credentials.NewCredential(&credentials.Config{Type: dara.String("access_key"), AccessKeyId: dara.String("synthetic-id"), AccessKeySecret: dara.String("synthetic-secret")})
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := aliyun.NewSDKWithCredential(region, cred)
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Close()
	for _, address := range []string{"", "example.com:443", "192.0.2.1:443", "127.0.0.1:0"} {
		if err := sdk.ConfigureESSResponseGate(region, address, g.CA()); err == nil {
			t.Fatal("nonlocal or invalid route accepted", address)
		}
	}
	if err := sdk.ConfigureESSResponseGate(region, g.Address(), g.CA()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	returned := make(chan error, 1)
	go func() {
		var result map[string]any
		returned <- sdk.Call(ctx, "ess", "ScaleWithAdjustment", map[string]string{"ScalingGroupId": "asg-test", "AdjustmentType": "TotalCapacity", "AdjustmentValue": "1", "SyncActivity": "false", "ClientToken": "token-test"}, &result)
	}()
	if _, err := g.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-returned:
		t.Fatal("SDK received a withheld response", err)
	default:
	}
	cancel()
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatal("SDK did not retain cancellation semantics", err)
	}
	proofCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := g.WaitDisconnect(proofCtx); err != nil {
		t.Fatal(err)
	}
	if upstream.Load() != 1 {
		t.Fatal("SDK request was replayed", upstream.Load())
	}
}
