package acceptance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

// JobEvidence stays in the private result. Public receipts expose only the
// scenario and counts after all identities and normal releases are verified.
type JobEvidence struct {
	Scenario        string `json:"scenario"`
	Nonce           string `json:"nonce"`
	PipelineID      int64  `json:"pipeline_id"`
	JobID           int64  `json:"job_id"`
	InstanceID      string `json:"instance_id"`
	ManagerPID      int    `json:"manager_pid"`
	Status          string `json:"status"`
	FailureReason   string `json:"failure_reason"`
	Checkpoint      bool   `json:"checkpoint"`
	CancelRequested bool   `json:"cancel_requested"`
	NormalRelease   bool   `json:"normal_release"`
}

func jobNonce() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("cannot create job nonce")
	}
	return hex.EncodeToString(b), nil
}

func jobCases(scenario string) []string {
	if scenario == "smoke" {
		return []string{"smoke"}
	}
	return []string{scenario, "smoke"}
}

func expectedJobOutcome(e JobEvidence) bool {
	switch e.Scenario {
	case "smoke":
		return e.Status == "success" && e.FailureReason == "" && !e.CancelRequested
	case "failure":
		return e.Checkpoint && e.Status == "failed" && e.FailureReason == "script_failure" && !e.CancelRequested
	case "cancel":
		return e.Checkpoint && e.CancelRequested && e.Status == "canceled"
	case "timeout":
		return e.Checkpoint && e.Status == "failed" && e.FailureReason == "job_execution_timeout" && !e.CancelRequested
	default:
		return false
	}
}

func verifyJobSequence(r *Result) error {
	cases := jobCases(r.JobScenario)
	if len(r.Jobs) != len(cases) || len(r.Instances) != len(cases) || r.CreationBudget != len(cases) || r.ManagerPID <= 0 {
		return errors.New("job sequence lacks its exact job and worker budget")
	}
	pipelines, jobs, workers, nonces := map[int64]bool{}, map[int64]bool{}, map[string]bool{}, map[string]bool{}
	for i, e := range r.Jobs {
		if e.Scenario != cases[i] || !expectedJobOutcome(e) || !e.NormalRelease || e.ManagerPID != r.ManagerPID ||
			e.PipelineID <= 0 || e.JobID <= 0 || e.InstanceID == "" || !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(e.Nonce) ||
			pipelines[e.PipelineID] || jobs[e.JobID] || workers[e.InstanceID] || nonces[e.Nonce] {
			return errors.New("job sequence outcome, Manager identity or isolation proof is invalid")
		}
		pipelines[e.PipelineID], jobs[e.JobID], workers[e.InstanceID], nonces[e.Nonce] = true, true, true, true
	}
	for _, instance := range r.Instances {
		if !workers[instance.InstanceID] || !instance.SpotVerified || !instance.InventoryComplete || !instance.DeletionVerified {
			return errors.New("job sequence lacks complete Spot worker release evidence")
		}
	}
	return nil
}

type jobSequence struct {
	gl               gitlab
	result           *Result
	runnerID         int64
	ref, tag         string
	checkManager     func(context.Context) error
	observe, release func(context.Context) error
	pollInterval     time.Duration
}

func (s jobSequence) run(ctx context.Context) error {
	r := s.result
	for _, scenario := range jobCases(r.JobScenario) {
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		nonce, err := jobNonce()
		if err != nil {
			return err
		}
		e := JobEvidence{Scenario: scenario, Nonce: nonce, ManagerPID: r.ManagerPID}
		variables := []map[string]string{
			{"key": "FLEETING_NONCE", "value": nonce}, {"key": "FLEETING_RUNNER_TAG", "value": s.tag},
			{"key": "FLEETING_EXPECTED_RUNNER_ID", "value": strconv.FormatInt(s.runnerID, 10)},
			{"key": "FLEETING_EXECUTION_MODE", "value": r.Executor}, {"key": "FLEETING_JOB_SCENARIO", "value": scenario},
		}
		var pipe pipeline
		if err = s.gl.request(ctx, "POST", "/pipeline", map[string]any{"ref": s.ref, "variables": variables}, &pipe); err != nil {
			return err
		}
		// Preserve a created pipeline for cancellation even when its SHA is wrong.
		r.PipelineID, e.PipelineID = pipe.ID, pipe.ID
		r.Jobs = append(r.Jobs, e)
		current := &r.Jobs[len(r.Jobs)-1]
		if pipe.ID <= 0 || pipe.SHA != r.FixtureSHA {
			return errors.New("pipeline identity mismatch")
		}
		if err = s.wait(ctx, current); err != nil {
			return err
		}
		if len(r.Instances) != len(r.Jobs) {
			return errors.New("job sequence must create exactly one new worker per job")
		}
		if err = s.checkManager(ctx); err != nil {
			return err
		}
		// This observer performs no scale-in mutation. The next job cannot start
		// until Runner has released the previous worker and its resources.
		if err = s.release(ctx); err != nil {
			return err
		}
		if err = s.checkManager(ctx); err != nil {
			return err
		}
		current.NormalRelease = true
	}
	return verifyJobSequence(r)
}

func (s jobSequence) wait(ctx context.Context, e *JobEvidence) error {
	for {
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		if err := s.observe(ctx); err != nil {
			return err
		}
		var pipe pipeline
		if err := s.gl.request(ctx, "GET", fmt.Sprintf("/pipelines/%d", e.PipelineID), nil, &pipe); err != nil {
			return err
		}
		if pipe.ID != e.PipelineID || pipe.SHA != s.result.FixtureSHA {
			return errors.New("polled pipeline identity mismatch")
		}
		var jobs []job
		if err := s.gl.request(ctx, "GET", fmt.Sprintf("/pipelines/%d/jobs?per_page=100", e.PipelineID), nil, &jobs); err != nil {
			return err
		}
		if len(jobs) > 1 {
			return errors.New("fixture pipeline must contain exactly one job")
		}
		if len(jobs) == 1 {
			j := jobs[0]
			if j.ID <= 0 || j.Commit.ID != s.result.FixtureSHA || j.Pipeline.ID != e.PipelineID ||
				(e.JobID != 0 && j.ID != e.JobID) || (j.Runner.ID != 0 && j.Runner.ID != s.runnerID) {
				return errors.New("job/runner/fixture identity mismatch")
			}
			e.JobID = j.ID
			if e.Scenario != "smoke" && j.Runner.ID == s.runnerID {
				if err := s.checkpoint(ctx, e, terminalJob(j.Status)); err != nil {
					return err
				}
				if e.Scenario == "cancel" && e.Checkpoint && !e.CancelRequested && j.Status == "running" {
					if err := s.gl.request(ctx, "POST", fmt.Sprintf("/pipelines/%d/cancel", e.PipelineID), nil, nil); err != nil {
						return err
					}
					e.CancelRequested = true
				}
			}
			if terminalJob(pipe.Status) && terminalJob(j.Status) {
				e.Status, e.FailureReason = j.Status, j.FailureReason
				if j.Runner.ID != s.runnerID || pipe.Status != j.Status || !expectedJobOutcome(*e) {
					return errors.New("job did not reach the intended terminal outcome")
				}
				if e.Scenario == "smoke" {
					var raw []byte
					if err := s.gl.request(ctx, "GET", fmt.Sprintf("/jobs/%d/artifacts/result.json", j.ID), nil, &raw); err != nil {
						return err
					}
					var artifact Artifact
					if err := jsonUnmarshal(raw, &artifact); err != nil {
						return errors.New("invalid job artifact")
					}
					if err := VerifyArtifact(artifact, &s.result.Ledger, e.Nonce, s.result.FixtureSHA, s.runnerID); err != nil {
						return err
					}
					if err := verifyArtifactMode(artifact, s.result.Executor); err != nil {
						return err
					}
					e.InstanceID = artifact.InstanceID
				}
				if !s.currentWorker(e.InstanceID) {
					return errors.New("job worker identity or inventory is unverified")
				}
				s.result.JobID, s.result.RunnerID = j.ID, s.runnerID
				return nil
			}
		}
		if err := aliyun.Wait(ctx, s.pollInterval); err != nil {
			return err
		}
	}
}

func terminalJob(status string) bool {
	return status == "success" || status == "failed" || status == "canceled" || status == "skipped"
}

func (s jobSequence) currentWorker(id string) bool {
	for _, r := range s.result.Instances {
		if r.InstanceID == id && r.SpotVerified && r.InventoryComplete && !r.DeletionVerified {
			return true
		}
	}
	return false
}

// Runner 19.5.1 prefixes full trace lines with an optional microsecond UTC
// timestamp and a two-digit hexadecimal stream ID plus O/E. A '+' continuation
// is not a new checkpoint line. Bare lines support older unframed trace files.
var runnerTraceHeader = regexp.MustCompile(`^(?:[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z )?[0-9a-f]{2}[OE] `)

func jobTraceLine(line string) string {
	return runnerTraceHeader.ReplaceAllString(strings.TrimSuffix(line, "\r"), "")
}

func (s jobSequence) checkpoint(ctx context.Context, e *JobEvidence, terminal bool) error {
	var trace []byte
	if err := s.gl.request(ctx, "GET", fmt.Sprintf("/jobs/%d/trace", e.JobID), nil, &trace); err != nil {
		var response *gitlabHTTPError
		if errors.As(err, &response) && response.statusCode == http.StatusNotFound && !terminal {
			return nil // The validated running job may not have uploaded its first log yet.
		}
		return fmt.Errorf("cannot read GitLab job trace: %w", err)
	}
	prefix := fmt.Sprintf("FLEETING_CHECKPOINT %s %s %d ", e.Nonce, e.Scenario, e.JobID)
	expectedExit := false
	for _, line := range strings.Split(string(trace), "\n") {
		if jobTraceLine(line) == "FLEETING_EXPECTED_EXIT "+e.Nonce+" 42" {
			expectedExit = true
		}
	}
	for _, line := range strings.Split(string(trace), "\n") {
		line = jobTraceLine(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		id := strings.TrimPrefix(line, prefix)
		if !regexp.MustCompile(`^i-[a-zA-Z0-9]+$`).MatchString(id) || (e.InstanceID != "" && e.InstanceID != id) {
			return errors.New("invalid or conflicting job checkpoint")
		}
		if s.currentWorker(id) {
			e.InstanceID = id
			// Deliberate script failure also proves it reached the exit injection.
			e.Checkpoint = e.Scenario != "failure" || expectedExit
		}
	}
	return nil
}
