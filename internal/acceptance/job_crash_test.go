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

func TestManagerCrashThenFreshJob(t *testing.T) {
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		for _, reason := range []string{"immediate", "force"} {
			s, events := fakeCrash(t, executor, reason, "")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if err := s.run(ctx); err != nil {
				cancel()
				t.Fatal(err)
			}
			cancel()
			want := "create1,crash,release,persist,cancel,create2,release"
			if reason == "force" {
				want = "create1,crash,release,persist,cancel,force,create2,release"
			}
			if events() != want {
				t.Fatal("wrong recovery order", events())
			}
			if !s.result.Jobs[0].CrashInjected || s.result.Jobs[0].ManagerPID == s.result.Jobs[1].ManagerPID {
				t.Fatal("crash identity missing")
			}
			for _, change := range []func(*Result){
				func(r *Result) { r.CrashVerified = false },
				func(r *Result) { r.OrphanReleased = false },
				func(r *Result) { r.RecoveryPolicy = "legacy" },
				func(r *Result) { r.Jobs[0].Finalization = "" },
				func(r *Result) { r.Jobs[0].CancelRequested = false },
				func(r *Result) { r.Jobs[0].CrashInjected = false },
				func(r *Result) { r.Jobs[1].CrashInjected = true },
				func(r *Result) { r.Jobs[0].ManagerPID = r.ManagerPID },
			} {
				copy := *s.result
				copy.Jobs = append([]JobEvidence(nil), s.result.Jobs...)
				change(&copy)
				if verifyJobSequence(&copy) == nil {
					t.Fatal("missing or forged crash proof accepted")
				}
			}
		}
	}
}

func TestManagerCrashRejectsFalseRecovery(t *testing.T) {
	for _, fault := range []string{"early-failure", "no-checkpoint", "crash-error", "same-pid", "still-running", "ordinary-failure", "canceled", "success", "second-restart", "leak", "replacement", "second-failure", "same-worker", "evidence-write", "force-denied", "stuck-canceling", "wrong-job", "wrong-sha"} {
		t.Run(fault, func(t *testing.T) {
			s, events := fakeCrash(t, "instance", "force", fault)
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			if s.run(ctx) == nil {
				t.Fatal("invalid crash recovery passed")
			}
			if (fault != "second-failure" && fault != "same-worker" && strings.Contains(events(), "create2")) || strings.Count(events(), "force") > 1 {
				t.Fatal("failed finalization started recovery or retried a write", events())
			}
			if (fault == "leak" || fault == "replacement" || fault == "evidence-write") && strings.Contains(events(), "cancel") {
				t.Fatal("job was canceled before persisted orphan release", events())
			}
		})
	}
}

func fakeCrash(t *testing.T, executor, failureReason, fault string) (crashSequence, func() string) {
	t.Helper()
	var mu sync.Mutex
	var events, nonces []string
	crashed, canceled, forced, persisted := false, false, false, false
	releases, actualPID := 0, 123
	r := &Result{Ledger: Ledger{ManagerPID: 123, InstanceLimit: 1, CreationBudget: 2}, JobScenario: "manager-crash", FixtureSHA: strings.Repeat("a", 40), Executor: executor}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(req.URL.Path, "/api/v4/projects/1")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if path == "/pipeline" && req.Method == "POST" {
			if len(nonces) == 1 && (!crashed || !canceled || !persisted || (failureReason == "force" && !forced) || releases != 1 || actualPID == 123) {
				http.Error(w, "premature recovery", 409)
				return
			}
			var body struct{ Variables []struct{ Key, Value string } }
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			for _, v := range body.Variables {
				if v.Key == "FLEETING_NONCE" {
					nonces = append(nonces, v.Value)
				}
			}
			events = append(events, "create"+strconv.Itoa(len(nonces)))
			write(pipeline{ID: int64(len(nonces)), SHA: r.FixtureSHA})
			return
		}
		if path == "/pipelines/1/cancel" && req.Method == "POST" {
			if !persisted || releases != 1 || !r.OrphanReleased || !r.Jobs[0].NormalRelease {
				http.Error(w, "cancellation preceded recovery proof", 409)
				return
			}
			canceled = true
			events = append(events, "cancel")
			write(pipeline{ID: 1})
			return
		}
		if path == "/jobs/11/cancel" && req.Method == "POST" {
			var body map[string]bool
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil || !body["force"] || !canceled || failureReason != "force" {
				http.Error(w, "invalid force request", 409)
				return
			}
			if fault == "force-denied" {
				http.Error(w, "not authorized", 403)
				return
			}
			forced = fault != "stuck-canceling"
			events = append(events, "force")
			write(job{ID: 11})
			return
		}
		index := len(nonces) - 1
		pipeID, jobID := int64(index+1), int64(index+11)
		status, reason := "running", ""
		if canceled && fault != "still-running" {
			status = "canceled"
			if failureReason == "force" && !forced {
				status = "canceling"
			}
		}
		if fault == "early-failure" {
			status, reason = "failed", "script_failure"
		}
		if crashed && index == 0 {
			switch fault {
			case "ordinary-failure":
				status, reason = "failed", "script_failure"
			case "canceled":
				status, reason = "canceled", "job_canceled"
			case "success":
				status, reason = "success", ""
			}
		}
		if index == 1 && fault != "second-failure" {
			status, reason = "success", ""
		}
		j := job{ID: jobID, Status: status, FailureReason: reason}
		j.Pipeline.ID, j.Commit.ID, j.Runner.ID = pipeID, r.FixtureSHA, 2
		if crashed && fault == "wrong-job" {
			j.ID++
		}
		if crashed && fault == "wrong-sha" {
			j.Commit.ID = strings.Repeat("b", 40)
		}
		switch path {
		case fmt.Sprintf("/pipelines/%d", pipeID):
			write(pipeline{ID: pipeID, SHA: r.FixtureSHA, Status: status})
		case fmt.Sprintf("/pipelines/%d/jobs", pipeID):
			write([]job{j})
		case fmt.Sprintf("/jobs/%d/trace", jobID):
			if fault != "no-checkpoint" {
				_, _ = fmt.Fprintf(w, "01O FLEETING_CHECKPOINT %s manager-crash %d i-worker0\n", nonces[index], jobID)
			}
		case fmt.Sprintf("/jobs/%d/artifacts/result.json", jobID):
			container := executor == "docker-autoscaler"
			id := "i-worker1"
			if fault == "same-worker" {
				id = "i-worker0"
			}
			write(Artifact{Nonce: nonces[index], FixtureSHA: r.FixtureSHA, InstanceID: id, RunnerID: 2, CheckoutOK: true, CommandOK: true, Executor: executor, ContainerDetected: &container})
		default:
			http.Error(w, "unexpected synthetic route", 404)
		}
	}))
	t.Cleanup(server.Close)
	s := crashSequence{jobSequence: jobSequence{
		gl: gitlab{base: server.URL, project: "1", token: "synthetic", http: server.Client()}, result: r, runnerID: 2,
		ref: "main", tag: "acceptance", pollInterval: time.Millisecond,
		checkManager: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if actualPID != r.ManagerPID || crashed && fault == "second-restart" {
				return errors.New("Manager changed unexpectedly")
			}
			return nil
		},
		observe: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if len(r.Instances) < len(nonces) {
				r.Instances = append(r.Instances, Resource{InstanceID: "i-worker" + strconv.Itoa(len(nonces)-1), SpotVerified: true, InventoryComplete: true})
			}
			return nil
		},
		release: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if !crashed || fault == "leak" {
				return errors.New("orphaned resources remain")
			}
			if fault == "replacement" {
				r.Instances = append(r.Instances, Resource{InstanceID: "i-unrequested"})
			}
			releases++
			events = append(events, "release")
			for i := range r.Instances {
				r.Instances[i].DeletionVerified = true
			}
			return nil
		},
	}, persist: func() error {
		mu.Lock()
		defer mu.Unlock()
		if !crashed || canceled || releases != 1 || fault == "evidence-write" {
			return errors.New("cannot persist crash recovery evidence")
		}
		persisted = true
		events = append(events, "persist")
		return nil
	}, crash: func(context.Context) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		if fault == "crash-error" {
			return 0, errors.New("crash or automatic restart failed")
		}
		crashed = true
		events = append(events, "crash")
		if fault != "same-pid" {
			actualPID = 456
		}
		return actualPID, nil
	}}
	return s, func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(events, ",") }
}
