# GitLab Runner Autoscaling on Alibaba Cloud (Aliyun)

Run GitLab CI/CD jobs on Alibaba Cloud ECS machines that scale with your workload.
`fleeting-plugin-aliyun` connects GitLab Runner to Alibaba Cloud Auto Scaling
(ESS). Runner requests workers when jobs need them and releases them after use.

This is a community project under active development. Real cloud scaling and SSH
tests have passed. Full GitLab job acceptance is still pending.
[`0.1.0-rc.2`](https://github.com/euyuil/fleeting-plugin-aliyun/releases/tag/v0.1.0-rc.2)
is available for evaluation. Anonymous OCI installation with Runner 19.5.1 has
passed on Linux amd64 and arm64; see the [installation checks](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40639528094).

## Choose how jobs run

| Mode | Where the job runs | What the worker needs |
|---|---|---|
| **VM — default** | Directly on an ECS VM, with the Instance executor | Linux, SSH, Git, GitLab Runner, and your build tools |
| **Container** | In a Docker container on an ECS VM, with Docker Autoscaler | A prepared Docker host and a job container image |

Both modes create ECS machines. The plugin manages those machines; GitLab Runner
runs the jobs. You do not need Kubernetes or ACK.

A [Windows VM example](examples/windows/config.toml) is also available. It uses
PowerShell and WinRM HTTPS. Windows and Docker jobs have not yet been tested on
real cloud workers. See [supported configurations](docs/operations.md).

## Quick start: run a job on a Linux VM

### 1. Install GitLab Runner on the Manager

The **Manager** is the host that runs GitLab Runner and this plugin. **Workers**
are the ECS machines that run your jobs. Start with a Linux amd64 or arm64
Manager that can reach GitLab, Alibaba Cloud APIs, and your workers over SSH.

The plugin runs as a separate process managed by Runner. The Manager can be a
separate server from GitLab CE, or share its host.

Install **GitLab Runner 19.5.1** using the
[GitLab installation guide](https://docs.gitlab.com/runner/install/linux-repository/)
and its instructions for choosing a specific version. Also install `curl`.
Check Runner on the Manager:

```sh
gitlab-runner --version
```

Runner will download the plugin from public GHCR in step 5. Use the same Linux
user for installation and for running the Manager. You can use an ordinary user
for this foreground quick start. See [installation details](docs/distribution.md)
for native archives and source builds.

### 2. Prepare a worker image and an ESS group

Follow the [infrastructure setup guide](infra/README.md) to prepare:

- A Linux image with Git, the matching GitLab Runner binary, SSH, your public
  key, and the tools your jobs need. The guide's bootstrap script creates the
  readiness marker required by the example configuration.
- A launch template with your image, ECS instance type, disk, and network settings.
- A dedicated, active ECS scaling group that uses a fixed launch-template version.
  Start with desired capacity `0`, minimum size `0`, and maximum size `1`.
- Alibaba Cloud credentials for the Manager, with the permissions listed in the
  [operating guide](docs/operations.md#authentication-and-permissions).

Each Runner pool needs its own ESS group. Let one Manager control its capacity.
Workers need the Runner binary, but do not run a Runner service or register each
worker with GitLab. Keep the SSH private key on the Manager.

Cloud machines, disks, images, and traffic can cost money. The setup guide's
creation script uses Spot instances. Review its settings before running it.

### 3. Create a project runner in GitLab

In your GitLab project, open **Settings > CI/CD > Runners** and create a project
runner. Give it the tag `aliyun-vm` and turn off **Run untagged jobs**. Save the
runner authentication token for the next step. Use an authentication token, not
a legacy registration token.

### 4. Configure the Manager

Download the [VM configuration example](examples/instance/config.toml) to a
private directory:

```sh
mkdir -p "$HOME/.config/gitlab-runner-aliyun"
chmod 700 "$HOME/.config/gitlab-runner-aliyun"
curl --fail --location --show-error \
  https://raw.githubusercontent.com/euyuil/fleeting-plugin-aliyun/v0.1.0-rc.2/examples/instance/config.toml \
  --output "$HOME/.config/gitlab-runner-aliyun/config.toml"
chmod 600 "$HOME/.config/gitlab-runner-aliyun/config.toml"
```

Edit that file and set:

| Setting | Your value |
|---|---|
| `url` | Your GitLab URL |
| `token` | The project runner authentication token |
| `plugin` | `ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.2` |
| `region` | The Alibaba Cloud region of your ESS group |
| `scaling_group_id` | Your dedicated ESS group ID |
| `username` | The SSH user in your worker image, such as `ci` |
| `key_path` | The absolute path to the matching private key on the Manager |

The downloaded example uses a native binary name. Replace its `plugin` value
with the full OCI reference above. Pin this exact version for the prerelease.

Also add this block if it is missing, to use `sh` on Linux:

```toml
[runners.feature_flags]
  FF_POSIXLY_CORRECT_ESCAPES = true
```

Keep the private key readable only by the Manager user. Configure Alibaba Cloud
credentials for that same user through a supported credential source; do not put
cloud access keys in this file. For a short test, you can supply temporary STS
credentials through `ALIBABA_CLOUD_ACCESS_KEY_ID`,
`ALIBABA_CLOUD_ACCESS_KEY_SECRET`, and `ALIBABA_CLOUD_SECURITY_TOKEN` in the shell
that starts Runner. Restart the Manager with fresh credentials before they
expire. See [authentication setup](docs/operations.md#authentication-and-permissions)
for role-based and other credential sources.

The example connects to a public worker address. For a reachable private network,
set `use_external_addr = false`. The pinned upstream connector does not check SSH
server host keys. Use a trusted, isolated route and restrict SSH access to the
Manager. See [connection limits](docs/adr/0003-state-and-connections.md).

### 5. Install the plugin and run your first job

Install and list the plugin with Runner:

```sh
gitlab-runner fleeting --config "$HOME/.config/gitlab-runner-aliyun/config.toml" install
gitlab-runner fleeting --config "$HOME/.config/gitlab-runner-aliyun/config.toml" list
```

The public package supports Linux amd64 and arm64. Registry login and a Docker
daemon are not required for this VM quick start. Installation stores the plugin
under `~/.config/fleeting/plugins` for the current user.

Start Runner in the foreground and leave it running. This allows jobs to create
billable workers:

```sh
gitlab-runner run --config "$HOME/.config/gitlab-runner-aliyun/config.toml"
```

Add this job to your GitLab project's `.gitlab-ci.yml`, commit it, and run a
pipeline:

```yaml
hello-aliyun:
  tags: [aliyun-vm]
  script:
    - uname -m
    - git --version
    - printf 'Hello from Alibaba Cloud\n' > hello.txt
  artifacts:
    paths: [hello.txt]
```

With the example settings, Runner creates one VM, runs one job, and releases the
VM after use. Check the job output and download `hello.txt`. Then check that the
ESS desired capacity returns to `0` and that the worker and its disks are gone.
Reusable infrastructure, such as the ESS group and VPC, stays in place; see
[teardown](infra/README.md#teardown-and-cost-boundaries) when you no longer need it.

For a long-running Manager, run Runner as a service with the same configuration,
credentials, and access to the installed plugin directory. Install the plugin
as the service user, or set the same `FLEETING_PLUGIN_PATH` for installation and
the service. See the
[GitLab Runner service commands](https://docs.gitlab.com/runner/commands/#service-related-commands).

## Choose CPU and memory

Choose the VM size **before jobs run**. Set an ECS `InstanceType` in the launch
template used by the ESS group. The infrastructure script takes this value from
`INSTANCE_TYPE`. The plugin does not accept `cpu` or `memory` settings.

For different job sizes, create separate Runner pools, each with its own ESS
group and launch template. For example, give a 2 vCPU / 4 GiB pool the tag
`aliyun-vm-2c4g` and an 8 vCPU / 16 GiB pool the tag `aliyun-vm-8c16g`. A job picks
one with `tags: [aliyun-vm-8c16g]`. Tags select pools; they do not set machine size.

`capacity_per_instance` means concurrent job slots per VM. `max_instances` means
the maximum number of VMs. Neither setting controls CPU or memory. The examples
use one slot per VM and release each VM after one job.

## Run jobs in containers

Use the [Docker Autoscaler example](examples/docker-autoscaler/config.toml)
instead of the VM example. Prepare a worker image with Docker Engine, allow the
SSH user to access Docker, and set a job image pinned by digest. Jobs can then use
the Docker executor's `image` and `services` features. Set the same OCI `plugin`
reference as in step 4, then run the install command with that configuration.

To limit the job container, add `cpus` and `memory` to the existing
`[runners.docker]` section. For example:

```toml
[runners.docker]
  image = "REPLACE_WITH_PINNED_JOB_IMAGE_DIGEST"
  cpus = "2"
  memory = "4g"
```

These are container limits. The ECS size still comes from the launch template.
Leave enough memory for the host and any helper or service containers. See the
[Runner Docker settings](https://docs.gitlab.com/runner/configuration/advanced-configuration/#the-runnersdocker-section).

## Current status and more help

Real cloud tests passed `0 → 1 → SSH → 0` and `0 → 2 → 1 → 0`, including SSH on
the remaining worker after the other was released. These tests did not run
GitLab jobs. Full GitLab job acceptance, real Docker and Windows jobs, and
production acceptance are still pending. Suspend/resume is disabled.

- [Operating guide](docs/operations.md): credentials, connections, and recovery.
- [Infrastructure guide](infra/README.md): images, networking, and teardown.
- [Acceptance report](docs/acceptance-report.md): what has been tested and what remains.
- [Testing guide](docs/testing.md): reproducible checks and explicit live tests.
- [Compatibility and limitations](docs/upstream-parity.md): upstream capability mapping.
- [Contributing](CONTRIBUTING.md): development commands and checks.
- [Design specification](docs/specification.md): architecture and requirements.
