package acceptance

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type independentAPI struct {
	base               *acceptanceAPI
	hideOnce           bool
	malformedInventory bool
}

func (a *independentAPI) Call(ctx context.Context, s, op string, q map[string]string, out any) error {
	var body string
	if op == "DescribeInstances" && a.hideOnce {
		a.hideOnce = false
		body = `{"TotalCount":0,"Instances":{"Instance":[]}}`
	}
	if a.malformedInventory && op == "DescribeDisks" && q["InstanceId"] != "" {
		body = `{"TotalCount":1,"Disks":{"Disk":[{"DeleteWithInstance":true}]}}`
	}
	if a.malformedInventory && op == "DescribeNetworkInterfaces" && q["InstanceId"] != "" {
		body = `{"TotalCount":1,"NetworkInterfaceSets":{"NetworkInterfaceSet":[{}]}}`
	}
	if body != "" {
		return json.Unmarshal([]byte(body), out)
	}
	return a.base.Call(ctx, s, op, q, out)
}
func TestReviewRegressionPendingECSCanRecoverCompleteEvidence(t *testing.T) {
	c, b, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	b.fake.Add("i-worker")
	c.API = &independentAPI{base: b, hideOnce: true}
	for i := 0; i < 2; i++ {
		if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
			t.Fatal(err)
		}
	}
	if l.EvidenceIncomplete || !l.Instances[0].InventoryComplete {
		t.Fatalf("transient ECS absence poisoned complete evidence: incomplete=%v inventory=%v", l.EvidenceIncomplete, l.Instances[0].InventoryComplete)
	}
}
func TestReviewRegressionMissingInventoryIDsCannotCertifyCleanup(t *testing.T) {
	c, b, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	b.fake.Add("i-worker")
	c.API = &independentAPI{base: b, malformedInventory: true}
	_ = Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := Cleanup(ctx, c, l, false)
	if err == nil || l.CleanupResult == "PASS" {
		t.Fatalf("missing resource IDs certified: err=%v cleanup=%s instances=%+v", err, l.CleanupResult, l.Instances)
	}
}
