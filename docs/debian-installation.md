# Debian package installation

Use this path on Ubuntu 24.04 or Debian 12 with systemd and a Linux amd64 or
arm64 manager. The package, executable, and repository are all named
`fleeting-plugin-aliyun`. This package includes the plugin and local manager
commands. GitLab Runner is installed separately.

The package is built and tested in GitHub Actions on every PR and push to main.
Until a release containing this feature is published, download the `release-candidate`
artifact from a successful **main** CI run for evaluation. Existing `0.1.0-rc.2`
releases do not contain a Debian package. A hosted APT repository is not provided.
CI executes the service lifecycle on Ubuntu 24.04 amd64 and checks the payloads
of both architecture packages; arm64 lifecycle execution is not covered yet.

## Install

Prepare the dedicated ESS group, worker image, SSH access and GitLab Runner
registration described in [installation prerequisites](installation.md#worker-and-cloud-prerequisites).
Install the tested GitLab Runner version from the
[official package repository](https://docs.gitlab.com/runner/install/linux-repository/).
The currently tested version is recorded in `upstream.lock.json` (19.5.1).
Keep any existing Runner pools separate.

Download the package for the manager's architecture and its `SHA256SUMS` file.
Check the package checksum against that file. Then install the local package:

```sh
sudo apt install ./fleeting-plugin-aliyun_VERSION_amd64.deb
fleeting-plugin-aliyun version
fleeting-plugin-aliyun manager help
```

Replace `VERSION` with the downloaded package version and use `arm64` when
appropriate. Debian prerelease versions use `~`, for example `0.1.0~rc.3`, so they
sort before `0.1.0`. The program still reports the original release version.
Installation does not start or enable the service and does not call GitLab or
Alibaba Cloud. It does not download a second plugin through OCI.

## Configure a pool

Copy one example to a private directory on the manager:

```sh
umask 077
mkdir -p "$HOME/aliyun-runner-setup"
cp /usr/share/doc/fleeting-plugin-aliyun/examples/instance/config.toml \
  "$HOME/aliyun-runner-setup/config.toml"
```

Use `examples/docker-autoscaler/config.toml` for Docker jobs. Edit the copied
file locally. Replace all `REPLACE_WITH_` values, select the worker region and
SSH username, and review the instance limits. CPU and memory are selected by
the ESS group's instance type. Keep tokens and resource identifiers private.

Prepare an unencrypted worker SSH private key with mode `0600`. For credentials,
prefer a refreshable source such as the manager's ECS RAM role. If an environment
file is needed, create a private `0600` file in systemd `EnvironmentFile` format:

```ini
ALIBABA_CLOUD_ACCESS_KEY_ID=REPLACE_WITH_ACCESS_KEY_ID
ALIBABA_CLOUD_ACCESS_KEY_SECRET=REPLACE_WITH_ACCESS_KEY_SECRET
ALIBABA_CLOUD_SECURITY_TOKEN=REPLACE_WITH_STS_TOKEN
```

Temporary STS values expire; this file does not refresh them. Use single-line
`ALIBABA_CLOUD_*` assignments. Do not use shell commands or `export` in it. These variables belong to the manager, not CI jobs.

```sh
sudo fleeting-plugin-aliyun manager setup \
  --config "$HOME/aliyun-runner-setup/config.toml" \
  --ssh-key /path/to/private/worker.key \
  --credentials /path/to/private/manager.env
sudo fleeting-plugin-aliyun manager doctor
```

Omit `--credentials` to use the service account's default credential chain, such
as its ECS RAM role. The root user's environment and home are not inherited by
the service. File-based credential sources must be readable by the service account.

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

Setup supports one Linux `instance` or `docker-autoscaler` pool. It requires
positive finite autoscaler limits, static SSH credentials and worker deletion
on shutdown. Repeating identical setup is safe. It refuses to overwrite edited
files or adopt an existing manual installation. For later changes, stop the
service, back up and edit its private files, run `doctor`, then start it again.

`doctor` checks local configuration, ownership, permissions, service identity and
parsing with the installed Runner as the service user. It never prints Runner
`list` output, which can include authentication tokens. It does **not** test
GitLab access, cloud credentials, ESS permissions, SSH reachability or real jobs.
Validate those with the [first job and cleanup check](../README.md#5-run-a-job-and-check-cleanup).

## Start and stop

```sh
sudo fleeting-plugin-aliyun manager start
sudo fleeting-plugin-aliyun manager status
sudo journalctl -u gitlab-runner-aliyun.service -n 100 --no-pager
sudo fleeting-plugin-aliyun manager stop
```

Start checks the local setup and enables the service at boot. Queued jobs can
then create billable workers. Stop sends `SIGQUIT` through systemd and waits
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
