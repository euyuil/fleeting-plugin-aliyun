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
		for _, reason := range []string{"server_timeout_running", "no_updates_running"} {
			s, events := fakeCrash(t, executor, reason, "")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if err := s.run(ctx); err != nil {
				cancel()
				t.Fatal(err)
			}
			cancel()
			if events() != "create1,crash,release,create2,release" {
				t.Fatal("wrong recovery order", events())
			}
			if !s.result.Jobs[0].CrashInjected || s.result.Jobs[0].ManagerPID == s.result.Jobs[1].ManagerPID {
				t.Fatal("crash identity missing")
			}
			for _, change := range []func(*Result){
				func(r *Result) { r.CrashVerified = false },
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
	for _, fault := range []string{"early-failure", "no-checkpoint", "crash-error", "same-pid", "still-running", "ordinary-failure", "canceled", "success", "second-restart", "leak", "replacement", "second-failure", "same-worker"} {
		t.Run(fault, func(t *testing.T) {
			s, _ := fakeCrash(t, "instance", "server_timeout_running", fault)
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			if s.run(ctx) == nil {
				t.Fatal("invalid crash recovery passed")
			}
		})
	}
}

func fakeCrash(t *testing.T, executor, failureReason, fault string) (crashSequence, func() string) {
	t.Helper()
	var mu sync.Mutex
	var events, nonces []string
	crashed, releases, actualPID := false, 0, 123
	r := &Result{Ledger: Ledger{ManagerPID: 123, InstanceLimit: 1, CreationBudget: 2}, JobScenario: "manager-crash", FixtureSHA: strings.Repeat("a", 40), Executor: executor}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(req.URL.Path, "/api/v4/projects/1")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if path == "/pipeline" && req.Method == "POST" {
			if len(nonces) == 1 && (!crashed || releases != 1 || actualPID == 123) {
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
		index := len(nonces) - 1
		pipeID, jobID := int64(index+1), int64(index+11)
		status, reason := "running", ""
		if crashed && fault != "still-running" || fault == "early-failure" {
			status, reason = "failed", failureReason
		}
		if crashed && index == 0 {
			switch fault {
			case "ordinary-failure":
				reason = "script_failure"
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
