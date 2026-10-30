package acceptance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type releaseDeadlineAPI struct {
	base       *acceptanceAPI
	writes     int
	afterWrite func()
}

func (a *releaseDeadlineAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == "ModifyInstanceAutoReleaseTime" {
		a.writes++
		at, err := time.Parse(time.RFC3339, q["AutoReleaseTime"])
		if err != nil || at.Truncate(time.Minute).Before(time.Now().Add(30*time.Minute)) {
			return errors.New("synthetic ECS rejection: automatic release requires 30 minutes of lead time")
		}
		if err := a.base.Call(ctx, service, action, q, out); err != nil {
			return err
		}
		if a.afterWrite != nil {
			a.afterWrite()
		}
		return nil
	}
	return a.base.Call(ctx, service, action, q, out)
}

func TestLateRecoveryWorkerGetsFixedConfirmedDeadline(t *testing.T) {
	for _, elapsed := range []time.Duration{35 * time.Minute, 49 * time.Minute} {
		c, base, l := fixture()
		l.StartedAt = time.Now().Add(-elapsed)
		l.WorkerExpiresAt = l.StartedAt.Add(90 * time.Minute).UTC().Truncate(time.Minute)
		// The legacy cleanup expiry must not control this worker deadline.
		l.ExpiresAt = time.Now().Add(5 * time.Minute)
		if err := Claim(context.Background(), c, l); err != nil {
			t.Fatal(err)
		}
		base.fake.Add("i-worker")
		api := &releaseDeadlineAPI{base: base}
		c.API = api
		for range 2 {
			if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
				t.Fatal(err)
			}
		}
		if api.writes != 1 || l.Instances[0].AutoReleaseAt != l.WorkerExpiresAt.Format(time.RFC3339) {
			t.Fatal("late worker deadline missing or extended", api.writes, l.Instances)
		}
	}
}

func TestUnconfirmedAutoReleaseIsNeverRecorded(t *testing.T) {
	for _, readback := range []string{"", "invalid", "2099-01-01T00:00Z", "2000-01-01T00:00Z"} {
		c, base, l := fixture()
		if err := Claim(context.Background(), c, l); err != nil {
			t.Fatal(err)
		}
		base.fake.Add("i-worker")
		api := &releaseDeadlineAPI{base: base, afterWrite: func() {
			worker := base.fake.Instances["i-worker"]
			worker.AutoReleaseTime = readback
			base.fake.Instances["i-worker"] = worker
		}}
		c.API = api
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		err := Observe(ctx, c, l, "ecs.test.small", "image-test", "cn-test-1a")
		cancel()
		if err == nil || api.writes != 1 || l.Instances[0].AutoReleaseAt != "" {
			t.Fatal("unconfirmed worker deadline recorded", err, l.Instances)
		}
	}
}

type releaseReadbackAPI struct {
	base  *acceptanceAPI
	reads int
	want  string
}

func (a *releaseReadbackAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == "DescribeInstances" {
		a.reads++
		if a.reads == 2 {
			worker := a.base.fake.Instances["i-worker"]
			worker.AutoReleaseTime = a.want
			a.base.fake.Instances["i-worker"] = worker
		}
	}
	return a.base.Call(ctx, service, action, q, out)
}

func TestAutoReleaseReadbackAllowsBoundedEventualVisibility(t *testing.T) {
	c, base, _ := fixture()
	base.fake.Add("i-worker")
	want := time.Now().Add(time.Hour).UTC().Truncate(time.Minute).Format(time.RFC3339)
	api := &releaseReadbackAPI{base: base, want: want}
	c.API = api
	if err := confirmWorkerAutoRelease(context.Background(), c, "i-worker", want); err != nil || api.reads != 2 {
		t.Fatal("eventual readback not confirmed", err, api.reads)
	}
}

func TestLegacyCleanupExpiryDoesNotShortenWorkerDeadline(t *testing.T) {
	c, base, l := fixture()
	l.ExpiresAt = l.StartedAt.Add(25 * time.Minute)
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	base.fake.Add("i-worker")
	if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
		t.Fatal(err)
	}
	if !l.WorkerExpiresAt.IsZero() || l.Instances[0].AutoReleaseAt != l.StartedAt.Add(time.Hour).UTC().Truncate(time.Minute).Format(time.RFC3339) {
		t.Fatal("legacy plugin-only fixture deadline changed", l.Instances)
	}
}

func TestWorkerAutoReleaseDeadlineBounds(t *testing.T) {
	started := time.Date(2026, 1, 1, 8, 0, 45, 0, time.UTC)
	for _, tc := range []struct {
		name, existing, want, failure string
		now                           time.Time
		write                         bool
	}{
		{name: "early missing deadline", now: started, want: "2026-01-01T09:00:00Z", write: true},
		{name: "minute precision inherited deadline", now: started.Add(40 * time.Minute), existing: "2026-01-01T09:00Z", want: "2026-01-01T09:00:00Z"},
		{name: "earlier seconds precision deadline", now: started.Add(40 * time.Minute), existing: "2026-01-01T08:55:00Z", want: "2026-01-01T08:55:00Z"},
		{name: "late missing deadline", now: started.Add(40 * time.Minute), failure: "insufficient lead time"},
		{name: "malformed deadline", now: started, existing: "not-a-time", failure: "invalid format"},
		{name: "expired deadline", now: started.Add(40 * time.Minute), existing: "2026-01-01T08:30Z", failure: "has expired"},
		{name: "deadline reached", now: started.Add(40 * time.Minute), existing: "2026-01-01T08:40:45Z", failure: "has expired"},
		{name: "deadline beyond ceiling", now: started, existing: "2026-01-01T09:01Z", failure: "exceeds the fixed scenario limit"},
		{name: "truncated minute leaves full margin", now: time.Date(2026, 1, 1, 8, 29, 0, 0, time.UTC), want: "2026-01-01T09:00:00Z", write: true},
		{name: "truncated minute loses margin", now: time.Date(2026, 1, 1, 8, 29, 1, 0, time.UTC), failure: "insufficient lead time"},
		{name: "future run start", now: started.Add(-time.Second), failure: "invalid run start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, write, err := workerAutoReleaseDeadline(started, started.Add(time.Hour).UTC().Truncate(time.Minute), tc.now, tc.existing)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) || at != "" || write {
					t.Fatalf("unsafe deadline accepted: at=%q write=%t err=%v", at, write, err)
				}
				return
			}
			if err != nil || at != tc.want || write != tc.write {
				t.Fatalf("at=%q write=%t err=%v", at, write, err)
			}
		})
	}
	if _, _, err := workerAutoReleaseDeadline(time.Time{}, started.Add(time.Hour), started, ""); err == nil {
		t.Fatal("zero run start accepted")
	}
}

func TestLateUnfencedWorkersFailWithoutMutation(t *testing.T) {
	for _, existing := range []string{"", "invalid", "2099-01-01T00:00Z", "2000-01-01T00:00Z"} {
		t.Run(existing, func(t *testing.T) {
			c, base, l := fixture()
			l.StartedAt = time.Now().Add(-40 * time.Minute)
			if err := Claim(context.Background(), c, l); err != nil {
				t.Fatal(err)
			}
			base.fake.Add("i-worker")
			worker := base.fake.Instances["i-worker"]
			worker.AutoReleaseTime = existing
			base.fake.Instances["i-worker"] = worker
			api := &releaseDeadlineAPI{base: base}
			c.API = api
			if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err == nil {
				t.Fatal("unsafe late worker accepted")
			}
			if api.writes != 0 || len(l.Instances) != 1 || l.Instances[0].AutoReleaseAt != "" {
				t.Fatal("unsafe late deadline mutated or certified")
			}
		})
	}
}

func TestAutoReleasePersistenceFailureFailsObservation(t *testing.T) {
	c, base, l := fixture()
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	base.fake.Add("i-worker")
	directory := t.TempDir()
	api := &releaseDeadlineAPI{base: base, afterWrite: func() { l.Path = directory }}
	c.API = api
	if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err == nil {
		t.Fatal("deadline persistence failure was hidden")
	}
	if api.writes != 1 {
		t.Fatal("test did not reach the accepted deadline write")
	}
}

func TestLateWorkerPreservesInheritedAutoRelease(t *testing.T) {
	c, base, l := fixture()
	l.StartedAt = time.Now().Add(-40 * time.Minute)
	if err := Claim(context.Background(), c, l); err != nil {
		t.Fatal(err)
	}
	base.fake.Add("i-worker")
	worker := base.fake.Instances["i-worker"]
	// DescribeInstances documents minute-precision UTC timestamps. The launch
	// template's already-installed deadline is valid even with <30 minutes left.
	existing := l.StartedAt.Add(59 * time.Minute).UTC().Truncate(time.Minute)
	worker.AutoReleaseTime = existing.Format("2006-01-02T15:04Z")
	base.fake.Instances["i-worker"] = worker
	api := &releaseDeadlineAPI{base: base}
	c.API = api
	for range 2 {
		if err := Observe(context.Background(), c, l, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
			t.Fatal(err)
		}
	}
	if api.writes != 0 || len(l.Instances) != 1 || l.Instances[0].AutoReleaseAt != existing.Format(time.RFC3339) {
		t.Fatal("existing bounded deadline was not preserved without mutation")
	}
}
