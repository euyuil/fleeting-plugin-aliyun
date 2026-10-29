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

func TestCapacityQueueAndIsolation(t *testing.T) {
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		t.Run(executor, func(t *testing.T) {
			s, events := fakeCapacity(t, executor, "")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.run(ctx); err != nil {
				t.Fatal(err)
			}
			if got := events(); got != "open0,release0,open1,open2,release-all" {
				t.Fatalf("unexpected order: %s", got)
			}
			if err := verifyJobSequence(s.result); err != nil {
				t.Fatal(err)
			}
			s.result.CapacityVerified = false
			if verifyJobSequence(s.result) == nil {
				t.Fatal("missing queue proof passed")
			}
		})
	}
}

func TestCapacityRejectsBrokenQueueReleaseAndIsolation(t *testing.T) {
	for _, fault := range []string{"early-third", "retained-failed", "release-failed", "wrong-isolation", "reused-worker", "manager-restarted", "no-replacement", "wrong-runner", "barrier-failed"} {
		t.Run(fault, func(t *testing.T) {
			s, _ := fakeCapacity(t, "instance", fault)
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			if s.run(ctx) == nil {
				t.Fatal("invalid capacity scenario passed")
			}
		})
	}
}

func fakeCapacity(t *testing.T, executor, fault string) (capacitySequence, func() string) {
	t.Helper()
	var mu sync.Mutex
	var nonces, events []string
	opened := [3]bool{}
	removed := false
	r := &Result{Ledger: Ledger{ManagerPID: 123, InstanceLimit: 2, CreationBudget: 3}, JobScenario: "capacity", FixtureSHA: strings.Repeat("a", 40), Executor: executor}
	worker := func(index int) string {
		if fault == "reused-worker" && index == 2 {
			return "i-worker1"
		}
		return "i-worker" + strconv.Itoa(index)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(req.URL.Path, "/api/v4/projects/1")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if path == "/pipeline" && req.Method == "POST" {
			var body struct{ Variables []struct{ Key, Value string } }
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			for _, v := range body.Variables {
				if v.Key == "FLEETING_NONCE" {
					nonces = append(nonces, v.Value)
				}
			}
			write(pipeline{ID: int64(len(nonces)), SHA: r.FixtureSHA, Status: "pending"})
			return
		}
		for index := range 3 {
			pipeID, jobID := int64(index+1), int64(index+11)
			status, reason := "running", ""
			if index == 2 && ((!removed && fault != "early-third") || fault == "no-replacement") {
				status = "pending"
			}
			if opened[index] {
				status = "success"
			}
			if fault == "retained-failed" && index == 1 && opened[0] {
				status, reason = "failed", "runner_system_failure"
			}
			j := job{ID: jobID, Status: status, FailureReason: reason}
			j.Pipeline.ID, j.Commit.ID = pipeID, r.FixtureSHA
			if status != "pending" {
				j.Runner.ID = 2
			}
			if fault == "wrong-runner" {
				j.Runner.ID = 3
			}
			switch path {
			case fmt.Sprintf("/pipelines/%d", pipeID):
				write(pipeline{ID: pipeID, SHA: r.FixtureSHA, Status: status})
				return
			case fmt.Sprintf("/pipelines/%d/jobs", pipeID):
				write([]job{j})
				return
			case fmt.Sprintf("/jobs/%d/trace", jobID):
				_, _ = fmt.Fprintf(w, "01O FLEETING_CHECKPOINT %s capacity %d %s\n", nonces[index], jobID, worker(index))
				return
			case fmt.Sprintf("/jobs/%d/artifacts/result.json", jobID):
				container := executor == "docker-autoscaler"
				write(Artifact{Nonce: nonces[index], FixtureSHA: r.FixtureSHA, InstanceID: worker(index), RunnerID: 2,
					CheckoutOK: true, CommandOK: true, Executor: executor, ContainerDetected: &container, IsolationOK: fault != "wrong-isolation"})
				return
			}
		}
		http.Error(w, "unexpected synthetic route", 404)
	}))
	t.Cleanup(server.Close)
	s := capacitySequence{jobSequence: jobSequence{gl: gitlab{base: server.URL, project: "1", token: "synthetic", http: server.Client()},
		result: r, runnerID: 2, ref: "main", tag: "acceptance", pollInterval: time.Millisecond,
		checkManager: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if fault == "manager-restarted" && opened[0] {
				return errors.New("Manager changed")
			}
			return nil
		},
		observe: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			count := 2
			if removed || fault == "early-third" {
				count = 3
			}
			for len(r.Instances) < count {
				r.Instances = append(r.Instances, Resource{InstanceID: "i-worker" + strconv.Itoa(len(r.Instances)), SpotVerified: true, InventoryComplete: true})
			}
			return nil
		},
		release: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			for i := range r.Instances {
				r.Instances[i].DeletionVerified = true
			}
			events = append(events, "release-all")
			return nil
		}},
		openJob: func(_ context.Context, e JobEvidence) error {
			mu.Lock()
			defer mu.Unlock()
			if fault == "barrier-failed" {
				return errors.New("barrier failed")
			}
			index := int(e.JobID) - 11
			if index > 0 && !removed {
				return errors.New("released another job before normal scale-in")
			}
			opened[index] = true
			events = append(events, "open"+strconv.Itoa(index))
			return nil
		},
		workerGone: func(_ context.Context, id string) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			if fault == "release-failed" {
				return false, errors.New("release failed")
			}
			if id != "i-worker0" || !opened[0] || opened[1] {
				return false, errors.New("wrong worker release")
			}
			removed = true
			r.Instances[0].DeletionVerified = true
			events = append(events, "release0")
			return true, nil
		},
	}
	return s, func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(events, ",") }
}

func TestCapacityControlsAreBoundedAndReleaseIsReadOnly(t *testing.T) {
	e := JobEvidence{Nonce: strings.Repeat("a", 48), JobID: 12, Checkpoint: true}
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		command, err := capacityCommand(e, executor)
		if err != nil || !strings.Contains(command, e.Nonce) {
			t.Fatal("bound command rejected")
		}
	}
	e.Nonce = "'; arbitrary-command"
	if _, err := capacityCommand(e, "instance"); err == nil {
		t.Fatal("invalid control nonce accepted")
	}
	c, a, l := twoWorkers(t)
	l.CreationBudget = 3
	ctx := context.Background()
	if gone, err := normalWorkerGone(ctx, c, l, "i-remove"); gone || err != nil {
		t.Fatal("live worker accepted as released")
	}
	removeFake(a, "i-remove")
	a.fake.Calls = nil
	if gone, err := normalWorkerGone(ctx, c, l, "i-remove"); !gone || err != nil {
		t.Fatal("normal release rejected", err)
	}
	for _, call := range a.fake.Calls {
		if !strings.HasPrefix(call.Action, "Describe") {
			t.Fatal("release observer mutated capacity")
		}
	}
	if _, exists := a.fake.Instances["i-keep"]; !exists {
		t.Fatal("other worker removed")
	}
	for _, size := range []int{1, 3} {
		a.fake.Group.MaxSize = size
		if checkJobCapacity(ctx, c) == nil {
			t.Fatal("wrong fixture limit accepted")
		}
	}
}
