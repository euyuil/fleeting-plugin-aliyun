package acceptance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type crashSequence struct {
	jobSequence
	crash   func(context.Context) (int, error)
	persist func() error
}

const crashRecoveryPolicy = "orphan-release-before-finalize-v1"

func (s crashSequence) run(ctx context.Context) error {
	r := s.result
	r.RecoveryPolicy = crashRecoveryPolicy
	first, err := s.start(ctx, "manager-crash")
	if err != nil {
		return err
	}
	controlled := capacitySequence{jobSequence: s.jobSequence}
	for !first.Checkpoint {
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		if err := s.observe(ctx); err != nil {
			return err
		}
		if err := controlled.poll(ctx, first); err != nil {
			return err
		}
		if err := aliyun.Wait(ctx, s.pollInterval); err != nil {
			return err
		}
	}
	if first.Status != "running" || len(r.Instances) != 1 {
		return errors.New("Manager crash requires one real running job")
	}
	pid, err := s.crash(ctx)
	if err != nil {
		return err
	}
	if pid <= 0 || pid == r.ManagerPID {
		return errors.New("Manager crash did not produce a distinct invocation")
	}
	first.CrashInjected = true
	r.ManagerPID, r.CrashVerified = pid, true
	// Prove automatic orphan recovery before any harness cancellation. GitLab's
	// natural timeout is independent of the recovered Manager's resource state.
	if err := s.release(ctx); err != nil {
		return err
	}
	if len(r.Instances) != 1 {
		return errors.New("Manager crash created an unrequested replacement")
	}
	first.NormalRelease = true
	r.OrphanReleased = true
	if err := s.checkManager(ctx); err != nil {
		return err
	}
	if s.persist == nil {
		return errors.New("crash recovery evidence persistence is unavailable")
	}
	if err := s.persist(); err != nil {
		return err
	}
	if err := s.finalize(ctx, first); err != nil {
		return err
	}
	second, err := s.start(ctx, "smoke")
	if err != nil {
		return err
	}
	if err := s.wait(ctx, second); err != nil {
		return err
	}
	if err := s.release(ctx); err != nil {
		return err
	}
	if err := s.checkManager(ctx); err != nil {
		return err
	}
	second.NormalRelease = true
	return verifyJobSequence(r)
}

// Only the exact interrupted job may use this path after its inventoried worker
// has already disappeared through normal release. Generic job waits still
// require a live worker; canceling is never accepted as a terminal outcome.
func (s crashSequence) finalize(parent context.Context, e *JobEvidence) error {
	r := s.result
	if !r.CrashVerified || !r.OrphanReleased || r.RecoveryPolicy != crashRecoveryPolicy ||
		!e.Checkpoint || !e.CrashInjected || !e.NormalRelease || e.CancelRequested || e.ControlReleased ||
		len(r.Jobs) != 1 || e != &r.Jobs[0] || len(r.Instances) != 1 || r.EvidenceIncomplete ||
		r.Instances[0].InstanceID != e.InstanceID || !r.Instances[0].InventoryComplete ||
		!r.Instances[0].SpotVerified || !r.Instances[0].DeletionVerified {
		return errors.New("crash finalization lacks exact orphan release proof")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	pipe, j, err := s.interruptedJob(ctx, e)
	if err != nil {
		return err
	}
	if pipe.Status != "running" || j.Status != "running" {
		return errors.New("crash job stopped before harness finalization")
	}
	if err := s.requestCancellation(ctx, e, false); err != nil {
		return err
	}
	e.CancelRequested, e.Finalization = true, "harness_after_recovery"
	for {
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		if err := s.observe(ctx); err != nil {
			return err
		}
		if len(r.Instances) != 1 {
			return productViolation("crash finalization created an unrequested replacement")
		}
		pipe, j, err := s.interruptedJob(ctx, e)
		if err != nil {
			return err
		}
		if j.Status == "canceled" && pipe.Status == "canceled" {
			e.Status, e.FailureReason = j.Status, j.FailureReason
			return nil
		}
		if terminalJob(j.Status) && j.Status != "canceled" {
			return errors.New("crash job reached an unexpected terminal outcome")
		}
		if j.Status == "canceling" && !e.ForceCanceled {
			if err := s.requestCancellation(ctx, e, true); err != nil {
				return err
			}
			e.ForceCanceled = true
		}
		if err := aliyun.Wait(ctx, s.pollInterval); err != nil {
			return err
		}
	}
}

func (s crashSequence) interruptedJob(ctx context.Context, e *JobEvidence) (pipeline, job, error) {
	var pipe pipeline
	if err := s.gl.request(ctx, "GET", fmt.Sprintf("/pipelines/%d", e.PipelineID), nil, &pipe); err != nil {
		return pipe, job{}, err
	}
	jobs, err := s.gl.pipelineJobs(ctx, e.PipelineID)
	if err != nil {
		return pipe, job{}, err
	}
	if pipe.ID != e.PipelineID || pipe.SHA != s.result.FixtureSHA || len(jobs) != 1 || jobs[0].ID != e.JobID ||
		jobs[0].Commit.ID != s.result.FixtureSHA || jobs[0].Pipeline.ID != e.PipelineID || jobs[0].Runner.ID != s.runnerID {
		return pipe, job{}, errors.New("crash finalization job identity changed")
	}
	if err := s.verifyLaneJob(jobs[0], e); err != nil {
		return pipe, job{}, err
	}
	return pipe, jobs[0], nil
}
