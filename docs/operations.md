# Operating the provider

## Worker and cloud prerequisites

Prepare these resources before starting Runner:

- A worker image with SSH, Git, a writable job directory, the matching Runner
  binary, `timeout`, and the tools needed by your jobs. The examples wait for
  `/var/lib/fleeting-worker/ready`; create that marker after preparing the image,
  or replace `instance_ready_command` with a check suitable for your image.
- For Docker jobs, a working Docker Engine on the worker, access to it for the
  SSH user, and a pinned job image. See the [Docker example](../examples/docker-autoscaler/config.toml).
- A launch template with the image, instance type, disk, network, and desired
  Spot policy. Pin its version. CPU and memory come from the instance type.
  For the Quick Start's disposable workers, configure the system disk and all
  data disks to be released with the instance so the cleanup check can pass.
- A dedicated, active ESS group with desired capacity enabled, minimum `0`, a
  finite maximum, and unit instance weights. For the first job, use desired `0`
  and maximum `1`. Follow the [group compatibility rules](#dedicated-ess-group).
- A cloud identity with the [required permissions](#authentication-and-permissions).
  Give it to the Manager, not to job workers.
- A route from the Manager to workers over SSH, and HTTPS access from both to
  GitLab. Workers also need access to job dependencies and image registries.

Use existing resources if they meet these requirements. The
[infrastructure guide](../infra/README.md) describes image preparation and a
separate test-fixture creation procedure. Its test markers and GitHub workflow
variables are for this project's acceptance tests, not for running your jobs.

The pinned upstream connector does not verify SSH server host keys. Use a
trusted, isolated network route and restrict SSH ingress to the Manager. See
[connection limits](adr/0003-state-and-connections.md).

## Dedicated ESS group

Prepare an existing Active ECS group with desired capacity, MinSize=0, finite
MaxSize, unit weights, and a pinned positive launch-template version. Use one
Manager as the sole capacity writer. Initialization rejects conflicting scaling
rules, enabled schedules, lifecycle hooks, active refresh tasks, non-releasing
scale-in policy, protection, standby/recycle pools, proactive Spot replacement,
and rebalancing. Foreign attached instances are rejected; ECS instances
explicitly entrusted to ESS for lifecycle release are accepted.

The plugin never enables a disabled group or changes its creation policy. VPCs,
vSwitches, security groups, images, launch templates, and the group belong to
[infrastructure setup](../infra/README.md). Give each Runner pool a separate group. The group must
have space for up to 13 reserved operation tags; do not edit `fleeting-operation*`
while the provider has pending intent. See [API boundaries](cloud-api.md) and
[capacity recovery](adr/0002-capacity-reconciliation.md).

## Static SSH and Instance executor

Follow the [online Quick Start](https://github.com/euyuil/fleeting-plugin-aliyun/blob/main/README.md#quick-start-run-a-job-on-a-linux-vm)
for `.deb` installation and local `manager` commands. The Manager can run on
the GitLab host or a separate host. The [manual OCI service guide](installation.md#linux-service-setup)
is an alternative for the published plugin; see [distribution](distribution.md)
for standalone native binaries.

Start from [`examples/instance/config.toml`](../examples/instance/config.toml).
Set the GitLab URL and runner authentication token, group and region,
worker username and private-key file. Do not commit credentials. The example
uses a pinned OCI plugin reference. With the Debian package, `manager setup`
replaces it with the installed binary and copies the SSH key to its private path.
See [Configure a pool](debian-installation.md#configure-a-pool) for the packaged
service. With manual OCI installation, keep the reference and use the same
plugin directory for installation and Runner.
Its default limits are max_instances=1, capacity_per_instance=1, and max_use_count=1.
Jobs run on the VM without worker Docker or per-worker Runner registration.

The worker image must already contain the compatible Runner binary on PATH,
the selected job shell/tools, an SSH service, the static public key, a writable
job directory, and the configured readiness marker. Prepare these before
creating instances. The Manager needs a network route to the selected public or
private address. Limit SSH ingress to the actual Manager source or private
network; do not open a public administration port to all addresses. See
[ADR 0003](adr/0003-state-and-connections.md) for the pinned connector's server
identity verification limits.

## Linux worker readiness

The two Linux examples allow up to 600 seconds for the ready command and
15 minutes for acquisition. These are finite connection/readiness policies,
not changes to pool capacity or instance lifetime. The command requires the
ready marker and successful Git/Runner probes, plus Docker for that executor.
Readiness still fails if any probe fails or the command deadline expires.

Cold bootstrap must finish package installation and Runner verification before
writing readiness. The Runner download supports three retries with a 120-second
limit per transfer; [curl resets that limit for each retry](https://curl.se/docs/manpage.html#--max-time).
A 300-second ready window can therefore terminate a supported retry sequence.
The larger window is not a guarantee that every package/network operation can
complete, and does not override the caller's overall execution deadline.

The bootstrap scripts own `/var/lib/fleeting-worker` and its regular `phase`,
`failed` and `ready` files as root. They refuse linked or unowned existing state.
Only a fixed phase number is stored; failures retain their original shell exit
status, remove a temporary Runner download, and never create readiness. A failed
marker takes precedence over an old ready marker and stops waiting immediately.

| Phase | Bootstrap operation |
| --- | --- |
| 1 | Ubuntu package index and version selection |
| 2 | Docker package installation |
| 3 | Docker service startup and initial check |
| 4 | Base tool packages and input validation |
| 5 | SSH user, directory and public-key setup |
| 6 | Runner download |
| 7 | Runner checksum verification and installation |
| 8 | Final bootstrap tool and user probes |

Phase and failure files are published atomically after their permissions are set.
The shipped ready command returns `201–208` for a failed phase and `221–228`
for the phase observed when reporting a deadline. That snapshot can advance
after the inner command stops; it does not prove the precise phase at expiry.
Unknown/invalid failed state returns `209`;
an unknown deadline phase returns `229`. Failed Git, Runner and Docker readiness
probes return `231`, `232` and `233`; a ready marker present when reporting a
deadline returns `234`, without proving that probes had already started.
All are failures. State reads are bounded and never echo their contents. These
fixed codes allow the acceptance Manager journal to retain a phase even though
the pinned Runner connector does not capture ready-command stdout/stderr.
The public projection interprets them only in a recognized ready-command error;
it does not publish worker logs, URLs, identities or arbitrary status strings.

Set `use_static_credentials=true`. Explicit user, port, OS, architecture, and
route settings take precedence. Omitted OS/architecture values are inferred from
ECS metadata; unknown values fail clearly. ConnectInfo refreshes both addresses
on every call. Cloud Running/InService does not prove that SSH or the image is
ready; Runner owns connection and readiness timeouts.

## Other prepared workers

[`examples/docker-autoscaler/config.toml`](../examples/docker-autoscaler/config.toml)
uses the same provider with the official Docker Autoscaler executor. Follow the
Quick Start, but copy
`/usr/share/doc/fleeting-plugin-aliyun/examples/docker-autoscaler/config.toml`
instead of the packaged VM example. Prepare a worker image with Docker Engine,
allow the SSH user to access Docker, and set `runners.docker.image` to a job image
pinned by digest. Use a dedicated ESS group for this pool. `manager setup` selects
the installed native plugin for either example. Jobs can use the Docker executor's
`image` and `services` features.

The packaged service manages one pool per host. Use a separate Manager host for
an additional pool, or manage multiple pools through a manual Runner setup.
Hosting the Manager itself in Docker is a separate deployment choice; it does
not select the job executor. Both job modes create ECS machines and require no
Kubernetes or ACK. The default Instance example does not depend on Docker.

To limit the job container, add `cpus` and `memory` to the existing
`[runners.docker]` section, keeping your pinned image:

```toml
[runners.docker]
  image = "REPLACE_WITH_PINNED_JOB_IMAGE_DIGEST"
  cpus = "2"
  memory = "4g"
```

These are container limits. The ECS size still comes from the launch template.
Leave enough memory for the host and any helper or service containers. See the
[Runner Docker settings](https://docs.gitlab.com/runner/configuration/advanced-configuration/#the-runnersdocker-section).

[`examples/windows/config.toml`](../examples/windows/config.toml) uses static
Windows amd64 WinRM HTTPS credentials, PowerShell, and a prepared listener/certificate.
The pinned upstream Fleeting connector skips server certificate verification,
and its provider protocol exposes no CA-verification setting. HTTPS encrypts
the connection but does not authenticate the server certificate. Use an isolated
authenticated network and treat verified TLS server identity as an upstream
limitation. Offline tests cover connection information; a real Windows session
has not run. See [ADR 0003](adr/0003-state-and-connections.md).

## Choose CPU and memory

Choose the VM size before jobs run. Set an ECS `InstanceType` in the launch
template used by the ESS group. The infrastructure script takes this value from
`INSTANCE_TYPE`. The plugin does not accept `cpu` or `memory` settings.

For different job sizes, create separate Runner pools, each with its own ESS
group and launch template. For example, give a 2 vCPU / 4 GiB pool the tag
`aliyun-vm-2c4g` and an 8 vCPU / 16 GiB pool the tag `aliyun-vm-8c16g`. A job picks
one with `tags: [aliyun-vm-8c16g]`. Tags select pools; they do not set machine size.

`capacity_per_instance` means concurrent job slots per VM. `max_instances` means
the maximum number of VMs. Neither setting controls CPU or memory. The examples
use one slot per VM and release each VM after one job.

## Authentication and permissions

The official Alibaba credentials library supplies environment, profile,
credential-process, instance-role, and workload/OIDC credential sources. Prefer
temporary roles. Static STS environment variables do not refresh themselves;
restart the Manager with fresh credentials when they expire. Never place access
keys in plugin configuration. Grant the selected dedicated group the required
read, scale, specified-release, tag, and untag permissions, plus ECS instance,
type, and launch-template reads. The journal requires group-tag permissions.
Some cloud read actions require account-level resource scope; check the official
RAM action metadata for the selected deployment before granting permissions.

`region` and `scaling_group_id` are required. `operation_timeout` defaults to 2m
and accepts 1s through 10m. Unknown configuration keys fail. Static SSH remains the default;
see [dynamic SSH preparation](../infra/README.md) for the explicit opt-in path. Suspend/Resume return the standard upstream
unsupported error and the provider declares no suspend capability.

## Recovery and limits

Capacity writes are serialized. A confirmed absolute target remains the basis
of subsequent increases; disappearance and ESS replacement do not create a
second compensation request. Normal scale-in releases only specified IDs in
batches of 20, with accepted subsets distinct from completed activities.
Updates emit deleting before verified absence; incomplete API snapshots do not
delete the previous published set. API errors do not make every worker unhealthy.

The cloud journal recovers pending intent after plugin restart. A failed partial
removal is automatically retired only when desired capacity matches the number
of IDs proven released. Unknown rollback semantics or an expired ambiguous
removal replay window stop mutations and retain the journal for investigation.
Do not clear it blindly. Manager job recovery depends on the pinned Runner state
storage behavior; preserving jobs across Manager loss is not promised.
Shutdown closes owned API connections and never deletes workers. Acceptance
cleanup, deployment, and release publication are separate operations. No live
cloud resources were created during this implementation's offline tests.
