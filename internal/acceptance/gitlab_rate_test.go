package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

type rateTransport func(*http.Request) (*http.Response, error)

func (f rateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Advance only the actual client's pacing waits; no cloud or GitLab is called.
type rateClock struct {
	elapsed, limit time.Duration
	attempts       []time.Duration
}

func (c *rateClock) attach(g *gitlab) {
	p := newGitLabPacer()
	p.now = func() time.Time { return time.Unix(0, 0).Add(c.elapsed) }
	p.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d > 0 {
			c.elapsed += d
		}
		if c.limit > 0 && c.elapsed > c.limit {
			return context.DeadlineExceeded
		}
		return nil
	}
	g.pace = p
	transport := g.http.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	g.http = &http.Client{Transport: rateTransport(func(r *http.Request) (*http.Response, error) {
		c.attempts = append(c.attempts, c.elapsed)
		return transport.RoundTrip(r)
	})}
}

func fillRateHistory(t *testing.T, s *jobSequence, f *laneDispatchServer, n int) {
	t.Helper()
	c, id, manager := s.gl.lane.context, int64(100), 0
	add := func(scope jobLaneContext, executor, outer, inner string, index int) {
		id++
		owner, err := scope.ownership(executor, outer, inner, index, fmt.Sprintf("%048x", manager), fmt.Sprintf("r_%012x", manager), fmt.Sprintf("%048x", id))
		if err != nil {
			t.Fatal(err)
		}
		values, _ := signedLaneVariables(owner, s.gl.token)
		var vars []laneVariable
		for _, v := range values {
			vars = append(vars, laneVariable{Key: v["key"], Value: v["value"], VariableType: "env_var"})
		}
		p := pipeline{ID: id, SHA: c.FixtureSHA, Ref: c.FixtureRef, Source: "api", ProjectID: 9, Status: "success"}
		p.User.ID, p.User.Username = 7, "fixture-owner"
		j := job{ID: id + 900, Status: "success", Name: "spot-smoke", Ref: c.FixtureRef, TagList: []string{owner["runner_tag"]}}
		if slices.Contains([]string{"capacity", "drain", "worker-loss"}, inner) {
			j.Name = "spot-controlled"
		} else if inner == "manager-crash" {
			j.Name = "spot-manager-crash"
		}
		j.Pipeline.ID, j.Commit.ID, j.RunnerManager.SystemID = id, c.FixtureSHA, owner["system_id"]
		if executor == "instance" {
			j.Runner.ID = 5
		} else {
			j.Runner.ID = 6
		}
		f.pipes[id], f.jobs[id], f.variables[id] = p, j, vars
	}
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		for _, outer := range []string{"smoke", "failure", "cancel", "timeout", "capacity", "drain", "worker-loss", "manager-crash", "upgrade"} {
			manager++
			for i, inner := range jobCases(outer) {
				if len(f.pipes) < n {
					add(c, executor, outer, inner, i+1)
				}
			}
		}
	}
	if n > 34 {
		c.Phase, c.Profile = "public", "public-smoke"
		for _, executor := range []string{"instance", "docker-autoscaler"} {
			manager++
			add(c, executor, "smoke", "smoke", 1)
		}
		s.gl.lane.context = c
	}
}

func TestLaneColdInventoryFitsOriginalDeadlineAtBoundedRate(t *testing.T) {
	for _, n := range []int{0, 34, 36} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, f := newDispatchTest(t, "smoke")
			fillRateHistory(t, s, f, n)
			clock := &rateClock{limit: 2 * time.Minute}
			clock.attach(&s.gl)
			inventory, err := s.gl.readLaneInventory(context.Background(), nil)
			want := n + 8
			if n == 0 {
				want = 6
			}
			if err != nil || len(inventory) != n || len(clock.attempts) != want || clock.elapsed >= 2*time.Minute {
				t.Fatal("cold inventory escaped unchanged budget", len(inventory), clock.elapsed, len(clock.attempts), err)
			}
			for _, offset := range []time.Duration{0, 7500 * time.Millisecond} {
				times := append([]time.Duration(nil), clock.attempts...)
				for _, at := range clock.attempts {
					times = append(times, at+offset)
				}
				slices.Sort(times)
				for i, at := range times {
					count := 0
					for _, later := range times[i:] {
						if later-at < time.Minute {
							count++
						}
					}
					if count > 80 {
						t.Fatal("two lanes exceeded harness request budget", count)
					}
				}
			}
			t.Logf("N=%d: %d actual requests, virtual duration %s; two lanes <=80 per rolling minute", n, want, clock.elapsed)
		})
	}
}

func TestGitLabPacingIncludesAttemptsCopiesAndFreshClients(t *testing.T) {
	clock := &rateClock{}
	g := gitlab{base: "https://synthetic.example", project: "9", http: &http.Client{Transport: rateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	for range 3 {
		// Python/Go and fresh-host handoffs start a fresh pacer after the previous
		// process. The initial wait keeps their combined schedule free of bursts.
		clock.attach(&g)
		copy := g
		for range 45 {
			if err := copy.request(context.Background(), "GET", "/jobs", nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		// Avoid wrapping the recording transport on the next process.
		g.http.Transport = rateTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})
	}
	for i, at := range clock.attempts {
		if at != time.Duration(i+1)*gitlabRequestInterval {
			t.Fatal("handoff or copied client reset request budget", i, at)
		}
	}
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			attempts := 0
			g.http.Transport = rateTransport(func(*http.Request) (*http.Response, error) {
				attempts++
				return nil, errors.New("synthetic transport loss")
			})
			c := &rateClock{}
			c.attach(&g)
			if g.request(context.Background(), method, "/pipeline", nil, nil) == nil {
				t.Fatal("transport failure passed")
			}
			want := 1
			if method == "GET" {
				want = 3
			}
			if attempts != want || len(c.attempts) != want {
				t.Fatal("retry escaped the same pacing state", attempts, c.attempts)
			}
		})
	}
	g.http.Transport = rateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	c := &rateClock{}
	c.attach(&g)
	if g.request(context.Background(), "GET", "/jobs", nil, nil) == nil || len(c.attempts) != 1 {
		t.Fatal("Go HTTP429 was retried")
	}
}

func TestGitLabPacerHonorsCallerCancellationBeforeHTTP(t *testing.T) {
	attempts := 0
	g := gitlab{base: "https://synthetic.example", project: "9", pace: newGitLabPacer(), http: &http.Client{Transport: rateTransport(func(*http.Request) (*http.Response, error) { attempts++; return nil, errors.New("unexpected HTTP") })}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if !errors.Is(g.request(ctx, "POST", "/pipeline", nil, nil), context.DeadlineExceeded) || attempts != 0 {
		t.Fatal("pacing sent a write after its original deadline", attempts)
	}
}

func TestKnownLaneTargetKeepsFreshBindingAndCleanupIsolation(t *testing.T) {
	for _, fault := range []string{"creator", "declaration", "manager", "retry", "intent-stage", "replay"} {
		t.Run(fault, func(t *testing.T) {
			s, f := newDispatchTest(t, "smoke")
			e, err := s.start(context.Background(), "smoke")
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "creator":
				p := f.pipes[e.PipelineID]
				p.User.ID++
				f.pipes[p.ID] = p
			case "declaration":
				f.variables[e.PipelineID][0].Value = "changed"
			case "manager":
				j := f.jobs[e.PipelineID]
				j.RunnerManager.SystemID = "r_000000000088"
				f.jobs[e.PipelineID] = j
			case "retry":
				f.extraJob = true
			case "intent-stage":
				intent, path, _ := s.readLaneIntent(e)
				intent.Stage = "response_received_unverified"
				if err := WriteJSON(path, intent); err != nil {
					t.Fatal(err)
				}
				f.replay = true
			case "replay":
				f.replay = true
			}
			err = s.cleanupLaneDispatch(context.Background(), e)
			if fault == "replay" {
				if err != nil || f.cancels != 1 {
					t.Fatal("unrelated replay blocked cleanup of the authenticated exact target", err)
				}
				if err = s.gl.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{}); err == nil {
					t.Fatal("replay was allowed to pass complete final verification")
				}
			} else if err == nil || f.cancels != 0 {
				t.Fatal("changed or unverified target was mutated", err, f.cancels)
			}
		})
	}
}

func TestLaneCrashCancelForceFitsOneOriginalControlDeadline(t *testing.T) {
	s, f := newDispatchTest(t, "manager-crash")
	e, err := s.start(context.Background(), "manager-crash")
	if err != nil {
		t.Fatal(err)
	}
	f.keepCanceling = true
	r := s.result
	r.CrashVerified, r.OrphanReleased, r.RecoveryPolicy = true, true, crashRecoveryPolicy
	e.InstanceID, e.Checkpoint, e.CrashInjected, e.NormalRelease = "i-synthetic", true, true, true
	r.Instances = []Resource{{InstanceID: e.InstanceID, InventoryComplete: true, SpotVerified: true, DeletionVerified: true}}
	s.checkManager = func(context.Context) error { return nil }
	s.observe = s.checkManager
	clock := &rateClock{limit: 2 * time.Minute}
	clock.attach(&s.gl)
	if err := (crashSequence{jobSequence: *s}).finalize(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if clock.elapsed >= 2*time.Minute || f.cancels != 1 || f.forces != 1 || e.Status != "canceled" {
		t.Fatal("crash control escaped its original deadline or write counts", clock.elapsed, f.cancels, f.forces)
	}
	t.Logf("cancel -> canceling -> force -> actual canceled: %d actual requests, virtual duration %s", len(clock.attempts), clock.elapsed)
}

func TestLaneCreatorBatchCannotHideForeignOrChangedRows(t *testing.T) {
	for _, fault := range []string{"missing", "missing-extra", "extra", "duplicate", "username", "identity"} {
		t.Run(fault, func(t *testing.T) {
			s, f := newDispatchTest(t, "smoke")
			fillRateHistory(t, s, f, 1)
			original := s.gl.http.Transport
			if original == nil {
				original = http.DefaultTransport
			}
			s.gl.http.Transport = rateTransport(func(req *http.Request) (*http.Response, error) {
				response, err := original.RoundTrip(req)
				if err != nil {
					return response, err
				}
				if strings.HasSuffix(req.URL.Path, "/pipelines/101") && fault == "username" {
					p := f.pipes[101]
					p.User.Username = ""
					data, _ := json.Marshal(p)
					response.Body.Close()
					response.Body = io.NopCloser(strings.NewReader(string(data)))
				}
				if req.URL.Query().Has("username") {
					p := f.pipes[101]
					rows := []pipeline{p}
					switch fault {
					case "missing":
						rows = []pipeline{}
					case "missing-extra":
						rows[0].ID = 999
					case "extra":
						p.ID = 999
						rows = append(rows, p)
					case "duplicate":
						rows = append(rows, p)
					case "identity":
						rows[0].SHA = strings.Repeat("0", 40)
					}
					data, _ := json.Marshal(rows)
					response.Body.Close()
					response.Body = io.NopCloser(strings.NewReader(string(data)))
				}
				return response, nil
			})
			err := s.gl.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{})
			if err == nil || (fault == "extra") != errors.Is(err, errLaneVisibility) {
				t.Fatal("creator batching confused pure visibility with a hard identity conflict", fault, err)
			}
		})
	}
}

func TestLaneCapacityPollingKeepsQueuedJobAndStopsRepeatedTraceReads(t *testing.T) {
	s, f := newDispatchTest(t, "capacity")
	for range 3 {
		if _, err := s.start(context.Background(), "capacity"); err != nil {
			t.Fatal(err)
		}
	}
	p, j := f.pipes[103], f.jobs[103]
	p.Status, j.Status = "pending", "pending"
	j.Runner.ID, j.RunnerManager.SystemID = 0, ""
	f.pipes[103], f.jobs[103] = p, j
	s.result.Instances = []Resource{{InstanceID: "i-synthetic1", InventoryComplete: true, SpotVerified: true}, {InstanceID: "i-synthetic2", InventoryComplete: true, SpotVerified: true}}
	clock := &rateClock{}
	clock.attach(&s.gl)
	seq := capacitySequence{jobSequence: *s}
	for i := range s.result.Jobs {
		if err := seq.poll(context.Background(), &s.result.Jobs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if len(clock.attempts) != 5 || !s.result.Jobs[0].Checkpoint || !s.result.Jobs[1].Checkpoint || s.result.Jobs[2].Status != "pending" || s.result.Jobs[2].Checkpoint {
		t.Fatal("capacity polling lost its two running and one queued assertion", len(clock.attempts), s.result.Jobs)
	}
	clock.elapsed += 10 * time.Second
	for i := range s.result.Jobs {
		if err := seq.poll(context.Background(), &s.result.Jobs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if len(clock.attempts) != 8 {
		t.Fatal("proved running checkpoints repeated trace or pipeline reads", len(clock.attempts))
	}
	// A terminal response must fetch and validate the trace again.
	e := &s.result.Jobs[0]
	e.InstanceID = "i-conflicting"
	if err := s.checkpoint(context.Background(), e, true); err == nil || len(clock.attempts) != 9 {
		t.Fatal("terminal checkpoint skipped fresh conflict validation", err)
	}
}

func TestLaneSmokeRunningPollDoesNotReadPipelineDetail(t *testing.T) {
	s, _ := newDispatchTest(t, "smoke")
	e, err := s.start(context.Background(), "smoke")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observations := 0
	s.checkManager = func(context.Context) error { return nil }
	s.observe = func(context.Context) error {
		observations++
		if observations == 4 {
			cancel()
		}
		return nil
	}
	clock := &rateClock{}
	clock.attach(&s.gl)
	if err := s.wait(ctx, e); !errors.Is(err, context.Canceled) || len(clock.attempts) != 3 {
		t.Fatal("running smoke amplified job reads", err, len(clock.attempts))
	}
}

func TestLaneLateVisibilityCannotExtendOriginalScanDeadline(t *testing.T) {
	s, f := newDispatchTest(t, "smoke")
	fillRateHistory(t, s, f, 34)
	s.gl.lane.executor = "docker-autoscaler"
	f.revivePipeline = true
	clock := &rateClock{limit: 2 * time.Minute}
	clock.attach(&s.gl)
	inventory, err := s.gl.readLaneInventory(context.Background(), nil)
	if !errors.Is(err, context.DeadlineExceeded) || inventory != nil || len(clock.attempts) > 80 {
		t.Fatal("late conflict returned partial admission or extended the original budget", len(inventory), clock.elapsed, len(clock.attempts), err)
	}
}

func TestLaneScopedJobBatchRejectsRetriesAndIncompleteViews(t *testing.T) {
	for _, fault := range []string{"new-peer", "new-peer-bad-sha", "new-peer-duplicate", "retry", "foreign-ref", "paged", "missing"} {
		t.Run(fault, func(t *testing.T) {
			s, f := newDispatchTest(t, "smoke")
			fillRateHistory(t, s, f, 1)
			original := s.gl.http.Transport
			if original == nil {
				original = http.DefaultTransport
			}
			s.gl.http.Transport = rateTransport(func(req *http.Request) (*http.Response, error) {
				response, err := original.RoundTrip(req)
				if err != nil {
					return response, err
				}
				if strings.HasSuffix(req.URL.Path, "/jobs") && req.URL.Query().Has("ref") {
					j := f.jobs[101]
					rows := []job{j}
					switch fault {
					case "new-peer", "new-peer-bad-sha", "new-peer-duplicate":
						j.ID, j.Pipeline.ID = 1002, 999
						rows = append([]job{j}, rows...)
						if fault == "new-peer-bad-sha" {
							rows[1].Commit.ID = "foreign-sha"
						}
						if fault == "new-peer-duplicate" {
							rows = append(rows, rows[0])
						}
					case "retry":
						j.ID++
						rows = append(rows, j)
					case "foreign-ref":
						rows[0].Ref = "foreign-ref"
					case "paged":
						response.Header.Set("Link", "</not-followed>; rel=next")
					case "missing":
						rows = []job{}
					}
					data, _ := json.Marshal(rows)
					response.Body.Close()
					response.Body = io.NopCloser(strings.NewReader(string(data)))
				}
				return response, nil
			})
			err := s.gl.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{})
			if err == nil {
				t.Fatal("partial or retried job view was admitted", fault)
			}
			if (fault == "new-peer" || fault == "missing") != errors.Is(err, errLaneVisibility) {
				t.Fatal("peer materialization did not use bounded read-only reconciliation", err)
			}
		})
	}
}

func TestLanePeerAppearingBetweenListsRequiresWholeAuthenticatedReread(t *testing.T) {
	s, f := newDispatchTest(t, "smoke")
	fillRateHistory(t, s, f, 1)
	original := s.gl.http.Transport
	if original == nil {
		original = http.DefaultTransport
	}
	inserted, scopedReads := false, 0
	s.gl.http.Transport = rateTransport(func(req *http.Request) (*http.Response, error) {
		response, err := original.RoundTrip(req)
		if err != nil {
			return response, err
		}
		if strings.HasSuffix(req.URL.Path, "/jobs") && req.URL.Query().Has("ref") {
			scopedReads++
			if !inserted {
				inserted = true
				c := s.gl.lane.context
				owner, err := c.ownership("docker-autoscaler", "smoke", "smoke", 1, strings.Repeat("8", 48), "r_000000000088", strings.Repeat("9", 48))
				if err != nil {
					t.Fatal(err)
				}
				values, _ := signedLaneVariables(owner, s.gl.token)
				var vars []laneVariable
				for _, v := range values {
					vars = append(vars, laneVariable{Key: v["key"], Value: v["value"], VariableType: "env_var"})
				}
				p := f.pipes[101]
				p.ID, p.Status = 102, "pending"
				j := f.jobs[101]
				j.ID, j.Pipeline.ID, j.Status = 1002, 102, "pending"
				j.Runner.ID, j.RunnerManager.SystemID = 0, ""
				j.TagList = []string{c.Runners["docker-autoscaler"].Tag}
				f.mu.Lock()
				f.pipes[102], f.jobs[102], f.variables[102] = p, j, vars
				f.mu.Unlock()
				rows := []job{f.jobs[101], j}
				data, _ := json.Marshal(rows)
				response.Body.Close()
				response.Body = io.NopCloser(strings.NewReader(string(data)))
			}
		}
		return response, nil
	})
	inventory, err := s.gl.readLaneInventory(context.Background(), nil)
	if err != nil || len(inventory) != 2 || scopedReads != 2 || inventory[102].owner["executor"] != "docker-autoscaler" {
		t.Fatal("peer was partly admitted or not fully authenticated on reread", len(inventory), scopedReads, err)
	}
}
