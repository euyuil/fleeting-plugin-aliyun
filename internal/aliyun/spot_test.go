package aliyun_test

import (
	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
	"testing"
)

func TestT18StrictSpot(t *testing.T) {
	g := testutil.New().Group
	g.MaxSize = 1
	var tmpl aliyun.Template
	d := &tmpl.LaunchTemplateData
	d.InstanceType = "ecs.test.small"
	d.ImageId = "image-test"
	d.ZoneId = "cn-test-1a"
	d.KeyPairName = "key-test"
	d.InstanceChargeType = "PostPaid"
	d.SpotStrategy = "SpotAsPriceGo"
	yes := true
	d.SystemDiskDeleteWithInstance = &yes
	d.InternetChargeType = "PayByTraffic"
	d.InternetMaxBandwidthOut = 1
	if err := aliyun.ValidateSpotSmoke(g, tmpl); err != nil {
		t.Fatal(err)
	}
	g.CompensateWithOnDemand = true
	if err := aliyun.ValidateSpotSmoke(g, tmpl); err == nil {
		t.Fatal("fallback allowed")
	}
	g.CompensateWithOnDemand = false
	d.SpotStrategy = "NoSpot"
	if err := aliyun.ValidateSpotSmoke(g, tmpl); err == nil {
		t.Fatal("on-demand allowed")
	}
}
