package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPackagedAcceptanceUsesShippedExamples(t *testing.T) {
	for _, executor := range []string{"instance", "docker-autoscaler"} {
		input, err := os.ReadFile(filepath.Join("../../examples", executor, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		o := smokeOptions{executor: executor, image: "docker.io/library/example@sha256:" + strings.Repeat("a", 64)}
		config, err := packagedRunnerConfig(input, "https://gitlab.example.test", "synthetic-token", "test-region", "test-group", "root", o)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if _, err = toml.Decode(string(config), &doc); err != nil {
			t.Fatal(err)
		}
		pool := doc["runners"].([]map[string]any)[0]
		autoscaler := pool["autoscaler"].(map[string]any)
		if pool["token"] != "synthetic-token" || pool["shell"] != "sh" || autoscaler["delete_instances_on_shutdown"] != true || !strings.Contains(autoscaler["instance_ready_command"].(string), "/var/lib/fleeting-worker/ready") {
			t.Fatal("package behavior was not preserved")
		}
		if _, err := packagedRunnerConfig([]byte(strings.Replace(string(input), "max_instances = 1", "max_instances = 2", 1)), "", "", "", "", "", o); err == nil {
			t.Fatal("unbounded package example accepted")
		}
	}
}

func TestServiceCleanupRequiresExactOwnerAndNoUnitProcesses(t *testing.T) {
	for _, scenario := range []string{"graceful", "forced", "different-owner", "changed-config", "remaining-process", "failed-kill", "mask-failure"} {
		t.Run(scenario, func(t *testing.T) {
			config := []byte("private configuration")
			hash := sha256.Sum256(config)
			owner := serviceOwner{Project: "example/project", Run: "10", Attempt: "1", Group: "test-group", ConfigSHA256: hex.EncodeToString(hash[:]), DriverPID: 123}
			m := &packagedManager{owner: owner}
			calls := []string{}
			stopCalls := 0
			m.command = func(_ context.Context, args ...string) ([]byte, error) {
				call := strings.Join(args, " ")
				calls = append(calls, call)
				switch {
				case call == "cat "+serviceOwnerPath:
					value := owner
					if scenario == "different-owner" {
						value.Run = "11"
					}
					return json.Marshal(value)
				case call == "cat "+packagedConfig:
					if scenario == "changed-config" {
						return []byte("replaced configuration"), nil
					}
					return config, nil
				case strings.Contains(call, "manager stop"):
					if scenario == "forced" || scenario == "failed-kill" {
						return nil, errors.New("timed out")
					}
				case strings.Contains(call, "systemctl kill"):
					if scenario == "failed-kill" {
						return nil, errors.New("kill failed")
					}
				case strings.Contains(call, "systemctl stop"):
					stopCalls++
					if stopCalls == 1 && (scenario == "forced" || scenario == "failed-kill") {
						return nil, errors.New("unit still busy")
					}
				case strings.Contains(call, "--property=LoadState"):
					if scenario == "mask-failure" {
						return []byte("loaded"), nil
					}
					return []byte("masked"), nil
				case strings.Contains(call, "--property=MainPID"):
					return []byte("0\n"), nil
				case strings.Contains(call, "--property=ControlGroup"):
					if scenario == "remaining-process" {
						return []byte("/system.slice/gitlab-runner-aliyun.service"), nil
					}
				}
				return nil, nil
			}
			forced, err := m.stop(context.Background())
			wantOK := scenario == "graceful" || scenario == "forced"
			if (err == nil) != wantOK || (scenario == "forced" && !forced) {
				t.Fatalf("forced=%v err=%v", forced, err)
			}
			if scenario == "different-owner" || scenario == "changed-config" {
				for _, call := range calls {
					if strings.Contains(call, "systemctl") || strings.Contains(call, "manager stop") {
						t.Fatal("changed ownership permitted service mutation")
					}
				}
			}
		})
	}
}

func TestServiceCredentialsRejectMultilineInput(t *testing.T) {
	if _, err := credentialEnvironment(func(string) string { return "one\nOTHER=value" }); err == nil {
		t.Fatal("environment injection accepted")
	}
	if _, err := credentialEnvironment(func(string) string { return "" }); err == nil {
		t.Fatal("long-lived or missing STS credentials accepted")
	}
	data, err := credentialEnvironment(func(string) string { return "synthetic-value" })
	if err != nil || strings.Count(string(data), "\n") != 3 {
		t.Fatal("expected exactly three credential assignments", err)
	}
}

func TestServiceRecoveryRejectsDriverBeforeFirstStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("recovery uses Linux procfs")
	}
	l := &Ledger{Project: "example/project", RunID: "10", Attempt: "1", GroupID: "test-group", DriverPID: os.Getpid()}
	owner := serviceOwner{Project: l.Project, Run: l.RunID, Attempt: l.Attempt, Group: l.GroupID, DriverPID: l.DriverPID, ConfigSHA256: strings.Repeat("a", 64)}
	command := func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "cat "+serviceOwnerPath {
			t.Fatal("recovery mutated service before driver exit")
		}
		return json.Marshal(owner)
	}
	if stopPackagedManagerWith(context.Background(), l, command) == nil {
		t.Fatal("recovery raced a driver which could still start the service")
	}
}

func TestServiceFinalShutdownRequiresTheOriginalInvocation(t *testing.T) {
	config := []byte("private configuration")
	hash := sha256.Sum256(config)
	owner := serviceOwner{ConfigSHA256: hex.EncodeToString(hash[:])}
	for _, restart := range []string{"none", "before-check", "after-check"} {
		m := &packagedManager{owner: owner, started: true, pid: 123}
		stopping := false
		m.command = func(_ context.Context, args ...string) ([]byte, error) {
			call := strings.Join(args, " ")
			switch {
			case call == "cat "+serviceOwnerPath:
				return json.Marshal(owner)
			case call == "cat "+packagedConfig:
				return config, nil
			case strings.Contains(call, "--property=ActiveState"):
				return []byte("active"), nil
			case strings.Contains(call, "--property=LoadState"):
				return []byte("masked"), nil
			case strings.Contains(call, "manager stop"):
				stopping = true
			case strings.Contains(call, "--property=MainPID"):
				if stopping {
					return []byte("0"), nil
				}
				if restart != "before-check" {
					return []byte("123"), nil
				}
				return []byte("456"), nil
			case strings.Contains(call, "--property=ExecMainPID"):
				if restart == "none" {
					return []byte("123"), nil
				}
				return []byte("456"), nil
			}
			return nil, nil
		}
		forced, err := m.stop(context.Background())
		if err != nil || forced != (restart != "none") {
			t.Fatal("unexpected final invocation verdict", restart, forced, err)
		}
	}
}

func TestServiceCleanupFencesAnOrphanedStartup(t *testing.T) {
	config := []byte("private configuration")
	hash := sha256.Sum256(config)
	owner := serviceOwner{ConfigSHA256: hex.EncodeToString(hash[:])}
	masked, running := false, false
	m := &packagedManager{owner: owner}
	m.command = func(_ context.Context, args ...string) ([]byte, error) {
		call := strings.Join(args, " ")
		switch {
		case call == "cat "+serviceOwnerPath:
			return json.Marshal(owner)
		case call == "cat "+packagedConfig:
			return config, nil
		case strings.Contains(call, "systemctl mask"):
			masked = true
		case strings.Contains(call, "systemctl stop"):
			running = false
		case strings.Contains(call, "--property=LoadState"):
			if masked {
				return []byte("masked"), nil
			}
			return []byte("loaded"), nil
		case strings.Contains(call, "--property=MainPID"):
			// An already-issued privileged start arrives after ordinary shutdown.
			running = !masked
			if running {
				return []byte("123"), nil
			}
			return []byte("0"), nil
		}
		return nil, nil
	}
	if _, err := m.stop(context.Background()); err != nil || running || !masked {
		t.Fatal("delayed service activation was not fenced", err)
	}
}
