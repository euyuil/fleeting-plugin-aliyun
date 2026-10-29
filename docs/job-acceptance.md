# Release job acceptance

`tools/job_acceptance.py` owns a fresh fixture with at most two active workers and drives real GitLab
jobs through the installed Debian package and its systemd Manager. Both
`instance` and `docker-autoscaler` must pass independently. The candidate phase
installs the sealed package; the public phase downloads the package and checksum
inventory anonymously and requires byte-for-byte equality with that candidate.
The plugin is never rebuilt between acceptance and publication.

The [systemd acceptance path](testing.md#packaged-systemd-manager-acceptance)
validates the installed binary, shipped example, service lifecycle, fixed
GitLab fixture, actual worker, executor and normal release. Emergency cleanup
does not turn an unexpected job outcome or failed normal release into PASS. All executor/scenario
receipts must bind to the same source, version, manifest, workflow attempt,
package bytes and fixture configurations. Receipts contain no account, project,
runner or resource IDs; raw logs and artifacts remain private on the hosted VM.

The `Release job acceptance` reusable workflow runs both executors and all nine scenarios serially on
fresh Ubuntu 26.04 amd64 GitHub-hosted Managers. `Cloud acceptance` invokes the
candidate phase in `all` and `jobs` modes; only `all`, including the six plugin
scenarios, permits publication. Release invokes the public phase after its
anonymous installation matrix succeeds. Any failed, canceled or skipped required
job prevents that phase from passing, even if some receipt artifacts exist.
The public installation matrix remains credential-free; cloud and GitLab
credentials are confined to the separate job workflow's Environment.

These live jobs cover the configured official Ubuntu 26.04 amd64 base image and
its exact type, zone and network assumptions. The wider amd64/arm64 Ubuntu and
Debian package matrix remains offline installation evidence. These receipts do not
establish future Spot availability or universal success on other user images.

## Expected failures and continued service

Every executor runs nine independently bounded scenarios:

| Scenario | First job | Follow-up job | Cumulative worker budget |
| --- | --- | --- | --- |
| `smoke` | Successful checkout, command and artifact | None | 1 |
| `failure` | Deliberate exit 42, reported as `script_failure` | Success | 2 |
| `cancel` | Driver cancels the pipeline after the script checkpoint | Success | 2 |
| `timeout` | Runner's fixed three-minute `RUNNER_SCRIPT_TIMEOUT`, reported as `job_execution_timeout` | Success | 2 |
| `capacity` | Two concurrent successful jobs, with a third queued | Third job succeeds on a fresh worker while the retained job continues | 3 |
| `drain` | Running job completes while `manager stop` drains the service | Success after an explicit Manager restart | 2 |
| `worker-loss` | Abruptly delete the checkpointed worker; require a proven loss outcome | Success on a fresh worker using the same Manager | 2 |
| `manager-crash` | Kill the owned Manager unit; require automatic restart and GitLab server-side job failure | Success on a fresh worker with the original Runner identity | 2 |
| `upgrade` | Install and configure the pinned published package, upgrade, roll back, and upgrade again; run a successful job | None | 1 |

The script checkpoint binds the nonce, scenario, job ID and IMDS worker ID.
Cancellation is requested only after that worker has complete independent Spot
and associated-resource inventory. A preparation failure, missing checkpoint,
wrong failure reason, unexpected job retry or different worker cannot satisfy an
expected-failure scenario. The timeout covers the actual script stage after
worker acquisition and preparation; it does not simulate a timeout with `exit`.

For `failure`, `cancel` and `timeout`, the same systemd Manager PID must survive
both jobs and both normal releases.
Before submitting the second pipeline, the driver observes zero desired/total/
pending/removing capacity, an idle activity history, and the absence of the first
worker and its disks/ENIs. It performs no cleanup mutation during this check.
The second job must use a fresh nonce, pipeline, job and worker, return the normal
success artifact, and then release normally. Emergency cleanup remains separate
and cannot make failed recovery pass.

Recovery drivers have a 40-minute execution deadline and a 60-minute hosted job
deadline, except `manager-crash`, which allows 50 and 80 minutes respectively for
GitLab's server-side job timeout and bounded shutdown/cleanup. Smoke and upgrade use a
20-minute driver deadline. The serial eighteen-case phase permits thirty-four cumulative workers,
with at most two active workers per fixture. Both candidate and public phases
require all eighteen schema-v2 receipts; a smoke receipt cannot substitute for a
recovery case. Raw GitLab traces and identities remain private; failure output
uses only the existing allowlisted diagnostic projection.

The Go job driver retries GitLab GET transport failures, including interrupted
response bodies, up to three attempts with one- and two-second waits. Each
attempt retains the existing twenty-second HTTP timeout and the caller's
scenario deadline; cancellation also interrupts the wait. A partial response is
discarded before retrying. HTTP error responses and malformed JSON still fail
immediately. Writes, including pipeline creation and cancellation, are never
retried by the driver because a lost response may already have committed the
operation. This read retry does not repeat a GitLab job or relax any acceptance
assertion.

## Upgrade from a published Debian package

The `upgrade` case downloads the actual `v0.1.0-rc.3` Debian release, with its
release commit, checksum inventory, package and installed binary hashes pinned in
[`tests/fixtures/debian-upgrade.json`](../tests/fixtures/debian-upgrade.json).
There is no candidate repackaging or download fallback. The installed old CLI
creates the Manager state using its own shipped example, then runs `manager doctor`.

While the service remains inactive with no unit processes, the test installs the
sealed candidate, reinstalls the saved old package, and installs the candidate
again. Each transition must preserve every private state file's bytes, owner and
mode, directory owners/modes, the service account's UID/GID/home/shell, and Runner
system identity. Each installed binary and Debian version must match the selected
artifact, and each installed CLI must pass `manager doctor`. The helper rejects
symlinks and other nonregular managed state. Package installation must never start
the service implicitly.

Only then does the candidate Manager start, complete a real GitLab job and release
its worker normally. Both executors run this case. The public phase upgrades to
the anonymously downloaded public candidate package. The rollback step validates
installation, preserved state and `doctor`; it does not execute a job on the old
version or promise compatibility with arbitrary older configurations.

The version must compare greater than the pinned baseline under Debian ordering.
Job preflight rejects `0.1.0-dev` and the baseline version before creating job
fixtures. Use an unpublished higher version such as `0.1.0-rc.4` for a manual run;
this does not publish a release. When advancing the baseline, review the actual
published assets and update every pinned hash and the manual default together.
Ordinary native package lifecycle CI separately repacks the current payload to
exercise package hooks; that test alone is not previous-release compatibility.

Public upgrade receipts bind the old version and package hash in addition to the
candidate identity. Private configuration and state fingerprints are not published.
For the operator procedure, see [upgrade and roll back](debian-installation.md#upgrade-and-roll-back).

## Capacity, queueing and job isolation

The `capacity` case keeps `capacity_per_instance=1` and `max_use_count=1`, and
sets both Manager concurrency and ESS MaxSize to two. It submits three distinct
single-job pipelines through the same Manager. Two nonce-bound script checkpoints
must prove simultaneous work on distinct inventoried Spot workers while the
third concrete GitLab job remains pending.

The driver releases one job's barrier over the existing fixture SSH connection.
For Docker, it selects the one running container labeled with that exact job ID
and checks the nonce inside it before releasing the barrier. The first job must
finish successfully and its worker, disks and ENIs must disappear through normal
Runner release. The other original job must remain running throughout. The
queued job must then reach its checkpoint on a third, fresh worker before the
driver releases either remaining barrier. All three artifacts, stable Manager
identity, final zero capacity and complete resource release are required.

Every job rejects pre-existing sentinels in its checkout and `/tmp`, writes its
nonce there and checks it again after the barrier. Together with three distinct
worker identities, this verifies workspace isolation across concurrent and
successive jobs. A changed job ID, unexpected retry, early third execution,
interrupted retained job, failed control command or missing artifact fails the
case. Control commands do not change cloud capacity or delete resources.

Barrier waits have a 30-minute fallback that fails the job. The capacity job's
GitLab timeout and the driver deadline are 40 minutes, with the same 60-minute
hosted job deadline and cleanup path. Configure the dedicated Runner's maximum
job timeout to permit this case. Recovery and smoke keep their existing bounds.

## Graceful stop during a real job

The `drain` case starts a held real job on one worker, observes its nonce-bound
checkpoint, and invokes the installed `fleeting-plugin-aliyun manager stop`
command. Before releasing the job barrier, the driver must observe systemd's
`deactivating` state with the original MainPID and a live unit control group.
An immediate stop, aborted job, changed invocation or failed stop command fails
acceptance. The job must succeed, the stop command must return successfully,
and the same invocation must reach `inactive`, MainPID zero and an empty control
group. Stop observation is bounded to four minutes; the privileged command has
its own three-minute timeout that survives a lost driver.

The driver then observes normal worker, disk and ENI release before explicitly
starting the Manager again. A distinct PID must complete a fresh successful job
and normal release using the same configured Runner identity. Only a verified
lifecycle transition permits different Manager PIDs in the receipt. It does not claim
recovery or resumption after a crash; those are separate failure cases.

## Abrupt worker loss during a real job

The `worker-loss` case holds a job at its verified script checkpoint, then calls
ECS `DeleteInstance` with `Force=true` and `ForceStop=true` for that exact worker.
Immediately before the call, the driver rechecks the run's exclusive group
ownership, sole automatically created member, complete disk/ENI inventory and
running Spot status. This injects loss outside the plugin's scale-in path.

The original Manager must remain alive, and GitLab must report the original job
as failed with `runner_system_failure`, without cancellation, retries or opening
the job barrier. For `instance` only, the pinned Runner can classify missing SSH
exit status as `script_failure`. That variant additionally requires the exact
terminal Runner error line proving missing remote exit status/signal. It may
include the pinned connector's keepalive-probe timeout wrapper: the duration must
be canonical Go duration text and at least the connector's 30-second minimum,
and the wrapped error must still report exit code zero and missing remote exit
status/signal. Other keepalive causes do not qualify. An ordinary
script failure, an echoed command, or a continued trace fragment cannot pass.
Before a follow-up pipeline is submitted, read-only observation
must prove zero group demand/capacity, quiet activities and absence of the worker
and its associated resources. Any unrequested replacement fails the case. The
second job must succeed on a distinct worker and finish through normal release.

Private evidence marks the first deletion as injected, with `normal_release`
false for both that job and the whole sequence; `worker_loss_verified` records the
subsequent reconciliation proof. The follow-up job still requires normal release.
Emergency cleanup cannot satisfy this proof. This tests abrupt instance loss,
not the timing or advance notification of a real Spot preemption.

## Manager crash during a real job

The `manager-crash` case holds a real job at its checkpoint, revalidates service
ownership/configuration and the original PID/invocation, then sends SIGKILL to
the entire owned systemd unit, including plugin subprocesses. It never invokes
`manager start` during recovery. Within two minutes, the shipped
`Restart=on-failure` policy must produce a distinct PID and invocation, exactly
one additional automatic restart, no original Manager process, and the same
Runner system ID and configuration. A restart loop or any later restart fails.

The killed Runner cannot report the first job's final result. This fixture uses
a fifteen-minute job timeout and waits for GitLab to fail it with
`server_timeout_running` or `no_updates_running`; cancellation, script timeout
and apparent success are rejected. GitLab documents these
[server-side failure thresholds](https://docs.gitlab.com/ci/jobs/job_troubleshooting/#job-dropped-after-being-stuck-or-exceeding-its-timeout).
The driver remains bounded to fifty minutes and retains the one-hour fixture
expiry and worker auto-release protections.

The one-hour STS session starts before setup. Slow setup or an execution deadline
can exhaust credentials during inline cleanup; that cleanup fails closed and
cannot produce a passing receipt. In that case, verify the independent janitor's
final zero-resource result using its fresh credentials. The service startup fence
and worker expiry remain in force while recovery runs.

Before submitting the second pipeline, the driver must observe the orphaned
worker and its resources removed, zero group demand/capacity and quiet activity
history, without making cleanup mutations. The automatically restarted Manager
must then complete a fresh successful job and normal release within the exact
two-worker budget. This verifies recovery with the shipped state-storage-disabled
configuration; it does not promise resumption of interrupted jobs.

## Dedicated configuration

Use the `aliyun-pre-release` GitHub Environment and its existing dedicated
Alibaba account, OIDC role, cleanup regions and independent janitor. The
plugin-only fixture configuration is separate. Add these Environment secrets:

- `RELEASE_GITLAB_API_TOKEN`: project-scoped fine-grained PAT, permitted to
  create/read/update pipelines and read jobs, job artifacts, the project,
  repository files/branches/commits and its dedicated runner. Fixture provisioning
  needs Commit Create on this project. Do not grant User or Global permissions.
- `RELEASE_GITLAB_RUNNER_TOKEN`: the persistent project's Runner authentication
  token. Reuse this Runner identity for every temporary Manager.
- `RELEASE_GITLAB_CONFIG`: the exact private project, runner and pinned fixture base:

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
variables. Initially commit only `.gitlab-ci.yml` and `smoke.sh` from
`tests/fixtures/gitlab`, then pin their full commit SHA. A moved base ref,
different runner, pending prior job or stale artifact fails acceptance.

Before package installation or cloud creation, `tools/gitlab_fixture.py` verifies
that private project and its pinned base. If the two files already match the
reviewed candidate checkout byte for byte, it reuses the base. Otherwise it uses
the [GitLab Commits API](https://docs.gitlab.com/api/commits/#create-a-commit)
to create `acceptance/FULL_CANDIDATE_SHA` from that exact base, changing only the
two fixed paths. Push pipelines are disabled by the fixture's API-only workflow.
It never force-updates or deletes a branch. An existing candidate branch must
have the expected single parent, commit message and exact file bytes. Complete
root-tree comparison also requires unchanged modes and every other file/subtree
object ID; a root with 100 or more entries is refused rather than truncated.
Conflicting content fails. An uncertain write is recovered through readback, without repeating
the POST. HTTP redirects are refused so the PAT cannot follow another endpoint.

The read-back commit becomes the runtime fixture pin for every executor/scenario
and both release phases. The configured base and GitHub secrets remain unchanged.
The Go driver rechecks the ref and requires each pipeline to use that exact SHA.
Only a configuration digest enters the public receipt. Keep generated branches
in the private fixture project for repeatable candidate and public verification;
do not move their refs. Updating the configured base remains an explicit reviewed
configuration change.

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
Smoke and recovery use MaxSize 1 and concurrency 1; capacity uses two. There is
no on-demand fallback. Smoke and upgrade permit one cumulative worker; each recovery
scenario permits exactly two successive workers; capacity permits three.
Pre- and post-publication executor/scenario modes have
distinct fixture names and ownership tags. Existing janitor discovery covers
all thirty-six modes even if local evidence is lost.

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
