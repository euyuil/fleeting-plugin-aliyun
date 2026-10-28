package acceptance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestCloudSmokeCleanupRequiresOwnedPluginTermination(t *testing.T) {
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
	l := &Ledger{Mode: "cloud-smoke", DriverPID: driver.Process.Pid, PluginDirectory: "plugin"}
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
	binary, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pluginDir, "fleeting-plugin-aliyun")
	if err = os.WriteFile(path, binary, 0700); err != nil {
		t.Fatal(err)
	}
	plugin := exec.Command(path, "60")
	if err = plugin.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = plugin.Wait(); close(done) }()
	t.Cleanup(func() { _ = plugin.Process.Kill(); <-done })
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
	case <-time.After(time.Second):
		t.Fatal("owned orphan plugin is still running")
	}
}
