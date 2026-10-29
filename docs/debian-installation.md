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

Every PR and push to main tests the service lifecycle on Ubuntu 24.04 amd64.
The [public release check](https://github.com/euyuil/fleeting-plugin-aliyun/actions/workflows/verify-release.yml)
tests downloaded packages on native Ubuntu amd64 and arm64 hosts.

## Download and install

Prepare the dedicated ESS group, worker image and SSH access described in
[installation prerequisites](installation.md#worker-and-cloud-prerequisites).
Install or reuse the tested GitLab Runner version from the
[official package repository](https://docs.gitlab.com/runner/install/linux-repository/).
Follow its specific-version instructions, including the matching helper images
package. The currently tested version is recorded in
[`upstream.lock.json`](../upstream.lock.json) (19.5.1). Keep any existing Runner
pools separate.

In an empty download directory on the Manager host, run:

```sh
package="fleeting-plugin-aliyun_0.1.0~rc.3_$(dpkg --print-architecture).deb"
release_url='https://github.com/euyuil/fleeting-plugin-aliyun/releases/download/v0.1.0-rc.3'
curl --fail --location --retry 3 "$release_url/$package" --output "$package"
curl --fail --location --retry 3 "$release_url/SHA256SUMS" --output SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
```

The checksum file covers native archives and Debian packages. `--ignore-missing`
checks the files you downloaded. Continue only if your `.deb` reports `OK`:

```sh
sudo apt install "./fleeting-plugin-aliyun_0.1.0~rc.3_$(dpkg --print-architecture).deb"
fleeting-plugin-aliyun version
fleeting-plugin-aliyun manager help
```

Use `amd64` for x86-64 hosts and `arm64` for ARM64 hosts. Debian uses version
`0.1.0~rc.3` so it sorts before the final `0.1.0` release; the program reports
`0.1.0-rc.3` and its source revision. Keep the downloaded package for rollback.
Installation does not start or enable the service and does not call GitLab or
Alibaba Cloud. It does not download a second plugin through OCI.

## Configure a pool

Create a project runner in GitLab as described in
[Quick Start step 2](../README.md#2-create-a-runner-in-gitlab), and save its runner
authentication token before continuing. Workers do not need their own registrations.

Copy one example to a private directory on the manager:

```sh
umask 077
mkdir -p "$HOME/aliyun-runner-setup"
cp /usr/share/doc/fleeting-plugin-aliyun/examples/instance/config.toml \
  "$HOME/aliyun-runner-setup/config.toml"
```

For Docker jobs, copy
`/usr/share/doc/fleeting-plugin-aliyun/examples/docker-autoscaler/config.toml`
instead. Edit the copied file locally:

| Setting | Your value |
| --- | --- |
| `url` | Your GitLab URL, reachable by the Manager and workers |
| `token` | The project runner authentication token |
| `region` | The region of your ESS group |
| `scaling_group_id` | Your dedicated ESS group ID |
| `username` | The SSH user in your worker image, such as `ci` |
| `use_external_addr` | `false` for a reachable private worker network; otherwise `true` |
| `runners.docker.image` | For Docker jobs, a job image pinned by digest |

Replace all `REPLACE_WITH_` values and review the instance limits. Keep the
example's `plugin` and `key_path` values for now: setup replaces them with the
installed binary and copied key paths. CPU and memory are selected by the ESS
group's instance type. Keep tokens and resource identifiers private.

Copy your unencrypted worker SSH private key to
`$HOME/aliyun-runner-setup/worker.key` and set its mode to `0600`. The worker image
must allow the matching public key for the chosen SSH user. For credentials,
prefer a refreshable source such as the manager's ECS RAM role. If an environment
file is needed, create `$HOME/aliyun-runner-setup/manager.env` with mode `0600`
in systemd `EnvironmentFile` format:

```ini
ALIBABA_CLOUD_ACCESS_KEY_ID=REPLACE_WITH_ACCESS_KEY_ID
ALIBABA_CLOUD_ACCESS_KEY_SECRET=REPLACE_WITH_ACCESS_KEY_SECRET
ALIBABA_CLOUD_SECURITY_TOKEN=REPLACE_WITH_STS_TOKEN
```

Replace the placeholders with your values. Omit `ALIBABA_CLOUD_SECURITY_TOKEN`
when the credentials have no session token. Temporary STS values expire; this
file does not refresh them. Use single-line `ALIBABA_CLOUD_*` assignments. Do
not use shell commands or `export` in it. These variables belong to the manager,
not CI jobs.

```sh
sudo fleeting-plugin-aliyun manager setup \
  --config "$HOME/aliyun-runner-setup/config.toml" \
  --ssh-key "$HOME/aliyun-runner-setup/worker.key" \
  --credentials "$HOME/aliyun-runner-setup/manager.env"
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

Setup supports one Linux `instance` or `docker-autoscaler` pool per host. Use a
separate Manager host for an additional pool, or manage multiple pools through
a manual Runner setup. Setup requires positive finite autoscaler limits,
static SSH credentials and worker deletion
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
```

Start checks the local setup and enables the service at boot. Queued jobs can
then create billable workers. Continue with the
[first job and cleanup check](../README.md#5-run-a-job-and-check-cleanup).

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
