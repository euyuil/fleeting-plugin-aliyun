//go:build live

package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

// These markers live only in the private driver log. The public receipt parser
// independently allowlists every field; API parameters and error text are never
// included here. A missing result remains UNKNOWN after a killed driver.
type acceptanceDiagnostics struct {
	mu      sync.Mutex
	output  io.Writer
	started time.Time
	scope   string
	phase   string
}

type diagnosticFakeAPI struct{ err error }

func (a diagnosticFakeAPI) Call(context.Context, string, string, map[string]string, any) error {
	return a.err
}

func TestAcceptanceDiagnosticsPreserveFailureAndExcludeAPIIdentities(t *testing.T) {
	var output bytes.Buffer
	d := &acceptanceDiagnostics{output: &output, started: time.Now()}
	d.Phase("scenario", "observe_before_ssh")
	want := &aliyun.Error{Code: "Forbidden.RAM", RequestID: "private-request"}
	a := diagnosticAPI{API: diagnosticFakeAPI{err: want}, diagnostics: d}
	if got := a.Call(context.Background(), "ecs", "DescribeInstances", map[string]string{"InstanceId": "private-instance"}, nil); got != want {
		t.Fatalf("diagnostic wrapper changed the API error: %v", got)
	}
	d.Result(false)
	d.Phase("cleanup", "driver_cleanup")
	d.Failure()
	d.Phase("cleanup", "evidence_write")
	d.Result(false)
	if strings.Contains(output.String(), "private") {
		t.Fatal("diagnostic marker leaked API input or request ID")
	}
	var events []map[string]any
	for line := range strings.Lines(output.String()) {
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "FLEETING_DIAGNOSTIC ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 7 || events[1]["code"] != "Forbidden.RAM" || events[2]["scope"] != "scenario" || events[2]["result"] != "FAIL" || events[4]["phase"] != "driver_cleanup" || events[6]["scope"] != "cleanup" || events[6]["result"] != "FAIL" {
		t.Fatalf("lost independent outcome or API context: %v", events)
	}
}

func TestAcceptanceDiagnosticsSuccessfulAPIHasNoErrorMarker(t *testing.T) {
	var output bytes.Buffer
	d := &acceptanceDiagnostics{output: &output, started: time.Now()}
	a := diagnosticAPI{API: diagnosticFakeAPI{}, diagnostics: d}
	if err := a.Call(context.Background(), "ecs", "DescribeInstances", nil, nil); err != nil || output.Len() != 0 {
		t.Fatal("successful API call changed or produced a failure marker")
	}
}

func (d *acceptanceDiagnostics) emit(fields map[string]any) {
	fields["scope"], fields["phase"] = d.scope, d.phase
	fields["elapsed_ms"] = time.Since(d.started).Milliseconds()
	data, _ := json.Marshal(fields)
	_, _ = fmt.Fprintf(d.output, "FLEETING_DIAGNOSTIC %s\n", data)
}

func (d *acceptanceDiagnostics) Phase(scope, phase string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scope, d.phase = scope, phase
	d.emit(map[string]any{"event": "phase"})
}

func (d *acceptanceDiagnostics) Result(passed bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := "FAIL"
	if passed {
		result = "PASS"
	}
	d.emit(map[string]any{"event": "result", "result": result})
}

func (d *acceptanceDiagnostics) Failure() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.emit(map[string]any{"event": "failure"})
}

type diagnosticAPI struct {
	aliyun.API
	diagnostics *acceptanceDiagnostics
}

func (a diagnosticAPI) Call(ctx context.Context, service, action string, query map[string]string, out any) error {
	err := a.API.Call(ctx, service, action, query, out)
	if err != nil {
		code := "UnknownError"
		var apiError *aliyun.Error
		switch {
		case errors.As(err, &apiError):
			code = apiError.Code
		case errors.Is(err, context.DeadlineExceeded):
			code = "DeadlineExceeded"
		case errors.Is(err, context.Canceled):
			code = "Canceled"
		}
		a.diagnostics.mu.Lock()
		a.diagnostics.emit(map[string]any{"event": "api_error", "service": service, "action": action, "code": code})
		a.diagnostics.mu.Unlock()
	}
	return err
}
