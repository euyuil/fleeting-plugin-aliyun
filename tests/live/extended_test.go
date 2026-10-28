//go:build live

package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
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
	creatingRestart := mode == "cloud-create-restart"
	removingRestart := mode == "cloud-remove-restart"
	lostIncrease := mode == "cloud-increase-response-loss"
	restartMode := mode == "cloud-restart" || creatingRestart || removingRestart || lostIncrease
	pluginMode := mode == "cloud-smoke" || mode == "cloud-scale" || restartMode
	required := []string{"ALIYUN_REGION", "ALIYUN_SCALING_GROUP_ID", "ACCEPTANCE_PROJECT", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "ALLOWED_INSTANCE_TYPE", "ALLOWED_IMAGE_ID", "ALLOWED_ZONE_ID", "WORKER_SSH_KEY", "WORKER_USER", "MANAGER_CIDR", "PLUGIN_COMMIT"}
	missing := []string{}
	if pluginMode {
		required = append(required, "PLUGIN_BINARY")
	}
	for _, key := range required {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if os.Getenv("RUN_LIVE_ACCEPTANCE") != "1" || !slices.Contains([]string{"cloud-smoke", "cloud-scale", "cloud-restart", "cloud-create-restart", "cloud-remove-restart", "cloud-increase-response-loss", "scale", "interruption", "suspend"}, mode) || len(missing) > 0 {
		dir := os.Getenv("ACCEPTANCE_OUTPUT")
		if dir == "" {
			dir = "../../.live/not-run"
		}
		if e := acceptance.WriteJSON(filepath.Join(dir, "extended-result.json"), map[string]any{"schema_version": 1, "result": "NOT_RUN_MISSING_CONFIGURATION", "missing_configuration": missing, "required_switch": "RUN_LIVE_ACCEPTANCE=1 and LIVE_EXTENDED_MODE=cloud-smoke|cloud-scale|cloud-restart|cloud-create-restart|cloud-remove-restart|cloud-increase-response-loss|scale|interruption|suspend"}); e != nil {
			t.Fatal(e)
		}
		t.Skip("NOT_RUN_MISSING_CONFIGURATION: extended live entry is explicitly disabled")
	}
	maxSize, budget := 1, 1
	if mode == "scale" || mode == "cloud-scale" {
		maxSize, budget = 2, 2
	}
	if mode == "interruption" {
		budget = 2
	}
	// The finite budget is selected explicitly by mode, never raised on capacity failure.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	pluginPath, pluginHash := "", ""
	if pluginMode {
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
		if lostIncrease {
			info, e := buildinfo.ReadFile(pluginPath)
			if e != nil {
				t.Fatal("plugin build information is unavailable")
			}
			var tags []string
			for _, setting := range info.Settings {
				if setting.Key == "-tags" {
					tags = strings.FieldsFunc(setting.Value, func(r rune) bool { return r == ',' || r == ' ' })
				}
			}
			if !slices.Contains(tags, "responsegate") || slices.Contains(tags, "componenttest") || os.Getenv("ALIYUN_REGION") != "us-east-1" {
				t.Fatal("response-loss mode requires a responsegate build without componenttest and the fixed US East fixture")
			}
		}
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
	var startPlugin func() error
	pluginPID := 0
	var gate *acceptance.ResponseGate
	var terminationStartedAt time.Time
	normalReleased := false
	sshCompleted := false
	retainedVerified := false
	restartVerified := false
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 8*time.Minute)
		defer stop()
		result := "PASS"
		if lostIncrease && killPlugin != nil {
			// A failed cutpoint can leave Increase blocked on the held response.
			// Stop that writer before Shutdown or independent cloud cleanup.
			killPlugin()
			killPlugin, worker = nil, nil
		}
		if worker != nil {
			if e := worker.Shutdown(cleanupCtx); e != nil {
				t.Error(e)
			}
		}
		if killPlugin != nil {
			killPlugin()
		}
		if gate != nil {
			_ = gate.Close()
		}
		if e := acceptance.Cleanup(cleanupCtx, cloud, ledger, false); e != nil {
			result = "CLEANUP_FAILED"
			t.Error(e)
		} else if t.Failed() || !normalReleased || !sshCompleted || len(ledger.Instances) > budget || (pluginMode && len(ledger.Instances) != maxSize) || (mode == "cloud-scale" && !retainedVerified) || (restartMode && !restartVerified) {
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
		if e := acceptance.WriteJSON(filepath.Join(dir, "extended-result.json"), map[string]any{"schema_version": 1, "result": result, "mode": mode, "creation_budget": budget, "resources": ledger, "transitions": transitions, "plugin_commit": os.Getenv("PLUGIN_COMMIT"), "plugin_sha256": pluginHash, "real_plugin_process": pluginMode, "response_gate_instrumented": lostIncrease, "ssh_completed": sshCompleted, "normal_release": normalReleased, "specified_release": retainedVerified, "restart_recovered": restartVerified}); e != nil {
			t.Error(e)
		}
	}()
	if err = acceptance.EnsureIngress(ctx, cloud, ledger, os.Getenv("MANAGER_CIDR")); err != nil {
		t.Fatal(err)
	}
	if pluginMode {
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
		if lostIncrease {
			gate, e = acceptance.NewResponseGate(cloud.GroupID)
			if e != nil {
				t.Fatal(e)
			}
			caPath := filepath.Join(pluginDir, "gate-ca.pem")
			if e = os.WriteFile(caPath, gate.CA(), 0600); e != nil {
				t.Fatal(e)
			}
			script = strings.Replace(script, "\nexec ", "\nunset DEBUG GODEBUG\nexport ALIYUN_RESPONSE_GATE_ADDR="+quote(gate.Address())+"\nexport ALIYUN_RESPONSE_GATE_CA="+quote(caPath)+"\nexec ", 1)
		}
		if e = os.WriteFile(launcher, []byte(script), 0700); e != nil {
			t.Fatal(e)
		}
		config, e := json.Marshal(map[string]string{"region": cloud.Region, "scaling_group_id": cloud.GroupID})
		if e != nil {
			t.Fatal(e)
		}
		startPlugin = func() error {
			if pluginPID > 0 {
				proofCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				defer stop()
				if err := acceptance.WaitForProcessExit(proofCtx, pluginPID); err != nil {
					return fmt.Errorf("previous plugin has not exited: %w", err)
				}
			}
			binary, err := os.ReadFile(copyPath)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(binary)
			if hex.EncodeToString(digest[:]) != pluginHash {
				return errors.New("private plugin binary changed before launch")
			}
			if err := os.Remove(filepath.Join(pluginDir, "pid")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			process, err := fleeting.RunPlugin(launcher, config, fleeting.WithPluginLogger(hclog.NewNullLogger()))
			if err != nil {
				return err
			}
			killPlugin = process.Kill
			worker = process.InstanceGroup()
			pid, err := os.ReadFile(filepath.Join(pluginDir, "pid"))
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(strings.TrimSpace(string(pid)))
			if err != nil || n <= 0 || n == os.Getpid() {
				return errors.New("plugin PID was not recorded")
			}
			pluginPID = n
			return nil
		}
		if e := startPlugin(); e != nil {
			t.Fatal(e)
		}
	} else {
		worker = &provider.InstanceGroup{Region: cloud.Region, ScalingGroupID: cloud.GroupID, Client: cloud}
	}
	settings := upstream.Settings{ConnectorConfig: upstream.ConnectorConfig{Username: os.Getenv("WORKER_USER"), Key: key, UseStaticCredentials: true, Timeout: time.Minute}}
	if _, err = worker.Init(ctx, nil, settings); err != nil {
		t.Fatal(err)
	}
	restartPlugin := func(afterExit func() error) (int, error) {
		oldPID := pluginPID
		if oldPID <= 0 || oldPID == os.Getpid() {
			return oldPID, errors.New("invalid owned plugin PID")
		}
		old, e := os.FindProcess(oldPID)
		if e != nil {
			return oldPID, e
		}
		terminationStartedAt = time.Now().UTC()
		if e = old.Kill(); e != nil {
			return oldPID, e
		}
		worker = nil
		killPlugin()
		proofCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		e = acceptance.WaitForProcessExit(proofCtx, oldPID)
		stop()
		if e != nil {
			return oldPID, e
		}
		if afterExit != nil {
			if e = afterExit(); e != nil {
				return oldPID, e
			}
		}
		if e = startPlugin(); e != nil {
			return oldPID, e
		}
		if pluginPID == oldPID {
			return oldPID, errors.New("restart did not produce a distinct plugin PID")
		}
		_, e = worker.Init(ctx, nil, settings)
		return oldPID, e
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
	var creationBefore, creationAfter acceptance.CreatingSnapshot
	if creatingRestart || lostIncrease {
		transitions = append(transitions, map[string]any{"action": "initial", "desired_capacity": group.DesiredCapacity, "total_capacity": group.TotalCapacity})
	}
	increase := 1
	if pluginMode {
		increase = maxSize
	}
	if lostIncrease {
		type increaseResult struct {
			n   int
			err error
		}
		returned := make(chan increaseResult, 1)
		originalWorker := worker
		go func() { n, e := originalWorker.Increase(ctx, 1); returned <- increaseResult{n, e} }()
		heldCtx, stop := context.WithTimeout(ctx, 20*time.Second)
		held, e := gate.Wait(heldCtx)
		stop()
		if e != nil {
			t.Fatal("successful upstream response was not withheld", e)
		}
		pending := func() error {
			select {
			case <-returned:
				return errors.New("old Increase returned before termination")
			default:
				return nil
			}
		}
		capture := func() (acceptance.CreatingSnapshot, error) {
			s, e := acceptance.CaptureCreating(ctx, cloud, ledger)
			if e != nil {
				return s, e
			}
			g, e := cloud.Group(ctx)
			if e != nil {
				return s, e
			}
			j, e := aliyun.ReadJournal(g)
			if e != nil || j == nil || j.Token != held.ClientToken || held.GroupID != cloud.GroupID || held.RequestID == "" || held.HTTPStatus != 200 {
				return s, errors.New("held response does not match the persisted expansion token")
			}
			return s, nil
		}
		creationBefore, e = capture()
		if e != nil {
			t.Fatal(e)
		}
		cutpointPath := filepath.Join(dir, "response-loss-cutpoint.json")
		if e = acceptance.WriteJSON(cutpointPath, map[string]any{"held_response": held, "before": creationBefore}); e != nil {
			t.Fatal(e)
		}
		current, e := gate.Receipt()
		if e != nil || !current.Held || current.ResponseDelivered || current.Forwarded != 1 || !current.ClientClosedAt.IsZero() || time.Since(held.ReceivedAt) > 10*time.Second {
			t.Fatal("upstream response is no longer held before termination", e)
		}
		if e = pending(); e != nil {
			t.Fatal(e)
		}
		var disconnected acceptance.HeldIncrease
		oldPID, e := restartPlugin(func() error {
			var err error
			creationAfter, err = capture()
			if err != nil {
				return err
			}
			if creationAfter.ActivityID != creationBefore.ActivityID || !slices.Equal(creationAfter.ActivityIDs, creationBefore.ActivityIDs) {
				return errors.New("creation activity changed while the response was held")
			}
			proofCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			disconnected, err = gate.WaitDisconnect(proofCtx)
			cancel()
			if err != nil || disconnected.ClientClosedAt.Before(terminationStartedAt) || disconnected.ResponseDelivered || disconnected.Forwarded != 1 {
				return errors.New("client disconnected before termination or another expansion was attempted")
			}
			if err = acceptance.WriteJSON(cutpointPath, map[string]any{"held_response": held, "after_old_exit_response": disconnected, "before": creationBefore, "after_old_exit": creationAfter, "termination_started_at": terminationStartedAt, "old_process_exit_confirmed": true, "old_increase_pending_before_kill": true}); err != nil {
				return err
			}
			gate.DropHeld()
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		select {
		case old := <-returned:
			if old.err == nil || old.n != 0 {
				t.Fatal("old Increase unexpectedly returned acceptance")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("old Increase did not terminate with its plugin")
		}
		retried, e := worker.Increase(ctx, 1)
		if e != nil || retried < 0 || retried > 1 {
			t.Fatal("retry did not reconcile the original expansion", retried, e)
		}
		proof, e := gate.Receipt()
		if e != nil || proof.Forwarded != 1 || proof.ResponseDelivered {
			t.Fatal("retry reissued an expansion", e)
		}
		transitions = append(transitions, map[string]any{"action": "plugin_restart_with_lost_increase_response", "held_response": held, "after_old_exit_response": disconnected, "before": creationBefore, "after_old_exit": creationAfter, "termination_started_at": terminationStartedAt, "old_plugin_pid": oldPID, "new_plugin_pid": pluginPID, "old_process_exit_confirmed": true, "old_increase_pending_before_kill": true, "old_increase_returned_acceptance": false, "retry_increase_accepted": retried, "response_delivered": false, "scale_to_one_forwarded": proof.Forwarded, "observed_at": time.Now().UTC()})
	} else if n, e := worker.Increase(ctx, increase); e != nil || n != increase {
		t.Fatal(n, e)
	}
	if creatingRestart {
		acceptedAt := time.Now().UTC()
		var e error
		creationBefore, e = acceptance.CaptureCreating(ctx, cloud, ledger)
		if e != nil {
			t.Fatal("creation restart window was not captured", e)
		}
		cutpointPath := filepath.Join(dir, "creation-cutpoint.json")
		if e = acceptance.WriteJSON(cutpointPath, map[string]any{"before": creationBefore, "accepted_at": acceptedAt}); e != nil {
			t.Fatal(e)
		}
		oldPID, e := restartPlugin(func() error {
			var err error
			creationAfter, err = acceptance.CaptureCreating(ctx, cloud, ledger)
			if err != nil {
				return fmt.Errorf("creation window ended before old process exit proof: %w", err)
			}
			if creationAfter.ActivityID != creationBefore.ActivityID || !slices.Equal(creationAfter.ActivityIDs, creationBefore.ActivityIDs) || (creationBefore.InstanceID != "" && creationAfter.InstanceID != "" && creationBefore.InstanceID != creationAfter.InstanceID) {
				return errors.New("creation identity or activity changed at restart")
			}
			return acceptance.WriteJSON(cutpointPath, map[string]any{"accepted_at": acceptedAt, "before": creationBefore, "after_old_exit": creationAfter, "old_process_exit_confirmed": true})
		})
		if e != nil {
			t.Fatal(e)
		}
		transitions = append(transitions, map[string]any{"action": "plugin_restart_during_creation", "accepted_at": acceptedAt, "before": creationBefore, "after_old_exit": creationAfter, "old_plugin_pid": oldPID, "new_plugin_pid": pluginPID, "old_process_exit_confirmed": true, "crash_without_provider_shutdown": true, "creating_before_and_after_old_exit": true, "observed_at": time.Now().UTC()})
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
	if pluginMode {
		current, e := cloud.Group(ctx)
		if e != nil || current.DesiredCapacity != maxSize || current.TotalCapacity != maxSize {
			t.Fatal("plugin scale-out did not reach the exact requested capacity", e)
		}
		if !creatingRestart && !lostIncrease {
			transitions = append(transitions, map[string]any{"action": "initial", "desired_capacity": group.DesiredCapacity, "total_capacity": group.TotalCapacity})
		}
		transitions = append(transitions, map[string]any{"action": "plugin_increase", "desired_capacity": current.DesiredCapacity, "total_capacity": current.TotalCapacity, "instance_ids": slices.Clone(ids), "observed_at": time.Now().UTC()})
		for _, id := range ids {
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
			if e = cloud.API.Call(ctx, "ecs", "DescribeDisks", map[string]string{"RegionId": cloud.Region, "InstanceId": id, "PageSize": "100"}, &disks); e != nil {
				t.Fatal(e)
			}
			if disks.TotalCount == nil || *disks.TotalCount != 1 || len(disks.Disks.Disk) != 1 {
				t.Fatal("plugin-only mode requires one complete system disk record per worker")
			}
			actual := disks.Disks.Disk[0]
			knownDisk := false
			for _, resource := range ledger.Instances {
				if resource.InstanceID == id && resource.InventoryComplete && len(resource.DiskIDs) == 1 && resource.DiskIDs[0] == actual.DiskId {
					knownDisk = true
				}
			}
			if !knownDisk || actual.DiskId == "" || actual.InstanceId != id || actual.Type != "system" || !actual.DeleteWithInstance || actual.Size != data.SystemDiskSize || actual.Category != data.SystemDiskCategory || actual.PerformanceLevel != data.SystemDiskPerformanceLevel {
				t.Fatal("actual disk differs from the pinned template")
			}
			transitions = append(transitions, map[string]any{"action": "actual_disk", "instance_id": id, "size_gib": actual.Size, "category": actual.Category, "performance_level": actual.PerformanceLevel, "delete_with_instance": actual.DeleteWithInstance})
		}
	}
	sshSequence := 0
	runSSH := func(id string) {
		t.Helper()
		if pluginMode {
			sshCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
			defer stop()
			sshSequence++
			nonce := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%d", ledger.Marker(), now.String(), id, sshSequence)))
			want := "plugin-cloud-" + hex.EncodeToString(nonce[:16])
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
						transitions = append(transitions, map[string]any{"action": "plugin_connect_info_ssh", "instance_id": id, "nonce_verified": true, "sequence": sshSequence, "observed_at": time.Now().UTC()})
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
	if creatingRestart || lostIncrease {
		original := ids[0]
		if (creationBefore.InstanceID != "" && creationBefore.InstanceID != original) || (creationAfter.InstanceID != "" && creationAfter.InstanceID != original) {
			t.Fatal("creation restart produced a different worker")
		}
		wait(func() (bool, error) {
			history, e := acceptance.History(ctx, cloud)
			if e != nil {
				return false, e
			}
			var activityIDs []string
			complete := false
			for _, activity := range history {
				activityIDs = append(activityIDs, activity.ScalingActivityId)
				if activity.ScalingActivityId == creationBefore.ActivityID {
					if activity.StatusCode != "InProgress" && activity.StatusCode != "Successful" {
						return false, errors.New("accepted creation activity failed")
					}
					complete = activity.StatusCode == "Successful" && activity.CreatedCapacity == 1 && slices.Equal(activity.CreatedInstances.CreatedInstance, []string{original})
				}
			}
			slices.Sort(activityIDs)
			if !slices.Equal(activityIDs, creationBefore.ActivityIDs) {
				return false, errors.New("creation restart introduced another scaling activity")
			}
			return complete, nil
		})
		restartVerified = true
		action := "creation_restart_recovered"
		if lostIncrease {
			proof, e := gate.Receipt()
			if e != nil || proof.Forwarded != 1 || proof.ResponseDelivered || proof.Held {
				t.Fatal("response-loss gate proof changed", e)
			}
			action = "increase_response_loss_recovered"
		}
		transitions = append(transitions, map[string]any{"action": action, "instance_id": original, "accepted_activity_id": creationBefore.ActivityID, "accepted_activity_created_exactly_one": true, "scaling_activity_ids_unchanged": true, "ssh_after_restart": true, "desired_capacity": 1, "total_capacity": 1, "observed_at": time.Now().UTC()})
	}
	if mode == "cloud-restart" {
		original := ids[0]
		activityIDs := func() ([]string, error) {
			history, e := acceptance.History(ctx, cloud)
			if e != nil {
				return nil, e
			}
			var activities []string
			for _, activity := range history {
				activities = append(activities, activity.ScalingActivityId)
			}
			slices.Sort(activities)
			return activities, nil
		}
		beforeActivities, e := activityIDs()
		if e != nil {
			t.Fatal(e)
		}
		before, e := cloud.Group(ctx)
		if e != nil {
			t.Fatal(e)
		}
		journal, e := aliyun.ReadJournal(before)
		if e != nil {
			t.Fatal(e)
		}
		journalKind := ""
		if journal != nil {
			journalKind = journal.Kind
		}
		oldPID, e := restartPlugin(nil)
		if e != nil {
			t.Fatal(e)
		}
		wait(func() (bool, error) {
			var running []string
			if e := worker.Update(ctx, func(id string, state upstream.State) {
				if state == upstream.StateRunning {
					running = append(running, id)
				}
			}); e != nil {
				return false, e
			}
			if len(running) > 1 || (len(running) == 1 && running[0] != original) {
				return false, errors.New("restart discovered a different or additional worker")
			}
			return len(running) == 1, nil
		})
		runSSH(original)
		after, e := cloud.Group(ctx)
		if e != nil || before.DesiredCapacity != 1 || before.TotalCapacity != 1 || after.DesiredCapacity != 1 || after.TotalCapacity != 1 || after.PendingCapacity != 0 || after.RemovingCapacity != 0 {
			t.Fatal("restart changed one-worker capacity", e)
		}
		afterActivities, e := activityIDs()
		if e != nil || !slices.Equal(beforeActivities, afterActivities) || len(ledger.Instances) != 1 || ledger.Instances[0].InstanceID != original {
			t.Fatal("restart changed scaling activities or cumulative worker identity", e)
		}
		restartVerified = true
		transitions = append(transitions, map[string]any{"action": "plugin_restart", "instance_id": original, "old_plugin_pid": oldPID, "new_plugin_pid": pluginPID, "old_process_exit_confirmed": true, "crash_without_provider_shutdown": true, "same_instance_rediscovered": true, "ssh_after_restart": true, "scaling_activity_ids_unchanged": true, "scaling_activity_count": len(afterActivities), "journal_kind_before_restart": journalKind, "desired_capacity": 1, "total_capacity": 1, "observed_at": time.Now().UTC()})
	}
	if mode == "cloud-scale" {
		removed, retained := ids[0], ids[1]
		if accepted, e := worker.Decrease(ctx, []string{removed}); e != nil || !slices.Equal(accepted, []string{removed}) {
			t.Fatal("plugin did not accept exactly the selected worker", e)
		}
		if e := acceptance.WaitForSpecifiedRelease(ctx, cloud, ledger, removed, retained, data.InstanceType, data.ImageId, data.ZoneId); e != nil {
			t.Fatal(e)
		}
		runSSH(retained)
		retainedVerified = true
		transitions = append(transitions, map[string]any{"action": "plugin_specified_decrease", "removed_instance_id": removed, "retained_instance_id": retained, "desired_capacity": 1, "total_capacity": 1, "target_associated_resources_absent": true, "retained_ssh_verified": true, "observed_at": time.Now().UTC()})
		ids = []string{retained}
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
	var removalBefore, removalAfter acceptance.RemovingSnapshot
	var beforeRemovalActivities []string
	if removingRestart {
		wait(func() (bool, error) {
			history, e := acceptance.History(ctx, cloud)
			if e != nil {
				return false, e
			}
			beforeRemovalActivities = nil
			for _, a := range history {
				if a.StatusCode != "Successful" {
					return false, nil
				}
				beforeRemovalActivities = append(beforeRemovalActivities, a.ScalingActivityId)
			}
			return true, nil
		})
	}
	if accepted, e := worker.Decrease(ctx, ids); e != nil || len(accepted) != len(ids) || (removingRestart && !slices.Equal(accepted, ids)) {
		t.Fatal(accepted, e)
	}
	if removingRestart {
		acceptedAt := time.Now().UTC()
		var e error
		removalBefore, e = acceptance.CaptureRemoving(ctx, cloud, ledger, ids[0], beforeRemovalActivities)
		if e != nil {
			t.Fatal("removal restart window was not captured", e)
		}
		cutpointPath := filepath.Join(dir, "removal-cutpoint.json")
		if e = acceptance.WriteJSON(cutpointPath, map[string]any{"accepted_at": acceptedAt, "before": removalBefore}); e != nil {
			t.Fatal(e)
		}
		oldPID, e := restartPlugin(func() error {
			var err error
			removalAfter, err = acceptance.CaptureRemoving(ctx, cloud, ledger, ids[0], beforeRemovalActivities)
			if err != nil {
				return fmt.Errorf("removal window ended before old process exit proof: %w", err)
			}
			if removalAfter.ActivityID != removalBefore.ActivityID || !slices.Equal(removalAfter.ActivityIDs, removalBefore.ActivityIDs) || removalAfter.InstanceID != removalBefore.InstanceID {
				return errors.New("removal identity or activity changed at restart")
			}
			return acceptance.WriteJSON(cutpointPath, map[string]any{"accepted_at": acceptedAt, "before": removalBefore, "after_old_exit": removalAfter, "old_process_exit_confirmed": true})
		})
		if e != nil {
			t.Fatal(e)
		}
		g, e := cloud.Group(ctx)
		if e != nil {
			t.Fatal(e)
		}
		j, e := aliyun.ReadJournal(g)
		if e != nil || j == nil || j.Kind != "remove" || j.Activity != removalBefore.ActivityID || j.Before != 1 || j.Target != 0 || j.AcceptedMask != 1 || !slices.Equal(j.IDs, ids) {
			t.Fatal("new process did not retain the accepted removal journal", e)
		}
		transitions = append(transitions, map[string]any{"action": "plugin_restart_during_removal", "accepted_at": acceptedAt, "before": removalBefore, "after_old_exit": removalAfter, "old_plugin_pid": oldPID, "new_plugin_pid": pluginPID, "old_process_exit_confirmed": true, "crash_without_provider_shutdown": true, "target_present_before_and_after_old_exit": true, "accepted_journal_after_init": true, "observed_at": time.Now().UTC()})
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
	if pluginMode {
		if err = acceptance.WaitForNormalRelease(ctx, cloud, ledger, data.InstanceType, data.ImageId, data.ZoneId); err != nil {
			t.Fatal(err)
		}
		transitions = append(transitions, map[string]any{"action": "plugin_decrease", "desired_capacity": 0, "total_capacity": 0, "associated_resources_absent": true, "observed_at": time.Now().UTC()})
	}
	if removingRestart {
		history, e := acceptance.History(ctx, cloud)
		if e != nil {
			t.Fatal(e)
		}
		var activityIDs []string
		acceptedFinished := false
		for _, a := range history {
			activityIDs = append(activityIDs, a.ScalingActivityId)
			if a.ScalingActivityId == removalBefore.ActivityID {
				acceptedFinished = a.StatusCode == "Successful" && a.CreatedCapacity == 0 && len(a.CreatedInstances.CreatedInstance) == 0
			}
		}
		slices.Sort(activityIDs)
		g, e := cloud.Group(ctx)
		if e != nil {
			t.Fatal(e)
		}
		j, e := aliyun.ReadJournal(g)
		if e != nil || j != nil || !acceptedFinished || !slices.Equal(activityIDs, removalAfter.ActivityIDs) || len(ledger.Instances) != 1 || ledger.Instances[0].InstanceID != ids[0] {
			t.Fatal("removal recovery changed activities, left a journal or changed worker identity", e)
		}
		restartVerified = true
		transitions = append(transitions, map[string]any{"action": "removal_restart_recovered", "instance_id": ids[0], "accepted_activity_id": removalBefore.ActivityID, "accepted_activity_successful": true, "scaling_activity_ids_unchanged": true, "journal_cleared": true, "desired_capacity": 0, "total_capacity": 0, "observed_at": time.Now().UTC()})
	}
	normalReleased = true
	fmt.Printf("extended %s normal release observed; final cleanup is mandatory\n", mode)
}
