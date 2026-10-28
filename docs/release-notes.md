# 0.1.0-rc.1

This is the first prerelease of the Alibaba Cloud ESS Fleeting plugin. Use it for
evaluation with GitLab Runner 19.5.1. It includes Linux amd64 and arm64 binaries,
checksums, dependency license notices, and a Fleeting OCI package.

## What is included

- ECS worker scaling through a dedicated ESS group, including release of
  specified workers and recovery of saved operation intent.
- Linux SSH workers with the Instance executor as the default job mode.
- A separate Docker Autoscaler configuration and static Windows WinRM HTTPS
  connection support.
- Optional temporary Linux SSH keys through Cloud Assistant. This path needs a
  separately prepared worker image.

## Validation and limits

Before this release, real plugin-only Alibaba Cloud tests passed:

- `0 -> 1 -> authenticated SSH -> 0`.
- `0 -> 2 -> 1 -> 0`, with fresh SSH on the worker kept after scale-in.
- Plugin restart, rediscovery of the same ready worker, fresh SSH, and release.

See the [acceptance report](https://github.com/euyuil/fleeting-plugin-aliyun/blob/v0.1.0-rc.1/docs/acceptance-report.md) for the exact tested sources
and scope. These tests did not run a GitLab CI job, and they did not execute the
final release binaries. Publication and installation checks do not create cloud
workers.

Full GitLab job acceptance, real Docker and Windows jobs, temporary SSH key
delivery, genuine Spot interruption, and broader recovery cases remain unverified.
Suspend/resume is disabled. The pinned upstream SSH connector does not verify
server host keys, and its WinRM connector does not verify server certificates.
Use an isolated, trusted network as described in the
[connection guide](https://github.com/euyuil/fleeting-plugin-aliyun/blob/v0.1.0-rc.1/docs/adr/0003-state-and-connections.md).

Public GHCR access and Runner installation are checked after publication. See the
[distribution guide](https://github.com/euyuil/fleeting-plugin-aliyun/blob/main/docs/distribution.md) for installation and verification
evidence. This prerelease does not claim production acceptance or full upstream
capability parity.
