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
is available through OCI. The new `.deb` package and `manager` commands are
available in main CI builds for evaluation; they are not in `0.1.0-rc.2`.

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

This path uses the `.deb` package on Ubuntu 24.04 or Debian 12 with systemd.
The package sets up a dedicated Runner service with `manager` commands. You
do not need to write a systemd unit or install another copy of the plugin.
For the released OCI version, use the [manual OCI service guide](docs/installation.md#linux-service-setup).

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

### 1. Install GitLab Runner and the plugin package

Install or reuse **GitLab Runner 19.5.1**. Follow the
[official Linux installation guide](https://docs.gitlab.com/runner/install/linux-repository/)
for that exact version, including the matching helper images package.
GitLab Runner is installed separately from this plugin.

Follow [Download and install](docs/debian-installation.md#download-and-install)
to get and verify the `release-candidate` artifact from a successful main CI run.
In the extracted directory, install the package for your host:

```sh
sudo apt install "./fleeting-plugin-aliyun_0.1.0~dev_$(dpkg --print-architecture).deb"
gitlab-runner --version
fleeting-plugin-aliyun version
```

Installation leaves the new service stopped. The [Debian guide](docs/debian-installation.md)
covers supported hosts and existing manual installations. There is no hosted
APT repository yet.

### 2. Create a runner in GitLab

In your project, open **Settings > CI/CD > Runners** and create a project runner.
Give it the tag `aliyun-vm` and turn off **Run untagged jobs**. Save its runner
authentication token. This is the token the Manager needs to receive jobs.
Workers do not register themselves with GitLab.

### 3. Configure the Manager

Follow [Configure a pool](docs/debian-installation.md#configure-a-pool) to copy
the packaged VM example, prepare your worker SSH key, and choose a cloud
credential source. Keep these files in a private directory outside your checkout.
Fill in the GitLab URL, runner token, region, ESS group ID, and SSH username.
If using a reachable private worker network, set `use_external_addr = false`.

Once the files are ready, set up the service and check it:

```sh
sudo fleeting-plugin-aliyun manager setup \
  --config "$HOME/aliyun-runner-setup/config.toml" \
  --ssh-key "$HOME/aliyun-runner-setup/worker.key" \
  --credentials "$HOME/aliyun-runner-setup/manager.env"
sudo fleeting-plugin-aliyun manager doctor
```

Omit `--credentials` when using a credential source available to the service,
such as the Manager's ECS RAM role. Setup creates the dedicated account and
private files. It replaces the example's OCI reference with the installed binary
and sets the copied SSH key path. `doctor` checks the local setup; the first job
below checks the connection to GitLab and the workers.

### 4. Start Runner as a service

```sh
sudo fleeting-plugin-aliyun manager start
sudo fleeting-plugin-aliyun manager status
```

The service starts at boot and keeps running when you disconnect. Queued jobs
can now create billable workers. Use one Manager per ESS group. Runner starts
the plugin automatically.

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

To pause the pool, run `sudo fleeting-plugin-aliyun manager stop`. It waits for
active jobs to finish. See the Debian guide for [logs and stopping](docs/debian-installation.md#start-and-stop),
[upgrades and rollback](docs/debian-installation.md#upgrade-and-roll-back), and
[removal](docs/debian-installation.md#remove).

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
the Docker executor's `image` and `services` features. Follow the same Quick Start
with the packaged Docker example and a dedicated ESS group for this pool.
`manager setup` selects the installed native plugin for either example.
The packaged service manages one pool per host; use a separate Manager host
for an additional pool, or manage multiple pools through a manual Runner setup.

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

- [Debian installation and lifecycle](docs/debian-installation.md): download, setup, upgrades, rollback and removal.
- [Manual OCI service setup](docs/installation.md): released plugin installation and a hand-written systemd unit.
- [Operating guide](docs/operations.md): credentials, connections, and recovery.
- [Infrastructure guide](infra/README.md): images, networking, and teardown.
- [Acceptance report](docs/acceptance-report.md): what has been tested and what remains.
- [Testing guide](docs/testing.md): reproducible checks and explicit live tests.
- [Compatibility and limitations](docs/upstream-parity.md): upstream capability mapping.
- [Contributing](CONTRIBUTING.md): development commands and checks.
- [Design specification](docs/specification.md): architecture and requirements.
