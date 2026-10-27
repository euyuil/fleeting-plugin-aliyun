package experimental

import (
	"context"
	"testing"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

func TestSuspendResumeAndRestart(t *testing.T) {
	f := experimentFixture()
	f.Add("i-a")
	f.Add("i-keep")
	cloud := &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}
	controller := &Controller{Client: cloud}
	ctx := context.Background()
	if ids, err := controller.Request(ctx, "suspend", []string{"i-a"}); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	restarted := &Controller{Client: cloud}
	for n := 0; n < 2; n++ {
		if _, err := restarted.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if State(f.Members["i-a"], f.Instances["i-a"], "") != upstream.StateSuspended || f.Group.DesiredCapacity != 2 || f.Instances["i-keep"].Status != "Running" {
		t.Fatal("suspend changed pool intent or another member")
	}
	if _, err := restarted.Request(ctx, "resume", []string{"i-a"}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if _, err := restarted.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if State(f.Members["i-a"], f.Instances["i-a"], "") != upstream.StateRunning || f.Group.DesiredCapacity != 2 {
		t.Fatal("resume did not converge")
	}
}
func TestResumeUnavailableCapacityRetainsIdentity(t *testing.T) {
	f := experimentFixture()
	f.Add("i-a")
	m := f.Members["i-a"]
	m.LifecycleState = "Standby"
	f.Members["i-a"] = m
	i := f.Instances["i-a"]
	i.Status = "Stopped"
	f.Instances["i-a"] = i
	f.Group.StandbyCapacity = 1
	f.Group.StoppedCapacity = 1
	f.Errors["StartInstance"] = []error{&aliyun.Error{Code: "InsufficientInstanceCapacity"}}
	cloud := &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}
	controller := &Controller{Client: cloud}
	if _, err := controller.Request(context.Background(), "resume", []string{"i-a"}); err == nil {
		t.Fatal("unavailable capacity reported success")
	}
	if f.Instances["i-a"].Status != "Stopped" || f.Group.DesiredCapacity != 1 || f.Group.Tag("fleeting-operation") == "" {
		t.Fatal("lost retained state")
	}
}

func experimentFixture() *testutil.Fake {
	f := testutil.New()
	for key, value := range map[string]string{"fleeting-mode": "suspend", "fleeting-project": "owner/repo", "fleeting-run": "100/1"} {
		f.Group.Tags.Tag = append(f.Group.Tags.Tag, struct {
			TagKey, TagValue string
			Propagate        bool
		}{key, value, false})
	}
	return f
}
func TestUnclaimedAndAttachedWorkersAreRejectedBeforeJournal(t *testing.T) {
	for _, attached := range []bool{false, true} {
		f := experimentFixture()
		f.Add("i-a")
		if attached {
			m := f.Members["i-a"]
			m.CreationType = "Attached"
			f.Members["i-a"] = m
		} else {
			for n := range f.Group.Tags.Tag {
				if f.Group.Tags.Tag[n].TagKey == "fleeting-run" {
					f.Group.Tags.Tag[n].TagValue = ""
				}
			}
		}
		c := &Controller{Client: &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}}
		if _, err := c.Request(context.Background(), "suspend", []string{"i-a"}); err == nil {
			t.Fatal("unsafe experiment accepted")
		}
		if f.Group.Tag("fleeting-operation") != "" || f.Instances["i-a"].Status != "Running" {
			t.Fatal("unsafe experiment mutated pool")
		}
	}
}

func TestForeignStandbyCannotBeAdmittedByExperiment(t *testing.T) {
	f := experimentFixture()
	f.Add("i-a")
	f.Add("i-other")
	member := f.Members["i-other"]
	member.LifecycleState = "Standby"
	f.Members["i-other"] = member
	f.Group.StandbyCapacity = 1
	c := &Controller{Client: &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}}
	if _, err := c.Request(context.Background(), "suspend", []string{"i-a"}); err == nil {
		t.Fatal("unselected standby admitted")
	}
	if f.Group.Tag("fleeting-operation") != "" {
		t.Fatal("unsafe journal written")
	}
}
