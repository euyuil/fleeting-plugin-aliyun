# Pre-release cloud acceptance

Release runs the six plugin-only cloud scenarios before creating a source tag,
publishing OCI packages or creating a GitHub Release. This applies to release
candidates and stable versions. PR and main CI remain credential-free. There is
no GitLab server, Runner Manager, GitLab token or executor job in this gate.

## Configure a separate account

Use a dedicated Alibaba test account. The gate never reads a developer CLI
profile or the older `ALIYUN_*` repository configuration. Nothing is enabled by
merging the workflow: incomplete configuration fails the release gate before
resource creation. Do not supply an AccessKey or a browser-login refresh token.

1. In the **new account**, activate ECS, VPC and ESS, confirm US East Spot quota,
   and prepare the ESS service-linked role required to create/release workers.
2. Create a RAM OIDC provider for issuer
   `https://token.actions.githubusercontent.com` and audience `sts.aliyuncs.com`.
   Create separate acceptance and cleanup RAM roles. Configure their OIDC trust
   for the exact subject `repo:OWNER/REPOSITORY:environment:aliyun-pre-release`.
   Replace OWNER/REPOSITORY with this repository. Follow the
   [official Alibaba OIDC action documentation](https://github.com/aliyun/configure-aliyun-credentials-action).
3. Create the GitHub Actions environment **aliyun-pre-release**. Restrict its
   deployment branches to **main**. Keep emergency cleanup runnable without a
   pending human approval; release initiation itself remains an explicit manual
   operation. Trust the exact repository/environment, not every repository in
   the owner account. See [GitHub OIDC guidance](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-cloud-providers).
4. Set these **environment variables**, initially leaving the enabled flag false:

   | Variable | Value |
   | --- | --- |
   | `RELEASE_CLOUD_ACCEPTANCE_ENABLED` | `true` only after reviewing the new account and role scopes |
   | `RELEASE_ALIYUN_ACCOUNT_ID` | The new account's 16-digit account ID |
   | `RELEASE_ALIYUN_ROLE_ARN` | Acceptance role ARN in that account |
   | `RELEASE_ALIYUN_CLEANUP_ROLE_ARN` | Cleanup-only role ARN in that account |
   | `RELEASE_ALIYUN_OIDC_PROVIDER_ARN` | OIDC provider ARN in that account |

5. Review the action/region/resource scopes below, then explicitly enable the
   gate. Before any writes, STS `GetCallerIdentity` must match the configured
   account. Every ARN must belong to that same account. Each scenario gets fresh
   one-hour STS credentials; no local profile fallback is permitted.

The acceptance role needs the [provider's ESS/ECS actions](cloud-api.md), plus
fixture creation and cleanup. Fixture operations are VPC Create/Describe/Delete
Vpc and VSwitch; ECS Create/Describe/DeleteSecurityGroup, AuthorizeSecurityGroup,
Import/Describe/DeleteKeyPairs, Create/Describe/DeleteLaunchTemplate, instance,
disk and ENI reads/deletes; and ESS Create/Enable/DeleteScalingGroup and member
reads. Create calls attach tags atomically, so include each API's dependent tag
permissions. Existing acceptance also manages owned ingress and instance expiry.
The cleanup role needs these inventories, ESS member reads and deletion actions,
but no create, scale-out or publication permissions. Restrict to `us-east-1` and
test purpose/project tags wherever the API's RAM authorization metadata supports
those conditions. Some Describe actions require account-wide resource scope.
Review the official action metadata rather than granting administrator policies.
Dedicated account isolation is required even with these application checks.

## Candidate and scenarios

The workflow pins a full commit that is already on main. It runs offline tests
and the real Linux distribution/installer validation, then seals the complete
unpublished native/OCI payload. A separate responsegate binary is built from the
same clean commit. Artifact IDs and the manifest digest travel through job
outputs; downloaded files, Go source/platform/build tags, native/OCI binary
equality, and per-scenario receipts are checked again before publication.
Publication downloads the accepted artifact and never rebuilds the distribution.
The test-only responsegate binary is outside `dist/` and is never published.

The matrix runs serially with fail-fast enabled. Each scenario has a fresh
fixture and private evidence directory. Ordinary scenarios execute the native
Linux amd64 archive's binary. The response-loss case uses its separately marked
instrumented binary. This is Linux amd64 Manager cloud evidence; arm64 native
installation verification remains separate.

| Mode | Required outcome | Creation budget |
| --- | --- | --- |
| `cloud-smoke` | 0 → 1 → SSH → 0 | 1 |
| `cloud-scale` | 0 → 2 → specified 1 → 0; retained worker still accepts SSH | 2 |
| `cloud-restart` | Ready-worker restart rediscovers the original worker | 1 |
| `cloud-create-restart` | Restart during accepted creation produces one worker | 1 |
| `cloud-remove-restart` | Restart during accepted removal reaches zero | 1 |
| `cloud-increase-response-loss` | Lost expansion reply and restart create only one worker | 1 |

The fixed fixture uses `us-east-1b`, `ecs.e-c4m1.large` (2 vCPU, 0.5 GiB), image
`ubuntu_26_04_x64_20G_alibase_20260916.vhd`, and a 20-GiB ESSD PL0 system disk.
Cloud-init configures a 512-MiB swap file and per-run SSH keys. There is no
on-demand fallback or replacement budget. A complete successful suite creates
seven workers cumulatively, with at most two at once. Each driver has a 15-minute
scenario deadline plus cleanup; each scenario job has a 45-minute ceiling.

## Recognize test resources

All explicitly named resources use
`fleeting-pre-release-MODE-RUNSUFFIX-HASH12`, within ESS's 64-character limit.
The hash binds project, candidate, full run/attempt and scenario; full identifiers
remain in tags. Every fixture create attaches:

| Tag | Meaning |
| --- | --- |
| `fleeting-purpose=pre-release-acceptance` | Test purpose, distinct from application workloads |
| `fleeting-project` | Exact owner/repository |
| `fleeting-candidate` | Full candidate source SHA |
| `fleeting-ci-run`, `fleeting-ci-attempt` | Owning GitHub run and attempt |
| `fleeting-scenario` | Exact acceptance mode |
| `fleeting-fixture-expires` | UTC fixture expiry |

Launch-template instance tags also label ECS workers, disks and primary ENIs;
template tags label the template itself. Group tags are created atomically and
propagated to new workers. Automatically managed VPC system resources belong to
the labelled test VPC. Names help operators find resources; deletion requires
the complete ownership tags, exact IDs and matching parent relationships.

## Cleanup and failure

The driver must prove ordinary scale-in and disk/ENI absence before emergency
cleanup. The fixture owner then independently removes its group, remaining
workers/disks/ENIs, template, key pair, security group, vSwitch and VPC. Two complete
inventories must confirm absence. Unknown creation outcomes are recorded before
the call, reconciled through atomic ownership tags, and never blindly replayed.
An unresolved create or ownership change prevents a clean result.

`Pre-release cloud janitor` runs independently after Release/Cloud acceptance,
hourly, and on manual dispatch. It discovers labelled fixtures without relying
on a prior runner's files/artifacts, verifies the owning GitHub run attempt is
completed, and uses the separate cleanup role. A completed-run event can recover
that exact run/attempt immediately; scheduled/manual scans recover expired
fixtures. Manual cleanup can also select an exact completed run and attempt.
ECS auto-release is an additional one-hour bound, not a substitute for
fixture teardown or an account spending cap.

A skipped/missing scenario, incomplete configuration, unavailable Spot capacity,
missed injection window, product failure, failed cleanup or missing receipt blocks
publication. Later janitor success does not turn a failed acceptance run into a
pass. Inspect failures and clean leftovers before starting a fresh complete run;
do not automatically retry paid scenarios or reuse old successful receipts.
Only sanitized summary receipts are uploaded. Raw CLI replies, account/resource
identities, SSH material and driver logs remain private on the ephemeral runner.

## Manual use

Use **Cloud acceptance** on main with an exact version and candidate SHA, selecting
one mode or `all`. A single-mode success never supplies publication approval.
Use **Pre-release cloud janitor** to request recovery without publishing.
[CONTRIBUTING.md](../CONTRIBUTING.md) owns executable dispatch commands.
Dispatching Cloud acceptance is a paid cloud operation; dispatching Release also
authorizes publication. Implementing or merging this gate performs neither.
