# 0.1.0-rc.3

This prerelease adds Debian packages and local Runner management commands.
Use it for evaluation with GitLab Runner 19.5.1. The repository, Debian package,
and executable share the name `fleeting-plugin-aliyun`.

## Install

Download the `.deb` for your Manager's architecture and `SHA256SUMS` from this
Release. Verify the checksum, then install the local package with APT. GitLab
Runner is installed separately. See the
[Quick Start](https://github.com/euyuil/fleeting-plugin-aliyun/blob/v0.1.0-rc.3/README.md#quick-start-run-a-job-on-a-linux-vm)
and [Debian guide](https://github.com/euyuil/fleeting-plugin-aliyun/blob/v0.1.0-rc.3/docs/debian-installation.md)
for download commands, configuration, upgrades, rollback, and removal.

Release assets include Linux amd64 and arm64 `.deb` packages, native archives,
SHA-256 checksums, and a dependency inventory. Debian package version
`0.1.0~rc.3` corresponds to plugin version `0.1.0-rc.3`.
The OCI plugin is `ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.3`.

## Changes since 0.1.0-rc.2

- Add `manager setup`, `doctor`, `start`, `stop`, and `status` commands for a
  dedicated Linux Runner service. Setup installs private configuration and
  selects the packaged plugin automatically.
- Add `.deb` packages with a systemd unit and safe package lifecycle handling.
  Installation does not start jobs. Upgrade and removal require a stopped
  service; private configuration and Runner identity are preserved.
- Test real dpkg/systemd installation, repeated setup, permissions, graceful
  stopping, upgrade, rollback, removal, and retained state in credential-free
  PR and main CI. Extend public release checks to downloaded Debian packages
  on native amd64 and arm64 hosts.
- Make the customer Quick Start use Release downloads and the manager commands.
  Keep manual OCI installation as an alternative.
- Add a mandatory six-scenario cloud gate before publication, with separate
  disposable fixtures, bounded Spot workers, per-scenario cleanup, and an
  independent janitor. Scope cleanup tokens to each fixture group and report
  sanitized diagnostics without private account or resource information.

## Validation and limits

Publication requires offline checks and all six cloud scenarios to pass for
the exact candidate, including complete fixture cleanup. The publisher reuses
those accepted binaries. The scenarios cover scale-out, specified scale-in,
ready-worker restart, restart during creation and removal, and instrumented
loss of an expansion reply. They test the plugin on Linux amd64; they do not
run GitLab CI jobs.

Full Linux VM and Docker job cycles previously passed with `0.1.0-rc.2` and
Runner 19.5.1. Those results are separate from this release's cloud gate and
public download checks. See the
[acceptance report](https://github.com/euyuil/fleeting-plugin-aliyun/blob/v0.1.0-rc.3/docs/acceptance-report.md)
for the exact sources, evidence, and remaining gaps.

The packaged service supports one Linux VM or Docker Autoscaler pool per host.
The package does not create your permanent cloud infrastructure or register a
runner in GitLab. `doctor` checks local setup, not cloud permissions or job
connectivity. This release does not claim production acceptance, real Windows
jobs, genuine Spot interruption, or full upstream capability parity.

Suspend/resume is disabled. The pinned upstream SSH connector does not verify
server host keys, and its WinRM connector does not verify server certificates.
Use an isolated, trusted network as described in the
[connection guide](https://github.com/euyuil/fleeting-plugin-aliyun/blob/v0.1.0-rc.3/docs/adr/0003-state-and-connections.md).

The earlier releases remain unchanged. `0.1.0-rc.1` has a known OCI installation
failure; `0.1.0-rc.2` fixed it but does not contain Debian packages or manager commands.
