//go:build !componenttest && !responsegate

package provider

import "github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"

func newAPI(region string) (aliyun.API, error) { return aliyun.NewSDK(region) }
