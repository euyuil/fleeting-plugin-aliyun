package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type releaseProbeAPI struct {
	aliyun.API
	probe func(context.Context, string) error
}

func (a releaseProbeAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if err := a.probe(ctx, action); err != nil {
		return err
	}
	return a.API.Call(ctx, service, action, q, out)
}

func releaseObservationFixture(t *testing.T, empty bool) (*aliyun.Client, *acceptanceAPI, *Ledger) {
	t.Helper()
	c, a, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	if empty {
		l.Instances = []Resource{{InstanceID: "i-deleted", InventoryComplete: true, SpotVerified: true, DiskIDs: []string{"d-test"}}}
	} else {
		a.fake.Add("i-worker")
		instance := a.fake.Instances["i-worker"]
		instance.AutoReleaseTime = l.StartedAt.Add(50 * time.Minute).UTC().Format(time.RFC3339)
		a.fake.Instances["i-worker"] = instance
	}
	return c, a, l
}

func TestNormalReleaseOwnDeadlineHasStableScopeAcrossReadsAndWait(t *testing.T) {
	for _, tc := range []struct {
		phase, action string
		occurrence    int
		empty         bool
	}{
		{"wait", "", 0, false},
		{"inventory", "DescribeScalingGroups", 1, false},
		{"group", "DescribeScalingGroups", 3, false},
		{"members", "DescribeScalingInstances", 3, false},
		{"instances", "DescribeInstances", 3, false},
		{"history", "DescribeScalingActivities", 2, false},
		{"associated", "DescribeDisks", 1, true},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			c, a, l := releaseObservationFixture(t, tc.empty)
			calls := 0
			c.API = jobDiagnosticAPI{releaseProbeAPI{API: c.API, probe: func(ctx context.Context, action string) error {
				if action == tc.action {
					calls++
					if calls == tc.occurrence {
						<-ctx.Done()
						return ctx.Err()
					}
				}
				return nil
			}}}
			before := a.mutations
			poll := time.Millisecond
			if tc.phase == "wait" {
				poll = time.Second
			}
			err := waitForNormalRelease(t.Context(), c, l, "ecs.test.small", "image-test", "cn-test-1a", 150*time.Millisecond, poll)
			if !errors.Is(err, context.DeadlineExceeded) || liveFailure(err) != "FAIL_INFRASTRUCTURE" {
				t.Fatal("confirmation deadline was lost or became a provider verdict", err)
			}
			got := l.LastNormalRelease
			if got == nil || got.Scope != "normal_release" || got.Phase != tc.phase || got.Result != "FAIL" || a.mutations != before {
				t.Fatalf("wrong release observation or fallback mutation: %+v, %v", got, err)
			}
			if tc.action != "" {
				var operation *operationError
				if !errors.As(err, &operation) || operation.operation != tc.action {
					t.Fatal("original API operation chain was lost", err)
				}
			}
			if tc.phase == "inventory" && (got.Sample != nil || got.SampleAge != nil) {
				t.Fatal("partial first round invented a sample")
			}
			if tc.phase == "associated" && (got.Sample == nil || got.Sample.Stable != 2 || got.Associated != "unconfirmed") {
				t.Fatal("associated read was certified before it completed", got)
			}
		})
	}
}

func TestNormalReleaseParentAndIndependentErrorsRemainDistinct(t *testing.T) {
	for _, mode := range []string{"parent_deadline", "parent_canceled", "operation_deadline", "cloud_error", "cloud_error_racing_deadline"} {
		t.Run(mode, func(t *testing.T) {
			c, _, l := releaseObservationFixture(t, false)
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "parent_deadline" {
				var stop context.CancelFunc
				parent, stop = context.WithTimeout(parent, 30*time.Millisecond)
				defer stop()
			}
			cloud := &aliyun.Error{Code: "Forbidden", Retryable: false}
			c.API = jobDiagnosticAPI{releaseProbeAPI{API: c.API, probe: func(ctx context.Context, _ string) error {
				switch mode {
				case "parent_canceled":
					cancel()
					return ctx.Err()
				case "parent_deadline":
					<-ctx.Done()
					return ctx.Err()
				case "operation_deadline":
					return context.DeadlineExceeded
				case "cloud_error_racing_deadline":
					<-ctx.Done()
					return cloud
				default:
					return cloud
				}
			}}}
			err := waitForNormalRelease(parent, c, l, "ecs.test.small", "image-test", "cn-test-1a", 150*time.Millisecond, time.Millisecond)
			want := mode
			if mode == "cloud_error" || mode == "cloud_error_racing_deadline" {
				want = "none"
				var original *aliyun.Error
				if !errors.As(err, &original) || original != cloud {
					t.Fatal("cloud error was overwritten by a local deadline", err)
				}
			} else if mode == "parent_canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if err == nil || l.LastNormalRelease.Scope != want || l.LastNormalRelease.Sample != nil {
				t.Fatalf("wrong error ownership: %+v, %v", l.LastNormalRelease, err)
			}
		})
	}
}

func TestNormalReleaseRetainsCompleteSampleAcrossPartialReadAndCleanup(t *testing.T) {
	c, a, l := releaseObservationFixture(t, false)
	calls := 0
	c.API = jobDiagnosticAPI{releaseProbeAPI{API: c.API, probe: func(ctx context.Context, action string) error {
		if action == "DescribeScalingGroups" {
			calls++
			// The next inventory round must not replace the completed first round.
			if calls == 4 {
				<-ctx.Done()
				return ctx.Err()
			}
		}
		return nil
	}}}
	if err := waitForNormalRelease(t.Context(), c, l, "ecs.test.small", "image-test", "cn-test-1a", 150*time.Millisecond, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	got := l.LastNormalRelease
	if got.Sample == nil || got.Sample.Desired != 1 || got.Sample.Instances != 1 || got.SampleAge == nil || got.Phase != "inventory" {
		t.Fatalf("last complete nonzero state was lost: %+v", got)
	}
	before, _ := json.Marshal(got)
	c.API = a
	if err := Cleanup(t.Context(), c, l, false); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(l.LastNormalRelease)
	if string(before) != string(after) || len(a.fake.Instances) != 0 || !l.Instances[0].DeletionVerified {
		t.Fatal("emergency cleanup rewrote failed normal-release evidence")
	}
}

func TestNormalReleaseSuccessfulEvidenceSurvivesDeferredCancel(t *testing.T) {
	c, _, l := releaseObservationFixture(t, true)
	if err := waitForNormalRelease(t.Context(), c, l, "ecs.test.small", "image-test", "cn-test-1a", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got := l.LastNormalRelease
	if got.Result != "PASS" || got.Scope != "none" || got.Sample.Stable != 2 || got.Associated != "verified" || !l.Instances[0].DeletionVerified {
		t.Fatalf("success acquired a synthetic cancellation: %+v", got)
	}
}
