//go:build !componenttest && !responsegate

package provider

import (
	"testing"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func TestReleaseFactoryIgnoresResponseGateConfiguration(t *testing.T) {
	t.Setenv("RUN_LIVE_ACCEPTANCE", "1")
	t.Setenv("ALIYUN_RESPONSE_GATE_ADDR", "invalid-address")
	t.Setenv("ALIYUN_RESPONSE_GATE_CA", "missing-certificate")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "synthetic-id")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "synthetic-secret")
	t.Setenv("ALIBABA_CLOUD_PROFILE", "")
	api, err := newAPI("us-east-1")
	if err != nil {
		t.Fatal("release factory consumed acceptance routing configuration", err)
	}
	sdk, ok := api.(*aliyun.SDK)
	if !ok {
		t.Fatal("release factory replaced the real SDK")
	}
	_ = sdk.Close()
}
