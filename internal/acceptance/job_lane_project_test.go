package acceptance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestLaneProjectPipelinesRequireCompleteOrderedHistory(t *testing.T) {
	for _, fault := range []string{"", "link-only", "second-page-null", "duplicate", "unordered", "missing-id", "empty-next", "second-page-429", "page-bound"} {
		t.Run(fault, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				q := r.URL.Query()
				if r.Method != "GET" || r.URL.Path != "/api/v4/projects/9/pipelines" || q.Get("source") != "parent_pipeline" ||
					q.Get("order_by") != "id" || q.Get("sort") != "asc" || q.Get("per_page") != "100" || q.Has("status") || q.Has("ref") || q.Has("sha") {
					t.Error("project history was filtered or not read in fixed ascending pages")
				}
				page, err := strconv.Atoi(q.Get("page"))
				if err != nil || page < 1 {
					t.Error("invalid page")
				}
				if fault == "second-page-429" && page == 2 {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				var rows any = []pipeline{{ID: int64(page), Status: "success"}}
				if fault == "page-bound" || page == 1 {
					if fault == "link-only" {
						w.Header().Set("Link", "</never-follow-this>; rel=next")
					} else {
						w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
					}
				}
				if fault == "second-page-null" && page == 2 {
					rows = nil
				}
				if fault == "duplicate" && page == 2 {
					rows = []pipeline{{ID: 1}}
				}
				if fault == "unordered" {
					rows = []pipeline{{ID: 4}, {ID: 3}}
				}
				if fault == "missing-id" {
					rows = []pipeline{{Status: "running"}}
				}
				if fault == "empty-next" {
					rows = []pipeline{}
				}
				_ = json.NewEncoder(w).Encode(rows)
			}))
			defer server.Close()
			g := gitlab{base: server.URL, token: "synthetic-existing-pat", project: "9", http: server.Client()}
			rows, err := g.laneProjectPipelines(context.Background(), "parent_pipeline")
			if fault == "" || fault == "link-only" {
				if err != nil || len(rows) != 2 || rows[0].ID != 1 || rows[1].ID != 2 || requests.Load() != 2 {
					t.Fatal("complete project history was not collected", rows, err, requests.Load())
				}
			} else if err == nil || rows != nil {
				t.Fatal("partial or invalid project history was accepted", rows, err)
			}
			if fault == "second-page-429" && requests.Load() != 2 {
				t.Fatal("HTTP429 was retried")
			}
			if fault == "page-bound" && requests.Load() != maxLaneProjectPipelinePages {
				t.Fatal("project pagination escaped its bound")
			}
		})
	}
}

func TestLaneProjectHistoryCannotHideForeignActivity(t *testing.T) {
	for _, source := range []string{"", "parent_pipeline"} {
		for _, status := range append(append([]string{}, laneActiveStates...), "unrecognized") {
			for _, laterPage := range []bool{false, true} {
				t.Run(source+"/"+status+"/later="+strconv.FormatBool(laterPage), func(t *testing.T) {
					c := testLaneContext()
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						q := r.URL.Query()
						out := []pipeline{}
						if r.URL.Path == "/api/v4/projects/9/pipelines" && !q.Has("ref") && q.Get("source") == source {
							out = []pipeline{{ID: 900, Status: status, Ref: "foreign-ref", Source: source}}
							if laterPage && q.Get("page") == "1" {
								out = []pipeline{{ID: 800, Status: "success", Ref: "old-terminal-ref", Source: source}}
								w.Header().Set("Link", "</not-followed>; rel=next")
							}
						}
						_ = json.NewEncoder(w).Encode(out)
					}))
					defer server.Close()
					g := gitlab{base: server.URL, token: "synthetic-existing-pat", project: "9", http: server.Client(), lane: &jobLaneScope{context: c, executor: "instance"}}
					if err := g.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{}); err == nil {
						t.Fatal("foreign activity without a materialized job escaped complete project history")
					}
				})
			}
		}
	}
}

func TestLaneProjectTerminalHistoryAndSinglePageRequestCost(t *testing.T) {
	for _, currentChild := range []bool{false, true} {
		t.Run(strconv.FormatBool(currentChild), func(t *testing.T) {
			c := testLaneContext()
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				q := r.URL.Query()
				if r.Method != "GET" {
					t.Error("occupancy check performed a write")
				}
				if q.Has("status") {
					t.Error("project inventory repeated a per-status query")
				}
				if r.URL.Path == "/api/v4/projects/9/jobs" {
					if !q.Has("ref") && !reflect.DeepEqual(q["scope[]"], laneActiveStates) {
						t.Error("project jobs omitted an active state")
					}
					_ = json.NewEncoder(w).Encode([]job{})
					return
				}
				out := []pipeline{}
				if !q.Has("ref") {
					ref := "old-terminal-ref"
					if currentChild && q.Get("source") == "parent_pipeline" {
						ref = c.FixtureRef
					}
					out = []pipeline{{ID: 800, Status: "success", Ref: ref}}
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			g := gitlab{base: server.URL, token: "synthetic-existing-pat", project: "9", http: server.Client(), lane: &jobLaneScope{context: c, executor: "instance"}}
			err := g.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{})
			if currentChild {
				if err == nil {
					t.Fatal("a current-ref child appeared after the first scoped check")
				}
			} else if err != nil || requests.Load() != 6 {
				t.Fatal("terminal history blocked occupancy or single-page cost exceeded six reads", err, requests.Load())
			}
		})
	}
}

func TestLaneProjectPaginationHonorsCallerDeadline(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode([]pipeline{})
	}))
	defer server.Close()
	g := gitlab{base: server.URL, token: "synthetic-existing-pat", project: "9", http: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rows, err := g.laneProjectPipelines(ctx, ""); err == nil || rows != nil || requests.Load() != 0 {
		t.Fatal("pagination read beyond the caller deadline", rows, err, requests.Load())
	}
}
