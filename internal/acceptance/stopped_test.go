package acceptance

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
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
