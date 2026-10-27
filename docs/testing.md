# Testing and acceptance

## Offline

[CONTRIBUTING.md](../CONTRIBUTING.md) owns executable developer commands. Default
`go test ./...` never calls live cloud/GitLab or discovers their credentials.
Fakes replace the ESS/ECS API boundary and retain real provider logic. Tests cover
pending increases, lost replies after actual mutation, specified 20/21/43-ID
release batches, ignored/failing activities, pagination, malformed snapshots,
restart/journal recovery, replacement, context cancellation, concurrent writes,
connection overrides, Windows static configuration, temporary SSH and strict
Spot. Local component tests load the actual compiled plugin through the pinned
Fleeting host and execute an authenticated SSH command. A separate loopback
registry test publishes with the official fleeting-artifact library, installs
with its matching installer and compares bytes. None is live cloud evidence.

The pinned Runner binary can be fetched with `python3 tools/fetch-runner.py`.
The checksum is locked in `upstream.lock.json` and checked again by the live
driver before executing the Manager. Use `gitlab-runner list --config <example>` to check the
actual Runner parser without contacting GitLab. `tools/check-configs.py` also
checks finite limits and executor configuration boundaries using Python 3.11+.

## Network connectivity before live acceptance

The manual **Network connectivity** workflow probes verified public HTTPS to the
US East ECS endpoint, the ESS endpoint and STS without cloud credentials. Store
the temporary ECS public IPv4 address in `ALIYUN_NETWORK_PROBE_HOST` and its
independently verified Ed25519 host-key fingerprint in
`ALIYUN_NETWORK_PROBE_HOST_KEY_SHA256`, both as repository secrets. No SSH client
private key is uploaded. Remove these temporary secrets after the test.

Download the early `network-egress` artifact while the workflow runs. Add only
that runner's IPv4 /32 to the test instance's security group for TCP port 22.
The probe waits up to four minutes and compares the scanned public host key.
The scan does not prove possession of the server's private host key. An unsigned
API's HTTP error response proves HTTPS reachability, not API authorization.
This workflow does not create or clean up cloud resources. The operator owns
the separately authorized instance, narrow rule and final cleanup. A passing
probe does not establish authenticated SSH client access, worker egress or
plugin/GitLab acceptance; verify those separately. The result artifact contains
statuses and timings without the target address.

## One-time live setup

1. Prepare a fixed GitLab fixture following its
   [instructions](../tests/fixtures/gitlab/README.md).
2. Prepare the dedicated pool and worker image using [infra](../infra/README.md).
3. Configure `ALIYUN_REGION`, `ALIYUN_SCALING_GROUP_ID`, exact
   `ALLOWED_INSTANCE_TYPE`, `ALLOWED_IMAGE_ID`, `ALLOWED_ZONE_ID`,
   `ALIYUN_ROLE_ARN`, `ALIYUN_CLEANUP_ROLE_ARN`, `ALIYUN_OIDC_PROVIDER_ARN` as
   GitHub repository variables. Configure role trust against actual claims.
4. Configure GitLab URL/project/ref/SHA/tag/expected-runner-ID variables. Store
   `GITLAB_RUNNER_TOKEN`, `GITLAB_API_TOKEN`, `WORKER_SSH_PRIVATE_KEY` as secrets.
   `GITLAB_URL` can be the existing reachable CE HTTPS URL.
5. Enable the cleanup role and set `ENABLE_ACCEPTANCE_JANITOR=true` only after
   reviewing its scope. The janitor skips all jobs until explicitly enabled.
6. Review trust, budgets, template and ownership. Invoke **Live acceptance** on
   main explicitly. Ordinary PRs, forks and pushes receive no cloud identity.

The workflow discovers its current public egress and creates only an owned /32
SSH rule when an equivalent existing rule is absent. It never opens 0.0.0.0/0.
Cleanup removes only rules matching this run's exact description/ID and expected
parameters. Existing reusable rules are not deleted. The public-network path
still requires worker egress to GitLab and bootstrap dependencies.

For local explicit use, build the current plugin, fetch the pinned Runner, set
the same configuration plus `ACCEPTANCE_PROJECT=owner/repository`,
`GITHUB_RUN_ID`, `GITHUB_RUN_ATTEMPT`, `MANAGER_CIDR`, `WORKER_SSH_KEY`,
`WORKER_USER`, `PLUGIN_BINARY`, `PLUGIN_COMMIT`, `RUNNER_BINARY`, and run:

```sh
RUN_LIVE_ACCEPTANCE=1 go run -tags live ./cmd/aliyun-acceptance smoke
```

`PLUGIN_BINARY` must report `PLUGIN_COMMIT` in its build information. Inputs do
not automatically enable live operations. The explicit command/build tag and
`RUN_LIVE_ACCEPTANCE=1` are both required. Missing inputs write
`NOT_RUN_MISSING_CONFIGURATION` and exit nonzero before constructing a cloud
client. Configuration values are never expanded from TOML placeholders.

## Smoke evidence and budgets

Default smoke permits one concurrent worker and one newly observed instance ID,
including failed instances/replacements. MaxSize and Runner max_instances are 1.
Region/type/image/zone/template remain fixed, all actual instances must be Spot,
and no on-demand fallback or proactive replacement is allowed. Before claiming
the empty pool, the driver captures its activity-ID baseline. New activity IDs
and group members contribute to the cumulative budget, avoiding both missed
short-lived instances and confusion with the previous run's minute-level times.
Incomplete creation history cannot certify a pass.

The GitHub VM temporarily runs the pinned Runner Manager, with the current
binary first in an absolute private PATH. Before any cloud ownership mutation,
the driver verifies that its authentication token belongs to the expected runner,
using the same private system ID as the Manager. The runner must be locked to
exactly the fixed project. Worker jobs verify checkout, runner ID,
nonce, fixture SHA and actual metadata instance ID. GitHub cross-checks the exact
pipeline/job/artifact and independent ECS Spot/type/image/zone facts. Old jobs,
wrong runners and stale artifacts fail.

After the job succeeds, the Manager remains running while the driver waits up to
3 minutes (within the 20-minute stage) for normal plugin-driven 1-to-0 release.
Both ESS and ECS, disks and ENIs must confirm absence over consecutive polls.
Only terminal, identity-checked ESS activities can certify an idle pool.
Only then can smoke pass. Emergency cleanup is a fallback and cannot convert a
failed normal scale-in into a pass. The final inventory is checked again after
cleanup; extra creation IDs or unverified resources invalidate PASS.

The stage is bounded at 20 minutes; acquisition at 10 minutes; workflow at 40
minutes, including dependency setup and cleanup. Cleanup gets up to 8 minutes
plus Manager shutdown time. Ownership expires after 30 minutes, and observed
instances receive a one-hour automatic release deadline (the ECS API requires
at least 30 minutes). The independent hourly janitor confirms expiry **and** the
owning GitHub run's completed status before reclaiming. The workflow timeout is
longer than the stage so initial setup cannot consume the cleanup margin.

The observation budget is not an atomic cumulative cloud billing cap. ESS may
replace a failed instance before detection; report an overrun and stop. Automatic
ECS release alone does not cancel ESS desired capacity. Disks, public traffic,
images and billing granularity remain chargeable according to actual services.

Private `.live/run-<id>-<attempt>/ledger.json`, `result.json` and `cleanup.json` use [schema version 1](../acceptance/live-result.schema.json). Run attempts are
serialized as decimal strings to preserve the exact ownership marker.
They retain run/attempt, hashes, pinned versions, nonce, fixture and job IDs,
resource origin, expiry, inventories and cleanup outcome. They exclude keys and
tokens. A failed evidence write cannot yield PASS. Artifact retention is seven
days; retain only reviewed, redacted summaries long term.

Results are `PASS`, `FAIL_PRODUCT`, `FAIL_INFRASTRUCTURE`,
`NOT_RUN_MISSING_CONFIGURATION`, or `CLEANUP_FAILED`. Cleanup failure overrides a
successful job. Infrastructure errors are not permission to change region,
instance type, budget or billing strategy.

## Three cleanup layers and incomplete evidence

The driver always attempts cleanup after stopping its Manager; the workflow has
an always-run step with fresh credentials; the independent janitor uses trusted
main code on workflow completion, manual dispatch and an hourly TTL sweep. All
pool users share one concurrency key with cancel-in-progress=false. A janitor
never creates workers or executes an uploaded artifact as code.

Cleanup verifies exact project/region/group/run ownership, inventories exclusive
members before zeroing replacement intent, verifies ECS/disks/ENIs, removes only
owned temporary ingress, then clears run and operation markers. It does not
remove VPCs, templates, shared security groups, images or fixed GitLab runners.
The local cleanup command verifies the recorded Manager PID/config identity in
/proc before signaling it. A janitor verifies the original GitHub run/attempt
through the API; an active run is not reclaimed solely because TTL elapsed.

The janitor first tries to download the prior selected JSON artifact as data.
The cloud baseline digest and scope fields validate recovered evidence.
If a machine disappears before its ledger is uploaded, the janitor can recover
current auto-created members from the dedicated group and recent activity IDs.
Missing baseline or pre-release associated-resource inventories are explicitly
unverified: it releases provably owned active resources but retains markers and
reports CLEANUP_FAILED, rather than asserting that unknown disks/ENIs are gone.
Recover the prior ledger from the workflow artifact or perform a scoped manual
inventory before clearing that condition. This is a declared evidence boundary,
not a successful cleanup result.

## Extended acceptance

The manual workflow defaults to `smoke`. Select `scale`, `interruption`, or
`suspend` explicitly to run the tagged direct provider/SDK/SSH tests with the
same ownership, fresh-identity cleanup step and independent janitor. These modes
do not claim a GitLab executor job. Docker and Windows need separate prepared
images and real executor jobs; those acceptance paths have not run.

`scale` requires a separately prepared empty group with MaxSize=2 and permits two
concurrent/two cumulative instances. `interruption` requires MaxSize=1 and permits
two cumulative identities, using an explicit DeleteInstance injection rather
than a genuine Spot notification. `suspend` requires MaxSize=1, one cumulative
identity, and the isolated standby/stop/start controller. It checks fresh SSH
after resumption; the production capability stays disabled. All modes retain the
exact type/image/zone/strict-Spot policy and abort on budget overrun.

For explicit local use, follow the same setup and authorization requirements:

```sh
RUN_LIVE_ACCEPTANCE=1 LIVE_EXTENDED_MODE=scale make test-live-extended
```

The target normalizes the key/output paths before Go changes its test directory.
Use a unique run/attempt from the actual owning GitHub run so the independent
janitor can verify completion. The suite records its process PID. A later local
cleanup step requires that process to be gone; an active driver is not reclaimed.
Results use `extended-result.json` with the common resource ledger embedded under
`resources`; the smoke result schema applies to `result.json`. Missing inputs
produce an explicit NOT_RUN artifact and a skipped test, never a live PASS.
A failed test stops its provider before fallback cleanup. No daily worker-creating
workflow exists. All extended live results remain unrun until actually invoked.

## Platform cost policies

Reviewed links on 2026-10-27: [GitHub Actions billing](https://docs.github.com/en/billing/concepts/product-billing/github-actions),
[GHCR visibility and anonymous pulls](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry),
[GitLab compute FAQ](https://about.gitlab.com/pricing/faq-compute-minutes/), and
[project access tokens](https://docs.gitlab.com/user/project/settings/project_access_tokens/).
Public GitHub standard runners and public GHCR are the free-first assumption;
private plans have allowances and policies can change. This project never changes
repository/package visibility automatically or requires paid GitLab.com project
tokens, trial credits, a persistent Manager VM, paid NAT/VPN, ACK or Kubernetes.
