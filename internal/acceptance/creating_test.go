package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type creatingAPI struct {
	aliyun.API
	body, activityState string
	activityInstances   []string
	controlRecord       bool
	controlStatus       string
	controlInstances    []string
	controlCapacity     int
	writes              int
}

func (a *creatingAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if !strings.HasPrefix(action, "Describe") {
		a.writes++
	}
	if action == "DescribeScalingInstances" {
		return json.Unmarshal([]byte(a.body), out)
	}
	if action == "DescribeScalingActivities" {
		activities := []map[string]any{}
		if a.controlRecord {
			activities = append(activities, map[string]any{"ScalingActivityId": "activity-desired", "ScalingGroupId": "asg-test", "StatusCode": a.controlStatus, "TriggerSourceType": "Api", "CreatedCapacity": a.controlCapacity, "CreatedInstances": map[string]any{"CreatedInstance": a.controlInstances}})
		}
		activities = append(activities, map[string]any{"ScalingActivityId": "activity-create", "ScalingGroupId": "asg-test", "StatusCode": a.activityState, "TriggerSourceType": "Ess", "CreatedInstances": map[string]any{"CreatedInstance": a.activityInstances}})
		b, _ := json.Marshal(map[string]any{"TotalCount": len(activities), "ScalingActivities": map[string]any{"ScalingActivity": activities}})
		return json.Unmarshal(b, out)
	}
	return a.API.Call(ctx, service, action, q, out)
}

func TestCreatingCutpointSeparatesDesiredCapacityRecordFromProvisioning(t *testing.T) {
	for _, mode := range []string{"zero-success", "in-progress", "created-capacity", "created-id"} {
		t.Run(mode, func(t *testing.T) {
			c, _, wire, l := creatingFixture(t, `{"TotalCount":1,"ScalingInstances":{"ScalingInstance":[{"ScalingGroupId":"asg-test","LifecycleState":"Init"}]}}`)
			wire.controlRecord, wire.controlStatus = true, "Successful"
			switch mode {
			case "in-progress":
				wire.controlStatus = "InProgress"
			case "created-capacity":
				wire.controlCapacity = 1
			case "created-id":
				wire.controlInstances = []string{"i-extra"}
			}
			s, err := CaptureCreating(context.Background(), c, l)
			if (err == nil) != (mode == "zero-success") || wire.writes != 0 {
				t.Fatal("API record incorrectly changed provisioning proof", s, err)
			}
			if err == nil && (s.APIRecords != 1 || s.ActivityTrigger != "Ess" || s.ActivityID != "activity-create" || len(s.ActivityIDs) != 2) {
				t.Fatal("provisioning identity or full history was lost", s)
			}
		})
	}
}

func TestCreatingCutpointChecksHistoryIdentityDespiteLaggingMembership(t *testing.T) {
	for _, body := range []string{
		`{"TotalCount":0,"ScalingInstances":{"ScalingInstance":[]}}`,
		`{"TotalCount":1,"ScalingInstances":{"ScalingInstance":[{"ScalingGroupId":"asg-test","LifecycleState":"Init"}]}}`,
	} {
		for _, state := range []string{"Running", "Starting"} {
			c, a, wire, l := creatingFixture(t, body)
			wire.activityInstances = []string{"i-test"}
			a.fake.Instances["i-test"] = aliyun.Instance{InstanceId: "i-test", Status: state}
			s, err := CaptureCreating(context.Background(), c, l)
			if (err == nil) != (state == "Starting") || wire.writes != 0 {
				t.Fatal("activity identity bypassed ECS readiness", state, s, err)
			}
			if err == nil && (s.InstanceID != "i-test" || s.ECSState != "Starting") {
				t.Fatal("activity identity was lost", s)
			}
		}
	}
}

func creatingFixture(t *testing.T, body string) (*aliyun.Client, *acceptanceAPI, *creatingAPI, *Ledger) {
	t.Helper()
	c, a, l := fixture()
	a.fake.Group.MaxSize = 1
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	a.fake.Group.DesiredCapacity, a.fake.Group.TotalCapacity, a.fake.Group.PendingCapacity = 1, 1, 1
	if err := c.StoreJournal(context.Background(), aliyun.Journal{Kind: "increase", Token: "accepted-test", Target: 1, Count: 1, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	wire := &creatingAPI{API: c.API, body: body, activityState: "InProgress"}
	c.API = wire
	return c, a, wire, l
}

func TestCreatingCutpointRecognizesUnassignedInitWithoutPublishingMembers(t *testing.T) {
	for _, rows := range []string{`[]`, `[{"ScalingGroupId":"asg-test","LifecycleState":"Init","InstanceId":""}]`} {
		count := 0
		if rows != `[]` {
			count = 1
		}
		c, _, wire, l := creatingFixture(t, fmt.Sprintf(`{"TotalCount":%d,"ScalingInstances":{"ScalingInstance":%s}}`, count, rows))
		s, err := CaptureCreating(context.Background(), c, l)
		if err != nil || s.ActivityID != "activity-create" || s.InstanceID != "" || s.ECSState != "NotAssigned" || s.ObservedAt.IsZero() || wire.writes != 0 || len(l.Instances) != 0 {
			t.Fatal("creation timing proof failed or became resource admission", s, err)
		}
	}
}

func TestCreatingCutpointRejectsReadyForeignIncompleteOrUnacceptedState(t *testing.T) {
	valid := `{"TotalCount":1,"ScalingInstances":{"ScalingInstance":[{"ScalingGroupId":"asg-test","LifecycleState":"Init"}]}}`
	for _, mode := range []string{"ready", "foreign", "missing-count", "missing-rows", "count-mismatch", "finished-activity", "foreign-owner", "no-baseline", "no-journal", "history-two", "history-member-mismatch", "ecs-running", "ecs-starting"} {
		t.Run(mode, func(t *testing.T) {
			c, a, wire, l := creatingFixture(t, valid)
			switch mode {
			case "ready":
				wire.body = strings.ReplaceAll(valid, "Init", "InService")
			case "foreign":
				wire.body = strings.ReplaceAll(valid, "asg-test", "asg-foreign")
			case "missing-count":
				wire.body = `{"ScalingInstances":{"ScalingInstance":[]}}`
			case "missing-rows":
				wire.body = `{"TotalCount":0,"ScalingInstances":{}}`
			case "count-mismatch":
				wire.body = strings.ReplaceAll(valid, `"TotalCount":1`, `"TotalCount":0`)
			case "finished-activity":
				wire.activityState = "Successful"
			case "foreign-owner":
				l.RunID = "other-run"
			case "no-baseline":
				l.HistoryBaselineKnown = false
			case "no-journal":
				if err := c.ClearJournal(context.Background()); err != nil {
					t.Fatal(err)
				}
				wire.writes = 0
			case "history-two":
				wire.activityInstances = []string{"i-one", "i-two"}
			case "history-member-mismatch":
				wire.activityInstances = []string{"i-history"}
				wire.body = `{"TotalCount":1,"ScalingInstances":{"ScalingInstance":[{"ScalingGroupId":"asg-test","LifecycleState":"Pending","InstanceId":"i-member"}]}}`
			case "ecs-running", "ecs-starting":
				status := "Running"
				if mode == "ecs-starting" {
					status = "Starting"
				}
				wire.body = `{"TotalCount":1,"ScalingInstances":{"ScalingInstance":[{"ScalingGroupId":"asg-test","LifecycleState":"Pending","InstanceId":"i-test"}]}}`
				a.fake.Instances["i-test"] = aliyun.Instance{InstanceId: "i-test", Status: status}
			}
			_, err := CaptureCreating(context.Background(), c, l)
			if (err == nil) != (mode == "ecs-starting") || wire.writes != 0 {
				t.Fatal("incorrect creation cutpoint verdict", err, wire.writes)
			}
		})
	}
}
