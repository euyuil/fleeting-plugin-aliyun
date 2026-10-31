package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

// These files stay beside the private ledger. A submitted request is never
// repeated, including after a lost response; reconciliation is read-only and
// can identify work for cleanup, never turn an unknown submission into PASS.
type laneDispatchIntent struct {
	Schema     int    `json:"schema_version"`
	Request    string `json:"request"`
	Stage      string `json:"stage"`
	PipelineID int64  `json:"pipeline_id"`
	JobID      int64  `json:"job_id"`
	CancelSent bool   `json:"pipeline_cancel_submitted"`
	ForceSent  bool   `json:"job_force_cancel_submitted"`
}

func (s jobSequence) laneIntentPath(e *JobEvidence) (string, error) {
	if s.result.Ledger.Path == "" || !laneHex48.MatchString(e.Nonce) {
		return "", errors.New("lane dispatch requires a private ledger and nonce")
	}
	return filepath.Join(filepath.Dir(s.result.Ledger.Path), "gitlab-dispatch", e.Nonce+".json"), nil
}

func (s jobSequence) laneRequest(e *JobEvidence) ([]byte, error) {
	owner, err := s.laneOwner(e)
	if err != nil {
		return nil, err
	}
	variables, err := signedLaneVariables(owner, s.gl.token)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"ref": s.ref, "variables": variables})
}

func (s jobSequence) readLaneIntent(e *JobEvidence) (laneDispatchIntent, string, error) {
	var intent laneDispatchIntent
	path, err := s.laneIntentPath(e)
	if err != nil {
		return intent, "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return intent, "", errors.New("cannot read frozen lane dispatch intent")
	}
	expected, err := s.laneRequest(e)
	if err != nil {
		return intent, "", err
	}
	if json.Unmarshal(raw, &intent) != nil || intent.Schema != 1 || !bytes.Equal([]byte(intent.Request), expected) {
		return intent, "", errors.New("lane dispatch intent differs from the exact local request")
	}
	return intent, path, nil
}

func (s jobSequence) startLane(ctx context.Context, evidence JobEvidence) (*JobEvidence, error) {
	r := s.result
	r.Jobs = append(r.Jobs, evidence)
	e := &r.Jobs[len(r.Jobs)-1]
	request, err := s.laneRequest(e)
	if err != nil {
		return nil, err
	}
	path, err := s.laneIntentPath(e)
	if err != nil {
		return nil, err
	}
	// Refuse reentry even if a previous process stopped before receiving a reply.
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("existing lane dispatch intent must be preserved")
	}
	intent := laneDispatchIntent{Schema: 1, Request: string(request), Stage: "submit_unknown"}
	if err := WriteJSON(path, intent); err != nil {
		return nil, err
	}
	var pipe pipeline
	if err := s.gl.request(ctx, "POST", "/pipeline", json.RawMessage(request), &pipe); err != nil {
		return nil, err
	}
	r.PipelineID, e.PipelineID = pipe.ID, pipe.ID
	intent.PipelineID, intent.Stage = pipe.ID, "response_received_unverified"
	if err := WriteJSON(path, intent); err != nil {
		return nil, err
	}
	if pipe.ID <= 0 || pipe.SHA != r.FixtureSHA {
		return nil, errors.New("pipeline identity mismatch")
	}
	if _, err := s.reconcileLaneDispatch(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

func (s jobSequence) reconcileLaneDispatch(parent context.Context, e *JobEvidence) (lanePipeline, error) {
	intent, path, err := s.readLaneIntent(e)
	if err != nil {
		return lanePipeline{}, err
	}
	owner, err := s.laneOwner(e)
	if err != nil {
		return lanePipeline{}, err
	}
	want, _ := json.Marshal(owner)
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	for {
		inventory := map[int64]lanePipeline{}
		err := s.gl.inspectLaneInventory(ctx, owner, inventory)
		if err != nil && !errors.Is(err, errLaneVisibility) {
			return lanePipeline{}, err
		}
		var found lanePipeline
		if err == nil {
			for _, p := range inventory {
				actual, _ := json.Marshal(p.owner)
				if bytes.Equal(actual, want) {
					if found.pipeline.ID != 0 {
						return lanePipeline{}, errors.New("lane dispatch was replayed")
					}
					found = p
				}
			}
		}
		if found.pipeline.ID > 0 {
			if (e.PipelineID > 0 && e.PipelineID != found.pipeline.ID) || (e.JobID > 0 && e.JobID != found.job.ID) ||
				(intent.PipelineID > 0 && intent.PipelineID != found.pipeline.ID) || (intent.JobID > 0 && intent.JobID != found.job.ID) {
				return lanePipeline{}, errors.New("lane dispatch server identity changed")
			}
			e.PipelineID, e.JobID = found.pipeline.ID, found.job.ID
			intent.PipelineID, intent.JobID, intent.Stage = e.PipelineID, e.JobID, "ownership_verified"
			if err := WriteJSON(path, intent); err != nil {
				return lanePipeline{}, err
			}
			if err := WriteJSON(filepath.Join(filepath.Dir(s.result.Ledger.Path), "result.json"), s.result); err != nil {
				return lanePipeline{}, err
			}
			return found, nil
		}
		if err := aliyun.Wait(ctx, laneVisibilityPollInterval); err != nil {
			return lanePipeline{}, errors.New("lane dispatch remains unconfirmed; submission was not repeated")
		}
	}
}

func (s jobSequence) requestCancellation(ctx context.Context, e *JobEvidence, force bool) error {
	path := fmt.Sprintf("/pipelines/%d/cancel", e.PipelineID)
	var body any
	if force {
		path, body = fmt.Sprintf("/jobs/%d/cancel", e.JobID), map[string]bool{"force": true}
	}
	if s.gl.lane == nil {
		return s.gl.request(ctx, "POST", path, body, nil)
	}
	p, err := s.reconcileLaneDispatch(ctx, e)
	if err != nil {
		return err
	}
	if terminalJob(p.pipeline.Status) && terminalJob(p.job.Status) {
		return nil
	}
	intent, file, err := s.readLaneIntent(e)
	if err != nil {
		return err
	}
	if force {
		if intent.ForceSent {
			return errors.New("force cancellation response is unknown; request was not repeated")
		}
		intent.ForceSent = true
	} else {
		if intent.CancelSent {
			return errors.New("pipeline cancellation response is unknown; request was not repeated")
		}
		intent.CancelSent = true
	}
	intent.Stage = "cancellation_unknown"
	if err := WriteJSON(file, intent); err != nil {
		return err
	}
	// Recompute from the authenticated identity, including an unknown create reply.
	path = fmt.Sprintf("/pipelines/%d/cancel", e.PipelineID)
	if force {
		path = fmt.Sprintf("/jobs/%d/cancel", e.JobID)
	}
	return s.gl.request(ctx, "POST", path, body, nil)
}

func (s jobSequence) cleanupLaneDispatch(ctx context.Context, e *JobEvidence) error {
	p, err := s.reconcileLaneDispatch(ctx, e)
	if err != nil {
		return err
	}
	if terminalJob(p.pipeline.Status) && terminalJob(p.job.Status) {
		return nil
	}
	intent, _, err := s.readLaneIntent(e)
	if err != nil {
		return err
	}
	if !intent.CancelSent {
		// Even an unknown cancellation reply is followed only by reads.
		_ = s.requestCancellation(ctx, e, false)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		p, err := s.reconcileLaneDispatch(readCtx, e)
		if err != nil {
			return err
		}
		if terminalJob(p.pipeline.Status) && terminalJob(p.job.Status) {
			return nil
		}
		if err := aliyun.Wait(readCtx, laneVisibilityPollInterval); err != nil {
			return errors.New("lane job cleanup lacks an exact terminal readback")
		}
	}
}
