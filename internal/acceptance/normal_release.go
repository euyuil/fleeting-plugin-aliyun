package acceptance

import (
	"context"
	"errors"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func WaitForNormalRelease(parent context.Context, c *aliyun.Client, l *Ledger, typ, image, zone string) error {
	return waitForNormalRelease(parent, c, l, typ, image, zone, 3*time.Minute, time.Second)
}

var errNormalReleaseDeadline = errors.New("normal release confirmation deadline")

type normalReleaseSample struct {
	Desired   int   `json:"desired_capacity"`
	Total     int   `json:"total_capacity"`
	Pending   int   `json:"pending_capacity"`
	Removing  int   `json:"removing_capacity"`
	Members   int   `json:"member_count"`
	Instances int   `json:"instance_count"`
	Quiet     bool  `json:"history_quiet"`
	Stable    int   `json:"stable_zero_samples"`
	SpanMS    int64 `json:"span_ms"`
}

// This is the most recent completed observation call, not the final cleanup
// state. A sample describes sequential reads, not an atomic cloud snapshot.
type normalReleaseObservation struct {
	Call       int                  `json:"call_index"`
	Result     string               `json:"result"`
	Scope      string               `json:"deadline_scope"`
	Phase      string               `json:"phase"`
	ElapsedMS  int64                `json:"elapsed_ms"`
	Sample     *normalReleaseSample `json:"last_complete_sample"`
	SampleAge  *int64               `json:"sample_age_ms"`
	Associated string               `json:"associated_resources"`
}

func waitForNormalRelease(parent context.Context, c *aliyun.Client, l *Ledger, typ, image, zone string, limit, poll time.Duration) (resultErr error) {
	ctx, cancel := context.WithTimeoutCause(parent, limit, errNormalReleaseDeadline)
	defer cancel()
	started, sampleAt := time.Now(), time.Time{}
	observation := normalReleaseObservation{Call: 1, Scope: "none", Associated: "not_started"}
	if l.LastNormalRelease != nil {
		observation.Call = l.LastNormalRelease.Call + 1
	}
	// Freeze before cancel() and before the caller can run emergency cleanup.
	// An independent error racing a deadline must retain its original meaning.
	defer func() {
		observation.Result = "PASS"
		observation.ElapsedMS = time.Since(started).Milliseconds()
		if !sampleAt.IsZero() {
			age := time.Since(sampleAt).Milliseconds()
			observation.SampleAge = &age
		}
		if resultErr != nil {
			observation.Result = "FAIL"
			if errors.Is(resultErr, context.DeadlineExceeded) || errors.Is(resultErr, context.Canceled) {
				switch {
				case errors.Is(context.Cause(ctx), errNormalReleaseDeadline):
					observation.Scope = "normal_release"
				case ctx.Err() == context.DeadlineExceeded:
					observation.Scope = "parent_deadline"
				case ctx.Err() == context.Canceled:
					observation.Scope = "parent_canceled"
				case errors.Is(resultErr, context.DeadlineExceeded):
					observation.Scope = "operation_deadline"
				default:
					observation.Scope = "operation_canceled"
				}
			}
		}
		l.LastNormalRelease = &observation
	}()
	stable := 0
	for {
		roundStarted := time.Now()
		observation.Phase = "inventory"
		if err := ObserveBudget(ctx, c, l, max(1, l.CreationBudget), typ, image, zone); err != nil {
			return err
		}
		observation.Phase = "persist"
		if err := l.Save(); err != nil {
			return errors.New("cannot persist release evidence")
		}
		observation.Phase = "group"
		g, err := c.Group(ctx)
		if err != nil {
			return err
		}
		observation.Phase = "members"
		members, err := c.Members(ctx)
		if err != nil {
			return err
		}
		ids := []string{}
		for _, r := range l.Instances {
			ids = append(ids, r.InstanceID)
		}
		observation.Phase = "instances"
		instances, err := c.Instances(ctx, ids)
		if err != nil {
			return err
		}
		observation.Phase = "history"
		quiet, err := historyQuiet(ctx, c)
		if err != nil {
			return err
		}
		if quiet && g.DesiredCapacity == 0 && g.TotalCapacity == 0 && g.PendingCapacity == 0 && g.RemovingCapacity == 0 && len(members) == 0 && len(instances) == 0 {
			stable++
		} else {
			stable = 0
		}
		// Commit only a complete round; partial reads retain the prior sample.
		observation.Sample = &normalReleaseSample{Desired: g.DesiredCapacity, Total: g.TotalCapacity,
			Pending: g.PendingCapacity, Removing: g.RemovingCapacity, Members: len(members), Instances: len(instances),
			Quiet: quiet, Stable: stable, SpanMS: time.Since(roundStarted).Milliseconds()}
		sampleAt = time.Now()
		if stable >= 2 {
			observation.Phase = "evidence"
			if l.EvidenceIncomplete {
				return errors.New("normal release resource evidence is incomplete")
			}
			observation.Phase, observation.Associated = "associated", "unconfirmed"
			if err = verifyAssociated(ctx, c, l); err != nil {
				return err
			}
			observation.Associated = "verified"
			for i := range l.Instances {
				l.Instances[i].DeletionVerified = true
			}
			return nil
		}
		observation.Phase = "wait"
		if err = aliyun.Wait(ctx, poll); err != nil {
			return err
		}
	}
}

// WaitForSpecifiedRelease observes the two-worker scale-in without deleting
// anything. The same retained identity must survive throughout the observation.
func WaitForSpecifiedRelease(parent context.Context, c *aliyun.Client, l *Ledger, removed, retained, typ, image, zone string) error {
	if removed == "" || retained == "" || removed == retained || l.CreationBudget != 2 || len(l.Instances) != 2 || l.EvidenceIncomplete {
		return errors.New("specified release requires two complete, distinct worker identities")
	}
	index := -1
	foundRetained := false
	for i, r := range l.Instances {
		if !r.InventoryComplete || !r.SpotVerified {
			return errors.New("specified release resource evidence is incomplete")
		}
		if r.InstanceID == removed {
			index = i
		}
		if r.InstanceID == retained {
			foundRetained = true
		}
	}
	if index < 0 || !foundRetained {
		return errors.New("specified release identities are not in the resource ledger")
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	stable := 0
	for {
		if err := ObserveBudget(ctx, c, l, 2, typ, image, zone); err != nil {
			return err
		}
		if l.EvidenceIncomplete {
			return errors.New("specified release resource evidence became incomplete")
		}
		if err := l.Save(); err != nil {
			return err
		}
		g, err := c.Group(ctx)
		if err != nil {
			return err
		}
		members, err := c.Members(ctx)
		if err != nil {
			return err
		}
		instances, err := c.Instances(ctx, []string{removed, retained})
		if err != nil {
			return err
		}
		keepMember, memberOK := members[retained]
		keepInstance, instanceOK := instances[retained]
		if !memberOK || !instanceOK {
			return errors.New("specified scale-in removed the retained worker")
		}
		quiet, err := historyQuiet(ctx, c)
		if err != nil {
			return err
		}
		if quiet && g.DesiredCapacity == 1 && g.TotalCapacity == 1 && g.PendingCapacity == 0 && g.RemovingCapacity == 0 && len(members) == 1 && len(instances) == 1 && keepMember.LifecycleState == "InService" && keepInstance.Status == "Running" {
			stable++
		} else {
			stable = 0
		}
		if stable >= 2 {
			onlyRemoved := *l
			onlyRemoved.Instances = []Resource{l.Instances[index]}
			onlyRemoved.Leftovers = nil
			if err = verifyAssociated(ctx, c, &onlyRemoved); err != nil {
				return err
			}
			l.Instances[index].DeletionVerified = true
			return l.Save()
		}
		if err = aliyun.Wait(ctx, time.Second); err != nil {
			return errors.New("specified scale-in did not reach one retained worker before the deadline")
		}
	}
}
