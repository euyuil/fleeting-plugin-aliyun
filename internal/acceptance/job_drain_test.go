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

func TestJobGracefulDrainAndRestart(t *testing.T) {
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		s, events := fakeDrain(t, executor, "")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := s.run(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		if got := events(); got != "create1,stop-start,open,stop-done,release,restart,create2,release" {
			t.Fatal("wrong drain order", got)
		}
		if s.result.Jobs[0].ManagerPID == s.result.Jobs[1].ManagerPID {
			t.Fatal("second job did not use a restarted Manager")
		}
		s.result.DrainVerified = false
		if verifyJobSequence(s.result) == nil {
			t.Fatal("missing drain proof passed")
		}
	}
}

func TestJobDrainRejectsAbortionPrematureStopAndIncompleteRecovery(t *testing.T) {
	for _, fault := range []string{"stop-early", "stop-error", "no-drain", "job-aborted", "restart-same-pid", "release-failed", "second-job-failed", "changed-invocation"} {
		t.Run(fault, func(t *testing.T) {
			s, _ := fakeDrain(t, "instance", fault)
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			if s.run(ctx) == nil {
				t.Fatal("invalid drain accepted")
			}
		})
	}
}

func fakeDrain(t *testing.T, executor, fault string) (drainSequence, func() string) {
	t.Helper()
	var mu sync.Mutex
	var events, nonces []string
	draining, opened, stopped := false, false, false
	releases, actualPID := 0, 123
	done := make(chan error, 1)
	r := &Result{Ledger: Ledger{ManagerPID: actualPID, InstanceLimit: 1, CreationBudget: 2}, JobScenario: "drain", FixtureSHA: strings.Repeat("a", 40), Executor: executor}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(req.URL.Path, "/api/v4/projects/1")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if path == "/pipeline" && req.Method == "POST" {
			if len(nonces) == 1 && (releases != 1 || actualPID == 123 || !stopped) {
				http.Error(w, "early second pipeline", 409)
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
			write(pipeline{ID: int64(len(nonces)), SHA: r.FixtureSHA, Status: "pending"})
			return
		}
		index := len(nonces) - 1
		pipeID, jobID := int64(index+1), int64(index+11)
		status, reason := "success", ""
		if index == 0 && !opened {
			status = "running"
		}
		if fault == "job-aborted" && draining && index == 0 || fault == "second-job-failed" && index == 1 {
			status, reason = "failed", "runner_system_failure"
		}
		j := job{ID: jobID, Status: status, FailureReason: reason}
		j.Pipeline.ID, j.Commit.ID, j.Runner.ID = pipeID, r.FixtureSHA, 2
		switch path {
		case fmt.Sprintf("/pipelines/%d", pipeID):
			write(pipeline{ID: pipeID, SHA: r.FixtureSHA, Status: status})
		case fmt.Sprintf("/pipelines/%d/jobs", pipeID):
			write([]job{j})
		case fmt.Sprintf("/jobs/%d/trace", jobID):
			_, _ = fmt.Fprintf(w, "01O FLEETING_CHECKPOINT %s drain %d i-worker%d\n", nonces[index], jobID, index)
		case fmt.Sprintf("/jobs/%d/artifacts/result.json", jobID):
			container := executor == "docker-autoscaler"
			write(Artifact{Nonce: nonces[index], FixtureSHA: r.FixtureSHA, InstanceID: "i-worker" + strconv.Itoa(index), RunnerID: 2,
				CheckoutOK: true, CommandOK: true, Executor: executor, ContainerDetected: &container, IsolationOK: true})
		default:
			http.Error(w, "unexpected synthetic route", 404)
		}
	}))
	t.Cleanup(server.Close)
	s := drainSequence{capacitySequence: capacitySequence{jobSequence: jobSequence{
		gl: gitlab{base: server.URL, project: "1", token: "synthetic", http: server.Client()}, result: r, runnerID: 2,
		ref: "main", tag: "acceptance", pollInterval: time.Millisecond,
		checkManager: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if actualPID != r.ManagerPID {
				return errors.New("Manager changed")
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
			if fault == "release-failed" {
				return errors.New("normal release failed")
			}
			if !stopped {
				return errors.New("drain did not complete")
			}
			releases++
			events = append(events, "release")
			for i := range r.Instances {
				r.Instances[i].DeletionVerified = true
			}
			return nil
		},
	}, openJob: func(_ context.Context, e JobEvidence) error {
		mu.Lock()
		defer mu.Unlock()
		if !draining || stopped || e.JobID != 11 {
			return errors.New("opened before drain")
		}
		opened = true
		events = append(events, "open")
		return nil
	}},
		beginDrain: func(context.Context) (<-chan error, error) {
			mu.Lock()
			defer mu.Unlock()
			draining = true
			events = append(events, "stop-start")
			if fault == "stop-error" {
				done <- errors.New("stop failed")
			}
			return done, nil
		},
		drainState: func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if fault == "changed-invocation" {
				return "", errors.New("foreign invocation")
			}
			if fault == "no-drain" {
				return "active", nil
			}
			if fault == "stop-early" {
				return "stopped", nil
			}
			if opened {
				if !stopped {
					stopped = true
					events = append(events, "stop-done")
					done <- nil
				}
				return "stopped", nil
			}
			return "draining", nil
		},
		restart: func(context.Context) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			if releases != 1 || !stopped {
				return 0, errors.New("restart before release")
			}
			events = append(events, "restart")
			if fault != "restart-same-pid" {
				actualPID++
			}
			return actualPID, nil
		},
	}
	return s, func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(events, ",") }
}
