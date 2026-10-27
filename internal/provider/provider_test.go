package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	provider "github.com/euyuil/fleeting-plugin-aliyun/internal/provider"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
	"github.com/hashicorp/go-hclog"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

func setup(t *testing.T, f *testutil.Fake, settings ...upstream.Settings) *provider.InstanceGroup {
	t.Helper()
	g := &provider.InstanceGroup{Region: f.Group.RegionId, ScalingGroupID: f.Group.ScalingGroupId, Client: &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}}
	s := upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{UseStaticCredentials: true, Username: "ci", Key: []byte("test-key")}}
	if len(settings) > 0 {
		s = settings[0]
	}
	info, err := g.Init(context.Background(), hclog.NewNullLogger(), s)
	if err != nil {
		t.Fatal(err)
	}
	if info.MaxSize != f.Group.MaxSize {
		t.Fatal("wrong maximum")
	}
	return g
}
func states(t *testing.T, g *provider.InstanceGroup) map[string]upstream.State {
	t.Helper()
	s := map[string]upstream.State{}
	if err := g.Update(context.Background(), func(id string, state upstream.State) { s[id] = state }); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestT01T02PendingIncreasesAndLimits(t *testing.T) {
	f := testutil.New()
	f.Group.MaxSize = 4
	g := setup(t, f)
	if n, err := g.Increase(context.Background(), 2); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	stale := f.Group
	stale.DesiredCapacity = 0
	f.StaleGroups = []aliyun.Group{stale}
	if n, err := g.Increase(context.Background(), 3); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if f.Group.DesiredCapacity != 4 {
		t.Fatal("lost pending capacity")
	}
	if n, err := g.Increase(context.Background(), 1); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}
func TestT03InterleavedDecreaseAndIncrease(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	f.Add("i-keep")
	g := setup(t, f)
	if n, err := g.Increase(context.Background(), 2); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	ids, err := g.Decrease(context.Background(), []string{"i-a"})
	if err != nil || !slices.Equal(ids, []string{"i-a"}) {
		t.Fatal(ids, err)
	}
	if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if f.Group.DesiredCapacity != 4 {
		t.Fatal(f.Group.DesiredCapacity)
	}
	if _, ok := f.Members["i-keep"]; !ok {
		t.Fatal("unrequested instance removed")
	}
}
func TestT04LostIncreaseReply(t *testing.T) {
	f := testutil.New()
	g := setup(t, f)
	first := true
	f.After = func(a string, q map[string]string) error {
		if a == "ScaleWithAdjustment" && first {
			first = false

			return &aliyun.Error{Code: "TransportError", Retryable: true}
		}
		return nil
	}
	if n, err := g.Increase(context.Background(), 2); n != 0 || err == nil {
		t.Fatal(n, err)
	}
	if n, err := g.Increase(context.Background(), 2); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if f.Group.DesiredCapacity != 2 {
		t.Fatal("duplicate logical increase")
	}
	var tokens []string
	for _, c := range f.Calls {
		if c.Action == "ScaleWithAdjustment" {
			tokens = append(tokens, c.Query["ClientToken"])
		}
	}
	if len(tokens) == 0 || (len(tokens) > 1 && tokens[0] != tokens[1]) {
		t.Fatal("retry token changed")
	}
}
func TestT04LostRemovalReply(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	g := setup(t, f)
	first := true
	f.After = func(a string, q map[string]string) error {
		if a == "RemoveInstances" && first {
			first = false
			return &aliyun.Error{Code: "TransportError", Retryable: true}
		}
		return nil
	}
	if _, err := g.Decrease(context.Background(), []string{"i-a"}); err == nil {
		t.Fatal("expected lost response")
	}
	ids, err := g.Decrease(context.Background(), []string{"i-a"})
	if err != nil || !slices.Equal(ids, []string{"i-a"}) || f.Group.DesiredCapacity != 0 {
		t.Fatal(ids, err, f.Group.DesiredCapacity)
	}
	var tokens []string
	for _, c := range f.Calls {
		if c.Action == "RemoveInstances" {
			tokens = append(tokens, c.Query["ClientToken"])
		}
	}
	if len(tokens) != 2 || tokens[0] != tokens[1] {
		t.Fatal("removal token changed")
	}
}
func TestT05T06SpecifiedBatchRelease(t *testing.T) {
	for _, n := range []int{20, 21, 43} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := testutil.New()
			var ids []string
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("i-%03d", i)
				f.Add(id)
				ids = append(ids, id)
			}
			f.Add("i-keep")
			g := setup(t, f)
			got, err := g.Decrease(context.Background(), append(ids, ids[0]))
			if err != nil || len(got) != n {
				t.Fatal(len(got), err)
			}
			if _, err := g.Decrease(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if len(f.Members) != 1 {
				t.Fatal("unrequested member removed")
			}
			calls := 0
			for _, c := range f.Calls {
				if c.Action == "RemoveInstances" {
					calls++
					if c.Query["DecreaseDesiredCapacity"] != "true" {
						t.Fatal("normal deletion must decrement desired capacity")
					}
					if c.Query["InstanceId.21"] != "" {
						t.Fatal("oversized batch")
					}
				}
			}
			if calls != (n+19)/20 {
				t.Fatal(calls)
			}
		})
	}
}
func TestT07IgnoredAndFailedActivity(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	f.Add("i-b")
	f.Ignored["i-b"] = true
	g := setup(t, f)
	ids, err := g.Decrease(context.Background(), []string{"i-a", "i-b"})
	if err != nil || !slices.Equal(ids, []string{"i-a"}) {
		t.Fatal(ids, err)
	}
	if _, ok := f.Members["i-b"]; !ok {
		t.Fatal("ignored member removed")
	}
	if _, err = g.Decrease(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	f.ActivityStatus = "Failed"
	f.Ignored["i-b"] = false
	if _, err = g.Decrease(context.Background(), []string{"i-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.Decrease(context.Background(), nil); err == nil {
		t.Fatal("failed activity reported successful")
	}
}
func TestT08PaginationAndFailureDoesNotDelete(t *testing.T) {
	f := testutil.New()
	f.Group.MaxSize = 200
	for i := 0; i < 105; i++ {
		f.Add(fmt.Sprintf("i-%03d", i))
	}
	g := setup(t, f)
	if len(states(t, g)) != 105 {
		t.Fatal("incomplete snapshot")
	}
	f.Errors["DescribeScalingInstances"] = []error{nil, errors.New("pagination unavailable")}
	calls := 0
	if err := g.Update(context.Background(), func(string, upstream.State) { calls++ }); err == nil || calls != 0 {
		t.Fatal("partial snapshot was published", err, calls)
	}
	m := f.Members["i-000"]
	m.LifecycleState = "FutureUnknown"
	f.Members[m.InstanceId] = m
	if states(t, g)[m.InstanceId] != upstream.StateCreating {
		t.Fatal("unknown state reported running")
	}
}
func TestT09NoIPAndReadiness(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	i := f.Instances["i-a"]
	i.PublicIpAddress.IpAddress = nil
	i.VpcAttributes.PrivateIpAddress.IpAddress = nil
	f.Instances["i-a"] = i
	g := setup(t, f)
	if states(t, g)["i-a"] != upstream.StateRunning {
		t.Fatal("cloud running state lost")
	}
	if _, err := g.ConnectInfo(context.Background(), "i-a"); err == nil {
		t.Fatal("missing address accepted")
	}
}
func TestT10CancellationAndConcurrentMutations(t *testing.T) {
	f := testutil.New()
	g := setup(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Increase(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n, err := g.Increase(context.Background(), 1); n != 1 || err != nil {
				t.Errorf("%d %v", n, err)
			}
		}()
	}
	wg.Wait()
	if f.Group.DesiredCapacity != 10 {
		t.Fatal("lost concurrent increase")
	}
}
func TestT11T12DisappearanceReplacementAndRestart(t *testing.T) {
	f := testutil.New()
	f.Add("i-old")
	g := setup(t, f)
	states(t, g)
	delete(f.Members, "i-old")
	delete(f.Instances, "i-old")
	f.Add("i-replacement")
	s := states(t, g)
	if s["i-old"] != upstream.StateDeleted || s["i-replacement"] != upstream.StateRunning {
		t.Fatal(s)
	}
	g2 := setup(t, f)
	if states(t, g2)["i-replacement"] != upstream.StateRunning {
		t.Fatal("restart did not rediscover")
	}
	for _, c := range f.Calls {
		if c.Action == "ScaleWithAdjustment" {
			t.Fatal("duplicate replacement requested")
		}
	}
}
func TestT13APIErrorsAreNotUnhealthy(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	g := setup(t, f)
	f.Errors["DescribeInstances"] = []error{&aliyun.Error{Code: "Forbidden"}}
	if err := g.Heartbeat(context.Background(), "i-a"); err == nil || errors.Is(err, upstream.ErrInstanceUnhealthy) {
		t.Fatal(err)
	}
	i := f.Instances["i-a"]
	i.Status = "Stopped"
	f.Instances["i-a"] = i
	if !errors.Is(g.Heartbeat(context.Background(), "i-a"), upstream.ErrInstanceUnhealthy) {
		t.Fatal("stopped instance not unhealthy")
	}
}
func TestT14Preflight(t *testing.T) {
	tests := []func(*testutil.Fake){func(f *testutil.Fake) { f.Group.MinSize = 1 }, func(f *testutil.Fake) { f.Group.EnableDesiredCapacity = false }, func(f *testutil.Fake) { f.Group.LaunchTemplateVersion = "Latest" }, func(f *testutil.Fake) { f.Group.SpotInstanceRemedy = true }, func(f *testutil.Fake) { f.Rules = 1 }, func(f *testutil.Fake) { f.Tasks = 1 }, func(f *testutil.Fake) { f.Hooks = 1 }, func(f *testutil.Fake) {
		f.Add("i-foreign")
		m := f.Members["i-foreign"]
		m.CreationType = "Attached"
		f.Members[m.InstanceId] = m
	}}
	for i, change := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f := testutil.New()
			change(f)
			g := &provider.InstanceGroup{Region: f.Group.RegionId, ScalingGroupID: f.Group.ScalingGroupId, Client: &aliyun.Client{API: f, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}}
			_, err := g.Init(context.Background(), nil, upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{UseStaticCredentials: true, Username: "ci"}})
			if err == nil {
				t.Fatal("incompatible group accepted")
			}
		})
	}
}
func TestT15ConnectionOverridesAndRefresh(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	s := upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{OS: "linux", Arch: "arm64", Protocol: upstream.ProtocolSSH, ProtocolPort: 2222, Username: "custom", Key: []byte("secret"), UseStaticCredentials: true}}
	g := setup(t, f, s)
	a, err := g.ConnectInfo(context.Background(), "i-a")
	if err != nil || a.Arch != "arm64" || a.ProtocolPort != 2222 || a.Username != "custom" || a.InternalAddr != "10.0.0.1" {
		t.Fatal(a, err)
	}
	i := f.Instances["i-a"]
	i.PublicIpAddress.IpAddress = []string{"192.0.2.2"}
	f.Instances["i-a"] = i
	b, err := g.ConnectInfo(context.Background(), "i-a")
	if err != nil || b.ExternalAddr != "192.0.2.2" {
		t.Fatal(b, err)
	}
}
func TestWindowsStaticHTTPS(t *testing.T) {
	f := testutil.New()
	f.Add("i-windows")
	i := f.Instances["i-windows"]
	i.OSType = "windows"
	f.Instances[i.InstanceId] = i
	s := upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{UseStaticCredentials: true, Username: "ci", Password: "synthetic"}}
	g := setup(t, f, s)
	info, err := g.ConnectInfo(context.Background(), i.InstanceId)
	if err != nil || info.OS != "windows" || info.Protocol != upstream.ProtocolWinRMHttps || info.ProtocolPort != 5986 {
		t.Fatal(info, err)
	}
}
func TestT16UnsupportedSuspend(t *testing.T) {
	g := setup(t, testutil.New())
	if _, err := g.Suspend(context.Background(), []string{"i-a"}); !errors.Is(err, upstream.ErrSuspendResumeNotSupported) {
		t.Fatal(err)
	}
	if _, err := g.Resume(context.Background(), []string{"i-a"}); !errors.Is(err, upstream.ErrSuspendResumeNotSupported) {
		t.Fatal(err)
	}
}
func TestT17RepeatedDeleteAndOrphan(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	g := setup(t, f)
	for i := 0; i < 2; i++ {
		ids, err := g.Decrease(context.Background(), []string{"i-a"})
		if err != nil || len(ids) != 1 {
			t.Fatal(ids, err)
		}
	}
	if f.Group.DesiredCapacity != 0 {
		t.Fatal("repeated decrement")
	}
	f.Add("i-orphan")
	delete(f.Members, "i-orphan")
	if _, err := g.Decrease(context.Background(), []string{"i-orphan"}); err == nil {
		t.Fatal("orphan silently accepted")
	}
}
func TestStrictConfigurationDecode(t *testing.T) {
	var g provider.InstanceGroup
	if err := json.Unmarshal([]byte(`{"region":"cn-test-1","scaling_group_id":"asg-test","regoin":"typo"}`), &g); err == nil {
		t.Fatal("unknown field accepted")
	}
}
func TestShutdownDoesNotDelete(t *testing.T) {
	f := testutil.New()
	f.Add("i-a")
	g := setup(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := g.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.Members) != 1 {
		t.Fatal("shutdown destroyed workers")
	}
	if _, err := g.Increase(ctx, 1); err == nil {
		t.Fatal("mutation after shutdown")
	}
}

func TestEntrustedECSMemberIsExplicitlyReleasable(t *testing.T) {
	f := testutil.New()
	f.Add("i-managed")
	m := f.Members["i-managed"]
	m.CreationType = "Attached"
	m.Entrusted = true
	f.Members[m.InstanceId] = m
	g := setup(t, f)
	ids, err := g.Decrease(context.Background(), []string{m.InstanceId})
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
}
