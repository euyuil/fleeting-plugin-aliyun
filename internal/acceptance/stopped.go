package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var githubProject = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ConfirmStoppedRun checks the original run, using the independent janitor identity.
func ConfirmStoppedRun(ctx context.Context, l *Ledger, token string) error {
	if !githubProject.MatchString(l.Project) {
		return errors.New("invalid owning GitHub repository")
	}
	id, e := strconv.ParseInt(l.RunID, 10, 64)
	if e != nil || id <= 0 {
		return errors.New("invalid owning run ID")
	}
	attempt, e := strconv.Atoi(l.Attempt)
	if e != nil || attempt <= 0 {
		return errors.New("invalid owning run attempt")
	}
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d/attempts/%d", l.Project, id, attempt)
	req, e := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if e != nil {
		return e
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("cannot verify GitHub run status")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub run status HTTP %d", resp.StatusCode)
	}
	var run struct {
		ID         int64
		RunAttempt int `json:"run_attempt"`
		Status     string
		Repository struct {
			FullName string `json:"full_name"`
		}
	}
	if json.NewDecoder(resp.Body).Decode(&run) != nil || run.ID != id || run.RunAttempt != attempt || run.Repository.FullName != l.Project || run.Status != "completed" {
		return errors.New("owning GitHub run is not confirmed completed")
	}
	return nil
}

// StopLocalManager only signals a PID whose /proc command line names this run's
// private config. Arbitrary PIDs from an artifact are never trusted.
func StopLocalManager(ctx context.Context, l *Ledger, outputDir string) error {
	if _, err := os.Stat("/proc/self"); err != nil {
		return errors.New("local process termination requires Linux procfs; use janitor after run completion")
	}
	if l.ManagerPID <= 0 {
		if (l.Mode == "scale" || l.Mode == "interruption" || l.Mode == "suspend") && l.DriverPID > 0 {
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", l.DriverPID)); errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return errors.New("extended driver termination is not confirmed")
		}
		return errors.New("local Manager identity missing; use janitor after run completion")
	}
	if !strings.HasPrefix(l.ManagerDirectory, "manager-") || filepath.Base(l.ManagerDirectory) != l.ManagerDirectory {
		return errors.New("invalid Manager directory")
	}
	root, e := filepath.Abs(outputDir)
	if e != nil {
		return e
	}
	config := filepath.Join(root, l.ManagerDirectory, "config.toml")
	proc := fmt.Sprintf("/proc/%d/cmdline", l.ManagerPID)
	matches := func() (bool, error) {
		b, e := os.ReadFile(proc)
		if errors.Is(e, os.ErrNotExist) {
			return false, nil
		}
		if e != nil {
			return false, errors.New("cannot prove local Manager identity on this platform")
		}
		args := bytes.Split(b, []byte{0})
		for i, a := range args {
			if string(a) == "--config" && i+1 < len(args) && string(args[i+1]) == config {
				return true, nil
			}
		}
		return false, errors.New("PID does not match this run's Manager")
	}
	alive, e := matches()
	if e != nil {
		return e
	}
	if !alive {
		return nil
	}
	process, e := os.FindProcess(l.ManagerPID)
	if e != nil {
		return e
	}
	_ = process.Signal(syscall.SIGQUIT)
	deadline := time.Now().Add(20 * time.Second)
	killed := false
	for {
		alive, e = matches()
		if e != nil {
			return e
		}
		if !alive {
			return nil
		}
		if time.Now().After(deadline) {
			if killed {
				return errors.New("Manager termination is not confirmed")
			}
			if e = process.Kill(); e != nil {
				return e
			}
			killed = true
			deadline = time.Now().Add(5 * time.Second)
		}
		if e = waitLocal(ctx); e != nil {
			return e
		}
	}
}
func waitLocal(ctx context.Context) error {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
