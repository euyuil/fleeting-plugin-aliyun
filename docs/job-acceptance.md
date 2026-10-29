# Release job acceptance

`tools/job_acceptance.py` owns a fresh one-worker fixture and drives real GitLab
jobs through the installed Debian package and its systemd Manager. Both
`instance` and `docker-autoscaler` must pass independently. The candidate phase
installs the sealed package; the public phase downloads the package and checksum
inventory anonymously and requires byte-for-byte equality with that candidate.
The plugin is never rebuilt between acceptance and publication.

The [systemd acceptance path](testing.md#packaged-systemd-manager-acceptance)
validates the installed binary, shipped example, service lifecycle, fixed
GitLab fixture, actual worker, executor and normal release. Emergency cleanup
does not turn a failed job or failed normal release into PASS. Both executor
receipts must bind to the same source, version, manifest, workflow attempt,
package bytes and fixture configurations. Receipts contain no account, project,
runner or resource IDs; raw logs and artifacts remain private on the hosted VM.

## Dedicated configuration

Use the `aliyun-pre-release` GitHub Environment and its existing dedicated
Alibaba account, OIDC role, cleanup regions and independent janitor. The
plugin-only fixture configuration is separate. Add these Environment secrets:

- `RELEASE_GITLAB_API_TOKEN`: project-scoped fine-grained PAT, permitted to
  create/read/update pipelines and read jobs, job artifacts, the project,
  repository commits and its dedicated runner. Initial fixture provisioning
  additionally needs Commit Create. Do not grant User or Global permissions.
- `RELEASE_GITLAB_RUNNER_TOKEN`: the persistent project's Runner authentication
  token. Reuse this Runner identity for every temporary Manager.
- `RELEASE_GITLAB_CONFIG`: the exact private project, runner and fixed test ref:

```json
{
  "url": "https://gitlab.com",
  "project_id": "123",
  "fixture_ref": "main",
  "fixture_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "runner_id": "456",
  "runner_tag": "aliyun-acceptance"
}
```

The project must be private, expose only the expected locked project Runner,
disable untagged jobs, and permit the token owner's role to supply pipeline
variables. Commit only the reviewed `.gitlab-ci.yml` and `smoke.sh` from
`tests/fixtures/gitlab`, then pin their full commit SHA. A moved ref, different
runner, pending prior job or stale artifact fails acceptance. Never update the
fixture ref without reviewing the new files and updating its pinned SHA.

Add `RELEASE_ALIYUN_JOB_FIXTURE_CONFIG` as a private Environment secret (or a
variable where the deployment policy permits private resource configuration).
Its two fields are `fixture` and `job_image`. `fixture` has the same validated
fields as `RELEASE_ALIYUN_FIXTURE_CONFIG`, but must reference a separately
prepared immutable job-worker image. `job_image` is a full registry/repository
reference with a SHA-256 digest. There are no fallback image IDs or instance
types. See the [cloud configuration contract](pre-release-cloud.md) for the base
fixture fields and independent cleanup requirements.

The job-worker image must support cloud-init and root SSH using the fresh
per-run ECS key pair. It needs Git, curl, coreutils, the pinned Runner, Docker
Engine, and `/var/lib/fleeting-worker/ready` after preparation succeeds. Bake
the pinned job image and matching Runner helper image into it to avoid large
downloads over the one-Mbps worker connection. Verify both VM and container
access to IMDSv2; the fixed job independently reads its instance ID. Prepare the
image using the [worker image procedure](../infra/README.md), remove temporary
preparation credentials and stale cloud-init state, and retain its private
image/snapshot ownership record. No GitLab or Manager cloud credentials enter
the image. Image creation and retained storage require separate cost approval.

Use an instance type suitable for full jobs. The initial target is 2 vCPU and
2 GiB, for example `ecs.e-c1m1.large`, subject to regional Spot availability.
Each mode uses MaxSize 1, concurrency 1 and a cumulative observation budget of
one instance, with no on-demand fallback. Pre- and post-publication modes have
distinct fixture names and ownership tags. Existing janitor discovery covers
all four modes even if local evidence is lost.

Keep `RELEASE_CLOUD_ACCEPTANCE_ENABLED` and `RELEASE_JOB_ACCEPTANCE_ENABLED`
disabled until the separate paid run is approved and its image is ready.
`RELEASE_CLOUD_CLEANUP_ENABLED` must already be true. Read-only local
configuration validation is available with:

```sh
python3 tools/job_acceptance.py preflight --read-only
```

This command validates supplied settings without authenticating to either
provider. It does not establish permissions, image readiness or job success.

## Credential maintenance

The PAT and Runner token have independent lifetimes. Record the PAT expiry
privately and replace its GitHub Secret before it expires. Inspect the Runner's
`token_expires_at` through the GitLab API; an unset expiration does not require
per-job rotation. Resetting a compromised token normally preserves the Runner
identity. If automatic Runner token rotation is enabled later, persist the new
token back to the central secret before disposing of its Manager. This workflow
does not perform that synchronization and must not assume GitHub Secrets update
automatically. Temporary local provisioning copies must be deleted after the
Environment secrets and read-only checks are verified.

Offline checks cover configuration rejection, stale/foreign receipt rejection,
candidate/public byte binding, job identity, cleanup barriers and fixture
teardown after failure. Real Linux systemd lifecycle checks remain distinct
from the separately authorized paid GitLab/ECS job run. A green offline CI run
is not evidence that the configured worker image or account is usable.
