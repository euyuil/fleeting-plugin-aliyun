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

The `Release job acceptance` reusable workflow runs both executors serially on
fresh Ubuntu 26.04 amd64 GitHub-hosted Managers. `Cloud acceptance` invokes the
candidate phase in `all` and `jobs` modes; only `all`, including the six plugin
scenarios, permits publication. Release invokes the public phase after its
anonymous installation matrix succeeds. Any failed, canceled or skipped required
job prevents that phase from passing, even if some receipt artifacts exist.
The public installation matrix remains credential-free; cloud and GitLab
credentials are confined to the separate job workflow's Environment.

These live jobs cover the configured official Ubuntu 26.04 amd64 base image and
its exact type, zone and network assumptions. The wider amd64/arm64 Ubuntu and
Debian package matrix remains offline installation evidence. Neither receipt
establishes future Spot availability or universal success on other user images.

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
fields as `RELEASE_ALIYUN_FIXTURE_CONFIG`, but must reference a fixed official
Ubuntu 26.04 amd64 base image. `job_image` is a full registry/repository
reference with a SHA-256 digest. There are no fallback image IDs or instance
types. See the [cloud configuration contract](pre-release-cloud.md) for the base
fixture fields and independent cleanup requirements.

Each fresh worker installs its tools through cloud-init. The fixture passes its
per-run SSH public key and the checked-in bootstrap scripts as launch-template
user data. `infra/bootstrap-job-worker.sh` selects the signed Ubuntu archive
snapshot pinned in `upstream.lock.json`, installs Docker for the container
executor, and then runs `infra/bootstrap-linux.sh` to install the checksum-pinned
Runner and verify Git, curl, coreutils and SSH. Readiness is written only after
preparation succeeds. Docker pulls the job and Runner helper images normally.
No custom ECS image is built or retained, and no container cache is prepared.
No GitLab PAT, Runner authentication token or Manager cloud credentials enter
the worker bootstrap. The selected public image must support cloud-init and root
SSH with an ECS key pair. Both VM and container jobs independently read IMDSv2.

The fixture job has a 20-minute timeout to include ordinary preparation. The
shipped Manager examples retain their readiness and acquisition deadlines;
installation failure or an exceeded deadline fails acceptance. The one-Mbps
setting limits outbound traffic, not downloads. See the separate inbound limit
in the [ECS bandwidth documentation](https://www.alibabacloud.com/help/en/ecs/user-guide/network-bandwidth/).

Use an instance type suitable for full jobs. The initial target is 2 vCPU and
2 GiB, for example `ecs.e-c1m1.large`, subject to regional Spot availability.
Each mode uses MaxSize 1, concurrency 1 and a cumulative observation budget of
one instance, with no on-demand fallback. Pre- and post-publication modes have
distinct fixture names and ownership tags. Existing janitor discovery covers
all four modes even if local evidence is lost.

Keep `RELEASE_CLOUD_ACCEPTANCE_ENABLED` and `RELEASE_JOB_ACCEPTANCE_ENABLED`
disabled until the paid run is approved and the official base image is configured.
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
