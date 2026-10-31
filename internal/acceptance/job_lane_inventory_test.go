package acceptance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestLaneInventoryAuthenticatesPeerAndRejectsReplay(t *testing.T) {
	for _, fault := range []string{"", "empty", "pending", "canceled-unassigned", "manager-not-visible", "extra-runner", "duplicate-runner", "extra-tag", "shared-project", "paged",
		"replay-after-terminal", "retried-job", "wrong-manager", "wrong-tag", "wrong-source", "wrong-sha", "wrong-creator", "extra-variable",
		"foreign-pipeline-no-jobs", "foreign-job", "own-lane-active", "unmaterialized", "null-list", "unknown-state",
		"child-on-run-ref", "foreign-child-no-jobs", "paged-child"} {
		t.Run(fault, func(t *testing.T) {
			c := testLaneContext()
			executor := "docker-autoscaler"
			if fault == "own-lane-active" {
				executor = "instance"
			}
			owner, err := c.ownership(executor, "manager-crash", "manager-crash", 1,
				strings.Repeat("e", 48), "r_000000000001", strings.Repeat("f", 48))
			if err != nil {
				t.Fatal(err)
			}
			variables, _ := signedLaneVariables(owner, "synthetic-existing-pat")
			vars := make([]laneVariable, 0, len(variables))
			for _, v := range variables {
				vars = append(vars, laneVariable{Key: v["key"], Value: v["value"], VariableType: "env_var"})
			}
			if fault == "extra-variable" {
				vars = append(vars, laneVariable{Key: "GIT_STRATEGY", Value: "none", VariableType: "env_var"})
			}
			pipe := pipeline{ID: 101, SHA: c.FixtureSHA, Ref: c.FixtureRef, Source: "api", Status: "running", ProjectID: 9}
			pipe.User.ID = 7
			if fault == "wrong-source" {
				pipe.Source = "web"
			}
			if fault == "wrong-sha" {
				pipe.SHA = strings.Repeat("0", 40)
			}
			if fault == "wrong-creator" {
				pipe.User.ID = 8
			}
			if fault == "unknown-state" {
				pipe.Status = "unrecognized"
			}
			j := job{ID: 1001, Status: pipe.Status, Name: "spot-manager-crash", Ref: c.FixtureRef, TagList: []string{owner["runner_tag"]}}
			j.Pipeline.ID, j.Commit.ID = pipe.ID, c.FixtureSHA
			j.Runner.ID, _ = strconv.ParseInt(owner["runner_id"], 10, 64)
			j.RunnerManager.SystemID = owner["system_id"]
			if fault == "pending" {
				pipe.Status, j.Status, j.Runner.ID, j.RunnerManager.SystemID = "pending", "pending", 0, ""
			}
			if fault == "canceled-unassigned" {
				pipe.Status, j.Status, j.Runner.ID, j.RunnerManager.SystemID = "canceled", "canceled", 0, ""
			}
			if fault == "manager-not-visible" {
				j.RunnerManager.SystemID = ""
			}
			if fault == "wrong-manager" {
				j.RunnerManager.SystemID = "r_000000000002"
			}
			if fault == "wrong-tag" {
				j.TagList = []string{"other-tag"}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var out any
				switch r.URL.Path {
				case "/api/v4/runners/verify":
					var in map[string]string
					_ = json.NewDecoder(r.Body).Decode(&in)
					if in["token"] != "synthetic-runner-token" || in["system_id"] != "r_000000000009" {
						t.Error("selected Runner verification used a peer token or Manager")
					}
					out = map[string]int{"id": 5}
				case "/api/v4/projects/9/runners":
					out = []map[string]int{{"id": 5}, {"id": 6}}
					if fault == "extra-runner" {
						out = []map[string]int{{"id": 5}, {"id": 6}, {"id": 7}}
					}
					if fault == "duplicate-runner" {
						out = []map[string]int{{"id": 5}, {"id": 5}}
					}
					if fault == "paged" {
						w.Header().Set("X-Next-Page", "2")
					}
					if fault == "null-list" {
						out = nil
					}
				case "/api/v4/runners/5", "/api/v4/runners/6":
					id := strings.TrimPrefix(r.URL.Path, "/api/v4/runners/")
					lane := c.Runners["instance"]
					if id == "6" {
						lane = c.Runners["docker-autoscaler"]
					}
					tags := []string{lane.Tag}
					projects := []map[string]int{{"id": 9}}
					if fault == "extra-tag" {
						tags = append(tags, "additional-tag")
					}
					if fault == "shared-project" {
						projects = append(projects, map[string]int{"id": 10})
					}
					value, _ := strconv.Atoi(id)
					out = map[string]any{"id": value, "runner_type": "project_type", "locked": true, "paused": false, "run_untagged": false,
						"tag_list": tags, "projects": projects}
				case "/api/v4/projects/9/pipelines":
					if r.URL.Query().Get("source") == "parent_pipeline" {
						out = []pipeline{}
						if fault == "child-on-run-ref" && r.URL.Query().Get("ref") != "" ||
							fault == "foreign-child-no-jobs" && r.URL.Query().Get("status") == "preparing" {
							out = []pipeline{{ID: 900, Ref: "foreign-ref", Status: "preparing", Source: "parent_pipeline"}}
						}
						if fault == "paged-child" {
							w.Header().Set("X-Next-Page", "2")
						}
						break
					}
					if ref := r.URL.Query().Get("ref"); ref != "" {
						if ref != c.FixtureRef || r.URL.Query().Has("sha") || r.URL.Query().Has("source") {
							t.Error("ownership inventory filtered away foreign rows")
						}
						out = []pipeline{pipe}
						if fault == "empty" {
							out = []pipeline{}
						}
						if fault == "replay-after-terminal" {
							old := pipe
							old.ID, old.Status = 102, "success"
							out = []pipeline{old, pipe}
						}
					} else {
						out = []pipeline{}
						if r.URL.Query().Get("status") == pipe.Status && fault != "empty" {
							out = []pipeline{pipe}
						}
						if fault == "foreign-pipeline-no-jobs" && r.URL.Query().Get("status") == "created" {
							out = []pipeline{{ID: 900, Ref: "foreign-ref", Status: "created"}}
						}
					}
				case "/api/v4/projects/9/pipelines/101", "/api/v4/projects/9/pipelines/102":
					out = pipe
					if strings.HasSuffix(r.URL.Path, "102") {
						old := pipe
						old.ID, old.Status = 102, "success"
						out = old
					}
				case "/api/v4/projects/9/pipelines/101/variables", "/api/v4/projects/9/pipelines/102/variables":
					out = vars
				case "/api/v4/projects/9/pipelines/101/jobs", "/api/v4/projects/9/pipelines/102/jobs":
					if r.URL.Query().Get("include_retried") != "true" {
						t.Error("pipeline job read hid retried identities")
					}
					out = []job{j}
					if strings.Contains(r.URL.Path, "/102/") {
						old := j
						old.ID, old.Pipeline.ID, old.Status = 1002, 102, "success"
						out = []job{old}
					}
					if fault == "retried-job" {
						out = []job{j, j}
					}
					if fault == "unmaterialized" {
						out = []job{}
					}
				case "/api/v4/projects/9/jobs":
					for _, state := range laneActiveStates {
						if !strings.Contains(r.URL.RawQuery, state) {
							t.Error("project occupancy read omitted a nonterminal state")
						}
					}
					out = []job{j}
					if fault == "empty" || terminalJob(j.Status) {
						out = []job{}
					}
					if fault == "foreign-job" {
						out = []job{{ID: 901, Status: "pending"}}
					}
				default:
					t.Error("unexpected API path", r.URL.Path)
					w.WriteHeader(400)
					return
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			g := gitlab{base: server.URL, token: "synthetic-existing-pat", project: "9", http: server.Client(), lane: &jobLaneScope{context: c, executor: "instance"}}
			var got error
			if fault == "unmaterialized" || fault == "manager-not-visible" {
				got = g.inspectLaneInventory(context.Background(), nil, map[int64]lanePipeline{})
			} else {
				got = VerifyGitLabFixture(context.Background(), g, 5, "instance-lane", "synthetic-runner-token", "r_000000000009")
			}
			if (got == nil) != (fault == "" || fault == "empty" || fault == "pending" || fault == "canceled-unassigned") {
				t.Fatal("unexpected two-lane isolation verdict", got)
			}
		})
	}
}
