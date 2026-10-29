# GitLab Runner Autoscaling on Alibaba Cloud (Aliyun)

Run GitLab CI/CD jobs on Alibaba Cloud ECS machines that scale with your workload.
`fleeting-plugin-aliyun` connects GitLab Runner to Alibaba Cloud Auto Scaling
(ESS). The **Runner Manager** runs as a service and starts the plugin; jobs run
on separate ECS **workers**, which Runner releases after use.

This community project is under active development. Use
[`0.1.0-rc.3`](https://github.com/euyuil/fleeting-plugin-aliyun/releases/tag/v0.1.0-rc.3)
for evaluation. Full Linux amd64 VM and Docker job cycles passed with `0.1.0-rc.2`
and Runner 19.5.1. Other images, larger pools, Windows, full arm64 jobs, and
production acceptance still need validation. Suspend/resume is disabled.

| Mode | Where the job runs | What the worker needs |
|---|---|---|
| **VM — default** | Directly on an ECS VM, with the Instance executor | Linux, SSH, Git, GitLab Runner, and your build tools |
| **Container** | In a Docker container on an ECS VM, with Docker Autoscaler | A prepared Docker host and a job container image |

## Quick start: run a job on a Linux VM

Use an Ubuntu 24.04 or Debian 12 host with systemd, sudo access, and Linux amd64
or arm64. Run the shell commands below on this Manager host in the same shell.

### Before you start

You need a GitLab project where you can create a runner, plus:

- A worker image with SSH, Git, Runner 19.5.1, `timeout`, a writable job directory,
  and your build tools. Prepare `/var/lib/fleeting-worker/ready` after image setup, or adapt
  the example's `instance_ready_command`. Keep the matching unencrypted SSH
  private key; its public key must be authorized for the worker's SSH user.
- A pinned launch-template version and a dedicated, active ESS group with desired
  capacity enabled, unit instance weights, minimum and desired capacity `0`, and
  maximum capacity `1`. Use one Manager as the group's only capacity writer.
  Configure the template's system disk and any temporary data disks to be released
  with the instance; use only disposable worker disks for this quick start.
- Cloud permissions for the Manager, SSH access from it to workers, and HTTPS
  access from both to GitLab. Workers also need access to job dependencies.

Check the [cloud and worker requirements](docs/operations.md#worker-and-cloud-prerequisites).
ECS workers, disks, images, and network traffic can cost money.

### 1. Install GitLab Runner and the plugin package

Reuse Runner 19.5.1 if installed at `/usr/bin/gitlab-runner`, the path required by
the packaged service. Check with `/usr/bin/gitlab-runner --version`. Otherwise,
in an empty download directory, install it from the
[official repository](https://docs.gitlab.com/runner/install/linux-repository/):

```sh
sudo apt update
sudo apt install ca-certificates curl less
curl --fail --location --retry 3 \
  https://packages.gitlab.com/install/repositories/runner/gitlab-runner/script.deb.sh \
  --output script.deb.sh
less script.deb.sh
```

After reviewing the script, add the repository and install the matching packages:

```sh
sudo bash script.deb.sh
sudo apt install gitlab-runner=19.5.1-1 gitlab-runner-helper-images=19.5.1-1
```

With `curl` available, download and verify the plugin in your download directory:

```sh
package="fleeting-plugin-aliyun_0.1.0~rc.3_$(dpkg --print-architecture).deb"
asset="fleeting-plugin-aliyun_0.1.0.rc.3_$(dpkg --print-architecture).deb"
release_url='https://github.com/euyuil/fleeting-plugin-aliyun/releases/download/v0.1.0-rc.3'
curl --fail --location --retry 3 "$release_url/$asset" --output "$package"
curl --fail --location --retry 3 "$release_url/SHA256SUMS" --output SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
```

Continue only if your `.deb` reports `OK`, then install it:

```sh
sudo apt install "./$package"
```

The plugin package leaves its dedicated Runner service stopped.

### 2. Create a runner in GitLab

In **Settings > CI/CD > Runners**, create a project runner with the tag
`aliyun-vm` and **Run untagged jobs** turned off. Save its runner authentication
token for the Manager configuration below. Workers do not register with GitLab.

### 3. Configure and start the Manager

Copy the packaged VM example and your worker SSH key to a private directory outside
your checkout. Replace `/path/to/worker.key` with your existing private-key path:

```sh
umask 077
setup_dir="$HOME/aliyun-runner-setup"
mkdir -p "$setup_dir"
cp /usr/share/doc/fleeting-plugin-aliyun/examples/instance/config.toml "$setup_dir/config.toml"
install -m 0600 /path/to/worker.key "$setup_dir/worker.key"
```

Edit `config.toml` in that directory: set `url`, `token`, `region`,
`scaling_group_id`, and the worker SSH `username`. Set `use_external_addr = false`
for a reachable private worker network. Keep the example's single-worker limits;
setup replaces `plugin` and `key_path` for you.

Prefer the Manager's ECS RAM role for cloud credentials. If you need an environment
file instead, create `$setup_dir/manager.env` with mode `0600` and fill in:

```ini
ALIBABA_CLOUD_ACCESS_KEY_ID=REPLACE_WITH_ACCESS_KEY_ID
ALIBABA_CLOUD_ACCESS_KEY_SECRET=REPLACE_WITH_ACCESS_KEY_SECRET
ALIBABA_CLOUD_SECURITY_TOKEN=REPLACE_WITH_STS_TOKEN
```

Omit the security-token line for credentials without a session token. Use plain
assignments, without `export`. STS values expire and this file does not refresh
them. Keep cloud credentials out of `config.toml` and CI job variables.

For an ECS RAM role, run the setup command below as written. For an environment
file, add `--credentials "$setup_dir/manager.env"` to it. The service does not
inherit credentials exported in your shell.

```sh
sudo fleeting-plugin-aliyun manager setup \
  --config "$setup_dir/config.toml" \
  --ssh-key "$setup_dir/worker.key"
```

Setup checks the local configuration. Start the service when ready to accept jobs
and create billable workers; it also starts automatically at boot:

```sh
sudo fleeting-plugin-aliyun manager start
```

### 4. Run a job and check cleanup

Create `.gitlab-ci.yml` at your project's repository root, or add this job to its
existing file. If the existing `stages` list has no `test` stage, replace
`stage: test` below with a stage from that list. Commit and push the file to
GitLab, then run a pipeline for that branch if one was not triggered automatically:

```yaml
hello-aliyun:
  stage: test
  tags: [aliyun-vm]
  script:
    - printf 'Hello from Alibaba Cloud\n' > hello.txt
  artifacts:
    paths: [hello.txt]
```

Confirm the job succeeds and download `hello.txt`. Runner should create one worker,
run one job, and release it. In Alibaba Cloud, verify ESS desired capacity returns
to `0`, the group has no members, and the worker and its disks are gone.
The ESS group, image, and network remain available for the next job.

To pause the pool, run `sudo fleeting-plugin-aliyun manager stop`; it waits for
active jobs to finish. Confirm worker cleanup in cloud inventory after stopping.
Use the [infrastructure guide](infra/README.md#teardown-and-cost-boundaries) when
retiring the reusable resources.

## Other configurations

ECS CPU and memory come from the launch template's instance type. Use separate
pools and job tags for different sizes; `capacity_per_instance` is the number of
concurrent job slots per VM. See [sizing and pool selection](docs/operations.md#choose-cpu-and-memory).

For container jobs, use the packaged Docker Autoscaler example with a Docker-ready
worker and a pinned job image. See [Docker and Windows workers](docs/operations.md#other-prepared-workers)
for setup and container limits. For installation without the Debian package, use
the [manual OCI service guide](docs/installation.md).

## Documentation

- [Debian package guide](docs/debian-installation.md): package details, credentials, diagnostics, upgrades, and removal.
- [Operating guide](docs/operations.md): cloud prerequisites, worker modes, sizing, and recovery.
- [Acceptance report](docs/acceptance-report.md): tested scope and remaining limitations.
- [Contributing](CONTRIBUTING.md): development, testing, and design documentation.
