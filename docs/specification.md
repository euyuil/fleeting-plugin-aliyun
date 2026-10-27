# Alibaba Cloud Fleeting Plugin: Implementation Specification and Acceptance Criteria

- Project name: `fleeting-plugin-aliyun`
- Document version: 1.0
- Reference review date in the original specification: 2026-10-26 (UTC)
- Purpose: implementation goals, review criteria, and an acceptance checklist for the implementing coding agent.
- Status: design specification. This document does not indicate that the plugin has been implemented or that live cloud acceptance tests have run.
- Keywords: GitLab Runner, Fleeting, Alibaba Cloud ECS / ESS, Spot, GitHub Actions, free-first testing.

## 0. Overall Implementation Task

Implement a maintainable, releasable **GitLab Runner Fleeting provider plugin** that lets GitLab Runner manage ECS workers through Alibaba Cloud Auto Scaling (ESS), including preemptible Spot instances. Implement equivalent capabilities from the official AWS, Google Cloud, and Azure Fleeting plugins wherever they are meaningful and feasible on Alibaba Cloud.

The default is the **Instance executor, with CI jobs running directly on ECS VMs**. Workers do not need Docker. Do not use ACK or Kubernetes. Keep a compatible path for the same plugin to serve the Docker Autoscaler executor so users can choose their execution model.

The primary repository, development, regular CI, builds, and releases are on GitHub. Regular tests use standard GitHub-hosted runners without accessing live Alibaba Cloud or GitLab services. Only explicitly enabled live acceptance tests reuse a GitLab.com Free test project or an existing GitLab CE project. A GitHub Actions VM temporarily runs the Runner Manager and the plugin built for that run, creates one short-lived Spot worker, completes a tiny GitLab job, and verifies resource release.

Deliver source code, tests, workflows, configuration examples, an infrastructure preparation procedure, cleanup tools, a feature parity matrix, and reproducible documentation. A design alone, a mock plugin, a compilable skeleton, or interfaces that only return success are not sufficient.

**If a cloud account, GitLab token, or publishing permission is unavailable, complete all implementation and offline acceptance work that does not depend on it, and accurately record validation that has not run. Skipped live tests must not be reported as live passes. Missing credentials must not stop all other work.**

### 0.1 Normative Terms

- **MUST**: a delivery requirement.
- **SHOULD**: the default approach; deviations require a specific, recorded reason.
- **MAY**: an optional extension.
- **Offline tests**: tests that do not call live Alibaba Cloud or GitLab services or use their credentials. Downloading Go modules, build tools, and public dependencies is allowed; the entire task need not be physically disconnected from the network.
- **Live acceptance / live tests**: tests that actually use Alibaba Cloud or GitLab services. They must be explicitly enabled.
- **Official parity**: an item-by-item comparison with specific upstream versions. It does not mean copying cloud APIs or claiming GitLab certification.

## 1. Fixed Project Constraints

| ID | Constraint | Acceptance method |
|---|---|---|
| C01 | Default workers use Linux, SSH, and the Instance executor; jobs run directly on the VM. | Examples and the basic live smoke test do not depend on Docker. |
| C02 | No dependency on ACK, Kubernetes, or Docker Machine. | Inspect architecture, dependencies, and deployment examples. |
| C03 | GitHub is the primary repository; regular development CI runs in GitHub Actions. | Inspect PR workflows. |
| C04 | Regular PRs and default `go test ./...` / `make test` do not call live cloud or GitLab services. | Test without credentials and assert fake-client behavior. |
| C05 | GitLab.com Free is sufficient for default acceptance; an existing GitLab CE instance is an alternative. | Provide both URL / token configuration paths without paid API features. |
| C06 | Acceptance does not create an additional persistent Runner Manager VM or start a full GitLab deployment for every run. | The Manager is a temporary process inside the GitHub job. |
| C07 | Default live smoke requests one Spot instance, returns capacity to zero, and verifies release. | Inspect cloud configuration, run evidence, and resource inventories. |
| C08 | Live tests do not run automatically on ordinary pushes or PRs, or start instances daily by default. | Inspect workflow triggers. |
| C09 | No silent fallback to on-demand instances or automatic expansion of instance sizes, regions, or limits. | Strict Spot preflight and verification after creation. |
| C10 | Cost protection and cleanup after abnormal termination are deliverables. | Test success, failure, cancellation, and credential-expiry scenarios. |
| C11 | Do not hide gaps with official plugins by reducing feature goals. | Maintain a capability matrix against pinned official versions. |
| C12 | Record implementation, offline validation, and live validation separately. | Machine-readable results agree with the final report. |

"Free-first" means placing most computation on platforms where free usage applies and minimizing the frequency and duration of live cloud use. **ECS Spot, disks, public network traffic, and other Alibaba Cloud resources are not promised to be free. The design must not depend on trial credits, temporary promotions, or paid plans.**

## 2. Architecture and Responsibility Boundaries

### 2.1 Where the Plugin Runs

This is a **Runner-side plugin**, not an extension installed on the GitLab CE server.

Production deployment responsibilities:

- The GitLab server manages pipelines and jobs.
- The Runner Manager acquires jobs and runs the official Instance / Docker Autoscaler executor and autoscaler.
- Fleeting loads this project's plugin.
- The plugin calls Alibaba Cloud APIs to manage a dedicated ESS ECS scaling group.
- ECS workers execute jobs; their creation settings come from images, launch templates, and associated infrastructure.

Official Fleeting uses a cloud instance-group abstraction. Its AWS, Google Cloud, and Azure plugins and the two executors are the primary references for this project. [S01]

### 2.2 Default Test Architecture

| Location | Contents / processes | Default frequency |
|---|---|---|
| Primary GitHub repository | Plugin code, tests, documentation, workflows, and fixture sources. | Continuous development. |
| Standard GitHub Actions Linux runner | Compilation and regular tests; also the Manager, plugin, and acceptance driver during live tests. | Every regular CI run; live tests are explicit. |
| Fixed GitLab.com Free / CE project | A tiny CI fixture, job status, and short-lived artifacts. | Live tests only. |
| Dedicated Alibaba Cloud ESS group | Worker creation settings and membership boundary; normally at zero capacity. | Reused. |
| Alibaba Cloud Spot ECS instance | One small acceptance job. | Live tests only, for a short duration. |

The GitLab test project needs only the fixture; it does not need to mirror the plugin repository. Build the plugin and collect test results on GitHub. Do not rebuild the entire plugin or rerun all Go unit tests on the Spot worker.

### 2.3 Plugin Responsibilities

The plugin must handle group validation, instance discovery, capacity changes, deletion of specified instances, state synchronization, connection information, health checks, the upstream protocol, and declared capabilities.

The plugin does not own:

- GitLab pipeline scheduling, job retry policy, or artifact transfer protocols.
- Routine creation and destruction of VPCs, vSwitches, security groups, images, or ESS groups.
- Registration of an individual GitLab runner for every worker.
- A custom SSH executor, container executor, or Runner communication protocol.
- Installation of the entire CI toolchain on every connection.

Separate infrastructure fixtures / bootstrap from the plugin. The initial implementation uses a **pre-existing dedicated ESS group**. Do not introduce a second, custom pool controller based on direct `RunInstances` calls at the same time.

## 3. Phase 0: Pin the Actual Upstream Baseline

Before coding, produce `docs/upstream-parity.md` and a machine-readable version lock file. Read the following directly from official repositories:

1. A current stable GitLab Runner release and an explicit minimum supported version.
2. The complete Fleeting module version / commit compatible with that Runner.
3. Pinned tags / commits for the official AWS, Google Cloud, and Azure plugins.
4. Versions of the Go toolchain, Alibaba Cloud SDK / credentials library, build tools, and OCI packaging tools.
5. Licenses for those versions and notices required for reused code.

Do not combine pkg.go.dev cache pages from different dates into an interface baseline that has not been compiled. Do not use floating `latest` as a lasting reproducible baseline. At the original reference review date, the official Google Cloud repository was named `googlecloud`; resolve its link from the main official documentation to avoid obsolete module paths. [S01][S02][S03]

At that review date, the available Fleeting provider API included these methods:

```text
Init
Update
Increase
Decrease
ConnectInfo
Heartbeat
Suspend
Resume
Shutdown
```

The pinned source defines actual signatures, states, errors, and capabilities. Include compile-time interface assertions and tests in which the corresponding Fleeting host loads the real plugin process. If the selected stable Runner lacks a newer capability, record the protocol difference and compatibility strategy instead of silently removing that capability from the goals. [S02]

## 4. Official Feature Parity Goals

### 4.1 Capability Matrix

This table defines project goals; it does not imply that the three official plugins behave identically in every detail.

| Capability | Project requirement | Completion and validation |
|---|---|---|
| Upstream provider / plugin protocol | Use official Go interfaces and bootstrap. | Compile-time assertions and real-process protocol tests. |
| Scale from zero; batch increases | Required. | Fake state sequences and single-instance live tests; batch live tests are separate. |
| Scale down specified IDs | Required. | Partial success, batching, retries, and unaffected remaining instances. |
| Pagination and discovery after restart | Required. | Do not depend on an instance list stored only in process memory. |
| Eventual consistency and asynchronous activities | Required. | Distinguish accepted, provisioning, and ready. |
| Spot instances | Required. | Strict Spot smoke and handling of interruption / disappearance. |
| On-demand and mixed configurations | Respect explicitly configured ESS policies. | Do not change strict Spot test defaults. |
| Private / public connections | Required. | Respect connector settings and refresh changed addresses. |
| Static SSH on Linux | Required; default path. | Local SSH protocol tests and Spot live acceptance. |
| Custom user, port, OS, and architecture | Required. | Explicit settings take precedence; reject unsupported combinations. |
| Instance executor | Required; default documentation path. | A real GitLab job executes directly on the VM. |
| Docker Autoscaler executor | Retain a compatible path. | Configuration and offline integration; live Docker tests as needed. |
| Windows worker / WinRM HTTPS | Full parity goal in a later milestone. | Implement the applicable static-credential connection path, offline tests, and documentation; live tests are separate. |
| Dynamic / short-lived SSH credentials | Investigate and implement where feasible. | ADR and tests for stable APIs, refresh, permissions, and timing windows. |
| Heartbeat / instance failure | Required. | Distinguish a failed instance from a temporary API failure. |
| Suspend / Resume | Evaluate and handle according to capability rules. | Declare support only after safety and usability are demonstrated; see Section 8. |
| Standard credential chain and temporary credentials | Required. | Use the official SDK and validate STS tokens and refresh boundaries. |
| Native binaries and OCI distribution | Required. | Verifiable binaries and installer-compatible OCI artifacts. |
| Configuration, versioning, and diagnostic logs | Required. | Stable configuration, traceable requests, and no sensitive information leaks. |

### 4.2 Completion Rules for Parity

Linux, static SSH, and a single Spot worker form the first usable milestone, not completion of the full goal.

Record the following for every capability:

- `implementation`: `not_started / implemented / deferred / unsupported`.
- `offline_validation`: `not_run / pass / fail`.
- `live_validation`: `not_run / pass / fail`.
- Applicable Runner / plugin versions, operating systems, and Alibaba Cloud configurations.
- Official references, Alibaba Cloud mappings, related tests, and remaining gaps.

`unsupported` requires a specific platform or protocol limitation with supporting evidence. Missing implementation, credentials, or test execution must be recorded as `deferred` or `not_run`.

Complete applicable capabilities or provide independently reviewable technical limitations. Saving test costs is not a reason to remove capabilities or their documentation. A full parity milestone remains incomplete while feasible capabilities are still unfinished.

## 5. ESS Resource Model and Prerequisites

### 5.1 Initially Supported Groups

The first stable implementation explicitly supports:

- ECS-type ESS groups using desired capacity and in the Active state required by the selected APIs. The plugin must not enable a group that a user has disabled.
- One Runner autoscaler / plugin as the sole capacity writer for each group.
- `MinSize=0` and a finite, explicit `MaxSize`.
- Instance weights of 1, so Fleeting instance counts match group capacity.
- A usable instance creation template with a pinned template version.
- Members created automatically by ESS, or managed instances explicitly verified as releasable by ESS.
- No scheduled, metric-based, or manual scaling processes competing with the plugin for capacity control.
- No unsupported warm pools, instance protection, standby, automatic rebalancing, proactive Spot replacement, instance refresh, or lifecycle hooks enabled by default.

These are project compatibility boundaries. Add state and ownership tests when extending support. ESS health replacement and desired-capacity reconciliation still need handling; do not assume that the cloud will never replace an instance that disappears externally. [A01][A08]

For multiple pools, configure multiple Runner entries with separate ESS groups. The initial version does not implement a distributed lock service for multiple Managers writing to one group.

### 5.2 Ownership

- `region + scaling_group_id` defines the management boundary; do not infer targets from name prefixes.
- Fixtures use project ownership tags. Explicit configuration and bootstrap documentation identify production groups as dedicated groups.
- `Init` validates configuration and the origin of existing instances. Reject incompatible groups without silently changing their Min / Max settings, Spot policy, or instances.
- Live tests require an initially idle target group with desired capacity zero and no other acceptance run in progress.
- Foreign or unmanaged attached instances must not silently become disposable workers.

Runner state storage also affects how Fleeting / Taskscaler handles existing instances. Document startup and restart behavior for the pinned version. Do not promise that Manager restarts always preserve running jobs. Default tests use an empty dedicated group, not a production worker pool. [S05]

## 6. Cloud API Mapping and Correctness Requirements

### 6.1 API Boundary

Use the official Alibaba Cloud Go SDK. Wrap only the ECS / ESS calls used by the implementation in small interfaces suitable for fakes. Do not implement custom signing, copy the entire SDK, or build a general-purpose cloud simulator.

| Responsibility | Main candidate API | Notes |
|---|---|---|
| Read group and capacity configuration | `DescribeScalingGroups` | Exact group / region; validate ownership and capacity. |
| Read group members | `DescribeScalingInstances` | Full pagination, lifecycle, health, and creation type. |
| Read instances and addresses | `DescribeInstances` | Query group members and respect batch limits. |
| Set scale-out target | `ModifyScalingGroup.DesiredCapacity` or absolute-target mode of `ScaleWithAdjustment` | Resolve pending, desired, and retry semantics first. |
| Delete specified members | `RemoveInstances` | Explicit release and corresponding desired-capacity reduction. |
| Read scaling progress | `DescribeScalingActivities` | API acceptance does not prove activity success. |
| Optional suspend / resume | ESS standby combined with ECS stop / start, or another justified path | Requires separate analysis; stopping alone is not sufficient. |

The pinned SDK and official API metadata define fields, pagination, error codes, and permissions. [A02]-[A07]

### 6.2 Capacity Invariants

The implementation must ensure:

1. Capacity mutations for a group are serialized; reads and caches cannot overwrite accepted operations that are not yet observable.
2. Cloud request acceptance, instance appearance, and readiness to accept a job are modeled separately.
3. Success counts returned upstream match accepted logical requests rather than always returning the requested count.
4. Group and Runner limits are respected. At a limit, report the accepted subset or a clear error.
5. Failures, timeouts, and lost responses do not cause duplicate increases or repeated desired-capacity reductions.
6. Deletion affects only upstream-specified instances; ESS must not choose other instances running jobs.
7. Automatic cloud replacement does not cause duplicate compensation for the same capacity.
8. Restart recovery converges from cloud state and recoverable operation information, rather than relying on an unrecoverable in-memory map.

### 6.3 Scale-Out Algorithm: Required Design Decisions

Do not unconditionally implement `Increase(n)` as `QuantityChangeInCapacity, +n`. Alibaba Cloud desired-capacity documentation describes adjustments based partly on steady-state instance counts; consecutive pending operations may differ from adding to the previous desired value. [A01]

Deliver `docs/adr/0002-capacity-reconciliation.md` covering:

- The write API and its absolute-target / delta semantics.
- How cloud desired capacity, in-flight requests, stable members, and deleting instances are counted.
- Confirmation and retry after a lost API response.
- How later `Increase` calls accumulate with an unfinished earlier request.
- Prevention of lost updates when `Increase` and `Decrease` interleave.
- Recovery after restart, temporarily stale reads, external replacement, and activity failure.
- Fake time-sequence tests for every case.

Evaluate absolute desired targets first. Where an API uses `ClientToken`, reuse the token for network retries of the same logical operation and use a new token for a different logical request. Absolute assignments still require concurrency and stale-read handling.

Asynchronous `ScaleWithAdjustment` may initially change only desired capacity. A successful activity response does not prove ECS creation or SSH readiness. Account for API clamping at Min / Max in the returned result. [A04]

### 6.4 Scale-In and Deletion

Use `RemoveInstances` or a proven equivalent to delete upstream-specified IDs. The default path explicitly sets:

```text
RemovePolicy = release
DecreaseDesiredCapacity = true
```

Requirements:

- Verify membership and management boundaries before the call.
- Follow API batch limits; at the original reference review date, `RemoveInstances` allowed at most 20 IDs per batch.
- Respect scaling-activity concurrency limits rather than submitting every batch in parallel unconditionally.
- Handle valid subsets, `IgnoredInstances`, asynchronous failures, and IDs that no longer exist.
- Retrying an ID already processed successfully must not reduce desired capacity again.
- Do not substitute `DetachInstances` for resource release.
- Do not delete ECS instances while leaving ESS desired capacity to trigger replacement.
- Do not merely lower desired capacity and let ESS choose instances, disrupting running jobs.
- Verify both ESS membership and ECS entities to prevent removal from the group while the VM continues billing. [A05]

Before deletion completes, report an appropriate deleting state to Fleeting. Model and test API acceptance separately from final resource disappearance.

### 6.5 State Synchronization

`Update` must cover:

- Complete pagination and all ECS query batches.
- Creating, ready, deleting, deleted, and any suspend / resume states supported by the pinned protocol.
- Windows of disagreement between ESS lifecycle and actual ECS state.
- Spot interruptions, external deletions, health replacements, and partially failed creation.
- Instances without an IP and running instances whose SSH service is not ready.
- Unknown states and temporarily incomplete query results.

One API timeout, authentication failure, or interrupted pagination must not mean every group instance has disappeared. Do not map unknown states to Running by default. Use complete successful snapshots, last-known state, and the pinned Fleeting contract to decide when to emit deletion notifications.

### 6.6 Heartbeat, Errors, and Shutdown

- `Heartbeat` checks instance state according to the upstream contract and uses the appropriate health errors.
- Distinguish unavailable instances from temporary Alibaba Cloud API failures, expired tokens, and insufficient permissions.
- All requests respect `context`. Retries have exponential backoff, jitter, attempt limits, and an overall deadline.
- Retry only retryable errors; do not retry permanent configuration or permission failures indefinitely.
- Logs retain request IDs, activity IDs, instance IDs, and operation results without authentication material.
- `Shutdown` releases the plugin's own connections and background tasks. Explicit Runner / test cleanup policies control instance destruction; do not delete the entire group unconditionally whenever the plugin exits.

## 7. Connections, Worker Images, and Execution Models

### 7.1 Default Linux Path

Use static SSH by default:

- The launch template / image contains the public key; the Manager holds the corresponding private key.
- Support public and private addresses, usernames, custom ports, operating systems, and architectures.
- Respect explicit connector configuration; do not overwrite it unconditionally with inferred values.
- `ConnectInfo` returns a currently valid address and refreshes it after instance recovery or an IP change.
- Private keys, passwords, and cloud API credentials must not appear in job artifacts.

Workers need Git, a GitLab Runner binary compatible with the pinned Manager, and the fixture's tools. They do not need a Runner daemon or individual registration with GitLab. The Instance executor runs jobs directly. [S04]

ECS Running, ESS InService, and SSH / toolchain readiness are separate conditions. Provide bounded connection and readiness checks with failure reasons and cleanup paths, rather than unlimited sleeps. [A09]

### 7.2 Image Preparation

Provide a reproducible minimal Linux bootstrap / cloud-init example that:

- Pins the base image selection and tool versions and verifies download checksums.
- Creates a job user and working directory and configures the SSH public key.
- Installs Git, the Runner binary, and small acceptance dependencies.
- Writes `/var/lib/fleeting-worker/ready` only after installation, permissions, and local self-checks succeed, and retains redacted diagnostics. The base image must include the `timeout` utility used by the readiness example below.
- Does not embed Manager tokens, GitLab PATs, or cloud management credentials in the image.

A continuously billed private image-building pipeline is not required by default. Existing user images may be reused; document costs of custom images, snapshots, and build machines separately.

### 7.3 Docker Autoscaler

Deliver a separate example using the same provider, a dedicated Docker worker image, and the corresponding executor. Do not put Docker daemon or job-container management into the provider.

The basic Spot smoke uses the Instance executor. Run live Docker acceptance only for relevant changes or an explicit selection. OCI publication does not require Docker on workers. [S01][S06]

### 7.4 Windows

In the parity milestone, implement the static-credential WinRM HTTPS path applicable to Alibaba Cloud and document worker preparation, certificates, ports, and networking. Do not remove it from the capability matrix because the default Linux path does not need it.

- Report Manager OS support separately from worker OS support.
- Do not assume Alibaba Cloud has a direct equivalent of the AWS Windows password-decryption API.
- Explain licensing / image cost differences when Windows images are selected explicitly.
- Code, offline tests, and examples can be completed without a live Windows environment, but `live_validation` remains `not_run`.
- Cross-compilation is not a substitute for actual WinRM validation.

### 7.5 Dynamic SSH

Alibaba Cloud offers short-lived public-key registration through `ali-instance-cli send_public_key`. The referenced official documentation describes a login window of about 60 seconds and prerequisites such as Cloud Assistant / configuration plugins. [A10]

Investigate and record an ADR addressing:

- Whether a public, stable API is suitable for a Go provider.
- Whether an additional CLI is necessary; if so, why, how its version is pinned, and how its absence is reported.
- Minimum permissions, image prerequisites, connection-information expiry, and reacquisition.
- Timing between key delivery and the SSH handshake, concurrency, and failure recovery.
- Supported Linux distributions, network paths, and SSH users.

Prefer a native SDK path. Explain any justified, controlled external dependency. An official CLI's existence does not prove implementation of this capability. Do not substitute `AttachKeyPair`, which requires a reboot to take effect on a running VM, for immediate temporary-key delivery. [A11]

## 8. Suspend / Resume

Implement methods required by the pinned provider interface, but **implementing a method does not imply declaring its capability**.

- Upstream supports declaring suspend / resume through capabilities. Use the pinned version's standard unsupported error where applicable.
- Do not return `nil` to simulate success when unsupported.
- If supported, return accepted IDs according to asynchronous semantics and reflect state through `Update`. Handle repetition, partial success, recovery, and expired connection information. [S02]

Distinguish:

1. Ordinary stopping.
2. Economical stopping that releases compute resources while retaining disks.
3. Hibernation that preserves memory state.
4. Suspension of ESS scaling processes.

These are different features. Alibaba Cloud economical-mode documentation includes Spot, while memory hibernation documentation restricts instances in ESS groups. Do not broadly state that Spot cannot suspend or equate `SuspendProcesses` with VM suspension. [A12][A13][A16]

Deliver a separate ADR for combinations of `EnterStandby / ExitStandby` and ECS stop / start, covering:

- Desired capacity, group health checks, and unintended replacement.
- How suspended instances count toward Fleeting capacity and maximum instance limits.
- Retention or refresh of instance identity, disks, network addresses, and credentials.
- Reporting failures to resume because of unavailable capacity.
- Resources still billed while suspended.
- Races with Spot interruption, Manager restart, and deletion.

Keep `suspend_resume` disabled until the path is proven reliable for the target ESS / ECS configuration. An experimental implementation with offline tests may be provided disabled by default. Formal support requires corresponding live acceptance evidence. Distinguish platform limits from unfinished engineering. [A14][A15]

## 9. Configuration and Authentication

### 9.1 Plugin Configuration

Keep core configuration simple:

| Field | Requirement |
|---|---|
| `region` | Required; explicit region without opaque defaults. |
| `scaling_group_id` | Required; manage only this group. |
| SDK credential source | Use the official credentials abstraction; do not require plaintext AccessKeys in TOML. |
| Optional retry / timeout settings | Expose only when needed, with finite defaults. |
| Optional capability flags | Conservative defaults consistent with prerequisites and the support matrix. |

SSH / WinRM settings belong in Runner `connector_config`. Instance size, image, vSwitch, and Spot policy belong in infrastructure creation templates. Do not repackage the entire cloud API as provider configuration.

Validate missing fields, unsupported combinations, and obvious misspellings. Detect unknown fields where upstream decoding permits. If strict rejection is unavailable, provide template validation and documentation rather than claiming those fields were checked.

### 9.2 Minimal Runner Configuration Example

This target template uses a locally built plugin for a disposable Linux smoke test. Validate parsing and actual field behavior with the pinned Runner, and replace placeholders. **Do not assume Runner TOML expands these placeholders automatically.** `instance_acquire_timeout` was introduced in Runner 18.1; provide an appropriate template if supporting earlier releases. This does not establish the plugin's overall minimum supported Runner version.

```toml
concurrent = 1

[[runners]]
  name = "aliyun-fleeting-smoke"
  url = "https://gitlab.com"
  token = "REPLACE_WITH_PROJECT_RUNNER_AUTH_TOKEN"
  executor = "instance"

  [runners.autoscaler]
    plugin = "fleeting-plugin-aliyun"
    capacity_per_instance = 1
    max_use_count = 1
    max_instances = 1
    instance_acquire_timeout = "10m"
    instance_ready_command = "timeout 300 sh -c 'until test -f /var/lib/fleeting-worker/ready; do sleep 2; done; git --version && gitlab-runner --version'"
    delete_instances_on_shutdown = true

    [runners.autoscaler.plugin_config]
      region = "REPLACE_WITH_REGION"
      scaling_group_id = "REPLACE_WITH_DEDICATED_SCALING_GROUP_ID"

    [runners.autoscaler.connector_config]
      protocol = "ssh"
      username = "REPLACE_WITH_WORKER_USER"
      key_path = "/absolute/path/to/temporary/private-key"
      use_static_credentials = true
      use_external_addr = true

    [[runners.autoscaler.policy]]
      idle_count = 0
      idle_time = "1m"
      preemptive_mode = false
```

Constraints:

- Set `max_instances=1` in both Runner and cloud configuration. Do not rely on a default of zero, which may mean unlimited.
- `max_use_count=1` and single-job capacity make the worker disposable.
- `preemptive_mode` governs Runner job acquisition / capacity preparation; it is not a Spot setting.
- The readiness command waits for the bootstrap marker within a bound, then checks tools. The image must include `timeout`; successful bootstrap or image preparation must create the marker. Do not fail immediately because SSH is available before installation finishes. A timeout ends the run and triggers cleanup rather than unlimited rebuilding.
- `use_external_addr` neither creates a public address nor proves reachability.
- `delete_instances_on_shutdown` is an explicit policy for a dedicated test group, not a setting to apply mechanically to shared user groups.
- These settings do not replace independent cleanup. [S04][S05]

### 9.3 Cloud Credentials

Support standard credential sources actually provided by the official SDK, including correct handling of AccessKeys and the three-part temporary STS credential set. Document verified RAM role, OIDC / web identity, profile, and other methods. Do not invent unsupported profile formats or automatic refresh behavior.

Prefer the official Alibaba Cloud credentials Action with GitHub OIDC for live workflows rather than defaulting to long-lived AccessKeys. [G04][G05]

Requirements:

- Grant `id-token: write` only to live / cleanup jobs, not regular PR jobs.
- Pin the Action to a reviewed commit SHA.
- Specify `role-to-assume`, `oidc-provider-arn`, `audience`, and `role-session-expiration`.
- The Action's default session duration differs from the STS API default; set it according to the actual selected version.
- Cover the live deadline and cleanup margin while respecting the RAM role's maximum duration.
- Do not assume static environment credentials exported by the Action refresh automatically. Cleanup / janitor processes must obtain a fresh identity.
- Verify SDK compatibility with `ALIBABA_CLOUD_ACCESS_KEY_ID`, `ALIBABA_CLOUD_ACCESS_KEY_SECRET`, and `ALIBABA_CLOUD_SECURITY_TOKEN`.
- Configure OIDC trust against the repository's actual issuer, audience, and subject claims. GitHub subject formats can depend on repository creation time, IDs, and Environment context; an old template is not universal. [G06]

### 9.4 Permissions

Document provider, fixture bootstrap, and cleanup permissions separately. Do not give the Manager Administrator permissions by default.

Check permission names against actual API authorization metadata rather than mechanically generating `ess:APIName`. For example, at the original review date, the `RemoveInstances` authorization table used `ess:DetachInstances`; verify and record this with current metadata and a minimal live test. [A05]

Workers must not receive the Manager's cloud management permissions or GitLab PAT. Users configure any additional job-resource permissions separately with minimum necessary access.

## 10. Code Structure and Deliverables

Suggested structure follows. File names may change, but responsibilities and deliverables must remain.

| Path | Contents |
|---|---|
| `cmd/fleeting-plugin-aliyun/` | Official plugin bootstrap and build information. |
| `internal/provider/` | Provider lifecycle, capacity, and state logic. |
| `internal/aliyun/` | ECS / ESS SDK adapters and small interfaces. |
| `internal/testutil/` | Fake clients, clocks, and state-sequence helpers. |
| `tests/component/` | Real plugin process, Fleeting host, and local SSH. |
| `tests/live/` | Explicitly enabled live validation driver. |
| `tests/fixtures/gitlab/` | Small GitLab fixture sources and synchronization instructions. |
| `examples/instance/` | Default direct-on-VM configuration. |
| `examples/docker-autoscaler/` | Separate Docker example. |
| `examples/windows/` | WinRM path and support status. |
| `infra/` | Minimal Terraform or official CLI / bootstrap procedure. |
| `tools/cleanup/` | Idempotent cleanup and janitor. |
| `docs/adr/` | Decisions on capacity, state, dynamic credentials, suspend, and related topics. |
| `docs/upstream-parity.md` | Item-by-item comparison against pinned versions. |
| `docs/testing.md` | Free-first testing strategy, live usage, and result definitions. |
| `docs/operations.md` | Production preparation, credentials, logs, recovery, and cleanup. |
| `.github/workflows/` | Regular CI, manual live tests, releases, and janitor. |
| `README.md` | Installation, minimal configuration, support scope, and test status. |

Deliver a LICENSE, dependency-license documentation, version injection, and stable build entry points. Do not add persistent databases, message queues, general-purpose scheduling services, or a second cloud control plane for testing.

## 11. Regular Tests: Most Work Runs in Free GitHub CI

### 11.1 Default Commands

Provide stable entry points equivalent to:

```sh
make build
make test
make test-component
make lint
make dist
```

- `go test ./...` and `make test` do not access live cloud / GitLab services or read their credentials by default.
- Live tests use an explicit build tag, separate entry point, and workflow input. Detecting environment variables must not automatically enable instance creation.
- Regular CI runs in a clean environment without account configuration.
- Regular tests do not require another self-hosted machine, a commercial mock service, or a full local GitLab deployment.

### 11.2 Provider Behavior Tests

Place fakes at the ECS / ESS SDK boundary; test the real Alibaba Cloud provider. At minimum, cover:

| ID | Scenario | Required assertions |
|---|---|---|
| T01 | Initialize an empty group; scale 0 to N. | Correct configuration / identity and accepted count consistent with the contract. |
| T02 | Increase while a previous Increase is pending. | No lost capacity or excess increase. |
| T03 | Interleaved Increase / Decrease. | Old snapshots do not overwrite accepted intent. |
| T04 | Cloud acceptance followed by a lost response. | Retries do not duplicate creation or desired-capacity reduction. |
| T05 | Delete specified IDs. | Other instances in the group and other groups are unaffected. |
| T06 | Delete batches of 20, 21, and multiple batches. | Correct batch boundaries, activity-conflict handling, and subset results. |
| T07 | Partial success, Ignored, Warning, and failure. | Do not mark every ID or the whole activity successful. |
| T08 | Pagination, temporary query failures, and unknown states. | Do not report every instance deleted or Running. |
| T09 | Creating without an IP; OS not ready. | Cloud lifecycle and connection readiness remain separate. |
| T10 | Throttling, timeouts, and context cancellation. | Bounded retries, correct exit, and no goroutine leaks. |
| T11 | Spot disappearance and automatic ESS replacement. | No duplicate capacity compensation; eventual convergence. |
| T12 | Manager / plugin restart. | Rediscover actual members without old in-memory state. |
| T13 | Missing / expired credentials or insufficient permissions. | Clear error classification; do not mark every instance unhealthy. |
| T14 | Weights, foreign members, and conflicting policies. | Preflight fails without silently modifying the group. |
| T15 | Public / private addresses and custom connectors. | User settings take precedence; credentials stay out of logs. |
| T16 | Unsupported / experimental Suspend. | Standard errors, consistent capabilities, and correct partial states. |
| T17 | Repeated cleanup and already deleted instances. | Idempotency and no deletion of shared resources. |
| T18 | Strict Spot policy. | Reject on-demand fallback and test-resource limit violations. |

Use controllable clocks and state progression instead of long real sleeps. Coverage helps find omissions; a percentage is not a substitute for critical behavior assertions.

### 11.3 Process, Protocol, and SSH Tests

Test the actual compiled plugin process loaded by the pinned Fleeting host, returning local instance information through a fake cloud boundary and executing local SSH commands.

A test-only client factory or controlled local HTTP fake endpoint may be used. Test switches must not become a default production path for contacting arbitrary endpoints.

The official integration harness may be reused after reading its pinned implementation and default instance counts. `SkipExecuteCommand` skips only SSH commands; it does not replace cloud APIs or guarantee zero cost. [S07]

The following are not substitutes:

- Running only the official dummy plugin and claiming integration coverage of this provider.
- Mocking the whole provider and checking only successful stub responses.
- Checking compilation without starting the plugin process.
- Treating ECS API acceptance as an established SSH connection.

If adding a local Runner coordinator stub, reuse upstream test facilities where available and keep it small. Do not reimplement a full GitLab server for zero-cloud testing. [S08]

### 11.4 Regular GitHub Workflow

Default PR / primary-branch CI includes:

1. A pinned toolchain and build.
2. Unit tests and Linux race tests.
3. Provider tests with fake cloud clients.
4. Plugin-process and local SSH component tests.
5. Static checks and parsing checks for documented configuration.
6. Release-candidate builds and OCI layout checks when needed; publication is not required.

Use a standard Linux hosted runner and avoid unnecessary large OS / Runner-version matrices. Maintain at least one primary pinned Runner version and use meaningful boundary cases when adding compatibility versions.

Standard hosted runners for public repositories use the applicable free policy; private repositories have plan-specific allowances. Do not make an existing private repository public to save money or automatically switch to paid larger runners. [G01][G02]

## 12. Reuse GitLab.com Free or Existing GitLab CE

### 12.1 Free-Plan Boundaries

GitLab.com Free is sufficient for fixture code, pipeline APIs, job status, and a dedicated project runner. Compute minutes on a user-provided runner do not consume the GitLab.com hosted-runner compute quota. [L01][L02]

Workers run on Alibaba Cloud and the Manager runs on GitHub, so the design does not rely on GitLab.com hosted-runner free minutes.

**GitLab.com project access tokens require a paid plan and must not be a prerequisite for the default design.** Use a dedicated test user's PAT by default. Self-Managed CE may use project tokens according to its version and administrator policy. [L03][L04]

### 12.2 One-Time Fixture Setup

Provide complete instructions to:

1. Create or select a fixed small project.
2. Commit CI scripts matching the fixture sources in GitHub.
3. Create a dedicated **project runner** with the new runner authentication-token workflow.
4. Set a dedicated tag, disable untagged jobs, and prevent other runners from taking fixture jobs.
5. Disable GitLab-hosted / shared-runner execution for the fixture project.
6. Save the runner authentication token and reuse it for later live runs rather than recreating projects and runners.
7. Create a PAT with appropriate API scope for the test user and limit the projects that user can access.
8. Configure short artifact retention, job timeouts, and allowed API pipeline parameters.

`project runner` is a registration scope; `instance executor` is an executor type. Do not confuse the latter with an instance runner serving an entire GitLab installation. [L05]

### 12.3 Configuration

| Name | Type | Purpose |
|---|---|---|
| `GITLAB_URL` | Regular configuration | Defaults to `https://gitlab.com`; may point to existing CE. |
| `GITLAB_PROJECT_ID` | Regular configuration | Fixed fixture project. |
| `GITLAB_FIXTURE_REF` | Regular configuration | Controlled branch / tag; record the actual SHA. |
| `GITLAB_RUNNER_TAG` | Regular configuration | Dedicated job routing. |
| `GITLAB_EXPECTED_RUNNER_ID` | Regular configuration | Verify execution by the intended runner. |
| `GITLAB_RUNNER_TOKEN` | Secret | Reusable project runner authentication token created once. |
| `GITLAB_API_TOKEN` | Secret | PAT for pipeline creation, status queries, and matching artifact downloads. |

By default, use a PAT with the pipeline creation API, such as `POST /projects/:id/pipeline`. Distinguish the PAT API from the different endpoint used by pipeline trigger tokens. [L06]

Existing CE must be reachable by both the GitHub Manager and Spot workers, including code checkout, job communication, and artifact upload. `clone_url` alone does not solve every API / artifact networking issue. [S05]

### 12.4 Fixed Runner Lifecycle

The Manager uses a temporary working directory and configuration and runs in user space through `gitlab-runner run --config ...` or another method supported by the pinned version. A system service is not required.

Each run may record a separate system ID. On exit, remove local processes and files and, if necessary, handle this run's runner-manager record. **Do not delete the fixed project runner or its reusable token.** Commands, signals, and API behavior follow the pinned Runner documentation. [L05][L07]

## 13. Minimal Live Acceptance: One 0-to-1-to-Job-to-0 Cycle

### 13.1 Trigger Policy

Provide a live workflow using `workflow_dispatch`, with `smoke` as the default mode:

- Run trusted commits only, defaulting to the primary branch or an explicit release-candidate SHA.
- Do not trigger automatically for ordinary PRs, fork PRs, or every main-branch push.
- Do not run daily by default.
- Serialize use of the same fixture and ESS group globally with `cancel-in-progress: false`.
- Other workflows using the pool use the same concurrency key.
- Do not use secret-bearing `pull_request_target` workflows to check out and execute external PR code. [G07]

Explicit live invocation is itself the cost-enabling switch; do not require repeated manual confirmations for every run. Maintainers may choose Environment protection, but default operation must not depend on paid environment features.

### 13.2 Preflight and Budget Parameters

These values apply to default `smoke`. Extended suites require an explicitly selected finite budget and matching preflight; do not silently expand smoke limits. Before any billable mutation, default smoke must:

- Check required configuration, credentials, versions, fixture SHA, and the current plugin binary hash.
- Verify a dedicated group, ownership, Active state, empty membership, desired capacity zero, `MinSize=0`, and `MaxSize=1`.
- Verify instance type / region / zone allowlists and the launch-template version.
- Enforce strict Spot: on-demand base capacity and percentage zero; disable on-demand fallback and proactive replacement / rebalancing that can create extra instances. Verify equivalent fields for the actual configuration mode.
- Verify worker networking, image, and SSH public-key source.
- Prepare the run ID, attempt, random nonce, resource ledger, and expiry markers.
- Report resources, limits, deadlines, and cleanup procedures before use.

Suggested defaults are 10 minutes for instance acquisition, 20 minutes for the whole test stage, and 30 minutes for the workflow, leaving at least 10 minutes for cleanup. These are configurable design budgets, not cloud billing guarantees.

Default smoke has a concurrency limit of one and a budget of one newly created instance. If the first instance fails and needs replacement, end and clean up the run rather than creating a second instance automatically. Network retries of the same idempotent request do not constitute a new instance budget.

Count the budget by actual new instance IDs, including failed instances and ESS-created replacements, not merely plugin `Increase` calls. Once the budget is reached, existing instances may finish their jobs, but no new cycle may start. If replacement is required or the budget has been exceeded, stop scheduling, cancel replacement intent, and clean up. Asynchronous cloud replacement may occur before detection; record the overrun and fail the run. Do not describe this protection, which has observation delay, as an atomic cumulative hard limit.

### 13.3 Acceptance Sequence

1. Build the plugin at the current GitHub SHA and record its version and SHA256.
2. Prepare temporary Runner configuration that loads this binary, not an older executable in `PATH` or another published version.
3. Obtain short-lived cloud credentials, complete preflight, and record information recoverable by independent cleanup.
4. Start the Manager with the fixed project-runner token.
5. Trigger a fixture pipeline through the GitLab API with this run's nonce and other explicitly allowed parameters.
6. The Manager acquires the job; this plugin increases group capacity from zero to one.
7. Independently verify ownership and actual `SpotStrategy` through cloud APIs, rather than relying on a Spot setting in the template.
8. The job executes a tiny task on that VM: check checkout content, run simple commands, and create a small artifact.
9. The job obtains the actual instance ID from a reliable source such as ECS metadata and cross-checks it with the cloud API result.
10. GitHub polls this pipeline / job, downloads that job's artifact, and checks the nonce, fixture SHA, instance ID, and expected contents.
11. Wait for the disposable worker to be released, stop new Manager work, and run cleanup.
12. Query ESS and ECS again to confirm desired capacity zero, no active workers, and release of the corresponding instances, then output the complete result.

Normal smoke verifies provisioning and GitLab integration together. Do not create another identical set of cloud instances on every run solely to invoke upstream `TestProvisioning`.

### 13.4 Fixture Requirements

The fixture contains little code and one short job; it does not run all plugin unit tests.

It must:

- Accept only controlled API sources and explicit variables, avoiding an additional run triggered by fixture pushes.
- Use the target runner tag and verify the actual runner ID.
- Have a bounded job timeout and no unlimited automatic retries in basic smoke.
- Produce a small JSON / text artifact with short retention.
- Report the runtime nonce, actual fixture SHA, instance identity, and command results.
- Exclude PATs, runner authentication tokens, SSH private keys, and cloud credentials from artifacts.
- Not accept arbitrary shell commands from user input as a flexible test mechanism.

A green pipeline alone is insufficient. Old pipelines, artifacts, or plugin binaries, and jobs executed by a different non-Spot runner, must not yield a passing result.

### 13.5 Machine-Readable Results

Output at least:

```json
{
  "schema_version": 1,
  "result": "PASS",
  "mode": "smoke",
  "plugin_commit": "<github-sha>",
  "plugin_sha256": "<binary-hash>",
  "runner_version": "<pinned-version>",
  "fleeting_version": "<pinned-module-version>",
  "github_run_id": "<run-id>",
  "github_run_attempt": 1,
  "run_nonce": "<random-nonce>",
  "gitlab_fixture_sha": "<fixture-sha>",
  "gitlab_project_id": "<project-id>",
  "gitlab_pipeline_id": "<pipeline-id>",
  "gitlab_job_id": "<job-id>",
  "gitlab_runner_id": "<actual-runner-id>",
  "region": "<region>",
  "scaling_group_id": "<group-id>",
  "instances": [
    {
      "instance_id": "<instance-id>",
      "zone": "<zone>",
      "instance_type": "<type>",
      "spot_verified": true,
      "deletion_verified": true
    }
  ],
  "cleanup_result": "PASS",
  "leftover_resources": []
}
```

This is a target schema example, not an executed result. Add timestamps, API errors, activity information, and missing configuration to the implementation so failures retain useful diagnostics.

Result states:

| State | Meaning |
|---|---|
| `PASS` | All assertions and cleanup for this mode completed. |
| `FAIL_PRODUCT` | Provider / integration behavior is incorrect. |
| `FAIL_INFRASTRUCTURE` | Capacity availability, quota, networking, or external services prevented acceptance. |
| `NOT_RUN_MISSING_CONFIGURATION` | Configuration is insufficient; live acceptance did not run. |
| `CLEANUP_FAILED` | Resource cleanup is unverified or incomplete; the run cannot pass. |

Infrastructure failures allow bounded retries within the stated budget, not automatic on-demand fallback. An otherwise successful job must not hide cleanup failure.

### 13.6 Extended Live Suites

| Mode | Purpose | Default policy |
|---|---|---|
| `smoke` | Current plugin, one real GitLab job, and verified release. | One Spot instance. |
| `scale` | Repeated increases, specified scale-in, and partial failures. | Explicit input; usually at most 2-3 instances. |
| `interruption` | Disappearance, replacement, Runner failure propagation, and recovery. | Explicit instance budget; on demand. |
| `docker` | Docker Autoscaler path. | Separate image and explicit enablement. |
| `windows` | WinRM and Windows workers. | Explicit selection with cost explanation. |
| `suspend` | Real behavior of implemented suspend / resume. | Capability acceptance; on demand. |

Regular smoke does not run all these suites sequentially. When adapting an upstream harness, explicitly pin counts instead of accepting defaults that may create multiple machines.

API-driven instance deletion can inject disappearance / connectivity failures, but does not validate an actual Spot interruption notice. Do not wait for random Spot interruptions and prolong every acceptance run.

## 14. Minimal Cloud Infrastructure Fixture

Provide one complete primary path using Terraform or the official CLI. The other may be a documented example; avoid maintaining two complex frameworks for the same task.

The minimal fixture includes:

- Reusable VPC, vSwitch, and security group.
- A pinned launch template / instance creation configuration.
- An empty dedicated ESS group.
- SSH public-key configuration.
- Minimal RAM role / OIDC trust and permission documentation.
- Ownership / run / expiry tagging conventions and a resource ledger.
- Information available independently to cleanup.

Do not create ACK, ALB, NAT Gateway, paid VPN, a persistent Manager VM, or a paid image registry by default.

### 14.1 Networking

Do not assume a GitHub-hosted runner can reach a private Alibaba Cloud VPC. Default tests use reachable public addresses or an existing usable private network path.

With public networking:

- Restrict SSH ingress to the current GitHub job's egress address where possible; record ownership and cleanup of temporary rules.
- Do not allow SSH / WinRM from the entire internet by default.
- Workers can reach services needed for GitLab code, APIs, and artifacts.
- Include public addresses, bandwidth, and traffic in test costs and cleanup inventories.
- Do not introduce a persistent paid NAT / VPN merely to save a configuration step.

When reusing private GitLab CE, verify actual reachability from both Manager and workers. An existing CE deployment does not by itself solve networking.

### 14.2 Strict Spot

Verify launch-template and ESS mixed-policy field combinations for the actual cloud configuration mode. Reject implicit costs from default on-demand percentages, automatic on-demand fallback, or proactive creation of extra replacements. [A08]

After creation, use ECS results to verify actual Spot instances. If Spot parameters were set but an on-demand instance was created, strict Spot acceptance fails and cleanup runs.

Production users may explicitly choose mixed / on-demand policies. Document these separately from strict Spot test settings; do not hard-code a requirement that every production instance be Spot.

## 15. Cleanup, Recovery, and Cost Protection

### 15.1 Three Cleanup Layers

Implement:

1. `defer / finally` cleanup in the test process.
2. An `if: always()` cleanup step in the GitHub workflow.
3. An independent janitor that obtains fresh credentials and reclaims abnormal leftovers according to ownership and expiry.

`always()` cannot guarantee execution after a VM disappears or is forcibly terminated, so an independent fallback is required. A `workflow_run` completion event can be combined with manual / infrequent TTL sweeps. The janitor does not create workers and is not a daily live test.

Janitor workflows load code from a trusted version. Artifacts / resource ledgers from failed builds are data to validate, not shell code to execute.

### 15.2 Cleanup Order

Design and test a safe sequence:

1. Stop the Manager from acquiring new jobs or requesting more capacity.
2. Confirm resources belong to this test group and run.
3. Stop or zero the dedicated group's replacement intent, handling activities and members according to actual ESS API constraints.
4. Release this run's instances and their exclusively associated resources.
5. Remove public network resources, temporary security-group rules, keys, and other resources created by this run.
6. Query to verify actual release; record remaining resources and errors.

"Stop or zero" specifies an outcome, not an unconditional instruction to call `DisableScalingGroup` first: some deletion APIs require an Active group. Choose an API-compatible order to avoid disabling the group before deletion becomes possible or deleting ECS instances only to have ESS replace them.

### 15.3 Ownership and Resource Ledger

Cleanup / janitor may affect resources only when all necessary conditions hold:

- The explicitly specified test group / region.
- Correct project ownership.
- The current run ID / attempt, or a historical test run confirmed expired.
- No other valid run is using the resources.

The resource ledger includes at least the group, instance IDs, associated resource IDs, creation origin, run, expiry, and cleanup result. Prepare recoverable identifiers before billable operations and promptly record API-accepted outcomes. Verify tag propagation rather than assuming group tags appear automatically on every ECS resource.

Before each use of a fixed pool, clean up or reject previous occupancy. A new run must not overwrite an older run's ownership markers. If a crash occurs between instance creation and tagging, the janitor must still recover safely from the dedicated group, current run marker, and instance origin.

Do not:

- Scan the whole account by name prefix and delete resources in bulk.
- Delete shared VPCs, security groups, user images, or resources not created by the run.
- Merely remove ESS membership without checking whether ECS instances, disks, or other resources are still billed.
- Declare cleanup complete because an API returned HTTP 200.

### 15.4 Cost Constraints

Each live run reports and respects:

- Region, instance type, image, and instance limit.
- Concurrency limit, total creation-attempt budget, and run deadline.
- Spot pricing strategy / configurable price limit and behavior on unavailable capacity.
- Disks, public network resources, and other associated resources.
- Expiry-based release where supported by the target API / billing type, plus an independent janitor.
- Cleanup results and residual billing risks.

Cloud automatic release does not replace handling ESS desired capacity, which may cause replacement after deletion. Stopping a VM does not release every billable resource.

Do not claim that unit price multiplied by count and duration imposes a real-time hard cap on an entire cloud account's bill. The design provides resource / time constraints and recovery mechanisms. Cost estimates vary with billing granularity, traffic, availability, and service items.

## 16. Distribution and Release

### 16.1 Native Binaries

Provide:

- Build artifacts for at least Linux amd64 / arm64.
- Version, commit, build information, and checksums.
- License and dependency inventory.
- Manual installation and execution through `PATH`.
- Separate statements of Manager-platform and worker-platform support.

Claims of Windows / macOS Manager support require corresponding builds and validation status. Cross-compilation proves only that a build is possible.

### 16.2 OCI

Produce **OCI plugin artifacts compatible with the Fleeting installer** using tools and layouts from the pinned upstream baseline. Publishing an arbitrary Docker image to a registry is not sufficient. [S01][S03]

Public GHCR packages and GitHub Releases are recommended:

- Build and layout validation can use a temporary local registry without Alibaba Cloud or GitLab services.
- Official publication uses the repository-associated `GITHUB_TOKEN` with minimum package permissions.
- Public packages use applicable free policies. A newly published GHCR package may still be private; public source does not prove public package visibility.
- Confirm package visibility once and verify anonymous retrieval and `gitlab-runner fleeting install` without registry login credentials.
- Pin publishing tools / Actions to versions or commits. Do not silently overwrite published versions. [G03][G08]

Without publishing permissions, complete builds, artifacts, workflows, and release documentation, and list upload / visibility steps not yet performed. Do not fabricate Release URLs or claim public publication.

### 16.3 Release and Live Acceptance

Regular builds, release candidates, and installation-format tests do not require ECS creation on every run.

Before first claiming live validation of a capability, provide live evidence for that version or a clearly applicable change scope. Select later smoke / extended suites according to change risk. Without new live evidence, release notes state that live tests did not run for this release and identify the latest verified version. Do not automatically extend previous results to every new capability.

## 17. Free-First Strategy

| Item | Default choice | Boundary |
|---|---|---|
| Source hosting | Public GitHub repository. | Public visibility is a cost-model assumption, not permission to change a user's repository visibility automatically. |
| Regular CI | Standard Linux GitHub-hosted runner. | Applicable public-repository free policy; private repositories have allowances. |
| Build storage | Small Releases / short-lived artifacts. | Do not upload VM images, secrets, or huge caches. |
| Plugin registry | Public GHCR. | Configure public visibility and verify anonymous installation. |
| GitLab server | Fixed GitLab.com Free project. | No dependency on paid project access tokens. |
| Alternative GitLab | Existing reachable CE. | Do not deploy a new instance for each CI run. |
| Manager compute | The GitHub job itself. | No additional Alibaba Cloud control VM. |
| Worker compute | One short-lived Spot instance in explicit live tests. | Billed in reality; cleanup is required even on failure. |
| Networking | Minimal public connectivity or an existing path. | No default paid NAT / VPN. |
| Correctness validation | Extensive fake / component tests and limited live tests. | Mocks do not constitute live evidence. |

Prefer ordinary artifact retention of seven days or less. Retain only necessary redacted live diagnostics and versioned acceptance summaries long term. Actual retention depends on platform and repository settings.

Do not depend on student plans, special open-source approvals, trial credits, or temporary offers. Free policies may change; the README should link to official policies with review dates rather than promise permanent free usage.

## 18. Milestones and Acceptance Gates

### M0: Baseline and Algorithm Decisions

Deliver:

- Pinned versions and the complete official parity matrix.
- ADRs for the ESS model, capacity reconciliation, ownership, and cleanup.
- Reproducible builds and regular CI without credentials.
- All paid / live paths disabled by default.

Exit criterion: no unresolved core design gaps such as an undecided interface version or postponing the deletion algorithm.

### M1: Core Linux Plugin

Deliver:

- A real provider implementation with all core methods.
- Static SSH, state synchronization, correct scaling, and Spot disappearance handling.
- Passing applicable core cases from T01-T18.
- Passing real plugin-process and local SSH tests.
- Instance executor documentation and minimal infrastructure examples.

Exit criterion: core implementation and protocol completeness are demonstrated without a cloud account. Do not describe this milestone as live-cloud validation or full official parity.

### M2: Low-Cost Live Validation Infrastructure

Deliver:

- The fixed GitLab fixture, Manager driver, and single-Spot smoke test.
- Live evidence schema and result classifications.
- Three-layer cleanup, resource ledger, budgets, and OIDC documentation.
- Infrastructure configuration, missing-configuration behavior, and repeat-run instructions.

With configuration: complete one actual 0-to-1-to-job-to-0 cycle with complete evidence and verified resource release.

Without configuration: still complete implementation and offline validation. Mark the live part of M2 `NOT_RUN_MISSING_CONFIGURATION`, list missing inputs, and do not fabricate a pass.

### M3: Extended Official Parity

Deliver:

- Docker Autoscaler compatibility and examples.
- Static Windows WinRM, tests, and preparation documentation.
- A feasible dynamic SSH design and implementation, or documented evidence of limitations.
- Correct Suspend / Resume capability declarations and experimental or formal support paths.
- Extended batch, failure, restart, and address-change tests.
- An explicit mapping and validation status for every official capability.

Exit criterion: no unexplained capability gaps. Feasible but deferred items keep the milestone incomplete; M1 usability does not justify hiding them.

### M4: Distribution and Maintenance Handoff

Deliver:

- Verifiable native binaries, OCI builds, and installation validation.
- Release workflows and complete README, operations, and testing documentation.
- Compatibility matrix, licensing, maintenance, and version-upgrade instructions.
- Final report and all resource cleanup results.

Missing publishing permissions or live credentials do not prevent local delivery. Mark the corresponding external publication / live acceptance work incomplete and distinguish it accurately.

## 19. Project Acceptance Checklist

### Implementation

- [ ] The pinned Runner loads the actual built Alibaba Cloud plugin.
- [ ] The implementation calls real ECS / ESS adapters, not only mocks.
- [ ] Scale-out handles pending work, partial acceptance, limits, and lost responses.
- [ ] Scale-in releases only specified instances rather than merely detaching them.
- [ ] Restart, pagination, transient API failures, and disappearance behave correctly.
- [ ] Connection configuration, static SSH, address choices, and architectures behave correctly.
- [ ] Windows, Docker, dynamic SSH, and suspend support status is explicit.
- [ ] Production examples do not default to unlimited capacity or hidden on-demand fallback.
- [ ] Logs and errors provide diagnostics without credentials.

### Free CI

- [ ] Default `go test ./...` / PR workflows pass without cloud or GitLab credentials.
- [ ] Fakes sit at the API boundary and retain real provider behavior.
- [ ] Real plugin-process and local SSH tests pass.
- [ ] Fork PRs receive neither Alibaba Cloud OIDC access nor GitLab secrets.
- [ ] Live testing is explicit, without default daily or every-push instance creation.
- [ ] GitLab.com Free does not depend on paid project access tokens.
- [ ] Runs sharing a fixture / ESS pool are serialized.
- [ ] Missing configuration and unexecuted tests are not reported as live passes.

### Live Acceptance and Cleanup

- [ ] Record the current plugin SHA, hash, Runner version, and fixture SHA.
- [ ] Verify the actual runner, pipeline, job, nonce, and artifact.
- [ ] Cloud APIs verify that this run's instance is Spot.
- [ ] Default smoke needs one real job and one normal creation cycle.
- [ ] Verify API acceptance separately from final creation / release.
- [ ] Desired capacity is zero after the test, without replacement or leftovers.
- [ ] Cancellation, Manager disappearance, and credential expiry have independent recovery paths.
- [ ] Cleanup acts only within explicit ownership boundaries.
- [ ] Unreleased resources fail live acceptance.

### Release

- [ ] Binaries include versions and checksums.
- [ ] OCI artifacts are readable by the corresponding Runner installer.
- [ ] GHCR permits anonymous installation if public publication is claimed.
- [ ] Capability status, verified scope, and Release descriptions agree.
- [ ] No fabricated test, cloud-operation, or publication results.

## 20. Implementing Agent Workflow and Final Report

### 20.1 Working Method

1. Read this specification and existing repository guidance, then complete M0 first.
2. Continue implementation by milestone; resolve routine reversible choices without repeatedly asking.
3. If cloud APIs or upstream versions differ from this specification's references, record evidence, revise mappings, and preserve user constraints.
4. Without credentials, advance all work that can be completed rather than reducing the task to asking the user to configure accounts first.
5. Do not purchase services, enable paid dependencies, or increase resource budgets automatically to solve test problems.
6. Use actual cloud resources through configured explicit live entry points. Existing authorization and clear inputs do not require repeated confirmation.
7. For each unfinished item, state the specific blocker, completed work, and next step instead of a generic TODO.

### 20.2 Required Final Report Contents

- What was implemented and the selected upstream versions.
- The official capability matrix and remaining gaps.
- Actual commands and results for builds, offline, component, and installation tests.
- Whether live acceptance ran; if so, evidence locations and resource outcomes.
- If live acceptance did not run, specific missing configuration rather than a claim of production readiness.
- Responsibilities and cost boundaries across GitHub, GitLab, and Alibaba Cloud.
- Release artifacts, installation methods, applicable platforms, and validation scope.
- All leftover resources; if no live operations occurred, explicitly state that no cloud resources were created.
- Minimum one-time user configuration still required.

Report `implementation_status`, `offline_validation_status`, `live_validation_status`, and `publication_status` separately. A single "all complete" label must not obscure different stages.

## 21. Official References

These links provide factual references and implementation entry points; they do not replace Phase 0 source-version pinning. Implementation requirements and suggested budgets are project design decisions. Platform policies and interfaces may change; recheck applicable versions during implementation. The review date above is retained from the original specification, not a claim of revalidation when this English edition was added.

### GitLab Runner / Fleeting

- [S01] [Fleeting: official plugins, executors, and installation](https://docs.gitlab.com/runner/fleet_scaling/fleeting/)
- [S02] [Fleeting provider Go API](https://pkg.go.dev/gitlab.com/gitlab-org/fleeting/fleeting/provider)
- [S03] [Fleeting plugin bootstrap](https://pkg.go.dev/gitlab.com/gitlab-org/fleeting/fleeting/plugin)
- [S04] [Instance executor](https://docs.gitlab.com/runner/executors/instance/)
- [S05] [Runner advanced configuration / autoscaler / connector / state storage](https://docs.gitlab.com/runner/configuration/advanced-configuration/)
- [S06] [Docker Autoscaler executor](https://docs.gitlab.com/runner/executors/docker_autoscaler/)
- [S07] [Fleeting integration harness](https://pkg.go.dev/gitlab.com/gitlab-org/fleeting/fleeting/integration)
- [S08] [Runner / Fleeting integration testing plan](https://gitlab.com/gitlab-org/gitlab-runner/-/issues/36787)
- [S09] [Official AWS plugin](https://gitlab.com/gitlab-org/fleeting/plugins/aws)
- [S10] [Official Google Cloud plugin](https://gitlab.com/gitlab-org/fleeting/plugins/googlecloud)
- [S11] [Official Azure plugin](https://gitlab.com/gitlab-org/fleeting/plugins/azure)
- [S12] [Fleeting integration changes related to SSH execution](https://gitlab.com/gitlab-org/fleeting/fleeting/-/merge_requests/150)

### Alibaba Cloud

- [A01] [ESS desired capacity and concurrent scaling semantics](https://help.aliyun.com/zh/auto-scaling/user-guide/expected-number-of-instances)
- [A02] [DescribeScalingGroups](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-describescalinggroups)
- [A03] [DescribeScalingInstances](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-describescalinginstances)
- [A04] [ScaleWithAdjustment](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-scalewithadjustment)
- [A05] [RemoveInstances](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-removeinstances)
- [A06] [DescribeInstances](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describeinstances)
- [A07] [DescribeScalingActivities](https://help.aliyun.com/en/auto-scaling/developer-reference/api-ess-2014-08-28-describescalingactivities)
- [A08] [ESS scaling policy and Spot / on-demand mixed policies](https://help.aliyun.com/en/auto-scaling/user-guide/scaling-policy)
- [A09] [ECS instance health status](https://help.aliyun.com/zh/ecs/user-guide/view-the-health-status-of-an-instance)
- [A10] [Register temporary SSH public keys with ali-instance-cli](https://help.aliyun.com/zh/ecs/user-guide/register-a-public-key-and-connect-to-an-instance-with-the-key-by-using-ali-instance-cli)
- [A11] [AttachKeyPair](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-attachkeypair/)
- [A12] [Economical mode, including Spot](https://help.aliyun.com/zh/ecs/user-guide/economical-mode)
- [A13] [ECS hibernation and limitations](https://help.aliyun.com/zh/ecs/user-guide/hibernate-an-instance)
- [A14] [EnterStandby](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-enterstandby)
- [A15] [ExitStandby](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-exitstandby)
- [A16] [SuspendProcesses](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-suspendprocesses)
- [A17] [ModifyScalingGroup](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-modifyscalinggroup)
- [A18] [Weighted capacity and performance-based scaling](https://help.aliyun.com/en/auto-scaling/user-guide/use-performance-metrics-to-measure-auto-scaling)

### GitLab.com Free and APIs

- [L01] [GitLab pricing](https://about.gitlab.com/pricing/)
- [L02] [Compute minutes FAQ: user-provided runners and hosted compute quotas](https://about.gitlab.com/pricing/faq-compute-minutes/)
- [L03] [Project access token availability by plan](https://docs.gitlab.com/user/project/settings/project_access_tokens/)
- [L04] [Personal access tokens](https://docs.gitlab.com/user/profile/personal_access_tokens/)
- [L05] [New runner creation workflow and token reuse](https://docs.gitlab.com/ci/runners/new_creation_workflow/)
- [L06] [Pipelines API](https://docs.gitlab.com/api/pipelines/)
- [L07] [GitLab Runner commands](https://docs.gitlab.com/runner/commands/)

### GitHub Free CI, Distribution, and OIDC

- [G01] [GitHub Actions billing and usage](https://docs.github.com/en/actions/concepts/billing-and-usage)
- [G02] [GitHub Actions billing](https://docs.github.com/en/billing/concepts/product-billing/github-actions)
- [G03] [GHCR: OCI, GITHUB_TOKEN, initial visibility, and anonymous pulls](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
- [G04] [Official Alibaba Cloud GitHub credentials Action](https://github.com/aliyun/configure-aliyun-credentials-action)
- [G05] [AssumeRoleWithOIDC](https://help.aliyun.com/zh/ram/developer-reference/api-sts-2015-04-01-assumerolewithoidc)
- [G06] [GitHub OIDC reference](https://docs.github.com/en/actions/reference/security/oidc)
- [G07] [GitHub Actions secure use](https://docs.github.com/en/actions/reference/security/secure-use)
- [G08] [GitHub Packages billing](https://docs.github.com/en/billing/concepts/product-billing/github-packages)
