//go:build responsegate && !componenttest

package provider

import (
	"errors"
	"os"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

// This factory retains the real SDK but enables a per-process acceptance route.
// It is excluded from ordinary release and component-test builds.
func newAPI(region string) (aliyun.API, error) {
	if os.Getenv("RUN_LIVE_ACCEPTANCE") != "1" {
		return nil, errors.New("response gate requires explicit live acceptance")
	}
	ca, err := os.ReadFile(os.Getenv("ALIYUN_RESPONSE_GATE_CA"))
	if err != nil {
		return nil, errors.New("response gate CA is unavailable")
	}
	sdk, err := aliyun.NewSDK(region)
	if err != nil {
		return nil, err
	}
	if err = sdk.ConfigureESSResponseGate(region, os.Getenv("ALIYUN_RESPONSE_GATE_ADDR"), ca); err != nil {
		_ = sdk.Close()
		return nil, err
	}
	return sdk, nil
}
