package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/testutil"
)

type acceptanceAPI struct {
	fake      *testutil.Fake
	mutations int
}

func (a *acceptanceAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == "TagResources" {
		a.mutations++
		return a.fake.Call(ctx, service, action, q, out)
	}

	if action == "ScaleWithAdjustment" {
		a.mutations++
		if q["AdjustmentValue"] == "0" {
			a.fake.Members = map[string]aliyun.Member{}
			a.fake.Instances = map[string]aliyun.Instance{}
			a.fake.Group.TotalCapacity = 0
		}
	}
	return a.fake.Call(ctx, service, action, q, out)
}
func fixture() (*aliyun.Client, *acceptanceAPI, *Ledger) {
	f := testutil.New()
	f.Group.Tags.Tag = append(f.Group.Tags.Tag, struct {
		TagKey, TagValue string
		Propagate        bool
	}{ProjectTag, "owner/repo", false})
	api := &acceptanceAPI{fake: f}
	c := &aliyun.Client{API: api, Region: f.Group.RegionId, GroupID: f.Group.ScalingGroupId}
	l := &Ledger{SchemaVersion: 1, Project: "owner/repo", Region: c.Region, GroupID: c.GroupID, RunID: "100", Attempt: "1", StartedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	return c, api, l
}
func TestCleanupOwnershipAndExpiry(t *testing.T) {
	c, a, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	a.fake.Add("i-worker")
	before := a.mutations
	if err := Cleanup(context.Background(), c, l, true); err == nil || a.mutations != before {
		t.Fatal("janitor touched an unexpired run", err)
	}
	wrong := *l
	wrong.RunID = "foreign"
	if err := Cleanup(context.Background(), c, &wrong, false); err == nil || a.mutations != before {
		t.Fatal("cleanup touched another run", err)
	}
	a.fake.Members["i-foreign"] = aliyun.Member{InstanceId: "i-foreign", ScalingGroupId: c.GroupID, CreationType: "Attached"}
	if err := Cleanup(context.Background(), c, l, false); err == nil || a.mutations != before {
		t.Fatal("cleanup touched an attached member", err)
	}
}
func TestCleanupReleaseAndRepeat(t *testing.T) {
	c, a, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	a.fake.Add("i-worker")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Cleanup(ctx, c, l, false); err != nil {
		t.Fatal(err)
	}
	if l.CleanupResult != "PASS" || len(l.Instances) != 1 || !l.Instances[0].DeletionVerified {
		t.Fatal(l)
	}
	if err := Cleanup(ctx, c, l, false); err != nil {
		t.Fatal("cleanup is not idempotent", err)
	}
}

func TestCleanupTokensSeparateScenarioGroupsAndReplayRetries(t *testing.T) {
	tokens := map[string]string{}
	calls := 0
	interrupted := errors.New("simulated observation interruption after accepted cleanup")
	for _, mode := range []string{"cloud-smoke", "cloud-scale"} {
		c, a, l := fixture()
		c.GroupID = "asg-" + mode
		a.fake.Group.ScalingGroupId = c.GroupID
		l.GroupID, l.Mode = c.GroupID, mode
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := Claim(ctx, c, l); err != nil {
			t.Fatal(err)
		}
		first, failRead := true, false
		a.fake.Before = func(action string, q map[string]string) error {
			if action == "DescribeScalingGroups" && failRead {
				failRead = false
				return interrupted
			}
			if action != "ScaleWithAdjustment" {
				return nil
			}
			token := q["ClientToken"]
			if token == "" || len(token) > 64 {
				t.Fatal("cleanup token must fit the API contract")
			}
			request, err := json.Marshal(q)
			if err != nil {
				t.Fatal(err)
			}
			if previous, exists := tokens[token]; exists && previous != string(request) {
				return &aliyun.Error{Code: "IdempotentParameterMismatch"}
			}
			tokens[token] = string(request)
			calls++
			if first {
				first, failRead = false, true
			}
			return nil
		}
		if err := Cleanup(ctx, c, l, false); !errors.Is(err, interrupted) {
			t.Fatalf("%s: expected interruption after accepted cleanup, got %v", mode, err)
		}
		if err := Cleanup(ctx, c, l, false); err != nil || l.CleanupResult != "PASS" {
			t.Fatalf("%s: cleanup retry failed: %v", mode, err)
		}
	}
	if calls != 4 || len(tokens) != 2 {
		t.Fatalf("expected one stable token per group, got %d calls and %d tokens", calls, len(tokens))
	}
}

func TestMissingConfigurationNeverUsesCloud(t *testing.T) {
	t.Setenv("RUN_LIVE_ACCEPTANCE", "")
	r := Run(context.Background(), t.TempDir())
	if r.Result != "NOT_RUN_MISSING_CONFIGURATION" || len(r.Missing) == 0 || r.CleanupResult != "NOT_RUN" {
		t.Fatal(r)
	}
}
func TestArtifactCorrelation(t *testing.T) {
	_, _, l := fixture()
	l.Instances = []Resource{{InstanceID: "i-test", SpotVerified: true}}
	a := Artifact{Nonce: "nonce", FixtureSHA: "sha", InstanceID: "i-test", RunnerID: 5, CheckoutOK: true, CommandOK: true}
	if err := VerifyArtifact(a, l, "nonce", "sha", 5); err != nil {
		t.Fatal(err)
	}
	a.Nonce = "old"
	if err := VerifyArtifact(a, l, "nonce", "sha", 5); err == nil {
		t.Fatal("stale artifact accepted")
	}
	a.Nonce = "nonce"
	l.Instances[0].SpotVerified = false
	if err := VerifyArtifact(a, l, "nonce", "sha", 5); err == nil {
		t.Fatal("unverified spot accepted")
	}
	_ = errors.New("")
}

func TestLedgerSavePersistsRecoveryFields(t *testing.T) {
	c, _, l := fixture()
	l.Path = filepath.Join(t.TempDir(), "ledger.json")
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	l.ManagerPID = 12345
	l.ManagerDirectory = "manager-test"
	l.SecurityGroupID = "sg-test"
	l.IngressCIDR = "192.0.2.1/32"
	if err := l.Save(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	var disk Ledger
	if err = json.Unmarshal(b, &disk); err != nil {
		t.Fatal(err)
	}
	if !disk.HistoryBaselineKnown || disk.ManagerPID != l.ManagerPID || disk.SecurityGroupID != l.SecurityGroupID {
		t.Fatal("recovery fields not persisted")
	}
}
