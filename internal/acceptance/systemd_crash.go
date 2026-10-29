package acceptance

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

type serviceCrashState struct {
	active, invocation string
	pid, restarts      int
}

func (m *packagedManager) crashState(ctx context.Context) (serviceCrashState, error) {
	raw, err := m.command(ctx, "systemctl", "show", packagedService, "--property=ActiveState", "--property=MainPID", "--property=InvocationID", "--property=NRestarts")
	if err != nil {
		return serviceCrashState{}, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			fields[key] = value
		}
	}
	pid, pidErr := strconv.Atoi(fields["MainPID"])
	restarts, restartErr := strconv.Atoi(fields["NRestarts"])
	if pidErr != nil || restartErr != nil || pid < 0 || restarts < 0 {
		return serviceCrashState{}, errors.New("invalid Manager crash observation")
	}
	return serviceCrashState{active: fields["ActiveState"], invocation: fields["InvocationID"], pid: pid, restarts: restarts}, nil
}

func (m *packagedManager) crash(parent context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	if err := m.owned(ctx); err != nil {
		return 0, err
	}
	before, err := m.crashState(ctx)
	validInvocation := regexp.MustCompile(`^[0-9a-f]{32}$`)
	if err != nil || before.active != "active" || before.pid != m.pid || m.pid <= 0 || !validInvocation.MatchString(before.invocation) {
		return 0, errors.New("crash requires the original active Manager invocation")
	}
	identity, err := m.command(ctx, "cat", "/etc/gitlab-runner-aliyun/.runner_system_id")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(identity)), "s_") {
		return 0, errors.New("crash lacks the original Runner identity")
	}
	// Kill the complete owned unit so no detached plugin can continue mutating
	// the pool. Recovery must come from the shipped Restart=on-failure policy.
	if _, err := m.command(ctx, "systemctl", "kill", "--kill-whom=all", "--signal=SIGKILL", packagedService); err != nil {
		return 0, err
	}
	for {
		if err := m.owned(ctx); err != nil {
			return 0, err
		}
		after, err := m.crashState(ctx)
		if err != nil {
			return 0, err
		}
		if after.restarts > before.restarts+1 {
			return 0, errors.New("Manager entered a crash loop")
		}
		if after.active == "active" && after.pid > 0 && after.pid != before.pid {
			if after.restarts != before.restarts+1 || !validInvocation.MatchString(after.invocation) || after.invocation == before.invocation {
				return 0, errors.New("Manager restart lacks a distinct automatic invocation")
			}
			if _, err := m.command(ctx, "test", "!", "-e", "/proc/"+strconv.Itoa(before.pid)); err != nil {
				return 0, errors.New("crashed Manager process remains present")
			}
			actual, err := m.command(ctx, "cat", "/etc/gitlab-runner-aliyun/.runner_system_id")
			if err != nil || !bytes.Equal(identity, actual) {
				return 0, errors.New("Runner identity changed after the crash")
			}
			m.pid = after.pid
			return after.pid, nil
		}
		if err := aliyun.Wait(ctx, 250*time.Millisecond); err != nil {
			return 0, errors.New("Manager did not automatically restart after its crash")
		}
	}
}
