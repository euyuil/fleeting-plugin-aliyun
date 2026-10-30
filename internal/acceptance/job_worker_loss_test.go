package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkerLossThenSuccessfulJob(t *testing.T) {
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		s, events := fakeWorkerLoss(t, executor, "")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := s.run(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		if events() != "create1,delete,release,create2,release" || s.result.Jobs[0].NormalRelease || !s.result.Jobs[1].NormalRelease {
			t.Fatal("injected deletion was confused with normal release", events())
		}
		for _, change := range []func(*Result){
			func(r *Result) { r.WorkerLossVerified = false },
			func(r *Result) { r.RecoveryPolicy = "legacy" },
			func(r *Result) { r.Jobs[0].LossObserved = false },
			func(r *Result) { r.Jobs[0].LossInjected = false },
			func(r *Result) { r.Jobs[0].NormalRelease = true },
			func(r *Result) { r.Jobs[1].LossInjected = true },
		} {
			copy := *s.result
			copy.Jobs = append([]JobEvidence(nil), s.result.Jobs...)
			change(&copy)
			if verifyJobSequence(&copy) == nil {
				t.Fatal("incomplete or forged worker loss proof accepted")
			}
		}
	}
}

func TestWorkerLossUsesConfirmedLossInsteadOfTraceWording(t *testing.T) {
	for _, fault := range []string{"script-failure", "ssh-loss", "ssh-keepalive-loss", "spoof-transport", "continued-transport", "spoof-keepalive", "continued-keepalive"} {
		t.Run(fault, func(t *testing.T) {
			s, _ := fakeWorkerLoss(t, "instance", fault)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.run(ctx); err != nil {
				t.Fatal(err)
			}
			if !s.result.Jobs[0].LossObserved || s.result.Jobs[0].FailureReason != "script_failure" {
				t.Fatal("missing confirmed worker-loss evidence")
			}
			s.result.Executor = "docker-autoscaler"
			if verifyJobSequence(s.result) == nil {
				t.Fatal("instance SSH exception accepted for Docker")
			}
		})
	}
}

func TestWorkerLossDrainsOneReplacementBeforeFreshRecovery(t *testing.T) {
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		t.Run(executor, func(t *testing.T) {
			s, events := fakeWorkerLoss(t, executor, "ess-replacement")
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := s.run(ctx); err != nil {
				t.Fatal(err)
			}
			if events() != "create1,delete,replace,release,create2,release" ||
				s.result.LossReplacementID != "i-replacement" || len(s.result.Instances) != 3 {
				t.Fatal("replacement was not reconciled before fresh recovery", events())
			}
			for _, change := range []func(*Result){
				func(r *Result) { r.LossReplacementID = "" },
				func(r *Result) { r.LossReplacementID = r.Jobs[0].InstanceID },
				func(r *Result) { r.LossReplacementID = r.Jobs[1].InstanceID },
				func(r *Result) { r.LossReplacementID = "i-unknown" },
				func(r *Result) { r.CreationBudget = 4 },
				func(r *Result) { r.InstanceLimit = 2 },
				func(r *Result) { r.Instances[1].DeletionVerified = false },
				func(r *Result) { r.Instances[1].SpotVerified = false },
				func(r *Result) { r.Instances[1].InventoryComplete = false },
				func(r *Result) { r.Instances[1].UnsafeDiskPolicy = true },
				func(r *Result) { r.Instances[1] = r.Instances[0] },
			} {
				copy := *s.result
				copy.Instances = append([]Resource(nil), s.result.Instances...)
				change(&copy)
				if verifyJobSequence(&copy) == nil {
					t.Fatal("unbound or incomplete replacement evidence accepted")
				}
			}
		})
	}
}

const keepaliveTransportLossLine = "ERROR: Job failed: ssh connection closed by keepalive (keepalive probe timed out after 30s): run exit (exit code: 0): wait: remote command exited without exit status or exit signal"

func TestWorkerLossKeepaliveErrorRequiresPinnedCanonicalTimeoutAndMissingExit(t *testing.T) {
	for _, duration := range []string{"30s", "1m0s", "1m30.5s"} {
		if !isSSHTransportLossLine(strings.Replace(keepaliveTransportLossLine, "30s", duration, 1)) {
			t.Fatalf("valid connector timeout rejected: %s", duration)
		}
	}
	for _, duration := range []string{"0s", "-30s", "29s", "30000ms", "30.0s", "1e3s", "private", "999999999999999h"} {
		if isSSHTransportLossLine(strings.Replace(keepaliveTransportLossLine, "30s", duration, 1)) {
			t.Fatal("invalid or noncanonical timeout accepted")
		}
	}
	for _, line := range []string{
		strings.Replace(keepaliveTransportLossLine, "exit code: 0", "exit code: 1", 1),
		strings.Replace(keepaliveTransportLossLine, "keepalive probe timed out after 30s", "keepalive probe failed: EOF", 1),
		strings.Replace(keepaliveTransportLossLine, "wait: remote command exited without exit status or exit signal", "context canceled", 1),
		keepaliveTransportLossLine + " trailing output",
		"echo " + keepaliveTransportLossLine,
	} {
		if isSSHTransportLossLine(line) {
			t.Fatal("unproven transport error accepted")
		}
	}
}

func TestWorkerLossRejectsWrongFailureOrIncompleteRecovery(t *testing.T) {
	for _, fault := range []string{"early-failure", "no-checkpoint", "delete-error", "no-loss", "canceled", "unexpected-success", "manager-restart", "leak", "replacement", "replacement-leak", "two-replacements", "late-replacement", "reuse-replacement", "second-failure", "same-worker"} {
		t.Run(fault, func(t *testing.T) {
			s, _ := fakeWorkerLoss(t, "instance", fault)
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			if s.run(ctx) == nil {
				t.Fatal("invalid loss or recovery passed")
			}
		})
	}
}

func fakeWorkerLoss(t *testing.T, executor, fault string) (workerLossSequence, func() string) {
	t.Helper()
	var mu sync.Mutex
	var events, nonces []string
	lost, releases := false, 0
	r := &Result{Ledger: Ledger{ManagerPID: 123, InstanceLimit: 1, CreationBudget: 3}, JobScenario: "worker-loss", FixtureSHA: strings.Repeat("a", 40), Executor: executor}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := strings.TrimPrefix(req.URL.Path, "/api/v4/projects/1")
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if path == "/pipeline" && req.Method == "POST" {
			if len(nonces) == 1 && (!lost || releases != 1) {
				http.Error(w, "premature recovery", 409)
				return
			}
			if len(nonces) == 1 {
				for _, instance := range r.Instances {
					if !instance.DeletionVerified {
						http.Error(w, "recovery preceded resource release", 409)
						return
					}
				}
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
		if lost && fault != "no-loss" || fault == "early-failure" {
			status, reason = "failed", "runner_system_failure"
		}
		if lost && index == 0 {
			switch fault {
			case "script-failure", "ssh-loss", "ssh-keepalive-loss", "spoof-transport", "continued-transport", "spoof-keepalive", "continued-keepalive":
				reason = "script_failure"
			case "canceled":
				status, reason = "canceled", "job_canceled"
			case "unexpected-success":
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
				_, _ = fmt.Fprintf(w, "01O FLEETING_CHECKPOINT %s worker-loss %d i-worker0\n", nonces[index], jobID)
			}
			if lost {
				switch fault {
				case "ssh-loss":
					_, _ = fmt.Fprintln(w, "2026-10-29T00:00:00.000001Z 01O \x1b[31;1m"+sshTransportLossLine+"\x1b[0;m\r")
				case "ssh-keepalive-loss":
					_, _ = fmt.Fprintln(w, "2026-10-29T00:00:00.000001Z 01O \x1b[31;1m"+keepaliveTransportLossLine+"\x1b[0;m\r")
				case "spoof-transport":
					_, _ = fmt.Fprintln(w, "01O $ printf '"+sshTransportLossLine+"'")
				case "continued-transport":
					_, _ = fmt.Fprintln(w, "01+ "+sshTransportLossLine)
				case "spoof-keepalive":
					_, _ = fmt.Fprintln(w, "01O $ printf '"+keepaliveTransportLossLine+"'")
				case "continued-keepalive":
					_, _ = fmt.Fprintln(w, "01+ "+keepaliveTransportLossLine)
				}
			}
		case fmt.Sprintf("/jobs/%d/artifacts/result.json", jobID):
			container := executor == "docker-autoscaler"
			id := "i-worker1"
			if fault == "same-worker" {
				id = "i-worker0"
			}
			if fault == "reuse-replacement" {
				id = "i-replacement"
			}
			write(Artifact{Nonce: nonces[index], FixtureSHA: r.FixtureSHA, InstanceID: id, RunnerID: 2,
				CheckoutOK: true, CommandOK: true, Executor: executor, ContainerDetected: &container, IsolationOK: true})
		default:
			http.Error(w, "unexpected synthetic route", 404)
		}
	}))
	t.Cleanup(server.Close)
	s := workerLossSequence{jobSequence: jobSequence{
		gl: gitlab{base: server.URL, project: "1", token: "synthetic", http: server.Client()}, result: r, runnerID: 2,
		ref: "main", tag: "acceptance", pollInterval: time.Millisecond,
		checkManager: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if lost && fault == "manager-restart" {
				return errors.New("unexpected Manager restart")
			}
			return nil
		},
		observe: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			id := "i-worker" + strconv.Itoa(len(nonces)-1)
			known := false
			for _, instance := range r.Instances {
				known = known || instance.InstanceID == id
			}
			if !known {
				r.Instances = append(r.Instances, Resource{InstanceID: id, SpotVerified: true, InventoryComplete: true})
			}
			if lost && releases == 0 && len(r.Instances) == 1 &&
				(fault == "ess-replacement" || fault == "replacement-leak" || fault == "two-replacements" || fault == "reuse-replacement") {
				r.Instances = append(r.Instances, Resource{InstanceID: "i-replacement", SpotVerified: true, InventoryComplete: true})
				events = append(events, "replace")
				if fault == "two-replacements" {
					r.Instances = append(r.Instances, Resource{InstanceID: "i-extra", SpotVerified: true, InventoryComplete: true})
				}
			}
			if fault == "late-replacement" && len(nonces) == 2 && len(r.Instances) == 2 {
				r.Instances = append(r.Instances, Resource{InstanceID: "i-late", SpotVerified: true, InventoryComplete: true})
			}
			return nil
		},
		release: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			if !lost || fault == "leak" {
				return errors.New("loss resources were not reconciled")
			}
			if fault == "replacement" {
				r.Instances = append(r.Instances, Resource{InstanceID: "i-unrequested"})
			}
			releases++
			events = append(events, "release")
			for i := range r.Instances {
				r.Instances[i].DeletionVerified = fault != "replacement-leak" || r.Instances[i].InstanceID != "i-replacement"
			}
			return nil
		},
	}, loseWorker: func(_ context.Context, e JobEvidence) error {
		mu.Lock()
		defer mu.Unlock()
		if !e.Checkpoint || e.Status != "running" || e.JobID != 11 || e.InstanceID != "i-worker0" || fault == "delete-error" {
			return errors.New("invalid or rejected deletion")
		}
		lost = true
		events = append(events, "delete")
		return nil
	}}
	return s, func() string { mu.Lock(); defer mu.Unlock(); return strings.Join(events, ",") }
}

type lossInjectionAPI struct {
	base        *acceptanceAPI
	calls       []map[string]string
	err         error
	keepPresent bool
}

func (a *lossInjectionAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	if action == "DeleteInstance" {
		a.calls = append(a.calls, q)
		if a.err == nil && !a.keepPresent {
			delete(a.base.fake.Instances, q["InstanceId"])
		}
		return a.err
	}
	return a.base.Call(ctx, service, action, q, out)
}

func TestLossInjectionIsBoundedToTheInventoriedOwnedSpotWorker(t *testing.T) {
	for _, fault := range []string{"", "foreign-owner", "attached", "another-member", "incomplete", "unsafe-disk", "wrong-worker", "on-demand", "stopped", "no-checkpoint", "ambiguous-response", "still-present"} {
		t.Run(fault, func(t *testing.T) {
			cloud, base, ledger := fixture()
			base.fake.Group.MaxSize = 1
			if err := Claim(context.Background(), cloud, ledger); err != nil {
				t.Fatal(err)
			}
			base.fake.Add("i-worker")
			if err := Observe(context.Background(), cloud, ledger, "ecs.test.small", "image-test", "cn-test-1a"); err != nil {
				t.Fatal(err)
			}
			e := JobEvidence{Scenario: "worker-loss", Status: "running", Checkpoint: true, InstanceID: "i-worker"}
			api := &lossInjectionAPI{base: base}
			cloud.API = api
			switch fault {
			case "foreign-owner":
				ledger.RunID = "101"
			case "attached":
				member := base.fake.Members["i-worker"]
				member.CreationType = "Attached"
				base.fake.Members["i-worker"] = member
			case "another-member":
				base.fake.Add("i-other")
			case "incomplete":
				ledger.Instances[0].InventoryComplete = false
			case "unsafe-disk":
				ledger.Instances[0].UnsafeDiskPolicy = true
			case "wrong-worker":
				e.InstanceID = "i-foreign"
			case "on-demand":
				worker := base.fake.Instances["i-worker"]
				worker.SpotStrategy = "NoSpot"
				base.fake.Instances["i-worker"] = worker
			case "stopped":
				worker := base.fake.Instances["i-worker"]
				worker.Status = "Stopped"
				base.fake.Instances["i-worker"] = worker
			case "no-checkpoint":
				e.Checkpoint = false
			case "ambiguous-response":
				api.err = errors.New("response lost")
			case "still-present":
				api.keepPresent = true
			}
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			err := injectWorkerLoss(ctx, cloud, ledger, e)
			if (err == nil) != (fault == "") {
				t.Fatal("unexpected injection result", err)
			}
			if fault == "" || fault == "ambiguous-response" || fault == "still-present" {
				want := []map[string]string{{"RegionId": cloud.Region, "InstanceId": "i-worker", "Force": "true", "ForceStop": "true"}}
				if !reflect.DeepEqual(api.calls, want) {
					t.Fatal("wrong deletion target or retry", api.calls)
				}
			} else if len(api.calls) != 0 {
				t.Fatal("unverified worker was deleted")
			}
		})
	}
}
