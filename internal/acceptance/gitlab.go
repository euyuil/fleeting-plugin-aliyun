package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type gitlab struct {
	base, token, project string
	http                 *http.Client
	lane                 *jobLaneScope
}

type gitlabPage struct {
	value any
	next  string
}

type gitlabHTTPError struct{ statusCode int }

func (e *gitlabHTTPError) Error() string { return fmt.Sprintf("GitLab API HTTP %d", e.statusCode) }

func (g gitlab) request(ctx context.Context, method, path string, body, out any) error {
	return g.requestAPI(ctx, method, "/projects/"+url.PathEscape(g.project)+path, body, out)
}
func (g gitlab) requestAPI(ctx context.Context, method, path string, body, out any) (resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = &operationError{service: "gitlab", operation: gitlabOperation(path), method: method, err: resultErr}
		}
	}()
	var b []byte
	var err error
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		retry, err := g.requestOnce(ctx, method, path, b, out)
		// A lost write response may already have created a pipeline. Only reads
		// can be repeated, and all attempts share the caller's deadline.
		if err == nil || !retry || method != http.MethodGet || attempt == 2 {
			return err
		}
		if err := aliyun.Wait(ctx, time.Second<<attempt); err != nil {
			return err
		}
	}
}

func (g gitlab) requestOnce(ctx context.Context, method, path string, body []byte, out any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.base, "/")+"/api/v4"+path, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, errors.New("GitLab API transport failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, &gitlabHTTPError{statusCode: resp.StatusCode}
	}
	if out == nil {
		return false, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, errors.New("GitLab API response transport failure")
	}
	if p, ok := out.(*[]byte); ok {
		*p = data
		return false, nil
	}
	if page, ok := out.(*gitlabPage); ok {
		page.next = resp.Header.Get("X-Next-Page")
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return false, errors.New("GitLab inventory must be an explicit array")
		}
		return false, json.Unmarshal(data, page.value)
	}
	return false, json.NewDecoder(bytes.NewReader(data)).Decode(out)
}

type pipeline struct {
	ID          int64
	SHA, Status string
	Ref, Source string
	ProjectID   int64 `json:"project_id"`
	User        struct{ ID int64 }
}
type job struct {
	ID            int64
	Status        string
	FailureReason string `json:"failure_reason"`
	Runner        struct{ ID int64 }
	Commit        struct{ ID string }
	Pipeline      struct{ ID int64 }
	Name, Ref     string
	TagList       []string `json:"tag_list"`
	RunnerManager struct {
		SystemID string `json:"system_id"`
	} `json:"runner_manager"`
}
type Artifact struct {
	Nonce             string `json:"nonce"`
	FixtureSHA        string `json:"fixture_sha"`
	InstanceID        string `json:"instance_id"`
	RunnerID          int64  `json:"runner_id"`
	CheckoutOK        bool   `json:"checkout_ok"`
	CommandOK         bool   `json:"command_ok"`
	Executor          string `json:"executor"`
	ContainerDetected *bool  `json:"container_detected"`
	IsolationOK       bool   `json:"isolation_ok"`
}

func VerifyArtifact(a Artifact, l *Ledger, nonce, sha string, runner int64) error {
	if a.Nonce != nonce || a.FixtureSHA != sha || a.RunnerID != runner || !a.CheckoutOK || !a.CommandOK {
		return errors.New("artifact identity or command mismatch")
	}
	for _, r := range l.Instances {
		if r.InstanceID == a.InstanceID && r.SpotVerified {
			return nil
		}
	}
	return errors.New("artifact worker was not independently verified Spot")
}
