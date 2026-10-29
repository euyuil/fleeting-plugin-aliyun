# Operating the provider

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

Follow the [Quick Start](../README.md#quick-start-run-a-job-on-a-linux-vm)
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

Set `use_static_credentials=true`. Explicit user, port, OS, architecture, and
route settings take precedence. Omitted OS/architecture values are inferred from
ECS metadata; unknown values fail clearly. ConnectInfo refreshes both addresses
on every call. Cloud Running/InService does not prove that SSH or the image is
ready; Runner owns connection and readiness timeouts.

## Other prepared workers

[`examples/docker-autoscaler/config.toml`](../examples/docker-autoscaler/config.toml)
uses the same provider with the official Docker Autoscaler executor. It requires
a separately prepared compatible Docker worker image and an explicitly pinned
job image. The default Instance example does not depend on Docker.

[`examples/windows/config.toml`](../examples/windows/config.toml) uses static
Windows amd64 WinRM HTTPS credentials and a prepared listener/certificate.
The pinned upstream Fleeting connector skips server certificate verification,
and its provider protocol exposes no CA-verification setting. HTTPS encrypts
the connection but does not authenticate the server certificate. Use an isolated
authenticated network and treat verified TLS server identity as an upstream
limitation. Offline tests cover connection information; a real Windows session
has not run. See [ADR 0003](adr/0003-state-and-connections.md).

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
