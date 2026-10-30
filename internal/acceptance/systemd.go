package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const packagedService = "gitlab-runner-aliyun.service"
const packagedPlugin = "/usr/bin/fleeting-plugin-aliyun"
const packagedConfig = "/etc/gitlab-runner-aliyun/config.toml"
const serviceOwnerPath = "/run/fleeting-acceptance-owner.json"

type serviceOwner struct {
	Project, Run, Attempt, Group, ConfigSHA256 string
	DriverPID                                  int
}

// The root-owned marker binds this disposable service to one acceptance run.
// A local cleanup command may never adopt an unrelated existing Manager.
type packagedManager struct {
	owner          serviceOwner
	command        func(context.Context, ...string) ([]byte, error)
	journalCommand func(context.Context, ...string) ([]byte, bool, error)
	drainCommand   func(context.Context, ...string) ([]byte, error)
	started        bool
	pid            int
}

func serviceCommand(ctx context.Context, args ...string) ([]byte, error) {
	return boundedServiceCommand(ctx, 25*time.Second, args...)
}

func boundedServiceCommand(ctx context.Context, limit time.Duration, args ...string) ([]byte, error) {
	var output bytes.Buffer
	if err := serviceCommandOutput(ctx, limit, &output, args...); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func serviceCommandOutput(ctx context.Context, limit time.Duration, output io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, limit+10*time.Second)
	defer cancel()
	// The privileged timeout remains effective even if the unprivileged driver
	// dies and sudo's child survives it. The outer deadline includes kill grace.
	command := exec.CommandContext(ctx, "sudo", append([]string{"-n", "timeout", "--signal=TERM", "--kill-after=5s", strconv.Itoa(int(limit/time.Second)) + "s"}, args...)...)
	command.Stdout = output
	if err := command.Run(); err != nil {
		return errors.New("packaged Manager operation failed; inspect private diagnostics on the disposable host")
	}
	return nil
}

func (m *packagedManager) beginDrain(ctx context.Context) (<-chan error, error) {
	if err := m.owned(ctx); err != nil {
		return nil, err
	}
	if pid, err := m.running(ctx); err != nil || pid != m.pid {
		return nil, errors.New("drain requires the original active Manager")
	}
	run := m.drainCommand
	if run == nil {
		run = func(ctx context.Context, args ...string) ([]byte, error) {
			return boundedServiceCommand(ctx, 3*time.Minute, args...)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := run(ctx, packagedPlugin, "manager", "stop"); done <- err }()
	return done, nil
}

func (m *packagedManager) drainState(ctx context.Context) (string, error) {
	if err := m.owned(ctx); err != nil {
		return "", err
	}
	raw, err := m.command(ctx, "systemctl", "show", packagedService, "--property=ActiveState", "--property=MainPID", "--property=ExecMainPID", "--property=ControlGroup")
	if err != nil {
		return "", err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			fields[key] = value
		}
	}
	pid := strconv.Itoa(m.pid)
	if m.pid <= 0 || fields["ExecMainPID"] != pid {
		return "", errors.New("draining service invocation changed")
	}
	switch fields["ActiveState"] {
	case "active", "deactivating":
		if fields["MainPID"] == pid && fields["ControlGroup"] != "" {
			if fields["ActiveState"] == "active" {
				return "active", nil
			}
			return "draining", nil
		}
	case "inactive":
		if fields["MainPID"] == "0" && fields["ControlGroup"] == "" {
			return "stopped", nil
		}
	}
	return "", errors.New("Manager drain did not retain its original invocation or reach a clean stop")
}

func credentialEnvironment(get func(string) string) ([]byte, error) {
	var out strings.Builder
	for _, key := range []string{"ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_SECURITY_TOKEN"} {
		value := get(key)
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("packaged acceptance requires fresh single-line STS credentials")
		}
		// systemd EnvironmentFile double quotes preserve these credential bytes.
		replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$")
		out.WriteString(key + "=\"" + replacer.Replace(value) + "\"\n")
	}
	return []byte(out.String()), nil
}

func packagedRunnerConfig(input []byte, address, token, region, group, user string, options smokeOptions) ([]byte, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(input), &doc); err != nil {
		return nil, errors.New("cannot parse the installed package example")
	}
	pools, ok := doc["runners"].([]map[string]any)
	if !ok || len(pools) != 1 || pools[0]["executor"] != options.executor {
		return nil, errors.New("package example has a different executor or pool count")
	}
	pool := pools[0]
	autoscaler, _ := pool["autoscaler"].(map[string]any)
	provider, _ := autoscaler["plugin_config"].(map[string]any)
	connector, _ := autoscaler["connector_config"].(map[string]any)
	if provider == nil || connector == nil || doc["concurrent"] != int64(1) {
		return nil, errors.New("package example lacks the bounded acceptance configuration")
	}
	for _, key := range []string{"max_instances", "capacity_per_instance", "max_use_count"} {
		if autoscaler[key] != int64(1) {
			return nil, errors.New("package example exceeds the single-worker acceptance budget")
		}
	}
	pool["url"], pool["token"] = address, token
	if options.scenario == "capacity" {
		doc["concurrent"], autoscaler["max_instances"] = int64(2), int64(2)
	}
	provider["region"], provider["scaling_group_id"] = region, group
	connector["username"] = user
	if options.executor == "docker-autoscaler" {
		docker, _ := pool["docker"].(map[string]any)
		if docker == nil {
			return nil, errors.New("package example lacks Docker configuration")
		}
		docker["image"] = options.image
	}
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(doc); err != nil {
		return nil, errors.New("cannot encode the package example")
	}
	return out.Bytes(), nil
}

func preparePackagedManager(ctx context.Context, dir string, ledger *Ledger, expected []byte, keyPath string, config []byte) (*packagedManager, string, error) {
	if runtime.GOOS != "linux" || os.Getenv("ACCEPTANCE_DISPOSABLE_HOST") != "true" {
		return nil, "", errors.New("systemd acceptance requires an explicitly disposable Linux host")
	}
	installed, err := os.ReadFile(packagedPlugin)
	if err != nil || !bytes.Equal(installed, expected) {
		return nil, "", errors.New("installed package binary differs from the accepted candidate")
	}
	for _, path := range []string{"/etc/gitlab-runner-aliyun", "/var/lib/gitlab-runner-aliyun", "/var/lib/fleeting-plugin-aliyun", serviceOwnerPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return nil, "", errors.New("refusing an existing Manager installation or acceptance owner")
		}
	}
	environment, err := credentialEnvironment(os.Getenv)
	if err != nil {
		return nil, "", err
	}
	configPath, environmentPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "manager.env")
	for path, data := range map[string][]byte{configPath: config, environmentPath: environment} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			return nil, "", errors.New("cannot write private service setup input")
		}
	}
	if _, err = serviceCommand(ctx, packagedPlugin, "manager", "setup", "--config", configPath, "--ssh-key", keyPath, "--credentials", environmentPath); err != nil {
		return nil, "", err
	}
	actual, err := serviceCommand(ctx, "cat", packagedConfig)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(actual)
	m := &packagedManager{owner: serviceOwner{Project: ledger.Project, Run: ledger.RunID, Attempt: ledger.Attempt, Group: ledger.GroupID, ConfigSHA256: hex.EncodeToString(hash[:]), DriverPID: ledger.DriverPID}, command: serviceCommand}
	owner, _ := json.Marshal(m.owner)
	ownerPath := filepath.Join(dir, "service-owner.json")
	if err = os.WriteFile(ownerPath, owner, 0600); err != nil {
		return nil, "", err
	}
	if _, err = serviceCommand(ctx, "install", "-o", "root", "-g", "root", "-m", "0600", ownerPath, serviceOwnerPath); err != nil {
		return nil, "", err
	}
	id, err := serviceCommand(ctx, "cat", "/etc/gitlab-runner-aliyun/.runner_system_id")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(id)), "s_") {
		return nil, "", errors.New("cannot read the package-created Runner identity")
	}
	return m, strings.TrimSpace(string(id)), nil
}

func (m *packagedManager) owned(ctx context.Context) error {
	data, err := m.command(ctx, "cat", serviceOwnerPath)
	var owner serviceOwner
	if err != nil || json.Unmarshal(data, &owner) != nil || owner != m.owner || owner.ConfigSHA256 == "" {
		return errors.New("packaged Manager is not owned by this acceptance run")
	}
	config, err := m.command(ctx, "cat", packagedConfig)
	hash := sha256.Sum256(config)
	if err != nil || hex.EncodeToString(hash[:]) != owner.ConfigSHA256 {
		return errors.New("packaged Manager configuration changed; refusing service mutation")
	}
	return nil
}

func (m *packagedManager) start(ctx context.Context) (int, error) {
	if err := m.owned(ctx); err != nil {
		return 0, err
	}
	if _, err := m.command(ctx, packagedPlugin, "manager", "start"); err != nil {
		return 0, err
	}
	m.started = true
	pid, err := m.running(ctx)
	m.pid = pid
	return pid, err
}

func (m *packagedManager) running(ctx context.Context) (int, error) {
	state, err := m.command(ctx, "systemctl", "show", packagedService, "--property=ActiveState", "--value")
	if err != nil || strings.TrimSpace(string(state)) != "active" {
		return 0, errors.New("packaged Runner service is not active")
	}
	pid, err := m.command(ctx, "systemctl", "show", packagedService, "--property=MainPID", "--value")
	n, parseErr := strconv.Atoi(strings.TrimSpace(string(pid)))
	if err != nil || parseErr != nil || n <= 0 {
		return 0, errors.New("packaged Runner service has no live Manager")
	}
	return n, nil
}

func (m *packagedManager) stop(ctx context.Context) (forced bool, err error) {
	if err = m.owned(ctx); err != nil {
		return false, err
	}
	if m.started {
		if pid, err := m.running(ctx); err != nil || pid != m.pid {
			forced = true
		}
	}
	if _, err = m.command(ctx, packagedPlugin, "manager", "stop"); err != nil {
		forced = true
	}
	if m.started {
		// ExecMainPID retains the last invocation after the ordinary package stop.
		last, err := m.command(ctx, "systemctl", "show", packagedService, "--property=ExecMainPID", "--value")
		if err != nil || strings.TrimSpace(string(last)) != strconv.Itoa(m.pid) {
			forced = true
		}
	}
	// A privileged startup helper can outlive its driver. Fence future starts
	// permanently on this disposable host before proving the final stopped state.
	// Do this after manager stop: the package correctly rejects masked units.
	if _, err = m.command(ctx, "systemctl", "mask", "--runtime", packagedService); err != nil {
		return forced, err
	}
	if _, err = m.command(ctx, "systemctl", "stop", packagedService); err != nil {
		forced = true
		// Kill the complete owned unit, including a detached plugin subprocess.
		if _, err = m.command(ctx, "systemctl", "kill", "--kill-whom=all", "--signal=SIGKILL", packagedService); err != nil {
			return forced, err
		}
		if _, err = m.command(ctx, "systemctl", "stop", packagedService); err != nil {
			return forced, err
		}
	}
	load, loadErr := m.command(ctx, "systemctl", "show", packagedService, "--property=LoadState", "--value")
	pid, pidErr := m.command(ctx, "systemctl", "show", packagedService, "--property=MainPID", "--value")
	group, groupErr := m.command(ctx, "systemctl", "show", packagedService, "--property=ControlGroup", "--value")
	if loadErr != nil || pidErr != nil || groupErr != nil || strings.TrimSpace(string(load)) != "masked" || strings.TrimSpace(string(pid)) != "0" || strings.TrimSpace(string(group)) != "" {
		return forced, errors.New("packaged Manager is not confirmed masked and stopped with no unit processes")
	}
	return forced, nil
}

func stopPackagedManager(ctx context.Context, ledger *Ledger) error {
	return stopPackagedManagerWith(ctx, ledger, serviceCommand)
}

func stopPackagedManagerWith(ctx context.Context, ledger *Ledger, command func(context.Context, ...string) ([]byte, error)) error {
	if _, err := os.Stat("/proc/self"); err != nil {
		return errors.New("external service cleanup requires Linux procfs")
	}
	data, err := command(ctx, "cat", serviceOwnerPath)
	var owner serviceOwner
	if err != nil || json.Unmarshal(data, &owner) != nil || owner.Project != ledger.Project || owner.Run != ledger.RunID || owner.Attempt != ledger.Attempt || owner.Group != ledger.GroupID || owner.DriverPID != ledger.DriverPID || owner.DriverPID <= 0 {
		return errors.New("packaged Manager ownership differs from the cleanup ledger")
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", owner.DriverPID)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("acceptance driver termination must be proved before external service cleanup")
	}
	m := &packagedManager{owner: owner, command: command}
	_, err = m.stop(ctx)
	return err
}
