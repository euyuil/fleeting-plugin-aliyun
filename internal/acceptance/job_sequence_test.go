package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJobRecoverySequence(t *testing.T) {
	for _, scenario := range []string{"smoke", "failure", "cancel", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			s, requests := fakeJobSequence(t, scenario, "")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.run(ctx); err != nil {
				t.Fatal(err)
			}
			if err := verifyJobSequence(s.result); err != nil {
				t.Fatal(err)
			}
			want := "create,release"
			if scenario != "smoke" {
				want = "create,release,create,release"
			}
			if scenario == "cancel" {
				want = "create,cancel,release,create,release"
			}
			if got := requests(); got != want {
				t.Fatalf("job and release ordering = %s, want %s", got, want)
			}
			if scenario != "smoke" && s.result.Jobs[0].ManagerPID != s.result.Jobs[1].ManagerPID {
				t.Fatal("recovery used another Manager")
			}
		})
	}
}

func TestJobRecoveryRejectsIncompleteOrForeignExecution(t *testing.T) {
	for _, fault := range []string{"wrong-reason", "wrong-checkpoint", "wrong-runner", "foreign-pipeline", "foreign-commit", "extra-job", "release-failure", "manager-restart", "trace-denied", "trace-missing", "cancel-denied"} {
		t.Run(fault, func(t *testing.T) {
			scenario := "failure"
			if fault == "cancel-denied" {
				scenario = "cancel"
			}
			s, requests := fakeJobSequence(t, scenario, fault)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.run(ctx); err == nil {
				t.Fatal("invalid recovery evidence passed")
			}
			if strings.Count(requests(), "create") != 1 {
				t.Fatal("recovery job started before a proven first outcome and normal release")
			}
		})
	}
}

func TestRecoveryReceiptRejectsDuplicateIdentitiesAndChangedManager(t *testing.T) {
	s, _ := fakeJobSequence(t, "failure", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.run(ctx); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Result){
		func(r *Result) { r.Jobs[1].JobID = r.Jobs[0].JobID },
		func(r *Result) { r.Jobs[1].PipelineID = r.Jobs[0].PipelineID },
		func(r *Result) { r.Jobs[1].Nonce = r.Jobs[0].Nonce },
		func(r *Result) { r.Jobs[1].InstanceID = r.Jobs[0].InstanceID },
		func(r *Result) { r.Jobs[1].ManagerPID++ },
		func(r *Result) { r.Jobs[0].NormalRelease = false },
		func(r *Result) { r.Jobs[0].Checkpoint = false },
		func(r *Result) { r.Jobs[0].Status = "success" },
		func(r *Result) { r.Jobs = r.Jobs[1:] },
	} {
		r := *s.result
		r.Jobs = append([]JobEvidence(nil), r.Jobs...)
		mutate(&r)
		if verifyJobSequence(&r) == nil {
			t.Fatal("incomplete or duplicated job sequence passed")
		}
	}
}

func TestJobRecoveryRequiresSystemdBeforeResources(t *testing.T) {
	base := map[string]string{"ACCEPTANCE_MANAGER_MODE": "systemd", "ACCEPTANCE_DISPOSABLE_HOST": "true",
		"PLUGIN_VERSION": "0.1.0-dev", "GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_REF": "refs/heads/main"}
	for _, scenario := range []string{"failure", "cancel", "timeout", "capacity"} {
		base["ACCEPTANCE_JOB_SCENARIO"] = scenario
		if _, err := readSmokeOptions(func(k string) string { return base[k] }); err != nil {
			t.Fatal(err)
		}
		if _, err := readSmokeOptions(func(k string) string {
			if k == "ACCEPTANCE_JOB_SCENARIO" {
				return scenario
			}
			return ""
		}); err == nil {
			t.Fatal("recovery accepted a process Manager")
		}
	}
	base["ACCEPTANCE_JOB_SCENARIO"] = "arbitrary-command"
	if _, err := readSmokeOptions(func(k string) string { return base[k] }); err == nil {
		t.Fatal("unknown job scenario accepted")
	}
}

// Exercise the real GitLab HTTP client and sequencing code; replace only the
// cloud observation and systemd boundaries. The fake refuses an early next job.
func fakeJobSequence(t *testing.T, scenario, fault string) (jobSequence, func() string) {
	t.Helper()
	var mu sync.Mutex
	r := &Result{Ledger: Ledger{ManagerPID: 123, InstanceLimit: 1, CreationBudget: len(jobCases(scenario))},
		JobScenario: scenario, FixtureSHA: strings.Repeat("a", 40), Executor: "instance"}
	var events []string
	created, released, traces := 0, 0, 0
	var currentCase, nonce string
	canceled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(req.URL.Path, "/api/v4/projects/1")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if req.Header.Get("PRIVATE-TOKEN") != "synthetic-token" {
			t.Error("missing project token")
		}
		if path == "/pipeline" && req.Method == "POST" {
			if created != released {
				http.Error(w, "previous worker not released", 409)
				return
			}
			var body struct{ Variables []struct{ Key, Value string } }
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			for _, v := range body.Variables {
				if v.Key == "FLEETING_JOB_SCENARIO" {
					currentCase = v.Value
				}
				if v.Key == "FLEETING_NONCE" {
					nonce = v.Value
				}
			}
			created++
			events = append(events, "create")
			write(pipeline{ID: int64(created), SHA: r.FixtureSHA, Status: "pending"})
			return
		}
		status, reason := "success", ""
		switch currentCase {
		case "failure":
			status, reason = "failed", "script_failure"
		case "timeout":
			status, reason = "failed", "job_execution_timeout"
		case "cancel":
			status = "running"
			if canceled {
				status = "canceled"
			}
		}
		if fault == "trace-not-ready" && created == 1 && traces < 2 {
			status, reason = "running", ""
		}
		if fault == "wrong-reason" {
			reason = "runner_system_failure"
		}
		pipeID, jobID := int64(created), int64(created+10)
		j := job{ID: jobID, Status: status, FailureReason: reason}
		j.Runner.ID, j.Commit.ID, j.Pipeline.ID = 2, r.FixtureSHA, pipeID
		if fault == "wrong-runner" {
			j.Runner.ID++
		}
		if fault == "foreign-commit" {
			j.Commit.ID = strings.Repeat("b", 40)
		}
		if fault == "foreign-pipeline" {
			j.Pipeline.ID++
		}
		switch path {
		case fmt.Sprintf("/pipelines/%d", pipeID):
			write(pipeline{ID: pipeID, SHA: r.FixtureSHA, Status: status})
		case fmt.Sprintf("/pipelines/%d/jobs", pipeID):
			jobs := []job{j}
			if fault == "extra-job" {
				jobs = append(jobs, j)
			}
			write(jobs)
		case fmt.Sprintf("/jobs/%d/trace", jobID):
			traces++
			if fault == "trace-missing" || (fault == "trace-not-ready" && traces == 1) {
				http.Error(w, "no log yet", 404)
				return
			}
			if fault == "trace-denied" {
				http.Error(w, "private denial", 403)
				return
			}
			checkpointNonce := nonce
			if fault == "wrong-checkpoint" {
				checkpointNonce = strings.Repeat("c", 48)
			}
			_, _ = fmt.Fprintf(w, "2026-10-29T12:34:56.123456Z 01O FLEETING_CHECKPOINT %s %s %d i-worker%d\r\n2026-10-29T12:35:26.123456Z 01O FLEETING_EXPECTED_EXIT %s 42\r\n", checkpointNonce, currentCase, jobID, created, nonce)
		case fmt.Sprintf("/pipelines/%d/cancel", pipeID):
			if req.Method != "POST" || len(r.Instances) != created || !r.Instances[created-1].InventoryComplete {
				t.Error("canceled without actual worker proof")
			}
			if fault == "cancel-denied" {
				http.Error(w, "private denial", 403)
				return
			}
			canceled = true
			events = append(events, "cancel")
			write(pipeline{ID: pipeID, SHA: r.FixtureSHA, Status: "canceled"})
		case fmt.Sprintf("/jobs/%d/artifacts/result.json", jobID):
			container := false
			write(Artifact{Nonce: nonce, FixtureSHA: r.FixtureSHA, InstanceID: "i-worker" + strconv.Itoa(created),
				RunnerID: 2, CheckoutOK: true, CommandOK: true, Executor: "instance", ContainerDetected: &container})
		default:
			http.Error(w, "unexpected request", 404)
		}
	}))
	t.Cleanup(server.Close)
	s := jobSequence{gl: gitlab{base: server.URL, token: "synthetic-token", project: "1", http: server.Client()},
		result: r, runnerID: 2, ref: "main", tag: "acceptance", pollInterval: time.Millisecond,
		checkManager: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if fault == "manager-restart" && released > 0 {
				return errors.New("Manager changed")
			}
			return nil
		},
		observe: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if len(r.Instances) < created {
				r.Instances = append(r.Instances, Resource{InstanceID: "i-worker" + strconv.Itoa(created), SpotVerified: true, InventoryComplete: true})
			}
			return nil
		},
		release: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if fault == "release-failure" {
				return errors.New("normal release failed")
			}
			released++
			events = append(events, "release")
			for i := range r.Instances {
				r.Instances[i].DeletionVerified = true
			}
			return nil
		}}
	return s, func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(events, ",") }
}

func TestJobTraceUploadDelay(t *testing.T) {
	s, _ := fakeJobSequence(t, "failure", "trace-not-ready")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestJobTraceFraming(t *testing.T) {
	message := "FLEETING_CHECKPOINT nonce cancel 12 i-worker"
	for _, prefix := range []string{"", "01O ", "abE ", "2026-10-29T12:34:56.123456Z 01O "} {
		for _, ending := range []string{"", "\r"} {
			if got := jobTraceLine(prefix + message + ending); got != message {
				t.Fatalf("framed checkpoint was not normalized: %q", got)
			}
		}
	}
	for _, prefix := range []string{"$ echo ", "2026-10-29T12:34:56.123456Z 01O+", "01O+", "untrusted prefix "} {
		if jobTraceLine(prefix+message) == message {
			t.Fatal("a continuation or embedded command became a checkpoint")
		}
	}
}
