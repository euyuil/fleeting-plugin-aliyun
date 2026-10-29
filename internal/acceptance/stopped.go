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
	if l.ManagerMode == "systemd" {
		return stopPackagedManager(ctx, l)
	}
	if _, err := os.Stat("/proc/self"); err != nil {
		return errors.New("local process termination requires Linux procfs; use janitor after run completion")
	}
	if l.ManagerPID <= 0 {
		if l.Mode == "cloud-smoke" || l.Mode == "cloud-scale" || l.Mode == "cloud-restart" || l.Mode == "cloud-create-restart" || l.Mode == "cloud-remove-restart" || l.Mode == "cloud-increase-response-loss" {
			return stopCloudPlugin(ctx, l, outputDir)
		}
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

// stopCloudPlugin requires a dead driver, then signals only a process executing
// the binary inside this run's private plugin directory. Missing proof fails closed.
func stopCloudPlugin(ctx context.Context, l *Ledger, outputDir string) error {
	if l.DriverPID <= 0 {
		return errors.New("cloud-smoke driver identity is missing")
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", l.DriverPID)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("cloud-smoke driver termination is not confirmed")
	}
	if l.PluginDirectory != "plugin" {
		return errors.New("cloud-smoke plugin directory is missing")
	}
	dir, err := filepath.Abs(filepath.Join(outputDir, l.PluginDirectory))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		return errors.New("cloud-smoke plugin PID proof is missing")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return errors.New("cloud-smoke plugin PID is invalid")
	}
	proc := fmt.Sprintf("/proc/%d/cmdline", pid)
	for attempt := 0; ; attempt++ {
		args, err := os.ReadFile(proc)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("cloud-smoke plugin identity cannot be read")
		}
		if len(args) == 0 {
			state, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if errors.Is(e, os.ErrNotExist) || (e == nil && bytes.Contains(state, []byte(") Z "))) {
				return nil
			}
		}
		if string(bytes.Split(args, []byte{0})[0]) != filepath.Join(dir, "fleeting-plugin-aliyun") {
			return errors.New("PID is not this run's plugin; refusing to signal it")
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		if attempt == 0 {
			if err = process.Signal(syscall.SIGTERM); err != nil {
				return err
			}
		} else if attempt == 20 {
			if err = process.Kill(); err != nil {
				return err
			}
		} else if attempt > 40 {
			return errors.New("cloud-smoke plugin termination is not confirmed")
		}
		if err = waitLocal(ctx); err != nil {
			return err
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

// WaitForProcessExit only probes the given PID. Permission errors do not prove
// exit, and invalid PIDs must not address a process group.
func WaitForProcessExit(ctx context.Context, pid int) error {
	if pid <= 0 {
		return errors.New("invalid plugin PID")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	for {
		err = process.Signal(syscall.Signal(0))
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("plugin exit cannot be proven: %w", err)
		}
		if err = waitLocal(ctx); err != nil {
			return err
		}
	}
}
