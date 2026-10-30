package acceptance

import (
	"context"
	"errors"
	"fmt"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

// capacitySequence holds two real jobs until their checkpoints are observed,
// proves the third is queued, then releases one while preserving the other.
type capacitySequence struct {
	jobSequence
	openJob    func(context.Context, JobEvidence) error
	workerGone func(context.Context, string) (bool, error)
}

func (s capacitySequence) run(ctx context.Context) error {
	r := s.result
	for range 3 {
		r.activeJob = 0
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		if _, err := s.start(ctx, "capacity"); err != nil {
			return err
		}
	}
	first, retained, queued := -1, -1, -1
	for {
		r.activeJob = 0 // Pool-wide observations do not belong to one job.
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		if err := s.observe(ctx); err != nil {
			return err
		}
		for i := range r.Jobs {
			if !terminalJob(r.Jobs[i].Status) {
				if err := s.poll(ctx, &r.Jobs[i]); err != nil {
					return err
				}
			}
		}
		r.activeJob = 0
		if first < 0 {
			var running []int
			pending := -1
			for i, e := range r.Jobs {
				if e.Status == "running" && e.Checkpoint {
					running = append(running, i)
				}
				if e.Status == "pending" && e.JobID > 0 && !e.Checkpoint {
					pending = i
				}
			}
			if len(running) > 2 {
				return errors.New("capacity exceeded two simultaneous jobs")
			}
			if len(running) == 2 && pending >= 0 {
				first, retained, queued = running[0], running[1], pending
				if r.Jobs[first].InstanceID == r.Jobs[retained].InstanceID || len(r.Instances) != 2 {
					return errors.New("concurrent jobs lack two distinct inventoried workers")
				}
				if err := s.open(ctx, first); err != nil {
					return err
				}
			}
		} else if !r.CapacityVerified {
			// The held job must remain running during release and while the queued
			// job acquires its fresh worker. No restart or rerun can substitute.
			if r.Jobs[retained].Status != "running" {
				return errors.New("release interrupted the retained job")
			}
			if r.Jobs[first].Status == "success" && !r.Jobs[first].NormalRelease {
				r.activeJob = first + 1
				gone, err := s.workerGone(ctx, r.Jobs[first].InstanceID)
				if err != nil {
					return err
				}
				r.Jobs[first].NormalRelease = gone
				r.activeJob = 0
			}
			if r.Jobs[first].NormalRelease && r.Jobs[queued].Status == "running" && r.Jobs[queued].Checkpoint {
				if len(r.Instances) != 3 || r.Jobs[queued].InstanceID == r.Jobs[first].InstanceID || r.Jobs[queued].InstanceID == r.Jobs[retained].InstanceID {
					return errors.New("queued job did not acquire a fresh third worker")
				}
				if err := s.open(ctx, retained); err != nil {
					return err
				}
				if err := s.open(ctx, queued); err != nil {
					return err
				}
				r.CapacityVerified = true
			}
		}
		r.activeJob = 0
		done := r.CapacityVerified
		for _, e := range r.Jobs {
			done = done && e.Status == "success"
		}
		if done {
			if err := s.release(ctx); err != nil {
				return err
			}
			if err := s.checkManager(ctx); err != nil {
				return err
			}
			for i := range r.Jobs {
				r.Jobs[i].NormalRelease = true
			}
			r.JobID, r.RunnerID = r.Jobs[2].JobID, s.runnerID
			return verifyJobSequence(r)
		}
		if err := aliyun.Wait(ctx, s.pollInterval); err != nil {
			return err
		}
	}
}

func (s capacitySequence) open(ctx context.Context, index int) error {
	s.result.activeJob = index + 1
	e := &s.result.Jobs[index]
	if e.ControlReleased || !e.Checkpoint || e.Status != "running" {
		return errors.New("capacity barrier requires an active checkpoint")
	}
	if err := s.openJob(ctx, *e); err != nil {
		return err
	}
	e.ControlReleased = true
	return nil
}

func (s capacitySequence) poll(ctx context.Context, e *JobEvidence) error {
	s.selectJob(e)
	var pipe pipeline
	if err := s.gl.request(ctx, "GET", fmt.Sprintf("/pipelines/%d", e.PipelineID), nil, &pipe); err != nil {
		return err
	}
	if pipe.ID != e.PipelineID || pipe.SHA != s.result.FixtureSHA {
		return errors.New("capacity pipeline identity changed")
	}
	var jobs []job
	if err := s.gl.request(ctx, "GET", fmt.Sprintf("/pipelines/%d/jobs?per_page=100", e.PipelineID), nil, &jobs); err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}
	if len(jobs) != 1 {
		return errors.New("capacity pipeline must have exactly one job")
	}
	j := jobs[0]
	if j.ID <= 0 || j.Pipeline.ID != e.PipelineID || j.Commit.ID != s.result.FixtureSHA ||
		(e.JobID > 0 && e.JobID != j.ID) || (j.Runner.ID != 0 && j.Runner.ID != s.runnerID) {
		return errors.New("capacity job identity changed")
	}
	e.JobID, e.Status, e.FailureReason = j.ID, j.Status, j.FailureReason
	if j.Runner.ID == s.runnerID {
		if err := s.checkpoint(ctx, e, terminalJob(j.Status)); err != nil {
			return err
		}
	}
	if !terminalJob(j.Status) {
		return nil
	}
	if !terminalJob(pipe.Status) {
		e.Status = "running"
		return nil
	}
	if pipe.Status != j.Status || j.Runner.ID != s.runnerID || !expectedJobOutcome(*e, s.result.Executor) {
		return errors.New("capacity job did not finish through its released barrier")
	}
	var raw []byte
	if err := s.gl.request(ctx, "GET", fmt.Sprintf("/jobs/%d/artifacts/result.json", e.JobID), nil, &raw); err != nil {
		return err
	}
	var a Artifact
	if err := jsonUnmarshal(raw, &a); err != nil {
		return errors.New("invalid capacity job artifact")
	}
	if err := VerifyArtifact(a, &s.result.Ledger, e.Nonce, s.result.FixtureSHA, s.runnerID); err != nil {
		return err
	}
	if err := verifyArtifactMode(a, s.result.Executor); err != nil {
		return err
	}
	if a.InstanceID != e.InstanceID || !a.IsolationOK {
		return errors.New("capacity job lacks workspace isolation proof")
	}
	return nil
}
