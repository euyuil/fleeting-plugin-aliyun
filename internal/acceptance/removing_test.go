package acceptance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type removingAPI struct {
	aliyun.API
	activities []HistoricalActivity
	writes     int
}

func (a *removingAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == "DescribeScalingActivities" {
		b, _ := json.Marshal(map[string]any{"TotalCount": len(a.activities), "ScalingActivities": map[string]any{"ScalingActivity": a.activities}})
		return json.Unmarshal(b, out)
	}
	if action != "DescribeScalingGroups" && action != "DescribeScalingInstances" && action != "DescribeInstances" {
		a.writes++
	}
	return a.API.Call(ctx, service, action, q, out)
}

func removingFixture(t *testing.T) (*aliyun.Client, *acceptanceAPI, *removingAPI, *Ledger) {
	t.Helper()
	c, a, l := fixture()
	a.fake.Group.MaxSize = 1
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	a.fake.Add("i-test")
	m := a.fake.Members["i-test"]
	m.LifecycleState = "Removing"
	a.fake.Members[m.InstanceId] = m
	a.fake.Group.DesiredCapacity, a.fake.Group.RemovingCapacity = 0, 1
	l.Instances = []Resource{{InstanceID: "i-test", SpotVerified: true, InventoryComplete: true}}
	if err := c.StoreJournal(context.Background(), aliyun.Journal{Kind: "remove", Token: "accepted-test", Before: 1, Target: 0, IDs: []string{"i-test"}, Activity: "activity-remove", AcceptedMask: 1, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	created := HistoricalActivity{ScalingActivityId: "activity-create", ScalingGroupId: c.GroupID, StatusCode: "Successful", TriggerSourceType: "Ess", CreatedCapacity: 1}
	created.CreatedInstances.CreatedInstance = []string{"i-test"}
	wire := &removingAPI{API: c.API, activities: []HistoricalActivity{
		created,
		{ScalingActivityId: "activity-desired", ScalingGroupId: c.GroupID, StatusCode: "Successful", TriggerSourceType: "Api"},
		{ScalingActivityId: "activity-remove", ScalingGroupId: c.GroupID, StatusCode: "InProgress", TriggerSourceType: "Api"},
	}}
	c.API = wire
	return c, a, wire, l
}

func TestRemovingCutpointRequiresAcceptedActivityAndAnExistingTarget(t *testing.T) {
	for _, state := range []string{"Running", "Stopping", "Stopped"} {
		t.Run(state, func(t *testing.T) {
			c, a, wire, l := removingFixture(t)
			i := a.fake.Instances["i-test"]
			i.Status = state
			a.fake.Instances[i.InstanceId] = i
			s, err := CaptureRemoving(context.Background(), c, l, "i-test", []string{"activity-create"})
			if err != nil || s.ActivityID != "activity-remove" || s.ActivityStatus != "InProgress" || s.ActivityTrigger != "Api" || s.JournalKind != "remove" || s.JournalTarget != 0 || s.InstanceID != "i-test" || s.ECSState != state || s.MemberLifecycle != "Removing" || s.ObservedAt.IsZero() || len(s.ActivityIDs) != 3 || wire.writes != 0 {
				t.Fatal("accepted removal timing proof failed or mutated cloud state", s, err, wire.writes)
			}
		})
	}
}

func TestRemovingCutpointRejectsCompletedForeignOrUnacceptedRemoval(t *testing.T) {
	for _, mode := range []string{"no-journal", "prepared-journal", "foreign-journal-target", "finished-activity", "failed-activity", "missing-activity", "wrong-trigger", "created-capacity", "created-id", "new-provisioning", "history-disappeared", "absent-member", "ready-member", "foreign-member", "foreign-owner", "no-baseline", "desired-one", "absent-ecs", "unknown-ecs", "extra-member", "incomplete-ledger", "wrong-target"} {
		t.Run(mode, func(t *testing.T) {
			c, a, wire, l := removingFixture(t)
			before := []string{"activity-create"}
			target := "i-test"
			switch mode {
			case "no-journal", "prepared-journal", "foreign-journal-target":
				// Set up the alternative journal before checking the probe's reads.
				c.API = wire.API
				if err := c.ClearJournal(context.Background()); err != nil {
					t.Fatal(err)
				}
				if mode != "no-journal" {
					j := aliyun.Journal{Kind: "remove", Token: "different", Before: 1, Target: 1, IDs: []string{"i-test"}, CreatedAt: time.Now().UTC()}
					if mode == "foreign-journal-target" {
						j.Target, j.Activity, j.AcceptedMask, j.IDs = 0, "activity-remove", 1, []string{"i-other"}
					}
					if err := c.StoreJournal(context.Background(), j); err != nil {
						t.Fatal(err)
					}
				}
				c.API = wire
			case "finished-activity":
				wire.activities[2].StatusCode = "Successful"
			case "failed-activity":
				wire.activities[2].StatusCode = "Failed"
			case "missing-activity":
				wire.activities = wire.activities[:2]
			case "wrong-trigger":
				wire.activities[2].TriggerSourceType = "Ess"
			case "created-capacity":
				wire.activities[2].CreatedCapacity = 1
			case "created-id":
				wire.activities[2].CreatedInstances.CreatedInstance = []string{"i-extra"}
			case "new-provisioning":
				wire.activities[1].TriggerSourceType = "Ess"
			case "history-disappeared":
				before = []string{"activity-missing"}
			case "absent-member":
				delete(a.fake.Members, target)
			case "ready-member", "foreign-member":
				m := a.fake.Members[target]
				if mode == "ready-member" {
					m.LifecycleState = "InService"
				} else {
					m.ScalingGroupId = "asg-foreign"
				}
				a.fake.Members[target] = m
			case "foreign-owner":
				l.RunID = "foreign"
			case "no-baseline":
				l.HistoryBaselineKnown = false
			case "desired-one":
				a.fake.Group.DesiredCapacity = 1
			case "absent-ecs":
				delete(a.fake.Instances, target)
			case "unknown-ecs":
				i := a.fake.Instances[target]
				i.Status = "Unknown"
				a.fake.Instances[target] = i
			case "extra-member":
				a.fake.Add("i-extra")
			case "incomplete-ledger":
				l.Instances[0].InventoryComplete = false
			case "wrong-target":
				target = "i-other"
			}
			if s, err := CaptureRemoving(context.Background(), c, l, target, before); err == nil || wire.writes != 0 {
				t.Fatal("unsafe removal cutpoint accepted or cloud mutated", s, err, wire.writes)
			}
		})
	}
}
