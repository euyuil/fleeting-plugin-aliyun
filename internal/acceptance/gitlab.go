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
)

type gitlab struct {
	base, token, project string
	http                 *http.Client
}

type gitlabHTTPError struct{ statusCode int }

func (e *gitlabHTTPError) Error() string { return fmt.Sprintf("GitLab API HTTP %d", e.statusCode) }

func (g gitlab) request(ctx context.Context, method, path string, body, out any) error {
	return g.requestAPI(ctx, method, "/projects/"+url.PathEscape(g.project)+path, body, out)
}
func (g gitlab) requestAPI(ctx context.Context, method, path string, body, out any) error {
	var b []byte
	var err error
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.base, "/")+"/api/v4"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return errors.New("GitLab API transport failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &gitlabHTTPError{statusCode: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	if p, ok := out.(*[]byte); ok {
		*p, err = io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(out)
}

type pipeline struct {
	ID          int64
	SHA, Status string
}
type job struct {
	ID            int64
	Status        string
	FailureReason string `json:"failure_reason"`
	Runner        struct{ ID int64 }
	Commit        struct{ ID string }
	Pipeline      struct{ ID int64 }
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
