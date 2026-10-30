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

Manager diagnostics are collected after attempting to fence the owned service.
The collector retains at most 4 MiB of raw journal output, normalizes complete
UTF-8 messages (including journald byte arrays), and persists a chronological
whole-record tail bounded to 200 records and 128 KiB. Fixed capture reasons and
omitted/invalid-record counts distinguish missing and truncated evidence. The
public projection exposes only the latest eight recognized lifecycle events and
allowlisted error terms. Diagnostics never change the acceptance verdict.

The candidate `Release job acceptance` workflow runs both executors and all nine scenarios serially on
fresh Ubuntu 26.04 amd64 GitHub-hosted Managers. `Cloud acceptance` invokes the
candidate phase in `all` and `jobs` modes; only `all`, including the six plugin
scenarios, permits publication. Release invokes the public phase after its
anonymous installation matrix succeeds. That phase runs exactly one real smoke
per executor through the anonymous public package. It does not repeat the
candidate's recovery/upgrade scenarios. Any failed, canceled or skipped required
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
| `worker-loss` | Abruptly delete the checkpointed worker; require a proven loss outcome and drain at most one replacement | Success on a fresh worker using the same Manager | 3 |
| `manager-crash` | Kill the owned Manager unit; require automatic restart and orphan release before harness job finalization | Success on a fresh worker with the original Runner identity | 2 |
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

Recovery drivers have a 40-minute execution budget and a 60-minute hosted job
deadline. Crash recovery has the same budget; it does not wait for GitLab's
natural lost-job timeout. Smoke and upgrade use a
20-minute execution budget. Before cloud fixture creation, the orchestrator fixes
one start time, execution deadline and resource expiry. Fixture setup consumes
the same execution budget; starting the Go driver cannot extend it. Resource
expiry is the execution deadline plus forty minutes, rounded down to the minute:
at most sixty minutes for smoke/upgrade and eighty for recovery, including Manager
crash. This separate, immutable cost-protection deadline leaves more
than thirty minutes of ECS API lead time even for a late recovery worker.
Normal release and inline cleanup still happen immediately when the case ends.
The serial eighteen-case phase permits thirty-six cumulative workers,
with at most two active workers per fixture. The full candidate gate requires all
eighteen schema-v3 receipts; a smoke receipt cannot substitute for a recovery case.
The separate `public-smoke-gate` requires exactly the two public smoke receipts,
with `public_ready=true`, `full_suite=false` and `release_ready=false`. Full,
debug and public-smoke profiles cannot exchange receipts. Raw GitLab traces and identities remain private; failure output
uses only the existing allowlisted diagnostic projection.

GitLab fixture preparation precedes package installation and cloud creation.
Its failure artifact is a separate, run/attempt/case/candidate-bound preflight
record, with zero started jobs and cleanup marked `NOT_STARTED`; it cannot
satisfy a job receipt gate. Only fixed request operations, methods, transport
categories, HTTP statuses and attempt counts are published. GET requests permit
at most three attempts for transport failures and HTTP 429/502/503/504, with
one- and two-second backoffs and the existing per-attempt socket timeout.
POST is never retried. Uncertain commit creation is reconciled through the
existing exact branch, commit, file and tree readbacks; if readback also fails,
both sanitized failures are retained without claiming that no write occurred.

### Targeted debugging before a final full run

Manual `cloud-acceptance.yml` runs in `mode=jobs` may select explicit cases with
`job_cases`, for example `instance/manager-crash,docker-autoscaler/upgrade`.
The default `all` retains the complete eighteen-case matrix. Cases remain serial
on the existing dedicated project and Runner, with a fresh disposable Manager
and cloud fixture per case. The existing fail-fast policy remains: a failed or
unconfirmed-cleanup case cancels queued cases so no later fixture is created
while an earlier Manager or pool might remain active. Each case keeps its
original assertions and creation budget; canceled cases remain unrun for the
next targeted round.

During debugging, select failed or previously unrun cases and omit cases that
already passed. Every new run still builds and verifies its own sealed candidate;
manual `mode=jobs` uses the `debug` profile: security, `verify-fast`, and the same
complete distribution/manifest, followed by the selected real native amd64
package/systemd cases. It skips the four-platform lifecycle prerequisite. It
does not use a partial manifest, reuse historical binaries or bypass package
installation in the selected cases. Other modes retain `verify-code` and all
four native platform validations.

Each receipt binds its validation profile as well as the artifact and identity;
debug receipts cannot satisfy the full receipt gate, even when all eighteen
debug cases pass. Summaries retain the configuration digests, policy versions
and selected/omitted cases. The debug gate requires exactly the selected receipts
from that run and attempt, with
matching source, distribution and configuration. Historical receipts cannot fill
in omitted cases. The separate `debug-gate` emits a `selected-cases` summary with
the explicit omissions and `release_ready=false`, even if every selected case
passes. It cannot satisfy the full `gate`, which always requires all eighteen.
Targeted selection is rejected outside manual jobs mode, including release's
complete pre-publication path. After fixing the discovered issues, run `all`
once on the final candidate to establish complete same-candidate coverage.

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
its worker normally. Both candidate executors run this case; publication reuses
these exact package bytes and runs a fresh public smoke for each executor. The rollback step validates
installation, preserved state and `doctor`; it does not execute a job on the old
version or promise compatibility with arbitrary older configurations.

The version must compare greater than the pinned baseline under Debian ordering.
Job preflight rejects `0.1.0-dev` and the baseline version before creating job
fixtures. Use an unpublished higher version such as `0.1.0-rc.4` for a manual run;
this does not publish a release. When advancing the baseline, review the actual
published assets and update every pinned hash and the manual default together.
Ordinary native package lifecycle CI separately repacks the current payload to
exercise package hooks; that test alone is not previous-release compatibility.

Upgrade receipts bind the old version and package hash in addition to the
candidate identity. Private configuration and state fingerprints are not published.
For the operator procedure, see [upgrade and roll back](debian-installation.md#upgrade-and-roll-back).

## Publication checks after full candidate acceptance

The public phase covers the distribution boundary. Each of the four native
Ubuntu/architecture jobs receives the accepted artifact and verifies its manifest.
Anonymous GitHub asset enumeration must contain exactly the declared two native
archives, two Debian packages, checksum inventory and dependency inventory.
Each native tar/deb and both inventories must equal the sealed candidate bytes;
the complete checksum list also rejects duplicate, missing and undeclared packages.

The official Runner installer uses a digest resolved once from the exact OCI
version tag. Verification reads that immutable reference and compares all four
installed payload files (`plugin`, `LICENSE`, `NOTICE`, `licenses.tar.gz`) with
the sealed native payload. Version, source, native platform, artifact-layer
version, plugin protocol and empty-configuration rejection remain required.
Public Debian checks cover native bytes, installation, both configurations,
`doctor` and no implicit service start. Full lifecycle, real baseline migration,
failure/recovery and rollback assertions remain in candidate acceptance.

The final Release completion gate reads the candidate's full-job summary, all
four native installation/download receipt sets and both actual public job
receipts. Source, version, manifest, configuration and workflow attempt must
agree; any missing, failed, canceled or skipped required path blocks completion.
The configured release therefore runs 34 candidate GitLab jobs plus two public
smokes, rather than repeating 34 jobs after publication. A standalone historical
public installation check without a sealed artifact reports candidate binding as
not provided and cannot satisfy this release-completion gate.

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
exit status as `script_failure`. This variant requires independently confirmed
absence of the exact running, inventoried worker after the injected deletion,
with the job's nonce-bound barrier still closed. The failure must occur after
injection; cancellation, a prior failure, an opened barrier, a different worker,
and unknown failure reasons cannot pass. Exact Runner error wording and
keepalive durations remain diagnostic and do not decide the outcome.
Before a follow-up pipeline is submitted, read-only observation
must prove zero group demand/capacity, quiet activities and absence of the worker
and its associated resources. ESS retains desired capacity after disappearance
and can create a replacement, as described in
[ADR 0002](adr/0002-capacity-reconciliation.md#interruption-and-health-replacement)
and the [ESS desired-capacity contract](https://help.aliyun.com/zh/auto-scaling/user-guide/expected-number-of-instances).
At most one replacement is allowed, with complete Spot and disk/ENI inventory.
Runner must release it normally and the read-only zero check must finish before
the recovery pipeline is submitted. The driver records that replacement's
identity privately at this boundary; it cannot serve either job. Additional
replacements, including one first observed during recovery, fail the case.
The second job must succeed on a fresh worker and finish through normal release.
Concurrency remains one; the cumulative bound is three workers, including the
injected target, optional drained replacement, and recovery worker.

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

Immediately after automatic restart, the driver observes normal release within
three minutes: zero group desired/total/pending/removing capacity, quiet history,
two stable observations, and absence of the exact orphan worker and its disks/
ENIs. No harness cancellation, scale-in or resource deletion may precede this
proof. Only one cumulative worker may have existed at this boundary.

It persists `orphan_release_verified` and the `orphan-release-before-finalize-v1`
policy before canceling the interrupted pipeline. If the exact original job
enters `canceling`, it force-cancels only that job with `force=true`. Both the
pipeline and its sole original job must reach actual `canceled` within a separate
two-minute deadline; API acceptance and `canceling` are insufficient. Writes
are never retried. Permission errors, changed identities and unexpected terminal
outcomes fail closed. `finalization_source=harness_after_recovery` records this
harness action separately from resource recovery. Generic job waits still require
a live worker and do not accept this crash-only released-worker path.

This proves packaged Manager and cloud-resource recovery, not GitLab's natural
server-side lost-job classification or resumption of the interrupted job. The
execution is bounded to forty minutes including setup, with immutable resource
expiry at most eighty minutes after setup begins.
The driver checks each real worker's automatic release setting, sets a missing
deadline explicitly, and confirms it through `DescribeInstances` before
recording protection. An accepted API write alone is insufficient. ESS inherits
only a subset of launch-template parameters, so a template's `AutoReleaseTime`
is not evidence of an actual instance deadline. A previously established valid
deadline is retained without extension. Plugin-only acceptance keeps its legacy
one-hour worker deadline and separate janitor eligibility marker.

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
  create/read/update pipelines, read/update jobs, and read job artifacts, the project,
  repository files/branches/commits and its dedicated runner. Fixture provisioning
  needs Commit Create on this project. Job Update permits bounded finalization of
  the already-recovered crash job; it does not authorize canceling unrelated jobs.
  Do not grant User or Global permissions.
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

Once fixture setup begins, the public receipt and console diagnostics retain
separate setup, driver and cleanup verdicts. Fixture teardown cannot replace
the failure returned by setup or the driver invocation. Diagnostics include bounded durations,
allowlisted cloud API error categories and fixture creation-operation states,
plus any saved driver outcome after a Manager cleanup failure. Missing driver
evidence stays explicitly unrecorded. Raw exceptions, resource identities,
configuration and logs remain private. Failed cleanup still fails the gate;
confirm the independent janitor's result for the exact owning run and attempt.

Driver diagnostics preserve up to eight failures in occurrence order, including
unknown errors, with an explicit omitted count. Each record identifies its fixed
stage, error category and, when available, the failed API operation, HTTP status
or allowlisted cloud code. The job index is 1–3; pool-wide and teardown failures
have no job index. Source-message templates contain format placeholders instead
of interpolated values. These are diagnostic observations, not acceptance proof.
The original scenario classification survives a later forced Manager stop;
forced shutdown after an otherwise passing scenario still fails acceptance.
A resource cleanup failure still prevents a passing aggregate result while
the earlier failure records remain intact.

After attempting to stop its owned packaged Manager, the driver captures up to
200 journal entries from the current run, with a 128-KiB private evidence limit.
The public diagnostic projects only fixed taskscaler preparation, health and
removal events, bounded process exit codes, and allowlisted error-word outlines.
It retains at most eight events and reports omitted events and a full journal
window explicitly. Missing, unreadable or malformed evidence remains distinct
from a recorded window with no matching events. JSON nesting is limited to 32
levels independently of the Python interpreter. Raw journal messages and their
identities stay in the private driver result; the receipt upload does not include
them. Diagnostic collection cannot change the scenario verdict or prevent cleanup.

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
