package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

const maxJobFailures = 8

// These records accompany the private errors in occurrence order. The public
// receipt separately allowlists every field; neither projection copies an error.
type jobFailure struct {
	Stage      string `json:"stage"`
	JobIndex   int    `json:"job_index"`
	Kind       string `json:"kind"`
	Service    string `json:"service,omitempty"`
	Operation  string `json:"operation,omitempty"`
	Method     string `json:"method,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	CloudCode  string `json:"cloud_code,omitempty"`
}

type operationError struct {
	service, operation, method string
	err                        error
}

func (e *operationError) Error() string { return e.err.Error() }
func (e *operationError) Unwrap() error { return e.err }

// Only the acceptance client is wrapped. Retry and mutation semantics remain
// inside the existing API implementation; successful retries emit no failure.
type jobDiagnosticAPI struct{ aliyun.API }

func (a jobDiagnosticAPI) Call(ctx context.Context, service, action string, query map[string]string, out any) error {
	if err := a.API.Call(ctx, service, action, query, out); err != nil {
		return &operationError{service: service, operation: action, err: err}
	}
	return nil
}

var gitlabOperations = []struct {
	pattern *regexp.Regexp
	name    string
}{
	{regexp.MustCompile(`^/runners/verify$`), "runner_verify"},
	{regexp.MustCompile(`^/runners/[0-9]+$`), "runner_details"},
	{regexp.MustCompile(`^/projects/[^/]+/runners$`), "project_runners"},
	{regexp.MustCompile(`^/projects/[^/]+/jobs(?:\?.*)?$`), "project_jobs"},
	{regexp.MustCompile(`^/projects/[^/]+/repository/commits/[^/]+$`), "fixture_commit"},
	{regexp.MustCompile(`^/projects/[^/]+/pipeline$`), "pipeline_create"},
	{regexp.MustCompile(`^/projects/[^/]+/pipelines/[0-9]+$`), "pipeline_status"},
	{regexp.MustCompile(`^/projects/[^/]+/pipelines/[0-9]+/jobs(?:\?.*)?$`), "pipeline_jobs"},
	{regexp.MustCompile(`^/projects/[^/]+/pipelines/[0-9]+/cancel$`), "pipeline_cancel"},
	{regexp.MustCompile(`^/projects/[^/]+/jobs/[0-9]+/trace$`), "job_trace"},
	{regexp.MustCompile(`^/projects/[^/]+/jobs/[0-9]+/artifacts/result\.json$`), "job_artifact"},
}

func gitlabOperation(path string) string {
	for _, operation := range gitlabOperations {
		if operation.pattern.MatchString(path) {
			return operation.name
		}
	}
	return "unknown"
}

func (r *Result) recordFailure(stage string, err error) {
	r.Errors = append(r.Errors, err.Error())
	if len(r.Failures) >= maxJobFailures {
		r.FailuresOmitted++
		return
	}
	f := jobFailure{Stage: stage, Kind: "unknown"}
	if stage == "job_sequence" {
		f.JobIndex = r.activeJob
	}
	var operation *operationError
	if errors.As(err, &operation) {
		f.Service, f.Operation, f.Method = operation.service, operation.operation, operation.method
	}
	var cloud *aliyun.Error
	var httpError *gitlabHTTPError
	var syntax *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	var path *os.PathError
	switch {
	case errors.Is(err, context.Canceled):
		f.Kind = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		f.Kind = "deadline"
	case errors.As(err, &cloud):
		f.Kind, f.CloudCode = "cloud_api", cloud.Code
	case errors.As(err, &httpError):
		f.Kind, f.HTTPStatus = "gitlab_http", httpError.statusCode
	case errors.Is(err, io.ErrUnexpectedEOF):
		f.Kind = "unexpected_eof"
	case errors.Is(err, io.EOF):
		f.Kind = "eof"
	case errors.As(err, &syntax), errors.As(err, &typeError):
		f.Kind = "json_decode"
	case errors.As(err, &path):
		f.Kind = "filesystem"
	}
	r.Failures = append(r.Failures, f)
}

func (r *Result) forcedManagerStop() {
	if r.Result == "PASS" {
		r.Result = "FAIL_PRODUCT"
	}
	r.recordFailure("manager_stop", errors.New("packaged Manager did not complete a healthy graceful shutdown"))
}
