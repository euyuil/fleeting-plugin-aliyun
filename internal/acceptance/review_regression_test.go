package acceptance

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type independentAPI struct {
	base                            *acceptanceAPI
	hideOnce                        bool
	malformedInventory              bool
	missingMemberIdentity           bool
	persistentMissingMemberIdentity bool
}

func (a *independentAPI) Call(ctx context.Context, s, op string, q map[string]string, out any) error {
	var body string
	if op == "DescribeScalingInstances" && (a.missingMemberIdentity || a.persistentMissingMemberIdentity) {
		a.missingMemberIdentity = false
		body = `{"RequestId":"request-test","TotalCount":1,"ScalingInstances":{"ScalingInstance":[{"ScalingGroupId":"asg-test","LifecycleState":"Pending"}]}}`
	}
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

func TestReviewPersistentMemberIdentityCannotCertifyCleanup(t *testing.T) {
	c, b, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	b.fake.Add("i-worker")
	c.API = &independentAPI{base: b, persistentMissingMemberIdentity: true}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := Cleanup(ctx, c, l, false); err == nil || l.CleanupResult == "PASS" {
		t.Fatal("incomplete membership certified cleanup", l.CleanupResult, err)
	}
}

func TestReviewTransientMemberIdentityDoesNotPoisonObservationOrCleanup(t *testing.T) {
	c, b, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	b.fake.Add("i-worker")
	wire := &independentAPI{base: b, missingMemberIdentity: true}
	c.API = wire
	if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
		t.Fatal(err)
	}
	if l.EvidenceIncomplete || len(l.Instances) != 1 || !l.Instances[0].InventoryComplete {
		t.Fatal("complete observation was not recovered")
	}
	wire.missingMemberIdentity = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Cleanup(ctx, c, l, false); err != nil || l.CleanupResult != "PASS" {
		t.Fatal(l.CleanupResult, err)
	}
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
