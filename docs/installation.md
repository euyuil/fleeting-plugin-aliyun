# Run GitLab Runner as a Linux service

This guide uses a separate systemd service on Ubuntu or Debian. It can run on
the GitLab host or another Linux host. Jobs run on remote ECS workers. Runner
starts the plugin as a child process; the plugin needs no separate service.

Use GitLab Runner 19.5.1 and plugin `0.1.0-rc.2`, the combination used for the
recorded Linux VM and Docker job checks. See the [Quick Start](../README.md#quick-start-run-a-job-on-a-linux-vm).

## Worker and cloud prerequisites

Prepare these resources before starting Runner:

- A worker image with SSH, Git, a writable job directory, the matching Runner
  binary, and the tools needed by your jobs. The examples wait for
  `/var/lib/fleeting-worker/ready`; create that marker after preparing the image,
  or replace `instance_ready_command` with a check suitable for your image.
- For Docker jobs, a working Docker Engine on the worker, access to it for the
  SSH user, and a pinned job image. See the [Docker example](../examples/docker-autoscaler/config.toml).
- A launch template with the image, instance type, disk, network, and desired
  Spot policy. Pin its version. CPU and memory come from the instance type.
- A dedicated, active ESS group with desired capacity enabled, minimum `0`, a
  finite maximum, and unit instance weights. For the first job, use desired `0`
  and maximum `1`. Follow the [group compatibility rules](operations.md#dedicated-ess-group).
- A cloud identity with the [required permissions](operations.md#authentication-and-permissions).
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

## Linux service setup

Install the pinned Runner version using the
[official package instructions](https://docs.gitlab.com/runner/install/linux-repository/).
Confirm the executable path and version:

```sh
command -v gitlab-runner
gitlab-runner --version
```

The commands below use `/usr/bin/gitlab-runner`. If your installation uses a
different path, use that path in both the install commands and the service unit.

This is a new, separate setup named `gitlab-runner-aliyun`. Keep any existing
Runner services and configuration. If this service name or its directories
already belong to an installation, review that installation before proceeding.
Never run two Manager configurations against the same ESS group.

### Create the service account and private directories

```sh
sudo useradd --system --user-group --create-home \
  --home-dir /var/lib/gitlab-runner-aliyun \
  --shell /usr/sbin/nologin gitlab-runner-aliyun
sudo install -d -m 0700 -o gitlab-runner-aliyun -g gitlab-runner-aliyun \
  /etc/gitlab-runner-aliyun \
  /var/lib/gitlab-runner-aliyun \
  /var/lib/gitlab-runner-aliyun/plugins
```

### Add the Runner configuration and SSH key

Save the complete [VM example](../examples/instance/config.toml) as
`config.toml` in a private directory outside your checkout. Use the
[Docker example](../examples/docker-autoscaler/config.toml) for Docker jobs.
These examples already include the OCI plugin reference and Linux shell flag.

Set your GitLab URL, runner authentication token, cloud region, ESS group, and
worker SSH user. Set `key_path` to `/etc/gitlab-runner-aliyun/worker.key`. Use
`use_external_addr = false` if the Manager can reach worker private addresses.
For Docker jobs, also set the job image. Keep all cloud access keys out of this
TOML file and out of `[runners].environment`, which is passed to jobs.

Place the matching SSH private key in your private directory as `worker.key`.
Its public key must already be authorized in the worker image. From that directory:

```sh
chmod 600 config.toml worker.key
sudo install -m 0600 -o gitlab-runner-aliyun -g gitlab-runner-aliyun \
  config.toml /etc/gitlab-runner-aliyun/config.toml
sudo install -m 0600 -o gitlab-runner-aliyun -g gitlab-runner-aliyun \
  worker.key /etc/gitlab-runner-aliyun/worker.key
```

The configuration directory is writable by the service account because Runner
creates its `.runner_system_id` there. Keep that file when restarting the service.

### Configure cloud credentials for the service

Use a refreshable credential source for ongoing use, such as an instance role,
OIDC, or a credential process. Configure it for the `gitlab-runner-aliyun` account,
whose home is `/var/lib/gitlab-runner-aliyun`. See
[authentication and permissions](operations.md#authentication-and-permissions).

Create `/etc/gitlab-runner-aliyun/manager.env` with any environment variables
needed by that credential source. Create it even if it contains only a comment:

```sh
sudoedit /etc/gitlab-runner-aliyun/manager.env
sudo chown root:root /etc/gitlab-runner-aliyun/manager.env
sudo chmod 600 /etc/gitlab-runner-aliyun/manager.env
```

The file uses `NAME=value` lines, without `export`. For a short trial with
temporary STS credentials, its contents can be:

```ini
ALIBABA_CLOUD_ACCESS_KEY_ID=REPLACE_WITH_TEMPORARY_ACCESS_KEY_ID
ALIBABA_CLOUD_ACCESS_KEY_SECRET=REPLACE_WITH_TEMPORARY_ACCESS_KEY_SECRET
ALIBABA_CLOUD_SECURITY_TOKEN=REPLACE_WITH_TEMPORARY_SECURITY_TOKEN
```

Replace all three values. Static STS variables do not refresh themselves. Stop
accepting jobs and refresh the file before they expire, then restart the service.
When using another credential source, omit these STS lines entirely, including
their placeholders. A service does not inherit credentials exported in your
interactive shell. systemd reads this file and supplies it only to the Manager.

### Install and list the OCI plugin

Run these commands as the same account and with the same plugin directory used
by the service. Installation downloads a public plugin package; it does not
start Runner or create workers. Registry login and Docker are not required.

```sh
sudo -u gitlab-runner-aliyun env \
  HOME=/var/lib/gitlab-runner-aliyun \
  FLEETING_PLUGIN_PATH=/var/lib/gitlab-runner-aliyun/plugins \
  /bin/sh -c 'cd "$HOME" && exec /usr/bin/gitlab-runner fleeting --config /etc/gitlab-runner-aliyun/config.toml install'
sudo -u gitlab-runner-aliyun env \
  HOME=/var/lib/gitlab-runner-aliyun \
  FLEETING_PLUGIN_PATH=/var/lib/gitlab-runner-aliyun/plugins \
  /bin/sh -c 'cd "$HOME" && exec /usr/bin/gitlab-runner fleeting --config /etc/gitlab-runner-aliyun/config.toml list'
```

The GHCR reference is a Fleeting plugin package. It is not a container image for
`docker run`. Hosting the Manager in Docker is a separate deployment choice;
it does not determine whether jobs use the VM or Docker executor.

## Start the service

Create `/etc/systemd/system/gitlab-runner-aliyun.service` with `sudoedit` and
paste this unit:

```ini
[Unit]
Description=GitLab Runner with the Alibaba Cloud Fleeting plugin
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=gitlab-runner-aliyun
Group=gitlab-runner-aliyun
WorkingDirectory=/var/lib/gitlab-runner-aliyun
Environment=HOME=/var/lib/gitlab-runner-aliyun
Environment=FLEETING_PLUGIN_PATH=/var/lib/gitlab-runner-aliyun/plugins
EnvironmentFile=/etc/gitlab-runner-aliyun/manager.env
ExecStart=/usr/bin/gitlab-runner run --config /etc/gitlab-runner-aliyun/config.toml --working-directory /var/lib/gitlab-runner-aliyun
Restart=on-failure
RestartSec=5s
KillSignal=SIGQUIT
KillMode=mixed
TimeoutStopSec=7200
UMask=0077

[Install]
WantedBy=multi-user.target
```

`SIGQUIT` stops new job acquisition and waits for current jobs. The stop timeout
must exceed your longest job plus worker cleanup time; this example allows two
hours. At the deadline, systemd can force termination. See the
[Runner service documentation](https://docs.gitlab.com/runner/configuration/init/).

Starting the service allows queued jobs to create billable workers. After
checking the configured group and limits, enable and start it:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now gitlab-runner-aliyun.service
sudo systemctl status gitlab-runner-aliyun.service
sudo journalctl -u gitlab-runner-aliyun.service -n 100 --no-pager
```

Check that the Runner appears online in GitLab and that the logs contain no
plugin, credential, or group initialization errors. An active service alone
does not prove that a job can run. Return to the
[first job and cleanup check](../README.md#5-run-a-job-and-check-cleanup).

## Stop or update this service

Use `sudo systemctl stop gitlab-runner-aliyun.service` to stop taking jobs and
wait for active jobs to finish. The supplied examples request worker deletion
on shutdown. Check the logs and cloud inventory to confirm release; an abrupt
host failure or a cleanup error can leave resources behind.

Before an update, stop this service, verify cleanup, and preserve its private
configuration and state. Install the chosen compatible Runner/plugin versions
using the same service account and paths, then start the service again. If the
Runner package is shared with other pools, coordinate its update with their owners.
Keep permanent ESS, image, and network resources until you deliberately retire
the pool. See [operations and recovery](operations.md#recovery-and-limits).
