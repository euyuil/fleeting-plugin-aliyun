package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type laneDispatchServer struct {
	mu                                       sync.Mutex
	t                                        *testing.T
	s                                        *jobSequence
	pipes                                    map[int64]pipeline
	jobs                                     map[int64]job
	variables                                map[int64][]laneVariable
	creates, cancels                         int
	lostCreate, lostCancel, replay, swapJobs bool
	revivePipeline, reviveJob                bool
}

func (f *laneDispatchServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	c := f.s.gl.lane.context
	path := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/9")
	var out any
	switch {
	case path == "/pipeline" && r.Method == "POST":
		f.creates++
		body, _ := io.ReadAll(r.Body)
		e := &f.s.result.Jobs[len(f.s.result.Jobs)-1]
		intent, _, err := f.s.readLaneIntent(e)
		if err != nil || intent.Stage != "submit_unknown" || intent.Request != string(body) {
			f.t.Error("the exact request was not durable before its only POST")
		}
		var request struct {
			Ref       string
			Variables []laneVariable
		}
		_ = json.Unmarshal(body, &request)
		for i := range request.Variables {
			request.Variables[i].VariableType = "env_var"
		}
		owner, err := c.verifyDeclaration(request.Variables, f.s.gl.token, false)
		if err != nil {
			f.t.Error(err)
		}
		id := int64(100 + f.creates)
		p := pipeline{ID: id, SHA: c.FixtureSHA, Ref: request.Ref, Source: "api", ProjectID: 9, Status: "running"}
		p.User.ID = 7
		j := job{ID: id + 900, Status: "running", Name: "spot-smoke", Ref: c.FixtureRef, TagList: []string{owner["runner_tag"]}}
		if owner["job_scenario"] == "capacity" {
			j.Name = "spot-controlled"
		}
		j.Pipeline.ID, j.Commit.ID = id, c.FixtureSHA
		j.Runner.ID, _ = strconv.ParseInt(owner["runner_id"], 10, 64)
		j.RunnerManager.SystemID = owner["system_id"]
		f.pipes[id], f.jobs[id], f.variables[id] = p, j, request.Variables
		if f.lostCreate {
			w.WriteHeader(502)
			return
		}
		out = p
	case path == "/pipelines":
		rows := []pipeline{}
		if r.URL.Query().Get("source") == "parent_pipeline" {
			out = rows
			break
		}
		for _, p := range f.pipes {
			if f.revivePipeline && p.ID == 101 && r.URL.Query().Get("ref") == "" {
				p.Status = "running"
			}
			if r.URL.Query().Get("ref") != "" || p.Status == r.URL.Query().Get("status") {
				rows = append(rows, p)
			}
		}
		if f.replay {
			for _, p := range f.pipes {
				p.ID = 999
				rows = append(rows, p)
				break
			}
		}
		out = rows
	case path == "/jobs":
		rows := []job{}
		for _, j := range f.jobs {
			if f.reviveJob && j.ID == 1001 {
				j.Status = "running"
			}
			if !terminalJob(j.Status) {
				if f.swapJobs {
					j.ID = 2003 - j.ID
				}
				rows = append(rows, j)
			}
		}
		out = rows
	case strings.HasPrefix(path, "/pipelines/"):
		parts := strings.Split(strings.TrimPrefix(path, "/pipelines/"), "/")
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		lookup := id
		if id == 999 {
			lookup = 101
		}
		p, ok := f.pipes[lookup]
		if !ok {
			w.WriteHeader(404)
			return
		}
		p.ID = id
		if len(parts) == 1 {
			out = p
			break
		}
		switch parts[1] {
		case "variables":
			out = f.variables[lookup]
		case "jobs":
			if r.URL.Query().Get("include_retried") != "true" {
				f.t.Error("cleanup omitted retried jobs")
			}
			j := f.jobs[lookup]
			j.Pipeline.ID = id
			if id == 999 {
				j.ID = 1999
			}
			out = []job{j}
		case "cancel":
			if r.Method != "POST" {
				f.t.Error("wrong cancel method")
			}
			intent, _, err := f.s.readLaneIntent(&f.s.result.Jobs[0])
			if err != nil || !intent.CancelSent || intent.PipelineID != id || intent.JobID <= 0 {
				f.t.Error("cancel was not bound and persisted first")
			}
			f.cancels++
			p.Status = "canceled"
			f.pipes[id] = p
			j := f.jobs[id]
			j.Status = "canceled"
			f.jobs[id] = j
			if f.lostCancel {
				w.WriteHeader(502)
				return
			}
			out = p
		default:
			f.t.Errorf("unexpected pipeline operation %s", parts[1])
			w.WriteHeader(404)
			return
		}
	default:
		f.t.Errorf("unexpected lane request %s", path)
		w.WriteHeader(404)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func newDispatchTest(t *testing.T, scenario string) (*jobSequence, *laneDispatchServer) {
	t.Helper()
	c := testLaneContext()
	r := &Result{Ledger: Ledger{Path: filepath.Join(t.TempDir(), "ledger.json"), ManagerPID: 123}, Result: "FAIL_INFRASTRUCTURE",
		Executor: "instance", JobScenario: scenario, Nonce: strings.Repeat("e", 48), FixtureSHA: c.FixtureSHA}
	s := &jobSequence{result: r, ref: c.FixtureRef, systemID: "r_000000000009", runnerID: 5, tag: c.Runners["instance"].Tag}
	f := &laneDispatchServer{t: t, s: s, pipes: map[int64]pipeline{}, jobs: map[int64]job{}, variables: map[int64][]laneVariable{}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	s.gl = gitlab{base: server.URL, project: "9", token: "synthetic-existing-pat", http: server.Client(), lane: &jobLaneScope{context: c, executor: "instance"}}
	return s, f
}

func TestLaneUnknownSubmissionIsFrozenReconciledForCleanupAndNeverResubmitted(t *testing.T) {
	s, f := newDispatchTest(t, "smoke")
	f.lostCreate, f.lostCancel = true, true
	if _, err := s.start(context.Background(), "smoke"); err == nil {
		t.Fatal("unknown create was converted to success")
	}
	if len(s.result.Jobs) != 1 || s.result.Jobs[0].PipelineID != 0 {
		t.Fatal("unknown dispatch lost its local nonce or invented an identity")
	}
	if err := s.cleanupLaneDispatch(context.Background(), &s.result.Jobs[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupLaneDispatch(context.Background(), &s.result.Jobs[0]); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || f.cancels != 1 || s.result.Jobs[0].PipelineID != 101 || s.result.Jobs[0].JobID != 1001 || s.result.Result != "FAIL_INFRASTRUCTURE" {
		t.Fatal("unknown writes were repeated, misidentified or promoted to PASS")
	}
}

func TestLaneCleanupRejectsReplayAndChangedFrozenRequestBeforeMutation(t *testing.T) {
	for _, fault := range []string{"replay", "intent"} {
		t.Run(fault, func(t *testing.T) {
			s, f := newDispatchTest(t, "smoke")
			f.lostCreate = true
			_, _ = s.start(context.Background(), "smoke")
			if fault == "replay" {
				f.replay = true
			} else {
				intent, path, _ := s.readLaneIntent(&s.result.Jobs[0])
				intent.Request = "{}"
				if err := WriteJSON(path, intent); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.cleanupLaneDispatch(context.Background(), &s.result.Jobs[0]); err == nil {
				t.Fatal("incomplete ownership was admitted for cancellation")
			}
			if f.creates != 1 || f.cancels != 0 {
				t.Fatal("unsafe pipeline was mutated")
			}
		})
	}
}

func TestLaneProjectJobsMustMatchTheirExactPipelineJobPair(t *testing.T) {
	s, f := newDispatchTest(t, "capacity")
	for range 2 {
		if _, err := s.start(context.Background(), "capacity"); err != nil {
			t.Fatal(err)
		}
	}
	peer := s.gl
	peer.lane = &jobLaneScope{context: s.gl.lane.context, executor: "docker-autoscaler"}
	if err := peer.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{}); err != nil {
		t.Fatal(err)
	}
	f.swapJobs = true
	if err := peer.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{}); err == nil {
		t.Fatal("two legitimate jobs could swap pipeline associations")
	}
	if f.cancels != 0 {
		t.Fatal("read-only ownership check mutated a pipeline")
	}
}

func TestLaneIntentFailurePreventsPost(t *testing.T) {
	s, f := newDispatchTest(t, "smoke")
	s.result.Ledger.Path = ""
	if _, err := s.start(context.Background(), "smoke"); err == nil || f.creates != 0 {
		t.Fatal("POST escaped without durable intent")
	}
}

func TestLaneTerminalObservationCannotHideASecondActivePeerCase(t *testing.T) {
	for _, kind := range []string{"pipeline", "job"} {
		t.Run(kind, func(t *testing.T) {
			s, f := newDispatchTest(t, "smoke")
			if _, err := s.start(context.Background(), "smoke"); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			p := f.pipes[101]
			p.Status = "success"
			f.pipes[101] = p
			j := f.jobs[101]
			j.Status = "success"
			f.jobs[101] = j
			f.mu.Unlock()
			s.result = &Result{Ledger: Ledger{Path: filepath.Join(t.TempDir(), "ledger.json"), ManagerPID: 124}, Result: "FAIL_INFRASTRUCTURE",
				Executor: "instance", JobScenario: "failure", Nonce: strings.Repeat("f", 48), FixtureSHA: s.gl.lane.context.FixtureSHA}
			s.systemID = "r_000000000008"
			if _, err := s.start(context.Background(), "failure"); err != nil {
				t.Fatal(err)
			}
			peer := s.gl
			peer.lane = &jobLaneScope{context: s.gl.lane.context, executor: "docker-autoscaler"}
			if err := peer.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{}); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.revivePipeline = kind == "pipeline"
			f.reviveJob = kind == "job"
			f.mu.Unlock()
			if err := peer.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{}); !errors.Is(err, errLaneVisibility) {
				t.Fatal("terminal-to-active conflict did not require a complete reread", err)
			}
		})
	}
}
