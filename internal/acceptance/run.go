package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

const RunnerVersion = "19.5.1"
const FleetingVersion = "v0.0.0-20260915191451-6ed75a33d303"

type Result struct {
	Ledger
	Result           string        `json:"result"`
	Mode             string        `json:"mode"`
	PluginCommit     string        `json:"plugin_commit"`
	PluginSHA256     string        `json:"plugin_sha256"`
	RunnerSHA256     string        `json:"runner_sha256"`
	RunnerVersion    string        `json:"runner_version"`
	FleetingVersion  string        `json:"fleeting_version"`
	Nonce            string        `json:"run_nonce"`
	FixtureSHA       string        `json:"gitlab_fixture_sha"`
	ProjectID        string        `json:"gitlab_project_id"`
	PipelineID       int64         `json:"gitlab_pipeline_id"`
	JobID            int64         `json:"gitlab_job_id"`
	RunnerID         int64         `json:"gitlab_runner_id"`
	Executor         string        `json:"executor"`
	PluginReference  string        `json:"plugin_reference"`
	PluginVersion    string        `json:"plugin_version,omitempty"`
	JobImage         string        `json:"job_image,omitempty"`
	NormalRelease    bool          `json:"normal_release"`
	Missing          []string      `json:"missing_configuration,omitempty"`
	Errors           []string      `json:"errors,omitempty"`
	JobScenario      string        `json:"job_scenario"`
	Jobs             []JobEvidence `json:"jobs"`
	CapacityVerified bool          `json:"capacity_verified"`
	FinishedAt       time.Time     `json:"finished_at"`
}

func Missing(get func(string) string) []string {
	var missing []string
	for _, k := range []string{"RUN_LIVE_ACCEPTANCE", "ALIYUN_REGION", "ALIYUN_SCALING_GROUP_ID", "ACCEPTANCE_PROJECT", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITLAB_PROJECT_ID", "GITLAB_FIXTURE_REF", "GITLAB_FIXTURE_SHA", "GITLAB_RUNNER_TAG", "GITLAB_EXPECTED_RUNNER_ID", "GITLAB_RUNNER_TOKEN", "GITLAB_API_TOKEN", "WORKER_SSH_KEY", "WORKER_USER", "PLUGIN_BINARY", "PLUGIN_COMMIT", "RUNNER_BINARY", "ALLOWED_INSTANCE_TYPE", "ALLOWED_IMAGE_ID", "ALLOWED_ZONE_ID", "MANAGER_CIDR"} {
		if get(k) == "" {
			missing = append(missing, k)
		}
	}
	return missing
}
func Run(ctx context.Context, dir string) (result Result) {
	duration, budget, limit := 20*time.Minute, 1, 1
	if scenario := os.Getenv("ACCEPTANCE_JOB_SCENARIO"); scenario != "" && scenario != "smoke" {
		duration, budget = 40*time.Minute, 2
	}
	if os.Getenv("ACCEPTANCE_JOB_SCENARIO") == "capacity" {
		budget, limit = 3, 2
	}
	ctx, stageCancel := context.WithTimeout(ctx, duration)
	defer stageCancel()
	absoluteDir, absErr := filepath.Abs(dir)
	if absErr != nil {
		return Result{Result: "FAIL_INFRASTRUCTURE", Errors: []string{"invalid output directory"}}
	}
	dir = absoluteDir
	get := os.Getenv
	runDir, dirErr := EvidenceDirectory(dir, get("GITHUB_RUN_ID"), get("GITHUB_RUN_ATTEMPT"))
	if dirErr != nil {
		return Result{Ledger: Ledger{SchemaVersion: 1}, Result: "FAIL_INFRASTRUCTURE", Errors: []string{dirErr.Error()}}
	}
	dir = runDir
	now := time.Now().UTC()
	result = Result{Ledger: Ledger{SchemaVersion: 1, Project: get("ACCEPTANCE_PROJECT"), Region: get("ALIYUN_REGION"), GroupID: get("ALIYUN_SCALING_GROUP_ID"), RunID: get("GITHUB_RUN_ID"), Attempt: get("GITHUB_RUN_ATTEMPT"), StartedAt: now, ExpiresAt: now.Add(duration + 10*time.Minute), CleanupResult: "NOT_RUN", Instances: []Resource{}, Leftovers: []string{}}, Mode: "smoke", Result: "NOT_RUN_MISSING_CONFIGURATION", RunnerVersion: RunnerVersion, FleetingVersion: FleetingVersion, PluginCommit: get("PLUGIN_COMMIT"), ProjectID: get("GITLAB_PROJECT_ID"), FixtureSHA: get("GITLAB_FIXTURE_SHA")}
	result.InstanceLimit = limit
	result.CreationBudget = budget
	result.DeadlineAt = now.Add(duration)
	result.Ledger.Path = filepath.Join(dir, "ledger.json")
	writeFinal := true
	defer func() {
		if !writeFinal {
			return
		}
		result.FinishedAt = time.Now().UTC()
		if err := WriteJSON(filepath.Join(dir, "result.json"), result); err != nil {
			result.Result = "FAIL_INFRASTRUCTURE"
			result.Errors = append(result.Errors, "cannot persist final evidence")
		}
	}()
	if _, e := os.Stat(result.Ledger.Path); e == nil {
		writeFinal = false
		result.Result = "FAIL_INFRASTRUCTURE"
		result.Errors = []string{"existing run ledger is preserved; recover it or use a new run attempt"}
		return result
	} else if !errors.Is(e, os.ErrNotExist) {
		result.Result = "FAIL_INFRASTRUCTURE"
		result.Errors = []string{"cannot inspect existing run evidence"}
		return result
	}
	result.Missing = Missing(get)
	if len(result.Missing) > 0 {
		return result
	}
	if get("RUN_LIVE_ACCEPTANCE") != "1" {
		result.Errors = []string{"RUN_LIVE_ACCEPTANCE must equal 1"}
		return result
	}
	result.Result = "FAIL_INFRASTRUCTURE"
	fail := func(err error) { result.Errors = append(result.Errors, err.Error()) }
	options, err := readSmokeOptions(get)
	if err != nil {
		fail(err)
		return result
	}
	result.Executor, result.PluginReference = options.executor, options.reference
	result.PluginVersion, result.JobImage = options.version, options.image
	result.ManagerMode, result.JobScenario = options.manager, options.scenario
	if options.manager == "systemd" {
		result.DriverPID = os.Getpid()
	}
	gitlabURL := get("GITLAB_URL")
	if gitlabURL == "" {
		gitlabURL = "https://gitlab.com"
	}
	u, err := url.Parse(gitlabURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		fail(errors.New("GITLAB_URL must be HTTPS"))
		return result
	}
	runnerID, err := strconv.ParseInt(get("GITLAB_EXPECTED_RUNNER_ID"), 10, 64)
	if err != nil || runnerID <= 0 {
		fail(errors.New("invalid expected runner ID"))
		return result
	}
	pluginPath, err := filepath.Abs(get("PLUGIN_BINARY"))
	if err != nil {
		fail(err)
		return result
	}
	binary, err := os.ReadFile(pluginPath)
	if err != nil {
		fail(errors.New("plugin binary is unreadable"))
		return result
	}
	digest := sha256.Sum256(binary)
	result.PluginSHA256 = hex.EncodeToString(digest[:])
	pluginVersion, e := exec.CommandContext(ctx, pluginPath, "version").Output()
	if e != nil {
		fail(errors.New("plugin version command failed"))
		return result
	}
	if err = verifyPluginVersion(pluginVersion, options.version, result.PluginCommit); err != nil {
		fail(err)
		return result
	}
	runnerPath, err := filepath.Abs(get("RUNNER_BINARY"))
	if err != nil {
		fail(err)
		return result
	}
	if options.manager == "systemd" && runnerPath != "/usr/bin/gitlab-runner" {
		fail(errors.New("systemd acceptance must use the installed Runner package"))
		return result
	}
	result.RunnerSHA256, err = VerifyRunnerBinary(runnerPath, "upstream.lock.json")
	if err != nil {
		fail(err)
		return result
	}
	version, err := exec.CommandContext(ctx, runnerPath, "--version").Output()
	if err != nil || !strings.Contains(string(version), "Version:      "+RunnerVersion) {
		fail(errors.New("Runner binary does not match pinned version"))
		return result
	}
	result.Nonce, err = jobNonce()
	if err != nil {
		fail(err)
		return result
	}
	sdk, err := aliyun.NewSDK(result.Region)
	if err != nil {
		fail(err)
		return result
	}
	defer sdk.Close()
	cloud := &aliyun.Client{API: sdk, Region: result.Region, GroupID: result.GroupID}
	group, err := cloud.Group(ctx)
	if err != nil {
		fail(err)
		return result
	}
	if group.Tag(ProjectTag) != result.Project || group.Tag(RunTag) != "" {
		fail(errors.New("pool is owned by another run or project; previous evidence is preserved"))
		return result
	}
	if err = cloud.Validate(ctx, group); err != nil {
		fail(err)
		return result
	}
	tmpl, err := cloud.Template(ctx, group)
	if err != nil {
		fail(err)
		return result
	}
	if group.MaxSize != limit {
		fail(errors.New("job fixture MaxSize differs from the scenario limit"))
		return result
	}
	smokeGroup := group
	smokeGroup.MaxSize = 1
	if err = aliyun.ValidateSpotSmoke(smokeGroup, tmpl); err != nil {
		fail(err)
		return result
	}
	d := tmpl.LaunchTemplateData
	result.TemplateID = group.LaunchTemplateId
	result.TemplateVersion = group.LaunchTemplateVersion
	result.ImageID = d.ImageId
	result.SpotStrategy = d.SpotStrategy
	result.SpotPriceLimit = d.SpotPriceLimit
	result.PublicBandwidthMbps = d.InternetMaxBandwidthOut
	result.SystemDiskSizeGiB = d.SystemDiskSize
	result.SystemDiskCategory = d.SystemDiskCategory
	if d.InstanceType != get("ALLOWED_INSTANCE_TYPE") || d.ImageId != get("ALLOWED_IMAGE_ID") || d.ZoneId != get("ALLOWED_ZONE_ID") {
		fail(errors.New("fixture violates exact type, image or zone allowlist"))
		return result
	}
	gl := gitlab{base: gitlabURL, token: get("GITLAB_API_TOKEN"), project: result.ProjectID, http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}
	systemID := "r_" + result.Nonce[:12]
	if err = VerifyGitLabFixture(ctx, gl, runnerID, get("GITLAB_RUNNER_TAG"), get("GITLAB_RUNNER_TOKEN"), systemID); err != nil {
		fail(err)
		return result
	}
	keyBytes, keyErr := os.ReadFile(get("WORKER_SSH_KEY"))
	if keyErr != nil {
		fail(errors.New("worker SSH key is unreadable"))
		return result
	}
	if err = VerifyKeyPair(ctx, cloud, d.KeyPairName, keyBytes); err != nil {
		fail(err)
		return result
	}
	var commit struct{ ID string }
	if err = gl.request(ctx, "GET", "/repository/commits/"+url.PathEscape(get("GITLAB_FIXTURE_REF")), nil, &commit); err != nil {
		fail(err)
		return result
	}
	if commit.ID != result.FixtureSHA {
		fail(errors.New("fixture ref SHA differs from pinned input"))
		return result
	}
	// Recoverable identifiers are persisted before acquiring ownership or capacity.
	if err = WriteJSON(filepath.Join(dir, "ledger.json"), result.Ledger); err != nil {
		fail(err)
		return result
	}
	if err = Claim(ctx, cloud, &result.Ledger); err != nil {
		fail(err)
		return result
	}
	if err = EnsureIngress(ctx, cloud, &result.Ledger, get("MANAGER_CIDR")); err != nil {
		fail(err)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		if e := Cleanup(cleanupCtx, cloud, &result.Ledger, false); e != nil {
			result.Result = "CLEANUP_FAILED"
			fail(e)
		}
		return result
	}
	var manager *exec.Cmd
	var managerDone chan error
	var service *packagedManager
	stopManager := func() error {
		if service != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			forced, err := service.stop(stopCtx)
			if err != nil {
				return err
			}
			service = nil
			if forced {
				result.Result = "FAIL_PRODUCT"
				fail(errors.New("packaged Manager did not complete a healthy graceful shutdown"))
			}
		}
		if manager != nil && manager.Process != nil {
			_ = manager.Process.Signal(syscall.SIGQUIT)
			select {
			case <-managerDone:
			case <-time.After(20 * time.Second):
				_ = manager.Process.Kill()
				<-managerDone
			}
			manager = nil
		}
		return nil
	}
	defer func() {
		if err := stopManager(); err != nil {
			result.Result = "CLEANUP_FAILED"
			fail(err)
			return // Never delete workers while the service may still write capacity.
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		if result.Result != "PASS" {
			for _, e := range result.Jobs {
				if e.PipelineID > 0 {
					_ = gl.request(cleanupCtx, "POST", fmt.Sprintf("/pipelines/%d/cancel", e.PipelineID), nil, nil)
				}
			}
		}
		if err := Cleanup(cleanupCtx, cloud, &result.Ledger, false); err != nil {
			result.Result = "CLEANUP_FAILED"
			fail(err)
		}
		if result.Result == "PASS" {
			if e := verifyJobSequence(&result); e != nil {
				result.Result = "FAIL_PRODUCT"
				fail(e)
			}
		}
		if e := WriteJSON(filepath.Join(dir, "ledger.json"), result.Ledger); e != nil {
			result.Result = "FAIL_INFRASTRUCTURE"
			fail(errors.New("cannot persist resource evidence"))
		}
	}()
	privateDir, err := os.MkdirTemp(dir, "manager-")
	if err != nil {
		fail(err)
		return result
	}
	defer func() {
		if stopManager() == nil {
			_ = os.RemoveAll(privateDir)
		}
	}()
	if err = os.WriteFile(filepath.Join(privateDir, "fleeting-plugin-aliyun"), binary, 0700); err != nil {
		fail(err)
		return result
	}
	key, err := os.ReadFile(get("WORKER_SSH_KEY"))
	if err != nil {
		fail(errors.New("worker SSH key unreadable"))
		return result
	}
	keyPath := filepath.Join(privateDir, "worker-key")
	if err = os.WriteFile(keyPath, key, 0600); err != nil {
		fail(err)
		return result
	}
	if err = os.WriteFile(filepath.Join(privateDir, ".runner_system_id"), []byte(systemID), 0600); err != nil {
		fail(errors.New("cannot persist Manager system identity"))
		return result
	}
	if options.manager == "systemd" {
		example, e := os.ReadFile(filepath.Join("/usr/share/doc/fleeting-plugin-aliyun/examples", options.executor, "config.toml"))
		if e != nil {
			fail(errors.New("installed package example is missing"))
			return result
		}
		config, e := packagedRunnerConfig(example, gitlabURL, get("GITLAB_RUNNER_TOKEN"), result.Region, result.GroupID, get("WORKER_USER"), options)
		if e != nil {
			fail(e)
			return result
		}
		service, systemID, err = preparePackagedManager(ctx, privateDir, &result.Ledger, binary, keyPath, config)
		if err != nil {
			fail(err)
			return result
		}
		if err = VerifyGitLabFixture(ctx, gl, runnerID, get("GITLAB_RUNNER_TAG"), get("GITLAB_RUNNER_TOKEN"), systemID); err != nil {
			fail(err)
			return result
		}
		result.ManagerPID, err = service.start(ctx)
		if err != nil {
			fail(err)
			return result
		}
	} else {
		cfg := RunnerConfig(gitlabURL, get("GITLAB_RUNNER_TOKEN"), result.Region, result.GroupID, get("WORKER_USER"), keyPath, options)
		configPath := filepath.Join(privateDir, "config.toml")
		if err = os.WriteFile(configPath, []byte(cfg), 0600); err != nil {
			fail(err)
			return result
		}
		if options.reference != "fleeting-plugin-aliyun" {
			install := exec.CommandContext(ctx, runnerPath, "fleeting", "--config", configPath, "install")
			if err = install.Run(); err != nil {
				fail(errors.New("Runner OCI installation failed"))
				return result
			}
			if err = verifyInstalledPlugin(options.reference, binary); err != nil {
				fail(err)
				return result
			}
		}
		managerLog, err := os.OpenFile(filepath.Join(dir, "manager.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			fail(errors.New("cannot create private Manager log"))
			return result
		}
		defer func() { stopManager(); _ = managerLog.Close() }()
		manager = exec.Command(runnerPath, "run", "--config", configPath, "--working-directory", privateDir)
		manager.Env = append(os.Environ(), "PATH="+privateDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		manager.Stdout = managerLog
		manager.Stderr = managerLog
		managerDone = make(chan error, 1)
		if err = manager.Start(); err != nil {
			fail(errors.New("Runner Manager failed to start"))
			return result
		}
		managerProcess := manager
		go func() { managerDone <- managerProcess.Wait() }()
		result.ManagerPID = manager.Process.Pid
	}
	result.ManagerDirectory = filepath.Base(privateDir)
	if err = result.Ledger.Save(); err != nil {
		fail(errors.New("cannot persist Manager identity"))
		return result
	}
	checkManager := func(ctx context.Context) error {
		if service != nil {
			pid, err := service.running(ctx)
			if err != nil || pid != result.ManagerPID {
				return errors.New("packaged Manager exited or restarted during job acceptance")
			}
		}
		select {
		case <-managerDone:
			manager = nil
			return errors.New("Runner Manager exited unexpectedly")
		default:
			return nil
		}
	}
	sequence := jobSequence{
		gl: gl, result: &result, runnerID: runnerID, ref: get("GITLAB_FIXTURE_REF"), tag: get("GITLAB_RUNNER_TAG"),
		checkManager: checkManager, pollInterval: 3 * time.Second,
		observe: func(ctx context.Context) error {
			if err := ObserveBudget(ctx, cloud, &result.Ledger, budget, get("ALLOWED_INSTANCE_TYPE"), get("ALLOWED_IMAGE_ID"), get("ALLOWED_ZONE_ID")); err != nil {
				return err
			}
			if options.scenario == "capacity" {
				if err := checkJobCapacity(ctx, cloud); err != nil {
					return err
				}
			}
			return result.Ledger.Save()
		},
		release: func(ctx context.Context) error {
			return WaitForNormalRelease(ctx, cloud, &result.Ledger, get("ALLOWED_INSTANCE_TYPE"), get("ALLOWED_IMAGE_ID"), get("ALLOWED_ZONE_ID"))
		},
	}
	if options.scenario == "capacity" {
		err = (capacitySequence{jobSequence: sequence,
			openJob: func(ctx context.Context, e JobEvidence) error {
				return openCapacityJob(ctx, cloud, e, options.executor, get("WORKER_USER"), key)
			},
			workerGone: func(ctx context.Context, id string) (bool, error) {
				return normalWorkerGone(ctx, cloud, &result.Ledger, id)
			},
		}).run(ctx)
	} else {
		err = sequence.run(ctx)
	}
	if err != nil {
		result.Result = liveFailure(err)
		fail(err)
		return result
	}
	result.Result = "PASS"
	result.NormalRelease = true
	return result
}
func RunnerConfig(url, token, region, group, user, key string, options smokeOptions) string {
	q := strconv.Quote
	config := fmt.Sprintf(`concurrent = 1
[[runners]]
 name = "aliyun-fleeting-smoke"
 url = %s
 token = %s
 executor = %s
 shell = "sh"
 [runners.feature_flags]
 FF_POSIXLY_CORRECT_ESCAPES = true
 [runners.autoscaler]
 plugin = %s
 capacity_per_instance = 1
 max_use_count = 1
 max_instances = 1
 instance_acquire_timeout = "10m"
 instance_ready_command = "timeout 300 sh -c 'until test -f /var/lib/fleeting-worker/ready; do sleep 2; done; git --version && gitlab-runner --version'"
 delete_instances_on_shutdown = true
 [runners.autoscaler.plugin_config]
 region = %s
 scaling_group_id = %s
 [runners.autoscaler.connector_config]
 protocol = "ssh"
 username = %s
 key_path = %s
 use_static_credentials = true
 use_external_addr = true
 [[runners.autoscaler.policy]]
 idle_count = 0
 idle_time = "1m"
 preemptive_mode = false
`, q(url), q(token), q(options.executor), q(options.reference), q(region), q(group), q(user), q(key))
	if options.executor == "docker-autoscaler" {
		config = strings.Replace(config, "gitlab-runner --version'", "gitlab-runner --version && docker info'", 1)
		config += "\n [runners.docker]\n image = " + q(options.image) + "\n privileged = false\n"
	}
	return config
}

func liveFailure(err error) string {
	var cloudError *aliyun.Error
	if errors.As(err, &cloudError) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "FAIL_INFRASTRUCTURE"
	}
	return "FAIL_PRODUCT"
}
