# Debian package installation

Use this path on Ubuntu 24.04 or Debian 12 with systemd and a Linux amd64 or
arm64 manager. The package, executable, and repository are all named
`fleeting-plugin-aliyun`. This package includes the plugin and local manager
commands. GitLab Runner is installed separately.

Download `.deb` packages from the
[0.1.0-rc.3 Release](https://github.com/euyuil/fleeting-plugin-aliyun/releases/tag/v0.1.0-rc.3).
Downloads are public and need no GitHub login. A hosted APT repository is not
provided; install the downloaded file with APT. Versions before `0.1.0-rc.3`
do not include Debian packages or `manager` commands.

Every PR and push to main tests the same candidate package on native Ubuntu
24.04 amd64 and arm64, plus isolated Debian 12 amd64 with real systemd. The same
platform checks must pass before release cloud acceptance starts. Debian 12 arm64
is not a separately tested userspace configuration.
The [public release check](https://github.com/euyuil/fleeting-plugin-aliyun/actions/workflows/verify-release.yml)
tests downloaded packages on native Ubuntu amd64 and arm64 hosts.

## Download and install

Follow [Quick Start step 1](../README.md#1-install-gitlab-runner-and-the-plugin-package)
for the Runner installation, plugin download, checksum verification, and APT
commands. The tested Runner version is pinned in
[`upstream.lock.json`](../upstream.lock.json). Keep existing Runner pools separate.

GitHub replaces `~` with `.` in uploaded asset names. The Quick Start saves the
package under its original name so it matches `SHA256SUMS`. If downloading in a
browser, rename `0.1.0.rc.3` to `0.1.0~rc.3` before checking the checksum. The
package contents are unchanged. `--ignore-missing` checks only downloaded files;
continue only if the selected `.deb` reports `OK`.

Use `amd64` for x86-64 hosts and `arm64` for ARM64 hosts. Debian uses version
`0.1.0~rc.3` so it sorts before the final `0.1.0` release; the program reports
`0.1.0-rc.3` and its source revision. Keep the downloaded package for rollback.
Installation does not start or enable the service and does not call GitLab or
Alibaba Cloud. It does not download a second plugin through OCI.

To inspect installed versions and available commands:

```sh
gitlab-runner --version
fleeting-plugin-aliyun version
fleeting-plugin-aliyun manager help
```

## Configure a pool

[Quick Start steps 2 and 3](../README.md#2-create-a-runner-in-gitlab) cover the
GitLab runner token, private configuration, SSH key, credentials, and setup
command. For Docker jobs, use the [Docker worker configuration](operations.md#other-prepared-workers).

Omitting `--credentials` uses the service account's default credential chain,
such as its ECS RAM role. The root user's environment and home are not inherited
by the service. File-based credential sources must be readable by the service
account. For environment files, use single-line `ALIBABA_CLOUD_*` assignments in
systemd `EnvironmentFile` format. Do not use shell commands or `export`.
These variables belong to the Manager, not CI jobs. Temporary STS values do not
refresh themselves; stop the Manager and update them before they expire.

Setup creates the dedicated `gitlab-runner-aliyun` account and these private paths:

| Path | Purpose |
| --- | --- |
| `/etc/gitlab-runner-aliyun/` | Runner config, worker key, credential environment and Runner system ID |
| `/var/lib/gitlab-runner-aliyun/` | Service home and runtime state |
| `/var/lib/fleeting-plugin-aliyun/` | Root-owned installation receipt |

The configuration directory is root-owned with mode `0750` and the service group;
private files use `0600`. Setup pre-creates the service-owned Runner system ID so
Runner does not need directory write access. The home and receipt directories use
`0700`. Setup sets the native plugin path to `/usr/bin/fleeting-plugin-aliyun`
and copies the SSH key to the private configuration directory. Additional Runner
settings are preserved; TOML formatting and comments are not preserved in the copy.

Setup supports one Linux `instance` or `docker-autoscaler` pool per host. Use a
separate Manager host for an additional pool, or manage multiple pools through
a manual Runner setup. Setup requires positive finite autoscaler limits,
static SSH credentials and worker deletion
on shutdown. Repeating identical setup is safe. It refuses to overwrite edited
files or adopt an existing manual installation. For later changes, stop the
service, back up and edit its private files, run `doctor`, then start it again.

## Diagnostics

Both `manager setup` and `manager start` run the local checks automatically.
Use `doctor` separately after editing configuration or while troubleshooting,
and inspect service status and logs as needed:

```sh
sudo fleeting-plugin-aliyun manager doctor
sudo fleeting-plugin-aliyun manager status
sudo journalctl -u gitlab-runner-aliyun.service -n 100 --no-pager
```

`doctor` checks local configuration, ownership, permissions, service identity and
parsing with the installed Runner as the service user. It never prints Runner
`list` output, which can include authentication tokens. It does **not** test
GitLab access, cloud credentials, ESS permissions, SSH reachability or real jobs.
Validate those with the [first job and cleanup check](../README.md#4-run-a-job-and-check-cleanup).

## Start and stop

```sh
sudo fleeting-plugin-aliyun manager start
```

Start checks the local setup and enables the service at boot. Queued jobs can
then create billable workers. Continue with the
[first job and cleanup check](../README.md#4-run-a-job-and-check-cleanup).

When you want to pause the pool:

```sh
sudo fleeting-plugin-aliyun manager stop
```

Stop sends `SIGQUIT` through systemd and waits
for active jobs without a forced shutdown timeout. It can therefore take as
long as a job takes to finish. Confirm worker release in cloud inventory after
stopping; successful local shutdown alone does not prove cloud cleanup.
Logs can contain private operational information; inspect them locally.

## Upgrade and roll back

1. Download and verify the chosen package. Keep the previous package available.
2. Run `manager stop`, confirm worker cleanup, and back up the private paths above.
3. Install the new local `.deb` with `sudo apt install ./PACKAGE.deb`.
4. Run `manager doctor`, then `manager start` when ready.

The package refuses installation, upgrade or removal while the service is active,
starting, stopping or failed. Resolve failures and stop the service before retrying.
If systemd retains a `failed` state after stopping, inspect the journal and confirm
worker cleanup, then run `sudo systemctl reset-failed gitlab-runner-aliyun.service`.
If a package operation was interrupted, recover it with `sudo dpkg --configure -a`
or reinstall the chosen package before `manager start`.
It does not restart the pool after an upgrade. Configuration and Runner identity
are preserved. Updating a shared GitLab Runner package is a separate operation.

To roll back, stop and check cleanup, install the saved older package with
`sudo apt install --allow-downgrades ./PREVIOUS_PACKAGE.deb`, restore a compatible
private configuration if needed, then run `doctor` and `start`. There is no
promise of resuming a job that was interrupted outside a graceful stop.

## Remove

```sh
sudo fleeting-plugin-aliyun manager stop
# Confirm cloud worker cleanup before continuing.
sudo apt remove fleeting-plugin-aliyun
```

Removal disables the dedicated service and removes package-owned files. It
preserves the private directories, account and Runner identity, including on
`apt purge`, so credentials and recovery state are not silently lost. Reinstalling
the package can reuse them. After confirming cleanup and deliberately retiring
the pool, remove private state and the dedicated account yourself if no longer needed.
GitLab registrations, ESS groups, images and networks are not deleted. Other
Runner services and the separately installed GitLab Runner package are unchanged.

A manually created `/etc/systemd/system/gitlab-runner-aliyun.service` or systemd
drop-in is not automatically adopted. Preserve that installation and plan its
migration before using the package. The existing [OCI installation](installation.md)
remains available for that deployment style.
