package acceptance

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNormalReleaseDoesNotUseFallbackMutations(t *testing.T) {
	c, a, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	a.fake.Add("i-worker")
	queries := 0
	a.fake.Before = func(action string, q map[string]string) error {
		if action == "DescribeInstances" {
			queries++
			if queries == 5 {
				delete(a.fake.Members, "i-worker")
				delete(a.fake.Instances, "i-worker")
				a.fake.Group.DesiredCapacity = 0
				a.fake.Group.TotalCapacity = 0
			}
		}
		return nil
	}
	before := a.mutations
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForNormalRelease(ctx, c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
		t.Fatal(err)
	}
	if a.mutations != before || len(l.Instances) != 1 || !l.Instances[0].DeletionVerified {
		t.Fatal("fallback mutation was used")
	}
}
func TestNormalReleaseTimeoutDoesNotPass(t *testing.T) {
	c, a, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	a.fake.Add("i-worker")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := WaitForNormalRelease(ctx, c, l, "ecs.test.small", "image-test", "cn-test-1a"); !errors.Is(err, context.DeadlineExceeded) || liveFailure(err) != "FAIL_INFRASTRUCTURE" {
		t.Fatal("parent deadline was lost or mislabeled", err)
	}
	if len(a.fake.Members) != 1 {
		t.Fatal("normal acceptance silently cleaned instead of observing")
	}
}
func TestJanitorWithoutHistoryDoesNotCertifyUnknownInventory(t *testing.T) {
	c, a, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	a.fake.Add("i-worker")
	l.HistoryBaselineKnown = false
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Cleanup(ctx, c, l, false); err == nil {
		t.Fatal("unknown history certified")
	}
	if len(a.fake.Members) != 0 || l.CleanupResult == "PASS" {
		t.Fatal("resources not released or falsely certified")
	}
}
