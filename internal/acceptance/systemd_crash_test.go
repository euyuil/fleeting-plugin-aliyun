package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCrashRequiresOwnedAutomaticRestartAndStableIdentity(t *testing.T) {
	for _, fault := range []string{"", "foreign-owner", "original-pid", "kill-error", "no-restart", "manual-restart", "crash-loop", "same-invocation", "old-process", "identity-change", "config-change"} {
		t.Run(fault, func(t *testing.T) {
			config := []byte("synthetic config")
			hash := sha256.Sum256(config)
			m := &packagedManager{owner: serviceOwner{Run: "10", ConfigSHA256: hex.EncodeToString(hash[:])}, pid: 123, started: true}
			owner, _ := json.Marshal(m.owner)
			killed := false
			m.command = func(_ context.Context, args ...string) ([]byte, error) {
				call := strings.Join(args, " ")
				switch {
				case call == "cat "+serviceOwnerPath:
					if fault == "foreign-owner" {
						return []byte(`{"Run":"11"}`), nil
					}
					return owner, nil
				case call == "cat "+packagedConfig:
					if killed && fault == "config-change" {
						return []byte("changed"), nil
					}
					return config, nil
				case call == "cat /etc/gitlab-runner-aliyun/.runner_system_id":
					if killed && fault == "identity-change" {
						return []byte("s_changed"), nil
					}
					return []byte("s_original"), nil
				case call == "systemctl kill --kill-whom=all --signal=SIGKILL "+packagedService:
					killed = true
					if fault == "kill-error" {
						return nil, errors.New("kill failed")
					}
					return nil, nil
				case call == "test ! -e /proc/123":
					if fault == "old-process" {
						return nil, errors.New("old process exists")
					}
					return nil, nil
				case strings.HasPrefix(call, "systemctl show "+packagedService):
					pid, restarts, invocation := 123, 0, strings.Repeat("a", 32)
					if !killed && fault == "original-pid" {
						pid = 456
					}
					if killed && fault != "no-restart" {
						pid, restarts, invocation = 456, 1, strings.Repeat("b", 32)
						if fault == "manual-restart" {
							restarts = 0
						}
						if fault == "crash-loop" {
							restarts = 2
						}
						if fault == "same-invocation" {
							invocation = strings.Repeat("a", 32)
						}
					}
					return []byte(fmt.Sprintf("ActiveState=active\nMainPID=%d\nNRestarts=%d\nInvocationID=%s\n", pid, restarts, invocation)), nil
				default:
					t.Errorf("unexpected service mutation or query: %s", call)
					return nil, errors.New("unexpected command")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			pid, err := m.crash(ctx)
			if fault == "" {
				if err != nil || pid != 456 || m.pid != 456 || !killed {
					t.Fatal("automatic restart rejected", err)
				}
			} else if err == nil {
				t.Fatal("invalid crash recovery accepted")
			}
			if (fault == "foreign-owner" || fault == "original-pid") && killed {
				t.Fatal("foreign Manager was killed")
			}
		})
	}
}
