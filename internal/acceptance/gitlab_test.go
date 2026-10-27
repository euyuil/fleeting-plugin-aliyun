package acceptance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitLabRunnerTokenAndProjectIsolation(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-token", "unlocked", "shared-project"} {
		t.Run(mode, func(t *testing.T) {
			queriedRunner := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v4/runners/verify":
					var in map[string]string
					if json.NewDecoder(r.Body).Decode(&in) != nil || in["token"] != "synthetic-token" || in["system_id"] != "r_000000000001" {
						t.Error("verification identity missing")
						w.WriteHeader(400)
						return
					}
					id := int64(5)
					if mode == "wrong-token" {
						id = 6
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
				case "/api/v4/projects/9/runners":
					queriedRunner = true
					_, _ = w.Write([]byte(`[{"id":5}]`))
				case "/api/v4/runners/5":
					projects := []map[string]int{{"id": 9}}
					if mode == "shared-project" {
						projects = append(projects, map[string]int{"id": 10})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "runner_type": "project_type", "run_untagged": false, "locked": mode != "unlocked", "tag_list": []string{"fixture"}, "projects": projects})
				case "/api/v4/projects/9/jobs":
					_, _ = w.Write([]byte(`[]`))
				default:
					t.Error("unexpected endpoint", r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			g := gitlab{base: server.URL, token: "synthetic-pat", project: "9", http: server.Client()}
			err := VerifyGitLabFixture(context.Background(), g, 5, "fixture", "synthetic-token", "r_000000000001")
			if (err == nil) != (mode == "valid") {
				t.Fatal("unexpected isolation result", err)
			}
			if mode == "wrong-token" && queriedRunner {
				t.Fatal("wrong token did not stop preflight")
			}
		})
	}
}
