package aliyun_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type architectureAPI struct {
	t                   *testing.T
	value, instanceType string
}

func (a architectureAPI) Call(_ context.Context, service, action string, q map[string]string, out any) error {
	a.t.Helper()
	if service != "ecs" || action != "DescribeInstanceTypes" || q["InstanceTypes.1"] != "ecs.test.small" {
		a.t.Fatalf("unexpected architecture request: %s %s %v", service, action, q)
	}
	return json.Unmarshal([]byte(fmt.Sprintf(`{"InstanceTypes":{"InstanceType":[{"InstanceTypeId":%q,"CpuArchitecture":%q}]}}`, a.instanceType, a.value)), out)
}

func TestArchitectureUsesCatalogValues(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"X86", "amd64"}, {"ARM", "arm64"},
		{"x86_64", "amd64"}, {"x64", "amd64"},
		{"arm64", "arm64"}, {"aarch64", "arm64"},
		{"", ""}, {"unknown", ""},
	} {
		t.Run(tc.value, func(t *testing.T) {
			c := &aliyun.Client{API: architectureAPI{t, tc.value, "ecs.test.small"}, Region: "cn-test-1"}
			got, err := c.Architecture(context.Background(), "ecs.test.small")
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("architecture=%q err=%v", got, err)
			}
		})
	}
}

func TestArchitectureCannotUseAnotherInstanceType(t *testing.T) {
	c := &aliyun.Client{API: architectureAPI{t, "X86", "ecs.other.small"}, Region: "cn-test-1"}
	if got, err := c.Architecture(context.Background(), "ecs.test.small"); err == nil || got != "" {
		t.Fatalf("foreign type supplied architecture=%q err=%v", got, err)
	}
}
