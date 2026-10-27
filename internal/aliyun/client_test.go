package aliyun_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
)

func client(f *testutil.Fake) *aliyun.Client {
	return &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}
}

func TestCompleteSnapshotsAcrossPagesAndBatches(t *testing.T) {
	f := testutil.New()
	f.Limit = 7
	var ids []string
	for n := range 121 {
		id := fmt.Sprintf("i-test-%03d", n)
		f.Add(id)
		ids = append(ids, id)
	}
	c := client(f)
	members, err := c.Members(context.Background())
	if err != nil || len(members) != len(ids) {
		t.Fatalf("members=%d err=%v", len(members), err)
	}
	instances, err := c.Instances(context.Background(), ids)
	if err != nil || len(instances) != len(ids) {
		t.Fatalf("instances=%d err=%v", len(instances), err)
	}
	batches := 0
	for _, call := range f.Calls {
		if call.Action == "DescribeInstances" {
			batches++
		}
	}
	if batches != 2 {
		t.Fatalf("expected two ECS batches, got %d", batches)
	}
}

func TestIncompletePolicyResponseCannotAdmitGroup(t *testing.T) {
	f := testutil.New()
	c := client(f)
	if err := c.Validate(context.Background(), f.Group); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"DescribeScalingRules", "DescribeScheduledTasks", "DescribeLifecycleHooks"} {
		t.Run(action, func(t *testing.T) {
			c.API = actionOverride{f, action}
			if err := c.Validate(context.Background(), f.Group); err == nil {
				t.Fatal("missing policy count admitted")
			}
		})
	}
}

type actionOverride struct {
	aliyun.API
	action string
}

func (a actionOverride) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == a.action {
		return nil // A syntactically successful but incomplete cloud response.
	}
	return a.API.Call(ctx, service, action, q, out)
}

func TestSpecifiedReleaseRetainsOtherMembersAndReusesToken(t *testing.T) {
	f := testutil.New()
	f.Add("i-worker-a")
	f.Add("i-worker-b")
	c := client(f)
	ctx := context.Background()
	first, err := c.Remove(ctx, []string{"i-worker-a"}, "stable-remove-token")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Remove(ctx, []string{"i-worker-a"}, "stable-remove-token")
	if err != nil || second.ScalingActivityId != first.ScalingActivityId {
		t.Fatalf("replay changed activity: %+v %v", second, err)
	}
	if _, err := c.Activity(ctx, first.ScalingActivityId); err != nil {
		t.Fatal(err)
	}
	if len(f.Members) != 1 || f.Members["i-worker-b"].InstanceId == "" || f.Group.DesiredCapacity != 1 {
		t.Fatal("specified release changed another worker or decremented twice")
	}
}

func TestJournalSurvivesAcceptedWriteWithLostReply(t *testing.T) {
	f := testutil.New()
	c := client(f)
	j := aliyun.Journal{Kind: "increase", Token: "same-intent", Before: 0, Target: 1, Count: 1, CreatedAt: time.Now().UTC()}
	lost := errors.New("synthetic lost reply")
	f.After = func(action string, _ map[string]string) error {
		if action == "TagResources" {
			return lost
		}
		return nil
	}
	if err := c.StoreJournal(context.Background(), j); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	f.After = nil
	if err := c.StoreJournal(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	loaded, err := aliyun.ReadJournal(f.Group)
	if err != nil || loaded == nil || loaded.Token != j.Token || loaded.Target != 1 {
		t.Fatalf("lost journal: %+v %v", loaded, err)
	}
	j.Token = "different-intent"
	if err := c.StoreJournal(context.Background(), j); err == nil {
		t.Fatal("another logical operation overwrote the journal")
	}
	if err := c.ClearJournal(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err = aliyun.ReadJournal(f.Group)
	if err != nil || loaded != nil {
		t.Fatalf("journal not cleared: %+v %v", loaded, err)
	}
}

type refreshResponses struct {
	aliyun.API
	pages []string
	page  int
}

func (r *refreshResponses) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action != "DescribeInstanceRefreshes" {
		return r.API.Call(ctx, service, action, q, out)
	}
	if q["MaxResults"] != "50" {
		return errors.New("refresh page exceeds API limit")
	}
	if r.page >= len(r.pages) {
		return errors.New("unexpected refresh page")
	}
	body := r.pages[r.page]
	r.page++
	return json.Unmarshal([]byte(body), out)
}

func TestRefreshAdmissionRequiresCompleteStableTaskSet(t *testing.T) {
	task := `{"InstanceRefreshTaskId":"ir-a","ScalingGroupId":"asg-test","RegionId":"cn-test-1","Status":"Successful"}`
	page := func(total int, next, entries string) string {
		return fmt.Sprintf(`{"TotalCount":%d,"NextToken":%q,"InstanceRefreshTasks":{"InstanceRefreshTask":[%s]}}`, total, next, entries)
	}
	for _, test := range []struct {
		name      string
		pages     []string
		wantError bool
	}{
		{"missing tasks", []string{page(1, "", "")}, true},
		{"negative total", []string{page(-1, "", "")}, true},
		{"missing identity", []string{page(1, "", `{"ScalingGroupId":"asg-test","Status":"Successful"}`)}, true},
		{"duplicate tasks", []string{page(2, "next", task), page(2, "", task)}, true},
		{"changing total", []string{page(2, "next", task), page(1, "", "")}, true},
		{"repeated token", []string{page(3, "next", task), page(3, "next", `{"InstanceRefreshTaskId":"ir-b","ScalingGroupId":"asg-test","RegionId":"cn-test-1","Status":"Successful"}`)}, true},
		{"complete terminal tasks", []string{page(1, "", task)}, false},
		{"complete empty set", []string{page(0, "", "")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := testutil.New()
			c := client(f)
			c.API = &refreshResponses{API: f, pages: test.pages}
			err := c.Validate(context.Background(), f.Group)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestInvalidJournalCannotWriteOrReplaceIntent(t *testing.T) {
	f := testutil.New()
	c := client(f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	j := aliyun.Journal{Kind: "increase", Token: "stable", Before: 0, Target: 2, Count: 1, CreatedAt: time.Now().UTC()}
	if err := c.StoreJournal(ctx, j); err == nil {
		t.Fatal("invalid count was accepted")
	}
	for _, call := range f.Calls {
		if call.Action == "TagResources" {
			t.Fatal("invalid candidate changed cloud tags")
		}
	}
	j.Count = 2
	if err := c.StoreJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	j.Target, j.Count = 3, 3
	if err := c.StoreJournal(ctx, j); err == nil {
		t.Fatal("same token changed logical intent")
	}
	loaded, err := aliyun.ReadJournal(f.Group)
	if err != nil || loaded == nil || loaded.Target != 2 {
		t.Fatalf("original intent changed: %+v %v", loaded, err)
	}
}

func TestPreparedRemovalAllowsOnlyItsOwnAcceptance(t *testing.T) {
	f := testutil.New()
	f.Add("i-worker")
	c := client(f)
	ctx := context.Background()
	j := aliyun.Journal{Kind: "remove", Token: "stable", Before: 1, Target: 1, IDs: []string{"i-worker"}, CreatedAt: time.Now().UTC()}
	if err := c.StoreJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	other := j
	other.IDs = []string{"i-other"}
	if err := c.StoreJournal(ctx, other); err == nil {
		t.Fatal("removal IDs changed")
	}
	j.Activity, j.Target, j.AcceptedMask = "activity-test", 0, 1
	if err := c.StoreJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	j.Activity = "activity-other"
	if err := c.StoreJournal(ctx, j); err == nil {
		t.Fatal("accepted activity changed")
	}
}
