package acceptance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitLabLanePaginationRejectsEveryNextPageSignal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
		next    bool
	}{
		{"none", http.Header{}, false},
		{"empty-x", http.Header{"X-Next-Page": {"", ""}}, false},
		{"x-next", http.Header{"X-Next-Page": {"2"}}, true},
		{"repeated-x", http.Header{"X-Next-Page": {"", "2"}}, true},
		{"last-link", http.Header{"Link": {`<https://example.invalid?page=1>; rel="last"`}}, false},
		{"link-only", http.Header{"Link": {`<https://example.invalid?page=2>; rel="next"`}}, true},
		{"unquoted", http.Header{"Link": {`<https://example.invalid?page=2>; rel=next`}}, true},
		{"token-list", http.Header{"Link": {`<https://example.invalid?page=2>; rel="prev next"`}}, true},
		{"repeated-link", http.Header{"Link": {`<https://example.invalid?page=1>; rel="first"`, `<https://example.invalid?page=2>; rel="next"`}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitlabNextPage(tc.headers); got != tc.next {
				t.Fatal("incorrect complete-page verdict")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v4/projects/1/jobs" {
					t.Error("unexpected inventory request")
				}
				for key, values := range tc.headers {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				_, _ = w.Write([]byte("[]"))
			}))
			defer server.Close()
			g := gitlab{base: server.URL, project: "1", token: "synthetic-pat", http: server.Client()}
			var rows []job
			err := g.laneList(context.Background(), "/jobs?per_page=100", &rows)
			if (err != nil) != tc.next {
				t.Fatal("empty partial inventory was admitted or complete inventory rejected", err)
			}
		})
	}
}
