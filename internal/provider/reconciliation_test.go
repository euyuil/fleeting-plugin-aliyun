package provider_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	provider "github.com/euyuil/fleeting-plugin-aliyun/internal/provider"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
	"testing"
	"time"
)

func TestRegressionStaleDesiredAfterRemoval(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	f.Add("i-b")
	g := setup(t, f)
	stale := f.Group
	f.StaleGroups = []aliyun.Group{stale}
	if _, err := g.Decrease(context.Background(), []string{"i-a"}); err != nil {
		t.Fatal(err)
	}
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil {
		t.Fatalf("stale removal snapshot blocks next Increase: count=%d err=%v; cloud desired=%d", n, err, f.Group.DesiredCapacity)
	}
}

type reviewEmptyAPI struct {
	fake  *testutil.Fake
	empty bool
}

func (a *reviewEmptyAPI) Call(ctx context.Context, s, op string, q map[string]string, out any) error {
	if a.empty && (op == "DescribeInstances" || op == "DescribeScalingInstances") {
		return nil
	}
	return a.fake.Call(ctx, s, op, q, out)
}
func TestRegressionEmptyResponseMustNotDeleteKnownInstances(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	g := setup(t, f)
	states(t, g)
	a := &reviewEmptyAPI{fake: f, empty: true}
	g.Client.API = a
	deleted := false
	err := g.Update(context.Background(), func(id string, state upstream.State) {
		if state == upstream.StateDeleted {
			deleted = true
		}
	})
	if err == nil || deleted {
		t.Fatalf("empty malformed API response treated as valid snapshot: err=%v deleted=%v", err, deleted)
	}
}

func TestLostIncreaseReplyThenDiscoveredWorkerCanBeDeleted(t *testing.T) {
	f := testutil.New()
	g := setup(t, f)
	f.Errors["ScaleWithAdjustment"] = []error{&aliyun.Error{Code: "TransportError", Retryable: true}}
	if _, err := g.Increase(context.Background(), 1); err == nil {
		t.Fatal("expected response loss")
	}
	f.Add("i-new")
	states(t, g)
	if ids, err := g.Decrease(context.Background(), []string{"i-new"}); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
}

func TestRestartRecoversAcceptedRemovalJournal(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	f.Add("i-keep")
	g := setup(t, f)
	if ids, err := g.Decrease(context.Background(), []string{"i-a"}); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	if f.Group.Tag("fleeting-operation") == "" {
		t.Fatal("missing durable journal")
	}
	stale := f.Group
	stale.DesiredCapacity = 2
	f.StaleGroups = []aliyun.Group{stale}
	restart := setup(t, f)
	if n, err := restart.Increase(context.Background(), 1); err != nil || n != 1 || f.Group.DesiredCapacity != 2 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
	if _, ok := f.Members["i-keep"]; !ok {
		t.Fatal("unrequested instance deleted")
	}
}
func TestFailedRemovalRollsBackAndAllowsRetry(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	f.Add("i-keep")
	g := setup(t, f)
	if _, err := g.Decrease(context.Background(), []string{"i-a"}); err != nil {
		t.Fatal(err)
	}
	f.ActivityStatus = "Failed"
	f.Before = func(action string, q map[string]string) error {
		if action == "DescribeScalingActivities" {
			f.Group.DesiredCapacity = 2
			m := f.Members["i-a"]
			m.LifecycleState = "InService"
			f.Members["i-a"] = m
		}
		return nil
	}
	if _, err := g.Decrease(context.Background(), []string{"i-a"}); err == nil {
		t.Fatal("failed activity must report error")
	}
	f.Before = nil
	f.ActivityStatus = ""
	if ids, err := g.Decrease(context.Background(), []string{"i-a"}); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	if _, err := g.Decrease(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if f.Group.DesiredCapacity != 1 || len(f.Members) != 1 {
		t.Fatal("capacity deducted twice")
	}
}

func TestAcceptedJournalWriteLostResponseRecoversSameProcess(t *testing.T) {
	for _, mode := range []string{"increase", "remove"} {
		t.Run(mode, func(t *testing.T) {
			f := testutil.New()
			if mode == "remove" {
				f.Add("i-a")
			}
			g := setup(t, f)
			first := true
			f.After = func(action string, q map[string]string) error {
				if action == "TagResources" && first {
					first = false
					return &aliyun.Error{Code: "TransportError", Retryable: true}
				}
				return nil
			}
			if mode == "increase" {
				if _, err := g.Increase(context.Background(), 1); err == nil {
					t.Fatal("expected journal response loss")
				}
			} else {
				if _, err := g.Decrease(context.Background(), []string{"i-a"}); err == nil {
					t.Fatal("expected journal response loss")
				}
			}
			for _, call := range f.Calls {
				if call.Action == "ScaleWithAdjustment" || call.Action == "RemoveInstances" {
					t.Fatal("billable write preceded journal confirmation")
				}
			}
			if mode == "increase" {
				if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil {
					t.Fatal(n, err)
				}
			} else {
				if ids, err := g.Decrease(context.Background(), []string{"i-a"}); len(ids) != 1 || err != nil {
					t.Fatal(ids, err)
				}
			}
		})
	}
}

func TestRestartPreparedIncreaseDoesNotDoubleAllocate(t *testing.T) {
	f := testutil.New()
	g := setup(t, f)
	first := true
	f.After = func(action string, q map[string]string) error {
		if action == "TagResources" && first {
			first = false
			return &aliyun.Error{Code: "TransportError", Retryable: true}
		}
		return nil
	}
	if _, err := g.Increase(context.Background(), 1); err == nil {
		t.Fatal("expected prepared journal response loss")
	}
	f.After = nil
	restarted := setup(t, f)
	if n, err := restarted.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 1 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
}

func TestRestartAfterLostIncreaseResponseDoesNotReplayAcceptedMutation(t *testing.T) {
	f := testutil.New()
	f.Group.MaxSize = 1
	g := setup(t, f)
	f.After = func(action string, _ map[string]string) error {
		if action == "ScaleWithAdjustment" {
			return &aliyun.Error{Code: "TransportError", Retryable: true}
		}
		return nil
	}
	if n, err := g.Increase(context.Background(), 1); n != 0 || !aliyun.IsAmbiguous(err) {
		t.Fatal(n, err)
	}
	if f.Group.DesiredCapacity != 1 {
		t.Fatal("cloud mutation did not happen")
	}
	f.After = nil
	restarted := setup(t, f)
	if n, err := restarted.Increase(context.Background(), 1); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	f.Add("i-worker")
	if got := states(t, restarted); len(got) != 1 || got["i-worker"] != upstream.StateRunning {
		t.Fatal(got)
	}
	if _, err := restarted.Decrease(context.Background(), []string{"i-worker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Decrease(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, call := range f.Calls {
		if call.Action == "ScaleWithAdjustment" {
			writes++
		}
	}
	if writes != 1 || f.Group.DesiredCapacity != 0 || len(f.Instances) != 0 {
		t.Fatal("accepted mutation replayed or release failed", writes)
	}
}

func TestRestartAfterAcceptedIncreaseWaitsForOneWorkerWithoutReplay(t *testing.T) {
	f := testutil.New()
	f.Group.MaxSize = 1
	g := setup(t, f)
	if n, err := g.Increase(context.Background(), 1); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	f.Group.PendingCapacity = 1
	restarted := setup(t, f)
	if got := states(t, restarted); len(got) != 0 {
		t.Fatal("unassigned pending worker was invented", got)
	}
	f.Add("i-creating")
	i := f.Instances["i-creating"]
	i.Status = "Starting"
	f.Instances[i.InstanceId] = i
	m := f.Members[i.InstanceId]
	m.LifecycleState = "Pending"
	f.Members[i.InstanceId] = m
	if got := states(t, restarted); got[i.InstanceId] != upstream.StateCreating {
		t.Fatal("new process did not preserve creating state", got)
	}
	i.Status, m.LifecycleState, f.Group.PendingCapacity = "Running", "InService", 0
	f.Instances[i.InstanceId], f.Members[i.InstanceId] = i, m
	if got := states(t, restarted); len(got) != 1 || got[i.InstanceId] != upstream.StateRunning {
		t.Fatal("new process did not discover the completed original worker", got)
	}
	if ids, err := restarted.Decrease(context.Background(), []string{i.InstanceId}); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	if _, err := restarted.Decrease(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	scaleCalls := 0
	for _, call := range f.Calls {
		if call.Action == "ScaleWithAdjustment" {
			scaleCalls++
		}
	}
	if scaleCalls != 1 || f.Group.DesiredCapacity != 0 || len(f.Instances) != 0 {
		t.Fatal("accepted increase was replayed or final release failed", scaleCalls, f.Group)
	}
}

func TestUpdatePublicationGateHonorsCancellation(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	g := setup(t, f)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- g.Update(context.Background(), func(string, upstream.State) { close(entered); <-release })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := g.Update(ctx, func(string, upstream.State) { t.Error("canceled update published") })
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestRestartDiscoveredCapacityDoesNotFundNewDemand(t *testing.T) {
	f := testutil.New()
	g := setup(t, f)
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	f.Add("i-observed")
	restarted := setup(t, f)
	states(t, restarted)
	if n, err := restarted.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 2 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
}

func TestRestartAdoptsPendingDesiredWithoutJournal(t *testing.T) {
	f := testutil.New()
	f.Group.DesiredCapacity = 1
	g := setup(t, f)
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 1 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
	for _, call := range f.Calls {
		if call.Action == "ScaleWithAdjustment" {
			t.Fatal("pending desired was allocated twice")
		}
	}
}

func TestRestartCreditShrinksAfterDiscovery(t *testing.T) {
	f := testutil.New()
	f.Group.DesiredCapacity = 2
	g := setup(t, f)
	f.Add("i-observed-a")
	f.Add("i-observed-b")
	states(t, g)
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 3 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
}

func TestRestartAfterAcceptedRemovalFinishesWithoutReplay(t *testing.T) {
	f := testutil.New()
	f.Group.MaxSize = 1
	f.Add("i-worker")
	f.ActivityStatus = "InProgress"
	g := setup(t, f)
	if ids, err := g.Decrease(context.Background(), []string{"i-worker"}); err != nil || len(ids) != 1 || ids[0] != "i-worker" {
		t.Fatal(ids, err)
	}
	j, err := aliyun.ReadJournal(f.Group)
	if err != nil || j == nil || j.Activity == "" || j.AcceptedMask != 1 || j.Target != 0 || len(f.Instances) != 1 {
		t.Fatal("missing accepted removal or target already absent", j, err)
	}
	restarted := setup(t, f)
	if got := states(t, restarted); got["i-worker"] != upstream.StateDeleting {
		t.Fatal("new process did not restore the deleting target", got)
	}
	f.ActivityStatus = "Successful"
	if _, err := restarted.Decrease(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	j, err = aliyun.ReadJournal(f.Group)
	if err != nil || j != nil || f.Group.DesiredCapacity != 0 || len(f.Instances) != 0 || len(f.Members) != 0 {
		t.Fatal("accepted removal did not converge and clear its journal", j, err)
	}
	removals := 0
	for _, call := range f.Calls {
		if call.Action == "ScaleWithAdjustment" {
			t.Fatal("removal recovery changed capacity through another scaling request")
		}
		if call.Action == "RemoveInstances" {
			removals++
		}
	}
	if removals != 1 {
		t.Fatal("accepted removal was replayed", removals)
	}
}

func TestAcceptedRemovalFactsAreRetriedBeforeProgress(t *testing.T) {
	f := testutil.New()
	f.Add("i-worker")
	g := setup(t, f)
	writes := 0
	f.Before = func(action string, _ map[string]string) error {
		if action == "TagResources" {
			writes++
			if writes == 2 {
				return &aliyun.Error{Code: "Forbidden"}
			}
		}
		return nil
	}
	if _, err := g.Decrease(context.Background(), []string{"i-worker"}); err == nil {
		t.Fatal("expected accepted-facts write failure")
	}
	f.Before = nil
	f.ActivityStatus = "InProgress"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Decrease(ctx, []string{"i-worker"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	j, err := aliyun.ReadJournal(f.Group)
	if err != nil || j == nil || j.Activity == "" || j.AcceptedMask != 1 {
		t.Fatalf("accepted facts not recovered: %+v %v", j, err)
	}
	restarted := setup(t, f)
	f.ActivityStatus = "Successful"
	if ids, err := restarted.Decrease(context.Background(), []string{"i-worker"}); err != nil || len(ids) != 1 || f.Group.DesiredCapacity != 0 {
		t.Fatal(ids, err, f.Group.DesiredCapacity)
	}
	removals := 0
	for _, call := range f.Calls {
		if call.Action == "RemoveInstances" {
			removals++
		}
	}
	if removals != 1 {
		t.Fatal("accepted removal was reissued", removals)
	}
}

func TestRestartDuringRemovalAdoptsReplacementIntent(t *testing.T) {
	f := testutil.New()
	f.Add("i-preempted")
	member := f.Members["i-preempted"]
	member.LifecycleState = "Removing"
	f.Members[member.InstanceId] = member
	g := setup(t, f)
	delete(f.Members, member.InstanceId)
	delete(f.Instances, member.InstanceId)
	states(t, g)
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 1 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
	for _, call := range f.Calls {
		if call.Action == "ScaleWithAdjustment" {
			t.Fatal("replacement intent allocated twice")
		}
	}
}

func TestObservedLostReplyCapacityDoesNotFundNewDemand(t *testing.T) {
	f := testutil.New()
	g := setup(t, f)
	first := true
	f.After = func(action string, _ map[string]string) error {
		if action == "ScaleWithAdjustment" && first {
			first = false
			return &aliyun.Error{Code: "TransportError", Retryable: true}
		}
		return nil
	}
	if _, err := g.Increase(context.Background(), 1); err == nil {
		t.Fatal("expected lost reply")
	}
	f.Add("i-observed")
	states(t, g)
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 2 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
}

func TestFailedAdmissionCannotEnterProviderMethods(t *testing.T) {
	f := testutil.New()
	f.Add("i-worker")
	f.Rules = 1
	g := &provider.InstanceGroup{Region: f.Group.RegionId, ScalingGroupID: f.Group.ScalingGroupId, Client: &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}}
	_, err := g.Init(context.Background(), nil, upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{UseStaticCredentials: true, Username: "ci"}})
	if err == nil {
		t.Fatal("conflicting group admitted")
	}
	if _, err := g.Decrease(context.Background(), []string{"i-worker"}); err == nil {
		t.Fatal("failed provider accepted Decrease")
	}
	if err := g.Update(context.Background(), func(string, upstream.State) { t.Error("failed provider published states") }); err == nil {
		t.Fatal("failed provider accepted Update")
	}
	for _, call := range f.Calls {
		if call.Action == "RemoveInstances" {
			t.Fatal("failed admission released a worker")
		}
	}
}

func TestRecoveredPendingCreditSurvivesSpecifiedRemoval(t *testing.T) {
	f := testutil.New()
	f.Add("i-old")
	f.Group.DesiredCapacity = 2
	g := setup(t, f)
	states(t, g)
	if _, err := g.Decrease(context.Background(), []string{"i-old"}); err != nil {
		t.Fatal(err)
	}
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 1 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
}

func TestRecoveredCreditSurvivesRetriedRemovalAcceptance(t *testing.T) {
	f := testutil.New()
	f.Add("i-old")
	f.Group.DesiredCapacity = 2
	g := setup(t, f)
	states(t, g)
	writes := 0
	f.Before = func(action string, _ map[string]string) error {
		if action == "TagResources" {
			writes++
			if writes == 2 {
				return &aliyun.Error{Code: "Forbidden"}
			}
		}
		return nil
	}
	if _, err := g.Decrease(context.Background(), []string{"i-old"}); err == nil {
		t.Fatal("expected accepted-facts write failure")
	}
	f.Before = nil
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 1 {
		t.Fatal(n, err, f.Group.DesiredCapacity)
	}
}

func TestRestartRemovalWithStaleLifecyclePreservesPendingIntent(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprint(prepared), func(t *testing.T) {
			f := testutil.New()
			f.Add("i-old")
			f.Group.DesiredCapacity = 2
			g := setup(t, f)
			states(t, g)
			if prepared {
				writes := 0
				f.Before = func(action string, _ map[string]string) error {
					if action == "TagResources" {
						writes++
						if writes == 2 {
							return &aliyun.Error{Code: "Forbidden"}
						}
					}
					return nil
				}
			}
			_, err := g.Decrease(context.Background(), []string{"i-old"})
			if (err != nil) != prepared {
				t.Fatal(err)
			}
			f.Before = nil
			member := f.Members["i-old"]
			member.LifecycleState = "InService"
			f.Members[member.InstanceId] = member
			restarted := setup(t, f)
			if n, err := restarted.Increase(context.Background(), 1); n != 1 || err != nil || f.Group.DesiredCapacity != 1 {
				t.Fatal(n, err, f.Group.DesiredCapacity)
			}
		})
	}
}
