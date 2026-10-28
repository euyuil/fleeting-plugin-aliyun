# Dedicated infrastructure preparation

The [pre-release cloud gate](../docs/pre-release-cloud.md) owns fresh, labelled
fixtures in a separate test account using `tools/cloud_fixture.py`. The reusable
fixture procedure below belongs to the independently configured GitLab path.

## Primary path: official Alibaba CLI

Use Python 3.11+, an OpenSSH client, and official aliyun CLI 3.5.2, with its download/checksum verified through the
[official release](https://github.com/aliyun/aliyun-cli/releases/tag/v3.5.2).
Use a bootstrap identity separate from the Manager role. The script is explicit
and billable: it requires `CREATE_FIXTURE=1`. It creates a VPC/vSwitch/security
group, imports a public key, creates a version-1 launch template, and enables an
empty, strict-Spot ESS group with MaxSize=1. It creates no NAT, load balancer,
ACK, VPN, registry, Manager VM or GitLab deployment.

The cost-optimized ESS policy requires an explicit Spot pool count. This fixture
uses one pool because its launch template pins one instance type.

Before running, select an allowed region/zone and available small Spot instance
type with enough memory for the OS, Runner and selected executor. The 0.5-GiB
configuration used for SSH-only checks failed a later full-job preparation
attempt; see the [acceptance report](../docs/acceptance-report.md#released-plugin-job-preparation-attempts).
The successful VM/Docker cycles used 2 vCPU and 2 GiB; see the
[recorded worker setup](../docs/acceptance-report.md#released-plugin-vm-and-docker-jobs).
Select the exact immutable image ID. Prepare that image with
`bootstrap-linux.sh` as root: supply Runner version/checksum from
`upstream.lock.json`, the SSH public key/user, and exact distribution package
versions (`GIT_PACKAGE_VERSION`, `CURL_PACKAGE_VERSION`, `CA_PACKAGE_VERSION`,
`SSH_PACKAGE_VERSION`, `COREUTILS_PACKAGE_VERSION`). Use a fixed signed package
repository snapshot appropriate to the selected base image. The script checks
tools and permissions before writing the readiness marker. Include `timeout`
in the base/toolchain. No Manager/PAT/cloud credentials enter the worker image. Existing authorized
keys are preserved, and the user's home is resolved from passwd. Runner downloads
use a unique temporary file. Bake the image without active job-user processes;
the bootstrap rejects linked SSH directories and unexpected home/key-file owners.
Before capture, review shell startup files for noninteractive CI use, remove
preparation credentials and clear stale cloud-init/machine/network state. If
startup installs per-worker keys, create readiness only after that setup passes.
The recorded acceptance used a separately owned CLI image-building fixture.

```sh
export ALIYUN_REGION=REPLACE_REGION
export ZONE_ID=REPLACE_ZONE
export INSTANCE_TYPE=REPLACE_INSTANCE_TYPE
export IMAGE_ID=REPLACE_PREPARED_IMAGE_ID
export SSH_PUBLIC_KEY_FILE=./worker-key.pub # Dedicated OpenSSH RSA public key
export MANAGER_CIDR=REPLACE_CURRENT_IPV4/32
export ACCEPTANCE_PROJECT=OWNER/REPOSITORY
CREATE_FIXTURE=1 sh infra/prepare.sh
```

Use a dedicated RSA public key for this static smoke fixture, matching the
[ImportKeyPair API](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-importkeypair).
The private key stays with the Manager. The separate dynamic path uses expiring
Ed25519 keys through Cloud Assistant and does not import them as ECS key pairs.

The CLI handles the official credential chain. Keep its configuration and `.live`
outputs private. Each accepted create response is recorded before the next
operation. A local lock serializes setup, and existing receipts stop new writes.
The script is an initial creation path, not an idempotent apply tool:
if a call fails, inspect its recorded IDs and resume deliberately; do not rerun
it blindly and duplicate infrastructure. VPC/vSwitch creation may take time;
the script waits a bounded time for Available before dependent VPC/vSwitch
operations. Reserve and document the reusable ownership IDs in your operations store.
The general `prepare.sh` procedure itself has not been live validated; the
recorded real acceptance used separate, individually tracked CLI fixtures.

Set the resulting group ID and exact template fields in GitHub variables. The
live acceptance must additionally check ownership and zero occupancy, the pinned launch template,
strict Spot policy, the explicit type/image/zone allowlist, actual Spot billing,
disk deletion policy and no worker RAM role. Public networking is traffic-billed
with a small bandwidth limit; it must reach GitLab and bootstrap dependencies.
Per-run acceptance must manage its own narrow SSH rule. A private network path can be
used in production with `use_external_addr=false`; it must already be reachable
from the Manager. No paid VPN/NAT is created to make a private route work.

For Terraform users, translate these exact resource boundaries and policy fields
with a pinned provider and committed lockfile; do not maintain a second drifted
fixture beside the primary CLI procedure. Existing prepared groups can be used
if they satisfy the same preflight and ownership requirements.

## Dynamic SSH image preparation

Static SSH is the default. To enable short-lived keys, prepare a Linux image
with Cloud Assistant, synchronized UTC, OpenSSH supporting `expiry-time`, and
`flock`, `getent`, `awk`, `base64`, and the standard shell utilities.

Install `infra/fleeting-authorize-key` root-owned and executable at
`/usr/local/bin/fleeting-authorize-key`:

```sh
sudo install -o root -g root -m 0755 infra/fleeting-authorize-key /usr/local/bin/fleeting-authorize-key
```

Configure sshd's `AuthorizedKeysFile` to
include `.ssh/fleeting-keys` for the dedicated job user, while retaining the
image's intended static key path. Validate the sshd configuration before baking
the image. The helper resolves the user's actual passwd home, writes only its
dedicated key file, locks concurrent updates, and preserves other unexpired keys.
It accepts one generated public-key line and never receives private keys.

Set `ssh_credentials = "cloud_assistant"` in `[runners.autoscaler.plugin_config]`
and `use_static_credentials = false` in the connector configuration. Specify a
POSIX username. This mode supports Linux SSH only. Static key/password settings
are not an alternative fallback for failed dynamic authorization.

The Manager calls `ecs:RunCommand` and `ecs:DescribeInvocationResults` through the
native SDK. Current RunCommand RAM metadata requires broad account resource
scope; enforce CommandRunAs for the dedicated job user and isolate/review the
account boundary. Do not assume unverified instance-tag restrictions. The
command uses `WorkingDir=/`, a bounded invocation timeout, and KeepCommand=false.
No additional proprietary CLI is needed.

Each ConnectInfo generates a new key with an absolute 60-second expiry and
returns an earlier refresh time. A completed command must have an explicit zero exit code and the fixed
`fleeting-key-ready` output, with at least 20 seconds left to begin SSH; shortened windows fail and require reacquisition. Existing
SSH sessions can continue after the login key expires. Network reachability
still needs a public or private route from the Manager.

See [ADR 0005](../docs/adr/0005-dynamic-ssh.md) for API alternatives, limitations,
and evidence. Offline tests cover payloads, refresh, helper file operations,
expiry pruning, concurrent writes, and invalid input. Real Cloud Assistant
execution and SSH timing have not been validated on Alibaba Cloud.

## Other worker images

Use the [operating guide](../docs/operations.md) for Docker Autoscaler and static
Windows workers. Prepare Docker Engine and the pinned job image separately;
static Windows requires a licensed image and the documented upstream WinRM TLS
identity limitation. The selected Linux Docker image has
[live job evidence](../docs/acceptance-report.md#released-plugin-vm-and-docker-jobs);
the Windows path remains unvalidated.

## Teardown and cost boundaries

Per-run cleanup leaves reusable networking, key pair, template, prepared image,
and ESS group. Preserve their exact IDs from `.live`. Before explicitly tearing
down this dedicated fixture, stop Managers, verify no active run marker, desired
capacity zero, no members, and no remaining ECS/disks/interfaces. Delete the exact
owned group, template, key pair, rules/security group, vSwitch, and VPC in dependency
order with the official CLI. Do not delete a user image, shared network, snapshot,
or disk based on its name. Review image/snapshot storage separately. Inventory
and dry runs do not authorize deletion.

Spot, disks, traffic, images, and snapshots are not promised to be free. Limits
and expiry reduce exposure; they do not impose an account billing cap.
