//go:build live

package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/acceptance"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
	"github.com/euyuil/fleeting-plugin-aliyun/internal/experimental"
	provider "github.com/euyuil/fleeting-plugin-aliyun/internal/provider"
	"github.com/hashicorp/go-hclog"
	fleeting "gitlab.com/gitlab-org/fleeting/fleeting"
	"gitlab.com/gitlab-org/fleeting/fleeting/connector"
	upstream "gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

func TestExtendedCloud(t *testing.T) {
	mode := os.Getenv("LIVE_EXTENDED_MODE")
	required := []string{"ALIYUN_REGION", "ALIYUN_SCALING_GROUP_ID", "ACCEPTANCE_PROJECT", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "ALLOWED_INSTANCE_TYPE", "ALLOWED_IMAGE_ID", "ALLOWED_ZONE_ID", "WORKER_SSH_KEY", "WORKER_USER", "MANAGER_CIDR", "PLUGIN_COMMIT"}
	missing := []string{}
	if mode == "cloud-smoke" {
		required = append(required, "PLUGIN_BINARY")
	}
	for _, key := range required {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if os.Getenv("RUN_LIVE_ACCEPTANCE") != "1" || !slices.Contains([]string{"cloud-smoke", "scale", "interruption", "suspend"}, mode) || len(missing) > 0 {
		dir := os.Getenv("ACCEPTANCE_OUTPUT")
		if dir == "" {
			dir = "../../.live/not-run"
		}
		if e := acceptance.WriteJSON(filepath.Join(dir, "extended-result.json"), map[string]any{"schema_version": 1, "result": "NOT_RUN_MISSING_CONFIGURATION", "missing_configuration": missing, "required_switch": "RUN_LIVE_ACCEPTANCE=1 and LIVE_EXTENDED_MODE=cloud-smoke|scale|interruption|suspend"}); e != nil {
			t.Fatal(e)
		}
		t.Skip("NOT_RUN_MISSING_CONFIGURATION: extended live entry is explicitly disabled")
	}
	maxSize, budget := 1, 1
	if mode == "scale" {
		maxSize, budget = 2, 2
	}
	if mode == "interruption" {
		budget = 2
	}
	// The finite budget is selected explicitly by mode, never raised on capacity failure.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	pluginPath, pluginHash := "", ""
	if mode == "cloud-smoke" {
		var err error
		pluginPath, err = filepath.Abs(os.Getenv("PLUGIN_BINARY"))
		if err != nil {
			t.Fatal(err)
		}
		binary, err := os.ReadFile(pluginPath)
		if err != nil {
			t.Fatal("plugin binary unreadable")
		}
		version, err := exec.CommandContext(ctx, pluginPath, "version").Output()
		if err != nil || !strings.Contains(string(version), os.Getenv("PLUGIN_COMMIT")) {
			t.Fatal("plugin binary does not match the requested commit")
		}
		digest := sha256.Sum256(binary)
		pluginHash = hex.EncodeToString(digest[:])
	}
	sdk, err := aliyun.NewSDK(os.Getenv("ALIYUN_REGION"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Close()
	cloud := &aliyun.Client{API: sdk, Region: os.Getenv("ALIYUN_REGION"), GroupID: os.Getenv("ALIYUN_SCALING_GROUP_ID"), Log: hclog.New(&hclog.LoggerOptions{Output: os.Stderr, Level: hclog.Warn})}
	group, err := cloud.Group(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if group.MaxSize != maxSize {
		t.Fatalf("mode requires explicit group MaxSize=%d", maxSize)
	}
	if err = cloud.Validate(ctx, group); err != nil {
		t.Fatal(err)
	}
	template, err := cloud.Template(ctx, group)
	if err != nil {
		t.Fatal(err)
	}
	copyGroup := group
	copyGroup.MaxSize = 1
	if err = aliyun.ValidateSpotSmoke(copyGroup, template); err != nil {
		t.Fatal(err)
	}
	data := template.LaunchTemplateData
	if data.ImageId != os.Getenv("ALLOWED_IMAGE_ID") || data.InstanceType != os.Getenv("ALLOWED_INSTANCE_TYPE") || data.ZoneId != os.Getenv("ALLOWED_ZONE_ID") {
		t.Fatal("exact extended allowlist mismatch")
	}
	key, err := os.ReadFile(os.Getenv("WORKER_SSH_KEY"))
	if err != nil {
		t.Fatal("worker key unreadable")
	}
	if err = acceptance.VerifyKeyPair(ctx, cloud, data.KeyPairName, key); err != nil {
		t.Fatal(err)
	}
	base := os.Getenv("ACCEPTANCE_OUTPUT")
	if base == "" {
		base = "../../.live"
	}
	dir, err := acceptance.EvidenceDirectory(base, os.Getenv("GITHUB_RUN_ID"), os.Getenv("GITHUB_RUN_ATTEMPT"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ledger := &acceptance.Ledger{SchemaVersion: 1, Mode: mode, Project: os.Getenv("ACCEPTANCE_PROJECT"), Region: cloud.Region, GroupID: cloud.GroupID, RunID: os.Getenv("GITHUB_RUN_ID"), Attempt: os.Getenv("GITHUB_RUN_ATTEMPT"), StartedAt: now, ExpiresAt: now.Add(25 * time.Minute), Path: filepath.Join(dir, "ledger.json"), DriverPID: os.Getpid()}
	if _, err = os.Stat(ledger.Path); err == nil {
		t.Fatal("existing extended ledger is preserved")
	}
	ledger.InstanceLimit = maxSize
	ledger.CreationBudget = budget
	ledger.DeadlineAt, _ = ctx.Deadline()
	if err = acceptance.Claim(ctx, cloud, ledger); err != nil {
		t.Fatal(err)
	}
	transitions := []map[string]any{}
	var worker upstream.InstanceGroup
	var killPlugin func()
	normalReleased := false
	sshCompleted := false
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 8*time.Minute)
		defer stop()
		result := "PASS"
		if worker != nil {
			if e := worker.Shutdown(cleanupCtx); e != nil {
				t.Error(e)
			}
		}
		if killPlugin != nil {
			killPlugin()
		}
		if e := acceptance.Cleanup(cleanupCtx, cloud, ledger, false); e != nil {
			result = "CLEANUP_FAILED"
			t.Error(e)
		} else if t.Failed() || !normalReleased || !sshCompleted || len(ledger.Instances) > budget || (mode == "cloud-smoke" && len(ledger.Instances) != 1) {
			result = "FAIL_PRODUCT"
		}
		for _, resource := range ledger.Instances {
			if !resource.SpotVerified || !resource.DeletionVerified {
				if result == "PASS" {
					result = "FAIL_PRODUCT"
				}
				t.Error("resource acceptance is incomplete")
			}
		}
		if e := ledger.Save(); e != nil {
			if result != "CLEANUP_FAILED" {
				result = "FAIL_INFRASTRUCTURE"
			}
			t.Error(e)
		}
		if e := acceptance.WriteJSON(filepath.Join(dir, "extended-result.json"), map[string]any{"schema_version": 1, "result": result, "mode": mode, "creation_budget": budget, "resources": ledger, "transitions": transitions, "plugin_commit": os.Getenv("PLUGIN_COMMIT"), "plugin_sha256": pluginHash, "real_plugin_process": mode == "cloud-smoke", "ssh_completed": sshCompleted, "normal_release": normalReleased}); e != nil {
			t.Error(e)
		}
	}()
	if err = acceptance.EnsureIngress(ctx, cloud, ledger, os.Getenv("MANAGER_CIDR")); err != nil {
		t.Fatal(err)
	}
	if mode == "cloud-smoke" {
		// A per-run copy and PID file let independent cleanup identify an orphaned
		// plugin without trusting an arbitrary process ID from a receipt.
		ledger.PluginDirectory = "plugin"
		if e := ledger.Save(); e != nil {
			t.Fatal(e)
		}
		pluginDir := filepath.Join(dir, ledger.PluginDirectory)
		if e := os.Mkdir(pluginDir, 0700); e != nil {
			t.Fatal(e)
		}
		binary, e := os.ReadFile(pluginPath)
		if e != nil {
			t.Fatal(e)
		}
		digest := sha256.Sum256(binary)
		if hex.EncodeToString(digest[:]) != pluginHash {
			t.Fatal("plugin changed after verification")
		}
		copyPath := filepath.Join(pluginDir, "fleeting-plugin-aliyun")
		if e = os.WriteFile(copyPath, binary, 0700); e != nil {
			t.Fatal(e)
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
		launcher := filepath.Join(pluginDir, "launch.sh")
		script := "#!/bin/sh\nset -eu\numask 077\nprintf '%s\\n' \"$$\" > " + quote(filepath.Join(pluginDir, "pid")) + "\nexec " + quote(copyPath) + "\n"
		if e = os.WriteFile(launcher, []byte(script), 0700); e != nil {
			t.Fatal(e)
		}
		config, e := json.Marshal(map[string]string{"region": cloud.Region, "scaling_group_id": cloud.GroupID})
		if e != nil {
			t.Fatal(e)
		}
		process, e := fleeting.RunPlugin(launcher, config, fleeting.WithPluginLogger(hclog.NewNullLogger()))
		if e != nil {
			t.Fatal(e)
		}
		killPlugin = func() { process.Kill() }
		worker = process.InstanceGroup()
		pid, e := os.ReadFile(filepath.Join(pluginDir, "pid"))
		if e != nil {
			t.Fatal(e)
		}
		if n, e := strconv.Atoi(strings.TrimSpace(string(pid))); e != nil || n <= 0 {
			t.Fatal("plugin PID was not recorded")
		}
	} else {
		worker = &provider.InstanceGroup{Region: cloud.Region, ScalingGroupID: cloud.GroupID, Client: cloud}
	}
	settings := upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{Username: os.Getenv("WORKER_USER"), Key: key, UseStaticCredentials: true, Timeout: time.Minute}}
	if _, err = worker.Init(ctx, nil, settings); err != nil {
		t.Fatal(err)
	}
	observe := func() error {
		return acceptance.ObserveBudget(ctx, cloud, ledger, budget, data.InstanceType, data.ImageId, data.ZoneId)
	}
	wait := func(condition func() (bool, error)) {
		t.Helper()
		for {
			if err = observe(); err != nil {
				t.Fatal(err)
			}
			ok, e := condition()
			if e != nil {
				t.Fatal(e)
			}
			if ok {
				return
			}
			if e = aliyun.Wait(ctx, time.Second); e != nil {
				t.Fatal(e)
			}
		}
	}
	if n, e := worker.Increase(ctx, 1); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if mode == "scale" {
		if n, e := worker.Increase(ctx, 1); e != nil || n != 1 {
			t.Fatal(n, e)
		}
	}
	ids := []string{}
	wait(func() (bool, error) {
		ids = ids[:0]
		if e := worker.Update(ctx, func(id string, state upstream.State) {
			if state == upstream.StateRunning {
				ids = append(ids, id)
			}
		}); e != nil {
			return false, e
		}
		slices.Sort(ids)
		if len(ids) != maxSize {
			return false, nil
		}
		for _, id := range ids {
			if !slices.ContainsFunc(ledger.Instances, func(r acceptance.Resource) bool { return r.InstanceID == id && r.InventoryComplete }) {
				return false, nil
			}
		}
		return true, nil
	})
	if mode == "cloud-smoke" {
		current, e := cloud.Group(ctx)
		if e != nil || current.DesiredCapacity != 1 || current.TotalCapacity != 1 {
			t.Fatal("plugin scale-out did not reach one actual instance", e)
		}
		transitions = append(transitions,
			map[string]any{"action": "initial", "desired_capacity": group.DesiredCapacity, "total_capacity": group.TotalCapacity},
			map[string]any{"action": "plugin_increase", "desired_capacity": current.DesiredCapacity, "total_capacity": current.TotalCapacity, "instance_id": ids[0], "observed_at": time.Now().UTC()})
		var disks struct {
			TotalCount *int
			Disks      struct {
				Disk []struct {
					DiskId, InstanceId, Type, Category, PerformanceLevel string
					Size                                                 int
					DeleteWithInstance                                   bool
				}
			}
		}
		if e = cloud.API.Call(ctx, "ecs", "DescribeDisks", map[string]string{"RegionId": cloud.Region, "InstanceId": ids[0], "PageSize": "100"}, &disks); e != nil {
			t.Fatal(e)
		}
		if disks.TotalCount == nil || *disks.TotalCount != 1 || len(disks.Disks.Disk) != 1 {
			t.Fatal("cloud-smoke requires one complete system disk record")
		}
		actual := disks.Disks.Disk[0]
		knownDisk := false
		for _, resource := range ledger.Instances {
			if resource.InstanceID == ids[0] && resource.InventoryComplete && len(resource.DiskIDs) == 1 && resource.DiskIDs[0] == actual.DiskId {
				knownDisk = true
			}
		}
		if !knownDisk || actual.DiskId == "" || actual.InstanceId != ids[0] || actual.Type != "system" || !actual.DeleteWithInstance || actual.Size != data.SystemDiskSize || actual.Category != data.SystemDiskCategory || actual.PerformanceLevel != data.SystemDiskPerformanceLevel {
			t.Fatal("actual disk differs from the pinned template")
		}
		transitions = append(transitions, map[string]any{"action": "actual_disk", "size_gib": actual.Size, "category": actual.Category, "performance_level": actual.PerformanceLevel, "delete_with_instance": actual.DeleteWithInstance})
	}
	runSSH := func(id string) {
		t.Helper()
		if mode == "cloud-smoke" {
			sshCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
			defer stop()
			nonce := sha256.Sum256([]byte(ledger.Marker() + now.String()))
			want := "cloud-smoke-" + hex.EncodeToString(nonce[:16])
			var lastErr error
			for {
				if e := observe(); e != nil {
					t.Fatal(e)
				}
				info, e := worker.ConnectInfo(sshCtx, id)
				if e == nil {
					var output bytes.Buffer
					attemptCtx, cancel := context.WithTimeout(sshCtx, 15*time.Second)
					e = connector.Run(attemptCtx, info, connector.ConnectorOptions{DialOptions: connector.DialOptions{UseExternalAddr: true}, RunOptions: connector.RunOptions{Command: "printf " + want, Stdout: &output}})
					cancel()
					if e == nil {
						if output.String() != want {
							t.Fatal("live SSH nonce did not match")
						}
						sshCompleted = true
						return
					}
				}
				lastErr = e
				if e = aliyun.Wait(sshCtx, 2*time.Second); e != nil {
					t.Fatalf("live plugin SSH did not become ready: %v; last attempt: %v", e, lastErr)
				}
			}
		}
		info, e := worker.ConnectInfo(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		e = connector.Run(ctx, info, connector.ConnectorOptions{DialOptions: connector.DialOptions{UseExternalAddr: true}, RunOptions: connector.RunOptions{Command: "timeout 300 sh -c 'until test -f /var/lib/fleeting-worker/ready; do sleep 2; done; printf extended-ok'"}})
		if e != nil {
			t.Fatal(e)
		}
		sshCompleted = true
	}
	for _, id := range ids {
		runSSH(id)
	}
	if mode == "cloud-smoke" {
		transitions = append(transitions, map[string]any{"action": "plugin_connect_info_ssh", "instance_id": ids[0], "nonce_verified": sshCompleted, "observed_at": time.Now().UTC()})
	}
	if mode == "scale" {
		if accepted, e := worker.Decrease(ctx, ids[:1]); e != nil || len(accepted) != 1 {
			t.Fatal(accepted, e)
		}
		retained := ids[1]
		wait(func() (bool, error) {
			members, e := cloud.Members(ctx)
			if e != nil {
				return false, e
			}
			_, keep := members[retained]
			if !keep {
				return false, errors.New("specified scale-in removed another worker")
			}
			_, old := members[ids[0]]
			return !old, nil
		})
		ids = []string{retained}
		runSSH(retained)
	}
	if mode == "interruption" {
		old := ids[0]
		var out struct{ RequestId string }
		if err = cloud.API.Call(ctx, "ecs", "DeleteInstance", map[string]string{"RegionId": cloud.Region, "InstanceId": old, "Force": "true"}, &out); err != nil {
			t.Fatal(err)
		}
		wait(func() (bool, error) {
			members, e := cloud.Members(ctx)
			if e != nil {
				return false, e
			}
			for id := range members {
				if id != old {
					ids = []string{id}
					ready := false
					e = worker.Update(ctx, func(candidate string, state upstream.State) {
						if candidate == id && state == upstream.StateRunning {
							ready = true
						}
					})
					return ready, e
				}
			}
			return false, nil
		})
		runSSH(ids[0])
		g, e := cloud.Group(ctx)
		if e != nil || g.DesiredCapacity != 1 {
			t.Fatal("replacement intent changed", e)
		}
	}
	if mode == "suspend" {
		// Retire the accepted increase journal before handing this single writer to the experiment controller.
		if err = cloud.ClearJournal(ctx); err != nil {
			t.Fatal(err)
		}
		controller := &experimental.Controller{Client: cloud}
		for _, action := range []string{"suspend", "resume"} {
			for {
				_, err = controller.Request(ctx, action, ids)
				if !errors.Is(err, experimental.ErrPoolNotReady) {
					break
				}
				if e := aliyun.Wait(ctx, time.Second); e != nil {
					t.Fatal(e)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			for {
				if _, err = controller.Reconcile(ctx); err != nil && !errors.Is(err, experimental.ErrPoolNotReady) {
					t.Fatal(err)
				}
				g, e := cloud.Group(ctx)
				if e != nil {
					t.Fatal(e)
				}
				if g.DesiredCapacity != 1 {
					t.Fatal("standby changed desired capacity")
				}
				j, e := aliyun.ReadJournal(g)
				if e != nil {
					t.Fatal(e)
				}
				if e = acceptance.RecoverResources(ctx, cloud, ledger); e != nil {
					t.Fatal(e)
				}
				if len(ledger.Instances) > budget {
					t.Fatal("suspend created a replacement")
				}
				if j == nil {
					members, e := cloud.Members(ctx)
					if e != nil {
						t.Fatal(e)
					}
					instances, e := cloud.Instances(ctx, ids)
					if e != nil {
						t.Fatal(e)
					}
					if len(members) != 1 || len(instances) != 1 {
						t.Fatal("suspend identity disappeared or was replaced")
					}
					member, memberOK := members[ids[0]]
					instance, instanceOK := instances[ids[0]]
					if !memberOK || !instanceOK {
						t.Fatal("suspend identity changed")
					}
					wantMember, wantInstance := "Standby", "Stopped"
					if action == "resume" {
						wantMember, wantInstance = "InService", "Running"
					}
					if member.LifecycleState == wantMember && instance.Status == wantInstance {
						transitions = append(transitions, map[string]any{"action": action, "instance_id": ids[0], "ess_state": member.LifecycleState, "ecs_state": instance.Status, "desired_capacity": g.DesiredCapacity, "observed_at": time.Now().UTC()})
						break
					}
				}
				if e = aliyun.Wait(ctx, time.Second); e != nil {
					t.Fatal(e)
				}
			}
		}
		runSSH(ids[0])
	}
	if accepted, e := worker.Decrease(ctx, ids); e != nil || len(accepted) != len(ids) {
		t.Fatal(accepted, e)
	}
	wait(func() (bool, error) {
		if _, e := worker.Decrease(ctx, nil); e != nil {
			return false, e
		}
		g, e := cloud.Group(ctx)
		if e != nil {
			return false, e
		}
		members, e := cloud.Members(ctx)
		if e != nil {
			return false, e
		}
		remaining, e := cloud.Instances(ctx, ids)
		return g.DesiredCapacity == 0 && g.TotalCapacity == 0 && g.PendingCapacity == 0 && g.RemovingCapacity == 0 && len(members) == 0 && len(remaining) == 0, e
	})
	if mode == "cloud-smoke" {
		if err = acceptance.WaitForNormalRelease(ctx, cloud, ledger, data.InstanceType, data.ImageId, data.ZoneId); err != nil {
			t.Fatal(err)
		}
		transitions = append(transitions, map[string]any{"action": "plugin_decrease", "desired_capacity": 0, "total_capacity": 0, "associated_resources_absent": true, "observed_at": time.Now().UTC()})
	}
	normalReleased = true
	fmt.Printf("extended %s normal release observed; final cleanup is mandatory\n", mode)
}
