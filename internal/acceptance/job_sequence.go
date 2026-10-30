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
// scenario and counts after identities and resource release are verified.
type JobEvidence struct {
	Scenario         string `json:"scenario"`
	Nonce            string `json:"nonce"`
	PipelineID       int64  `json:"pipeline_id"`
	JobID            int64  `json:"job_id"`
	InstanceID       string `json:"instance_id"`
	ManagerPID       int    `json:"manager_pid"`
	Status           string `json:"status"`
	FailureReason    string `json:"failure_reason"`
	Checkpoint       bool   `json:"checkpoint"`
	CancelRequested  bool   `json:"cancel_requested"`
	ControlReleased  bool   `json:"control_released"`
	LossInjected     bool   `json:"loss_injected"`
	CrashInjected    bool   `json:"crash_injected"`
	SSHTransportLoss bool   `json:"ssh_transport_loss"`
	NormalRelease    bool   `json:"normal_release"`
}

func jobNonce() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("cannot create job nonce")
	}
	return hex.EncodeToString(b), nil
}

func jobCases(scenario string) []string {
	if scenario == "capacity" {
		return []string{"capacity", "capacity", "capacity"}
	}
	if scenario == "smoke" || scenario == "upgrade" {
		return []string{"smoke"}
	}
	return []string{scenario, "smoke"}
}

func expectedJobOutcome(e JobEvidence) bool {
	switch e.Scenario {
	case "manager-crash":
		serverDrop := e.FailureReason == "server_timeout_running" || e.FailureReason == "no_updates_running"
		return e.Checkpoint && e.CrashInjected && !e.ControlReleased && e.Status == "failed" && serverDrop && !e.CancelRequested
	case "worker-loss":
		failure := e.FailureReason == "runner_system_failure" || (e.FailureReason == "script_failure" && e.SSHTransportLoss)
		return e.Checkpoint && e.LossInjected && !e.ControlReleased && e.Status == "failed" && failure && !e.CancelRequested
	case "capacity", "drain":
		return e.Checkpoint && e.ControlReleased && e.Status == "success" && e.FailureReason == "" && !e.CancelRequested
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
	r.activeJob = 0
	if r.JobScenario == "upgrade" {
		version, hash, _, err := upgradeBaseline()
		if err != nil || r.Upgrade == nil || r.Upgrade.Version != version || r.Upgrade.PackageSHA256 != hash ||
			!r.Upgrade.StatePreserved || !r.Upgrade.RollbackVerified || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(r.Upgrade.CandidatePackageSHA256) {
			return errors.New("job sequence lacks the real previous-release upgrade proof")
		}
	}
	if r.JobScenario == "worker-loss" && !r.WorkerLossVerified {
		return errors.New("worker loss lacks reconciliation proof")
	}
	if r.JobScenario == "capacity" && (!r.CapacityVerified || r.InstanceLimit != 2) {
		return errors.New("capacity scenario lacks queue, retained-job and replacement proof")
	}
	cases := jobCases(r.JobScenario)
	if r.JobScenario == "drain" && (!r.DrainVerified || len(r.Jobs) != 2 || r.Jobs[0].ManagerPID <= 0 || r.Jobs[0].ManagerPID == r.ManagerPID) {
		return errors.New("drain scenario lacks graceful stop and distinct restart proof")
	}
	if r.JobScenario == "manager-crash" && (!r.CrashVerified || len(r.Jobs) != 2 || r.Jobs[0].ManagerPID <= 0 || r.Jobs[0].ManagerPID == r.ManagerPID) {
		return errors.New("crash scenario lacks automatic restart proof")
	}
	if len(r.Jobs) != len(cases) || len(r.Instances) != len(cases) || r.CreationBudget != len(cases) || r.ManagerPID <= 0 {
		return errors.New("job sequence lacks its exact job and worker budget")
	}
	pipelines, jobs, workers, nonces := map[int64]bool{}, map[int64]bool{}, map[string]bool{}, map[string]bool{}
	for i, e := range r.Jobs {
		managerMatches := e.ManagerPID == r.ManagerPID || ((r.JobScenario == "drain" || r.JobScenario == "manager-crash") && i == 0)
		injected := r.JobScenario == "worker-loss" && i == 0
		normalRelease := !injected
		if e.Scenario != cases[i] || !expectedJobOutcome(e) || e.NormalRelease != normalRelease || e.LossInjected != injected || !managerMatches ||
			e.CrashInjected != (r.JobScenario == "manager-crash" && i == 0) ||
			(e.SSHTransportLoss && (!injected || r.Executor != "instance")) ||
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

func (s jobSequence) start(ctx context.Context, scenario string) (*JobEvidence, error) {
	r := s.result
	r.activeJob = len(r.Jobs) + 1
	nonce, err := jobNonce()
	if err != nil {
		return nil, err
	}
	e := JobEvidence{Scenario: scenario, Nonce: nonce, ManagerPID: r.ManagerPID}
	variables := []map[string]string{
		{"key": "FLEETING_NONCE", "value": nonce}, {"key": "FLEETING_RUNNER_TAG", "value": s.tag},
		{"key": "FLEETING_EXPECTED_RUNNER_ID", "value": strconv.FormatInt(s.runnerID, 10)},
		{"key": "FLEETING_EXECUTION_MODE", "value": r.Executor}, {"key": "FLEETING_JOB_SCENARIO", "value": scenario},
	}
	var pipe pipeline
	if err = s.gl.request(ctx, "POST", "/pipeline", map[string]any{"ref": s.ref, "variables": variables}, &pipe); err != nil {
		return nil, err
	}
	// Preserve a created pipeline for cancellation even when its SHA is wrong.
	r.PipelineID, e.PipelineID = pipe.ID, pipe.ID
	r.Jobs = append(r.Jobs, e)
	current := &r.Jobs[len(r.Jobs)-1]
	if pipe.ID <= 0 || pipe.SHA != r.FixtureSHA {
		return nil, errors.New("pipeline identity mismatch")
	}
	return current, nil
}

func (s jobSequence) run(ctx context.Context) error {
	r := s.result
	for _, scenario := range jobCases(r.JobScenario) {
		r.activeJob = 0
		if err := s.checkManager(ctx); err != nil {
			return err
		}
		current, err := s.start(ctx, scenario)
		if err != nil {
			return err
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
	s.selectJob(e)
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

func (s jobSequence) selectJob(e *JobEvidence) {
	s.result.activeJob = 0
	for i := range s.result.Jobs {
		if &s.result.Jobs[i] == e {
			s.result.activeJob = i + 1
			return
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

var runnerTraceColor = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Pinned Runner wraps Fleeting's missing SSH exit status in an unclassified
// BuildError. GitLab labels this script_failure, despite the transport loss.
const sshTransportLossLine = "ERROR: Job failed: run exit (exit code: 0): wait: remote command exited without exit status or exit signal"

func isSSHTransportLossLine(line string) bool {
	if line == sshTransportLossLine {
		return true
	}
	const prefix = "ERROR: Job failed: ssh connection closed by keepalive (keepalive probe timed out after "
	const suffix = "): run exit (exit code: 0): wait: remote command exited without exit status or exit signal"
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
	timeout, err := time.ParseDuration(value)
	// The pinned connector uses max(keepalive interval, 30s) for its probe
	// timeout and formats it with time.Duration.String before wrapping Run's error.
	return err == nil && timeout >= 30*time.Second && timeout.String() == value
}

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
	if terminal && e.Scenario == "worker-loss" && e.LossInjected && s.result.Executor == "instance" {
		e.SSHTransportLoss = false
		for _, line := range strings.Split(string(trace), "\n") {
			if isSSHTransportLossLine(runnerTraceColor.ReplaceAllString(jobTraceLine(line), "")) {
				e.SSHTransportLoss = true
			}
		}
	}
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
