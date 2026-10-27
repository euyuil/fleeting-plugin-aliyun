package acceptance

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type inventoryAPI struct {
	base                        *acceptanceAPI
	emptyDisk, emptyENI, unsafe bool
	orphan                      bool
}

func (a *inventoryAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	var response any
	if action == "DescribeDisks" && q["InstanceId"] != "" {
		if a.emptyDisk {
			a.emptyDisk = false
			response = map[string]any{"TotalCount": 0}
		} else if a.unsafe {
			response = map[string]any{"TotalCount": 1, "Disks": map[string]any{"Disk": []map[string]any{{"DiskId": "d-retained", "InstanceId": q["InstanceId"], "Type": "system", "DeleteWithInstance": false}}}}
		}
	}
	if action == "DescribeDisks" && q["DiskIds"] != "" && a.orphan {
		response = map[string]any{"TotalCount": 1}
	}
	if action == "DescribeNetworkInterfaces" && q["InstanceId"] != "" && a.emptyENI {
		a.emptyENI = false
		response = map[string]any{"TotalCount": 0}
	}
	if response != nil {
		b, _ := json.Marshal(response)
		return json.Unmarshal(b, out)
	}
	return a.base.Call(ctx, service, action, q, out)
}
func TestEmptyAssociatedQueriesRemainPendingUntilVisible(t *testing.T) {
	for _, disk := range []bool{true, false} {
		c, base, l := fixture()
		if err := Claim(context.Background(), c, l); err != nil {
			t.Fatal(err)
		}
		base.fake.Add("i-worker")
		api := &inventoryAPI{base: base, emptyDisk: disk, emptyENI: !disk}
		c.API = api
		if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
			t.Fatal(err)
		}
		if len(l.Instances) != 1 || l.Instances[0].InventoryComplete {
			t.Fatal("temporary empty inventory certified")
		}
		if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
			t.Fatal(err)
		}
		if !l.Instances[0].InventoryComplete || len(l.Instances[0].DiskIDs) == 0 || len(l.Instances[0].NetworkInterfaceIDs) == 0 {
			t.Fatal("later inventory not recovered")
		}
	}
}
func TestUnsafeDiskPolicyStillReleasesComputeAndReportsDisk(t *testing.T) {
	c, base, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	base.fake.Add("i-worker")
	api := &inventoryAPI{base: base, unsafe: true, orphan: true}
	c.API = api
	if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err == nil {
		t.Fatal("unsafe disk policy accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Cleanup(ctx, c, l, false); err == nil {
		t.Fatal("retained disk certified deleted")
	}
	if len(base.fake.Members) != 0 || base.fake.Group.DesiredCapacity != 0 {
		t.Fatal("unsafe disk policy prevented compute release")
	}
	if len(l.Leftovers) == 0 || l.CleanupResult == "PASS" {
		t.Fatal("retained disk was hidden")
	}
}

type malformedResourceAPI struct {
	base         *acceptanceAPI
	action, body string
}

func (a *malformedResourceAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == a.action && q["InstanceId"] != "" {
		return json.Unmarshal([]byte(a.body), out)
	}
	return a.base.Call(ctx, service, action, q, out)
}
func TestInventoryRequiresCompleteResourceIdentities(t *testing.T) {
	cases := []struct{ action, body string }{
		{"DescribeDisks", `{"TotalCount":1,"Disks":{"Disk":[{"DiskId":"d-a","InstanceId":"i-foreign","Type":"system","DeleteWithInstance":true}]}}`},
		{"DescribeDisks", `{"TotalCount":2,"Disks":{"Disk":[{"DiskId":"d-a","InstanceId":"i-worker","Type":"system"},{"DiskId":"d-a","InstanceId":"i-worker","Type":"system"}]}}`},
		{"DescribeDisks", `{"TotalCount":1,"Disks":{"Disk":[{"DiskId":"d-a","InstanceId":"i-worker","Type":"data","DeleteWithInstance":true}]}}`},
		{"DescribeNetworkInterfaces", `{"TotalCount":1,"NetworkInterfaceSets":{"NetworkInterfaceSet":[{"NetworkInterfaceId":"","InstanceId":"i-worker","Type":"Primary"}]}}`},
		{"DescribeNetworkInterfaces", `{"TotalCount":1,"NetworkInterfaceSets":{"NetworkInterfaceSet":[{"NetworkInterfaceId":"eni-a","InstanceId":"i-foreign","Type":"Primary"}]}}`},
		{"DescribeNetworkInterfaces", `{"TotalCount":1,"NetworkInterfaceSets":{"NetworkInterfaceSet":[{"NetworkInterfaceId":"eni-a","InstanceId":"i-worker","Type":"Secondary"}]}}`},
	}
	for _, tc := range cases {
		c, base, _ := fixture()
		base.fake.Add("i-worker")
		c.API = &malformedResourceAPI{base: base, action: tc.action, body: tc.body}
		r := &Resource{InstanceID: "i-worker"}
		if err := inventory(context.Background(), c, r); err == nil || r.InventoryComplete {
			t.Fatal("malformed inventory certified", tc.body)
		}
	}
}
func TestMissedResourceLifetimeKeepsEvidenceGap(t *testing.T) {
	c, base, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	base.fake.Add("i-worker")
	c.API = &independentAPI{base: base, hideOnce: true}
	if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
		t.Fatal(err)
	}
	delete(base.fake.Instances, "i-worker")
	delete(base.fake.Members, "i-worker")
	if err := RecoverResources(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	if !l.EvidenceIncomplete {
		t.Fatal("lost lifetime inventory certified complete")
	}
}
