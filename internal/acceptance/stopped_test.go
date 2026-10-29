package acceptance

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestExtendedCleanupRequiresTerminatedDriver(t *testing.T) {
	l := &Ledger{Mode: "scale", DriverPID: os.Getpid()}
	if err := StopLocalManager(context.Background(), l, t.TempDir()); err == nil {
		t.Fatal("live driver accepted as stopped")
	}
	if runtime.GOOS != "linux" {
		return
	}
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	l.DriverPID = cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := StopLocalManager(context.Background(), l, t.TempDir()); err != nil {
		t.Fatal("terminated driver rejected", err)
	}
	l.Mode = "unknown"
	if err := StopLocalManager(context.Background(), l, t.TempDir()); err == nil {
		t.Fatal("unknown driver mode trusted")
	}
}

func TestPluginCloudCleanupRequiresOwnedPluginTermination(t *testing.T) {
	if os.Getenv("FLEETING_TEST_OWNED_PLUGIN") == "1" {
		if _, err := os.Stdout.WriteString("ready\n"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	for _, mode := range []string{"cloud-smoke", "cloud-scale", "cloud-restart", "cloud-create-restart", "cloud-remove-restart", "cloud-increase-response-loss"} {
		t.Run(mode, func(t *testing.T) { checkOwnedPluginTermination(t, mode) })
	}
}

func TestProcessExitProofRequiresADeadPositivePID(t *testing.T) {
	for _, pid := range []int{-1, 0} {
		if err := WaitForProcessExit(context.Background(), pid); err == nil {
			t.Fatal("invalid PID was accepted")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := WaitForProcessExit(ctx, os.Getpid()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("running process was accepted as exited", err)
	}
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := WaitForProcessExit(ctx, child.Process.Pid); err != nil {
		t.Fatal("reaped process was not confirmed stopped", err)
	}
}

func checkOwnedPluginTermination(t *testing.T, mode string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("independent termination proof requires Linux procfs")
	}
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "plugin")
	if err := os.Mkdir(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	driver := exec.Command("sh", "-c", "exit 0")
	if err := driver.Run(); err != nil {
		t.Fatal(err)
	}
	l := &Ledger{Mode: mode, DriverPID: driver.Process.Pid, PluginDirectory: "plugin"}
	if err := StopLocalManager(context.Background(), l, dir); err == nil {
		t.Fatal("missing plugin PID proof accepted")
	}
	pidFile := filepath.Join(pluginDir, "pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := StopLocalManager(context.Background(), l, dir); err == nil {
		t.Fatal("unrelated process accepted as the owned plugin")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pluginDir, "fleeting-plugin-aliyun")
	// System utilities may dispatch on argv[0], so use our own helper process.
	if err = os.Symlink(executable, path); err != nil {
		t.Fatal(err)
	}
	ready, announce, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	defer announce.Close()
	plugin := exec.Command(path, "-test.run=^TestPluginCloudCleanupRequiresOwnedPluginTermination$")
	plugin.Env = append(os.Environ(), "FLEETING_TEST_OWNED_PLUGIN=1")
	plugin.Stdout = announce
	plugin.Stderr = os.Stderr
	if err = plugin.Start(); err != nil {
		t.Fatal(err)
	}
	_ = announce.Close()
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = plugin.Wait(); close(done) }()
	t.Cleanup(func() { _ = plugin.Process.Kill(); <-done })
	if err = ready.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var message [6]byte
	if _, err = io.ReadFull(ready, message[:]); err != nil || string(message[:]) != "ready\n" {
		t.Fatalf("plugin helper did not become ready: %q, %v", message, err)
	}
	if err = os.WriteFile(pidFile, []byte(strconv.Itoa(plugin.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = StopLocalManager(ctx, l, dir); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		var exit *exec.ExitError
		if !errors.As(exitErr, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
			t.Fatalf("owned plugin was not terminated by SIGTERM: %v", exitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("owned orphan plugin is still running")
	}
}
