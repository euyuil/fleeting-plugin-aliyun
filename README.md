# GitLab Runner Autoscaling on Alibaba Cloud (Aliyun)

Run GitLab CI/CD jobs on Alibaba Cloud ECS machines that scale with your workload.
`fleeting-plugin-aliyun` connects GitLab Runner to Alibaba Cloud Auto Scaling
(ESS). Runner requests workers when jobs need them and releases them after use.

Install GitLab Runner, install this plugin, and run Runner as a service. Runner
starts and manages the plugin for you. This running Runner process is called the
**Runner Manager**. You can run it on the same server as GitLab or on another host.
Your jobs run on separate ECS **workers**.

This is a community project under active development. Real cloud scaling and SSH
tests have passed. Full Linux amd64 VM and Docker job cycles have also passed
with Runner 19.5.1; see the [acceptance report](docs/acceptance-report.md#released-plugin-vm-and-docker-jobs).
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
PowerShell and WinRM HTTPS. Windows jobs have not yet been tested on real cloud
workers. See [supported configurations](docs/operations.md).

## Quick start: run a job on a Linux VM

### Before you start

You need a Linux amd64 or arm64 host for Runner, a GitLab project where you can
create a runner, and prepared Alibaba Cloud resources:

- A worker image with SSH, Git, the matching Runner binary, and your build tools.
- A launch template and a dedicated, active ESS group. Start with minimum and
  desired capacity `0` and maximum capacity `1`.
- Cloud permissions for the Manager and a working network route to the workers.

See [worker and cloud prerequisites](docs/installation.md#worker-and-cloud-prerequisites)
for the full checklist. You can use existing resources that meet these requirements.
Spot workers, disks, images, and network traffic can cost money.

### 1. Install or reuse GitLab Runner

Use **GitLab Runner 19.5.1**, the version tested with this prerelease. Follow the
[official Linux installation guide](https://docs.gitlab.com/runner/install/linux-repository/)
and its instructions for installing a specific version. Check your installation:

```sh
gitlab-runner --version
```

Keep any existing Runner configuration. The service guide below uses a separate
service and private directories for this worker pool.

### 2. Create a runner in GitLab

In your project, open **Settings > CI/CD > Runners** and create a project runner.
Give it the tag `aliyun-vm` and turn off **Run untagged jobs**. Save its runner
authentication token. This is the token the Manager needs to receive jobs.
Workers do not register themselves with GitLab.

### 3. Configure Runner and install the plugin

Save the complete [VM configuration example](examples/instance/config.toml) as
`config.toml` in a private directory outside your checkout. It already includes
the OCI plugin reference and the Linux shell settings. Fill in these values:

| Setting | Your value |
|---|---|
| `url` | Your GitLab URL, reachable by the Manager and workers |
| `token` | The runner authentication token from step 2 |
| `region` | The Alibaba Cloud region of your ESS group |
| `scaling_group_id` | Your dedicated ESS group ID |
| `username` | The SSH user in your worker image, such as `ci` |
| `key_path` | The private-key path used by the service setup |

Keep `plugin = "ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.2"` pinned to this
exact version. If using a reachable private worker network, set
`use_external_addr = false`.

Follow the [Linux service setup](docs/installation.md#linux-service-setup) to
place the configuration and key in private directories, configure cloud
credentials, and run `gitlab-runner fleeting install` as the service user.
The public plugin package needs no registry login or Docker daemon.

### 4. Start Runner as a service

Complete the [systemd service setup](docs/installation.md#start-the-service),
then check its status and logs using the commands in that guide. The service
uses the same configuration, credentials, and plugin directory as the install
step. It keeps running when you disconnect from the host.

Starting the service allows queued jobs to create billable workers. Use one
Manager configuration per ESS group. Runner starts the plugin automatically.

### 5. Run a job and check cleanup

Add this job to your project's `.gitlab-ci.yml`, commit it, and run a pipeline:

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

Check the job output and download `hello.txt`. With the example settings, Runner
creates one worker, runs one job, and releases the worker after use. Confirm that
ESS desired capacity returns to `0` and that the worker and its disks are gone.
The ESS group, image, and network remain available for the next job. See
[teardown](infra/README.md#teardown-and-cost-boundaries) when you no longer need them.

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
instead of the VM example. This selects where jobs run; running the Manager
itself in Docker is a separate deployment choice. Prepare a worker image with
Docker Engine, allow the SSH user to access Docker, and set a job image pinned
by digest. Jobs can then use
the Docker executor's `image` and `services` features. The example includes the
same pinned OCI plugin. Use the service setup with this configuration and a
dedicated ESS group for the Docker pool.

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
the remaining worker after the other was released. Linux amd64 VM and Docker
jobs also passed checkout, scripts, artifacts, and normal release to zero.
Other images, larger pools, Windows, full arm64 jobs, and production acceptance
still need validation. Suspend/resume is disabled.

- [Linux service setup](docs/installation.md): configuration, plugin installation, and systemd.
- [Operating guide](docs/operations.md): credentials, connections, and recovery.
- [Infrastructure guide](infra/README.md): images, networking, and teardown.
- [Acceptance report](docs/acceptance-report.md): what has been tested and what remains.
- [Testing guide](docs/testing.md): reproducible checks and explicit live tests.
- [Compatibility and limitations](docs/upstream-parity.md): upstream capability mapping.
- [Contributing](CONTRIBUTING.md): development commands and checks.
- [Design specification](docs/specification.md): architecture and requirements.
