package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

func TestJobFailuresRetainPrimaryCauseThroughForcedStopAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		err        error
	}{
		{"http", "gitlab_http", &operationError{service: "gitlab", operation: "pipeline_jobs", method: "GET", err: &gitlabHTTPError{503}}},
		{"eof", "eof", io.EOF},
		{"partial", "unexpected_eof", io.ErrUnexpectedEOF},
		{"json", "json_decode", &json.SyntaxError{}},
		{"path", "filesystem", &os.PathError{Op: "open", Path: "/synthetic/private-path", Err: os.ErrPermission}},
		{"context", "deadline", context.DeadlineExceeded},
		{"canceled", "canceled", context.Canceled},
		{"cloud", "cloud_api", &aliyun.Error{Code: "Throttling", RequestID: "synthetic-private-request", Retryable: true}},
		{"unknown", "unknown", errors.New("synthetic-private-error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := liveFailure(tc.err)
			r := Result{Result: original, activeJob: 2}
			r.recordFailure("job_sequence", tc.err)
			first := r.Failures[0]
			r.forcedManagerStop()
			if r.Result != original || len(r.Failures) != 2 || r.Failures[0] != first ||
				first.Kind != tc.kind || first.JobIndex != 2 || r.Failures[1].Stage != "manager_stop" || r.Failures[1].JobIndex != 0 {
				t.Fatal("forced stop obscured the primary outcome", r.Failures, r.Result)
			}
			r.Result = "CLEANUP_FAILED"
			r.recordFailure("resource_cleanup", context.DeadlineExceeded)
			if r.Failures[0] != first || len(r.Errors) != 3 || r.Failures[2].Stage != "resource_cleanup" {
				t.Fatal("cleanup replaced or reordered the primary failure")
			}
			b, err := json.Marshal(r.Failures)
			if err != nil || strings.Contains(string(b), "synthetic-private") {
				t.Fatal("failure metadata copied raw error text")
			}
		})
	}
	r := Result{Result: "PASS"}
	r.forcedManagerStop()
	if r.Result != "FAIL_PRODUCT" || len(r.Failures) != 1 {
		t.Fatal("forced stop left an otherwise successful scenario passing")
	}
}

func TestJobFailureLimitPreservesEarliestEvidence(t *testing.T) {
	r := Result{}
	for i := range 11 {
		stage := "job_sequence"
		if i > 0 {
			stage = "manager_stop"
		}
		r.recordFailure(stage, io.EOF)
	}
	if len(r.Failures) != 8 || r.FailuresOmitted != 3 || r.Failures[0].Stage != "job_sequence" || len(r.Errors) != 11 {
		t.Fatal("failure cap lost primary evidence or concealed truncation")
	}
}

func TestFailureAttributionRequiresAnExplicitProductViolation(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		err        error
	}{
		{"observed contract", "FAIL_PRODUCT", productViolation("synthetic contract violation")},
		{"wrapped contract", "FAIL_PRODUCT", fmt.Errorf("observation: %w", productViolation("synthetic contract violation"))},
		{"GitLab HTTP", "FAIL_INFRASTRUCTURE", &operationError{service: "gitlab", err: &gitlabHTTPError{503}}},
		{"GitLab transport", "FAIL_INFRASTRUCTURE", &operationError{service: "gitlab", err: io.ErrUnexpectedEOF}},
		{"GitLab decode", "FAIL_INFRASTRUCTURE", &operationError{service: "gitlab", err: &json.SyntaxError{}}},
		{"local evidence", "FAIL_UNCLASSIFIED", &os.PathError{Op: "open", Path: "/synthetic/evidence", Err: os.ErrPermission}},
		{"harness timing", "FAIL_UNCLASSIFIED", errors.New("insufficient lead time to establish the fixed worker automatic release deadline")},
		{"unknown operation", "FAIL_UNCLASSIFIED", &operationError{service: "synthetic", err: io.EOF}},
		{"untyped assertion", "FAIL_UNCLASSIFIED", errors.New("actual worker is not Spot")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveFailure(tc.err); got != tc.want || got == "PASS" {
				t.Fatalf("unexpected attribution: got %s, want %s", got, tc.want)
			}
		})
	}
}

type failingJobAPI struct{ calls int }

func (a *failingJobAPI) Call(_ context.Context, service, action string, query map[string]string, _ any) error {
	a.calls++
	if service != "ecs" || action != "DescribeInstances" || query["InstanceIds"] != "synthetic-private-id" {
		return errors.New("wrapper changed API input")
	}
	return &aliyun.Error{Code: "TransportError", Retryable: true}
}

func TestJobAPIMetadataPreservesTypedCausesAndDoesNotRetry(t *testing.T) {
	api := &failingJobAPI{}
	err := (jobDiagnosticAPI{api}).Call(t.Context(), "ecs", "DescribeInstances", map[string]string{"InstanceIds": "synthetic-private-id"}, nil)
	var cloud *aliyun.Error
	if api.calls != 1 || !errors.As(err, &cloud) || !aliyun.IsRetryable(err) || liveFailure(err) != "FAIL_INFRASTRUCTURE" {
		t.Fatal("diagnostics changed cloud failure semantics", err)
	}
	r := Result{}
	r.recordFailure("job_sequence", err)
	f := r.Failures[0]
	if f.Service != "ecs" || f.Operation != "DescribeInstances" || f.Kind != "cloud_api" || f.CloudCode != "TransportError" {
		t.Fatal("failed cloud operation was lost", f)
	}
	for _, raw := range []string{"", "{", "[]"} {
		calls := 0
		g := gitlab{base: "https://gitlab.invalid", project: "9", http: &http.Client{Transport: gateTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}, nil
		})}}
		var out pipeline
		err := g.request(t.Context(), "GET", "/pipelines/7", nil, &out)
		r := Result{}
		r.recordFailure("job_sequence", err)
		f := r.Failures[0]
		if calls != 1 || f.Operation != "pipeline_status" || (f.Kind != "eof" && f.Kind != "unexpected_eof" && f.Kind != "json_decode") {
			t.Fatal("GitLab response error lost its operation or type", f)
		}
	}
	calls := 0
	g := gitlab{base: "https://gitlab.invalid", project: "9", http: &http.Client{Transport: gateTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("synthetic-private-body"))}, nil
	})}}
	err = g.request(t.Context(), "GET", "/pipelines/7/jobs?per_page=100", nil, nil)
	var status *gitlabHTTPError
	if calls != 1 || !errors.As(err, &status) || status.statusCode != 503 {
		t.Fatal("diagnostics changed HTTP failure semantics", err)
	}
	r = Result{}
	r.recordFailure("job_sequence", err)
	if r.Failures[0].HTTPStatus != 503 || r.Failures[0].Operation != "pipeline_jobs" || r.Failures[0].Method != "GET" {
		t.Fatal("HTTP operation or status was lost")
	}
}

func TestJobFailureIndexFollowsSelectedCapacityJob(t *testing.T) {
	r := Result{Jobs: []JobEvidence{{}, {}, {}}}
	s := jobSequence{result: &r}
	s.selectJob(&r.Jobs[1])
	r.recordFailure("job_sequence", io.EOF)
	s.selectJob(&JobEvidence{})
	r.recordFailure("job_sequence", io.EOF)
	if r.Failures[0].JobIndex != 2 || r.Failures[1].JobIndex != 0 {
		t.Fatal("failure index incorrectly identifies the last created job")
	}
}

func TestJobFailureBetweenJobsAndDuringWholeSequenceValidationHasNoJobIndex(t *testing.T) {
	stop := errors.New("synthetic Manager check failure")
	s, events := fakeJobSequence(t, "failure", "")
	check := s.checkManager
	s.checkManager = func(ctx context.Context) error {
		if len(s.result.Jobs) == 1 && s.result.Jobs[0].NormalRelease {
			return stop
		}
		return check(ctx)
	}
	err := s.run(t.Context())
	if !errors.Is(err, stop) || events() != "create,release" {
		t.Fatal("expected a Manager failure between the released first job and second creation", err, events())
	}
	s.result.recordFailure("job_sequence", err)
	if s.result.Failures[0].JobIndex != 0 {
		t.Fatal("between-job Manager failure was attributed to the completed job")
	}
	c, _ := fakeCapacity(t, "instance", "")
	check = c.checkManager
	c.checkManager = func(ctx context.Context) error {
		if len(c.result.Jobs) == 1 {
			return stop
		}
		return check(ctx)
	}
	err = c.run(t.Context())
	if !errors.Is(err, stop) || len(c.result.Jobs) != 1 {
		t.Fatal("expected a Manager failure before the second capacity job")
	}
	c.result.recordFailure("job_sequence", err)
	if c.result.Failures[0].JobIndex != 0 {
		t.Fatal("between-creation Manager failure inherited the previous job index")
	}
	r := Result{JobScenario: "capacity", activeJob: 3}
	err = verifyJobSequence(&r)
	if err == nil {
		t.Fatal("incomplete sequence passed")
	}
	r.recordFailure("job_sequence", err)
	if r.Failures[0].JobIndex != 0 {
		t.Fatal("whole-sequence validation was attributed to one job")
	}
}
