package acceptance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestGitLabReadTransportRetriesAreBounded(t *testing.T) {
	for _, failures := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(failures), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				var previous *http.Request
				g := gitlab{base: "https://gitlab.invalid", token: "synthetic-token", project: "1", http: &http.Client{
					Transport: gateTransport(func(r *http.Request) (*http.Response, error) {
						calls++
						if r == previous || r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects/1/pipelines/2" || r.Header.Get("PRIVATE-TOKEN") != "synthetic-token" {
							t.Fatal("retry changed the request identity or reused a request")
						}
						previous = r
						if calls <= failures {
							return nil, errors.New("synthetic confidential transport detail")
						}
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":2}`))}, nil
					}),
				}}
				start := time.Now()
				var got pipeline
				err := g.request(t.Context(), http.MethodGet, "/pipelines/2", nil, &got)
				delay := time.Second
				if failures >= 2 {
					delay = 3 * time.Second
				}
				if calls != min(failures+1, 3) || time.Since(start) != delay || (err != nil) != (failures == 3) {
					t.Fatalf("unexpected retry result: calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
				}
				if err == nil && got.ID != 2 {
					t.Fatal("successful response was not decoded")
				}
				if err != nil && err.Error() != "GitLab API transport failure" {
					t.Fatal("transport diagnostics were not redacted")
				}
			})
		})
	}
}

type interruptedGitLabBody struct {
	io.Reader
	closed bool
}

func (b *interruptedGitLabBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (b *interruptedGitLabBody) Close() error { b.closed = true; return nil }

func TestGitLabReadRetriesInterruptedBodiesWithoutPartialOutput(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(strconv.FormatBool(raw), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				broken := &interruptedGitLabBody{Reader: strings.NewReader(`{"id":123`)}
				got, data := pipeline{ID: 7}, []byte("original")
				var out any = &got
				if raw {
					out = &data
				}
				g := gitlab{base: "https://gitlab.invalid", http: &http.Client{Transport: gateTransport(func(*http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return &http.Response{StatusCode: 200, Body: broken}, nil
					}
					if !broken.closed || got.ID != 7 || string(data) != "original" {
						t.Fatal("failed response was not closed or partially changed output")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":9}`))}, nil
				})}}
				if err := g.requestAPI(t.Context(), http.MethodGet, "/read", nil, out); err != nil || calls != 2 {
					t.Fatal("interrupted response did not recover", calls, err)
				}
				if (!raw && got.ID != 9) || (raw && string(data) != `{"id":9}`) {
					t.Fatal("recovered output differs")
				}
			})
		})
	}
}

func TestGitLabWritesAndPermanentReadFailuresAreNotRetried(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, brokenBody := range []bool{false, true} {
			calls := 0
			g := gitlab{base: "https://gitlab.invalid", http: &http.Client{Transport: gateTransport(func(*http.Request) (*http.Response, error) {
				calls++
				if brokenBody {
					return &http.Response{StatusCode: 200, Body: &interruptedGitLabBody{Reader: strings.NewReader(`{"id":123`)}}, nil
				}
				return nil, io.ErrUnexpectedEOF
			})}}
			var out pipeline
			if err := g.requestAPI(t.Context(), method, "/pipeline", map[string]string{"ref": "fixture"}, &out); err == nil || calls != 1 {
				t.Fatalf("write retried after an ambiguous response: method=%s calls=%d error=%v", method, calls, err)
			}
		}
	}
	for _, status := range []int{200, 401, 403, 404, 429, 500, 502, 503, 504} {
		calls := 0
		g := gitlab{base: "https://gitlab.invalid", http: &http.Client{Transport: gateTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("not-json"))}, nil
		})}}
		var out pipeline
		if err := g.requestAPI(t.Context(), http.MethodGet, "/read", nil, &out); err == nil || calls != 1 {
			t.Fatalf("HTTP or malformed JSON failure retried: status=%d calls=%d error=%v", status, calls, err)
		}
	}
}

func TestGitLabReadRetryRespectsCancellationAndClientTimeout(t *testing.T) {
	for _, deadline := range []time.Duration{0, 500 * time.Millisecond, 25 * time.Second, 70 * time.Second} {
		t.Run(deadline.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				g := gitlab{base: "https://gitlab.invalid", http: &http.Client{Timeout: 20 * time.Second, Transport: gateTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if deadline < time.Second {
						return nil, io.ErrUnexpectedEOF
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				})}}
				ctx, cancel := context.WithTimeout(t.Context(), deadline)
				defer cancel()
				start := time.Now()
				err := g.requestAPI(ctx, http.MethodGet, "/read", nil, nil)
				wantCalls := map[time.Duration]int{0: 0, 500 * time.Millisecond: 1, 25 * time.Second: 2, 70 * time.Second: 3}[deadline]
				if err == nil || calls != wantCalls || time.Since(start) != min(deadline, 63*time.Second) {
					t.Fatalf("request escaped its time/attempt bound: calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
				}
				if deadline < 63*time.Second && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("caller cancellation was not preserved")
				}
			})
		})
	}
}
