# Pre-release cloud acceptance

Release requires the six plugin-only cloud scenarios and both
[packaged GitLab job checks](job-acceptance.md) before creating a source tag,
publishing OCI packages or creating a GitHub Release. This applies to release
candidates and stable versions. PR and main CI remain credential-free. The six
scenarios below exercise the plugin directly; the separate job gate uses a real
systemd Runner Manager and the dedicated GitLab.com fixture project.

## Configure a separate account

Use a dedicated Alibaba test account. The gate never reads a developer CLI
profile or the older `ALIYUN_*` repository configuration. Nothing is enabled by
merging the workflow: incomplete configuration fails the release gate before
resource creation. Do not supply an AccessKey or a browser-login refresh token.

1. In the **new account**, activate ECS, VPC and ESS, confirm Spot quota in the configured region,
   and prepare the ESS service-linked role required to create/release workers.
2. Create a RAM OIDC provider for issuer
   `https://token.actions.githubusercontent.com` and audience `sts.aliyuncs.com`.
   Create one RAM role for acceptance and cleanup. Configure its OIDC trust
   for the repository's exact `aliyun-pre-release` environment subject, resolved
   using the RAM walkthrough below. Do not assume the name-only subject format. Follow the
   [official Alibaba OIDC action documentation](https://github.com/aliyun/configure-aliyun-credentials-action).
3. Create the GitHub Actions environment **aliyun-pre-release**. Restrict its
   deployment branches to **main**. Keep emergency cleanup runnable without a
   pending human approval; release initiation itself remains an explicit manual
   operation. Trust the exact repository/environment, not every repository in
   the owner account. See [GitHub OIDC guidance](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-cloud-providers).
4. Set the following in that **Environment**, initially leaving both enabled flags false.
   Account identifiers and ARNs should normally be **Secrets**. The workflow also
   accepts Variables; a nonempty Secret takes precedence. Resource settings may
   be Variables or Secrets. Do not keep duplicate names in both stores. The
   reusable Release workflow explicitly forwards these named secrets. Values
   are never committed; local/offline tests use synthetic fixtures only:

   | Name | Value |
   | --- | --- |
   | `RELEASE_CLOUD_ACCEPTANCE_ENABLED` | Allows new paid acceptance runs; requires cleanup to be enabled too |
   | `RELEASE_CLOUD_CLEANUP_ENABLED` | Allows independent recovery with new acceptance disabled; enable after reviewing the account, role and cleanup regions |
   | `RELEASE_ALIYUN_ACCOUNT_ID` | The new account's 16-digit account ID |
   | `RELEASE_ALIYUN_ROLE_ARN` | Acceptance role ARN in that account |
   | `RELEASE_ALIYUN_CLEANUP_ROLE_ARN` | Optional separate cleanup role ARN; omitted by default, so cleanup uses the acceptance role |
   | `RELEASE_ALIYUN_OIDC_PROVIDER_ARN` | OIDC provider ARN in that account |
   | `RELEASE_ALIYUN_FIXTURE_CONFIG` | Required resource configuration JSON described below |
   | `RELEASE_ALIYUN_CLEANUP_REGIONS` | Required JSON array of region IDs, including the fixture region and previous regions with outstanding resources |

5. Follow the RAM walkthrough below and run **Cloud configuration check** on
   main while the enabled flag is still false. Review write scopes separately
   before explicitly enabling paid acceptance. Before any writes, STS
   `GetCallerIdentity` must match the configured account. Every ARN must belong to that same account. Each scenario gets fresh
   one-hour STS credentials; no local profile fallback is permitted.

The pinned CLI requires a configured profile before applying credential
overrides. Each invocation receives a private temporary profile populated only
from the fresh OIDC STS credentials. Its directory is mode 0700, its file is mode
0600, and both are removed when the invocation returns or raises an exception.
Credentials are never included in command arguments or acceptance artifacts.

The acceptance role needs the [provider's ESS/ECS actions](cloud-api.md), plus
fixture creation and cleanup. Fixture operations are VPC Create/Describe/Delete
Vpc and VSwitch; ECS Create/Describe/DeleteSecurityGroup, AuthorizeSecurityGroup,
Import/Describe/DeleteKeyPairs, Create/Describe/DeleteLaunchTemplate, instance,
disk and ENI reads/deletes; and ESS Create/Enable/DeleteScalingGroup and member
reads. Create calls attach tags atomically, so include each API's dependent tag
permissions. Existing acceptance also manages owned ingress and instance expiry.
With the default single role, both workflows receive the acceptance role
permissions. An optional separate cleanup role only needs these inventories,
ESS member reads and deletion actions, without create or scale-out permissions.
Restrict to the configured regions and
test purpose/project tags wherever the API's RAM authorization metadata supports
those conditions. Some Describe actions require account-wide resource scope.
Review the official action metadata rather than granting administrator policies.
Dedicated account isolation is required even with these application checks.

## RAM setup walkthrough

1. Sign in to the new account as a RAM administrator. Open **Integrations > SSO >
   Role-based SSO > OIDC** (some console versions call this **Identity providers**).
   Create a provider, use the issuer/audience above, and obtain its certificate
   thumbprint using the console's **Get Fingerprint** control. Follow the
   [official provider procedure](https://help.aliyun.com/en/ram/manage-an-oidc-idp).
2. Open **Identities > Roles > Create role**, select the OIDC identity provider,
   or switch to the script editor. Create one role, for example
   `fleeting-pre-release`. Before saving its trust policy, query the repository's
   current OIDC subject settings using the
   [contributor guide](../CONTRIBUTING.md#inspect-the-oidc-subject).
   With `use_default: true`, append `:environment:aliyun-pre-release` to the
   returned `sub_claim_prefix`. With `use_immutable_subject: true`, that prefix
   includes immutable owner and repository IDs, for example
   `repo:OWNER@OWNER_ID/REPOSITORY@REPOSITORY_ID`; a name-only subject will not
   match it. For a customized template (`use_default: false`), inspect its
   configured claims and actual environment subject instead of applying the
   default suffix. Never log or share the full OIDC token. See
   [GitHub's immutable subject format](https://github.blog/changelog/2026-04-23-immutable-subject-claims-for-github-actions-oidc-tokens/).

   Replace both placeholders below with the provider ARN and exact resolved
   environment subject. Keep `StringEquals`; do not broaden trust with wildcards
   or disable immutable subjects to work around a mismatch:

   ```json
   {
     "Version": "1",
     "Statement": [{
       "Effect": "Allow",
       "Principal": {"Federated": "OIDC_PROVIDER_ARN_FROM_NEW_ACCOUNT"},
       "Action": "sts:AssumeRole",
       "Condition": {"StringEquals": {
         "oidc:iss": "https://token.actions.githubusercontent.com",
         "oidc:aud": "sts.aliyuncs.com",
         "oidc:sub": "EXACT_ENVIRONMENT_SUBJECT_FROM_GITHUB"
       }}
     }]
   }
   ```

   The trust-policy action is `sts:AssumeRole`, including for OIDC. Retain a
   maximum session duration of at least one hour. See the
   [official role procedure](https://help.aliyun.com/zh/ram/user-guide/create-a-ram-role-for-a-trusted-idp).
3. Copy the account ID, provider ARN and role ARN directly into the three required
   GitHub **Environment Secrets** above. Leave the optional cleanup-role secret
   absent for the single-role setup. No AccessKey, SSH private key or developer
   login profile is needed.
4. In **RAM > Permissions > Policies**, create a custom policy with the following
   content and attach it to the role. This initial policy permits only the setup
   check's inventory reads. It deliberately does not enable paid acceptance:

   ```json
   {
     "Version": "1",
     "Statement": [{
       "Effect": "Allow",
       "Action": [
         "vpc:DescribeVpcs", "vpc:DescribeVSwitches",
         "ecs:DescribeSecurityGroups", "ecs:DescribeKeyPairs",
         "ecs:DescribeLaunchTemplates", "ess:DescribeScalingGroups",
         "ecs:DescribeInstances", "ecs:DescribeDisks",
         "ecs:DescribeNetworkInterfaces"
       ],
       "Resource": "*"
     }]
   }
   ```

5. Run **Cloud configuration check** on main. It validates external configuration,
   obtains fresh OIDC credentials, checks the expected account with STS, and
   queries all nine inventories in each configured cleanup region. It also checks
   an optional separate cleanup role if configured. The CLI client has a read-only
   action allowlist; creation, deletion and scaling calls are rejected locally.
   The workflow never enables acceptance or uploads inventory/account values.
6. A passing check proves the selected identity, network and inventory reads.
   It does **not** prove create/delete permissions, service-linked-role readiness,
   instance quota, image availability, Spot capacity, SSH or cleanup behavior.
   Have the administrator add the acceptance operations listed above, scoped by
   the selected account/region/resource and supported tags. Verify each API's RAM
   authorization metadata; for example, `RemoveInstances` authorizes through
   [`ess:DetachInstances`](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2022-02-22-removeinstances),
   while expansion uses
   [`ess:ScaleWithAdjustment`](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2022-02-22-scalewithadjustment).
   Do not grant administrator policies to solve a missing action. Enable the gate
   only when this review and separate paid-operation authorization are complete.
   Set `RELEASE_CLOUD_CLEANUP_ENABLED=true` before enabling acceptance. Existing
   installations must add this explicit variable; an unset value disables the
   janitor and blocks new acceptance. Read-only configuration checks need neither
   switch. To stop new runs, set only `RELEASE_CLOUD_ACCEPTANCE_ENABLED=false`;
   keep cleanup enabled until all owned resources are confirmed absent.

If the setup check fails, use its step to locate the boundary: local preflight
means missing/invalid configuration; OIDC authentication means issuer/audience,
subject, provider or role trust; `GetCallerIdentity` means wrong account or STS;
`Describe*` means inventory permission/service availability. Do not paste raw
credentials or inventory responses into public issues. The
[contributor guide](../CONTRIBUTING.md) owns workflow dispatch commands.

## Candidate and scenarios

The workflow pins a full commit that is already on main. It runs offline tests
and the real Linux distribution/installer validation, then seals the complete
unpublished native/OCI payload. A separate responsegate binary is built from the
same clean commit. Artifact IDs and the manifest digest travel through job
outputs; downloaded files, Go source/platform/build tags, native/OCI binary
equality, and per-scenario receipts are checked again before publication.
Publication downloads the accepted artifact and never rebuilds the distribution.
The test-only responsegate binary is outside `dist/` and is never published.

Mode `all` requires all six scenarios followed by all real executor/scenario checks. Mode
`jobs` runs the fourteen executor/scenario checks against a sealed candidate, without
publication. A selected single plugin scenario remains available independently.
Only a complete `all` run can return `release_ready=true`; missing job
configuration or disabled job acceptance fails the full release gate.

The matrix runs serially with fail-fast enabled. Each scenario has a fresh
fixture and private evidence directory. Ordinary scenarios execute the native
Linux amd64 archive's binary. The response-loss case uses its separately marked
instrumented binary. This is Linux amd64 Manager cloud evidence; arm64 native
installation verification remains separate.

The caller holds the shared cloud concurrency group through the candidate job
checks. After publication and the anonymous installation matrix, Release
reacquires that group for all public-package job checks. Executor jobs are
serial and use distinct temporary fixtures. The reusable job workflow does not
acquire its caller's group a second time. Completion-triggered janitor recovery
includes these job fixtures through the owning Release or Cloud acceptance run.

Every scenario receipt retains a restricted diagnostic summary: test source line numbers,
fixed API/transport error categories, observed worker/check counts, boolean acceptance
checks, and matching error-message templates from the candidate's public Go
source. Templates retain format placeholders instead of private runtime values.
Raw driver logs, resource identities, API request IDs, and cloud error messages
remain private. The summary does not replace a passing scenario/cleanup receipt.

The `diagnostics` object separates fixture setup, the driver and outer fixture
cleanup, with an outcome and elapsed milliseconds for each attempted operation.
Driver evidence separately records the scenario outcome **before cleanup** and
the driver's own cleanup outcome. A cleanup failure cannot overwrite the original
scenario failure. Phase summaries identify inventory checks before SSH,
connection-info retrieval, SSH execution, restart checks and release/cleanup.
Repeated phase visits are aggregated; durations stop at the last received marker,
so a killed driver's final phase can be incomplete and its outcome stays `UNKNOWN`.
The first reported failure phase is retained even when later cleanup phases run.

API summaries retain only allowlisted service/action/code values and fixed error
categories. Unknown codes become `unknown`; request IDs, API parameters and error
messages are excluded. API actions are available for the live harness and fixture
CLI; plugin subprocess errors may have only a code/category. Phase attribution
describes when a message was observed, not proof of the underlying cause. The
driver runs with verbose output into its private log so interrupted runs retain
phase markers. A sanitized console summary also survives receipt-write failure.
All existing candidate, functional and zero-resource cleanup requirements still
apply; diagnostic outcomes never make an otherwise failed gate pass.

| Mode | Required outcome | Creation budget |
| --- | --- | --- |
| `cloud-smoke` | 0 → 1 → SSH → 0 | 1 |
| `cloud-scale` | 0 → 2 → specified 1 → 0; retained worker still accepts SSH | 2 |
| `cloud-restart` | Ready-worker restart rediscovers the original worker | 1 |
| `cloud-create-restart` | Restart during accepted creation produces one worker | 1 |
| `cloud-remove-restart` | Restart during accepted removal reaches zero | 1 |
| `cloud-increase-response-loss` | Lost expansion reply and restart create only one worker | 1 |

## External resource configuration

`RELEASE_ALIYUN_FIXTURE_CONFIG` is one JSON object. Every field below is required;
there are no deployed-account defaults in source control. Copy the field names
into the GitHub Environment Variable/Secret and select actual values in the new
account's ECS catalog. Do not paste private configuration into issues or commits.

| JSON field | Value and validation |
| --- | --- |
| `region` | Exact ECS/VPC/ESS region ID; must also be in the cleanup region list |
| `zone` | Exact zone ID in that region |
| `instance_type` | Explicit `ecs.*` instance type; compatible with the selected image |
| `image` | Immutable Linux image ID with cloud-init and root SSH support |
| `disk_category` | `cloud_efficiency`, `cloud_ssd`, `cloud_essd`, `cloud_essd_entry` or `cloud_auto` |
| `disk_size_gib` | Integer 20–2048; must satisfy the image/category's cloud requirements |
| `disk_performance_level` | Explicit `PL0`–`PL3` for `cloud_essd`; empty string otherwise |
| `internet_max_bandwidth_out` | Integer 1; the live acceptance policy currently requires 1-Mbps traffic billing |
| `spot_duration` | Integer 0 or 1 (hours) |
| `vpc_cidr` | Canonical RFC1918 IPv4 network, /8 through /28 |
| `vswitch_cidr` | Canonical IPv4 subnet within that VPC, /16 through /29 |
| `swap_mib` | Integer 0–2048; cloud-init swap size |

Malformed JSON, duplicate/unknown/missing keys, wrong types, unsafe CIDRs, or a
fixture region missing from the cleanup list fail before OIDC/resource creation.
The selected size/category/image/Spot availability still requires cloud-side
verification; syntactic validation does not promise capacity or a quoted price.
Every scenario uses the selected values for creation and the driver's allowlist.
Receipts contain only a canonical configuration digest, not private values;
all six receipts must have the same digest to pass the release gate.

VPC and vSwitch readiness polling allows a bounded delay for atomic creation
tags to become visible on the accepted resource ID. Conflicting tag values fail
immediately. Missing tags never authorize deletion: cleanup still requires the
complete ownership labels and matching resource identity and parents.
After an authorized deletion is accepted, absence polling tracks the recorded
resource IDs even if deletion has already removed their tags. It never issues
another delete or treats missing tags as proof that a resource is gone.

Cloud-init creates per-run SSH keys. Fixed protocol and safety requirements
remain in code: root SSH for these disposable Linux fixtures, strict Spot,
traffic billing, no on-demand fallback, no worker RAM role, deletion-on-release,
owned /32 SSH ingress, test purpose/ownership tags and bounded worker counts.
These are test invariants, not a hidden deployment configuration.
A complete successful suite creates seven workers cumulatively, with at most
two at once. Each driver has a 15-minute scenario deadline plus cleanup; each
scenario job has a 45-minute ceiling and a one-hour resource expiry.

Before changing region, retain both old and new regions in
`RELEASE_ALIYUN_CLEANUP_REGIONS` until the old region is proven empty. The janitor
validates identity and that list independently; it does not require a valid
fixture configuration. Before changing account/roles, finish cleanup with the
old identity first: the new account cannot discover or delete old-account
resources. Keep cleanup enabled until that proof is complete.

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
The driver's emergency zero-capacity request uses a stable token scoped to the
region, scaling group, CI run and attempt. Different scenario groups in one run
cannot share a token, while retries for the same group retain their identity.
Idempotency parameter conflicts have a fixed public diagnostic category.

`Pre-release cloud janitor` runs independently after Release/Cloud acceptance/Cloud cleanup drill,
hourly, and on manual dispatch. It discovers labelled fixtures without relying
on a prior runner's files/artifacts, verifies the owning GitHub run attempt is
completed, and uses the configured role (the acceptance role by default).
A completed-run event can recover
that exact run/attempt immediately; scheduled/manual scans recover expired
fixtures. Manual cleanup can also select an exact completed run and attempt;
this selection never includes another run's expired resources. An expiry scan
without a selector still requires each owning attempt to be completed, and
preserves active, foreign or ambiguously owned resources.
The janitor uses `RELEASE_CLOUD_CLEANUP_ENABLED`, independently of the creation
switch. Its CLI boundary allows only identity/inventory reads and deletion;
creation, expansion and tag mutation are rejected even when using the same RAM
role as acceptance. This local guard supplements the RAM policy and ownership
checks; it does not grant additional permissions.
ECS auto-release is an additional one-hour bound, not a substitute for
fixture teardown or an account spending cap.

A skipped/missing scenario, incomplete configuration, unavailable Spot capacity,
missed injection window, product failure, failed cleanup or missing receipt blocks
publication. Later janitor success does not turn a failed acceptance run into a
pass. Inspect failures and clean leftovers before starting a fresh complete run;
do not automatically retry paid scenarios or reuse old successful receipts.
Only sanitized summary receipts are uploaded. Raw CLI replies, account/resource
identities, SSH material and driver logs remain private on the ephemeral runner.

## Controlled interruption drill

**Cloud cleanup drill** is a separate manual entry on main. It is not a release
scenario and cannot satisfy the six-scenario gate. It uses the same parameterized
fixture configuration and dedicated account, requires both cloud switches plus
the explicit `confirm_abandon=true` input, and limits capacity to one Spot worker.
All resources have the existing ownership tags with `fleeting-scenario=cleanup-drill`
and names beginning `fleeting-pre-release-cleanup-`. It does not use GitLab.

The supervisor starts an isolated writer, which creates the fixture and requests
capacity one exactly once. After observing one running worker with the expected
Spot strategy, image, zone and instance type, it verifies cumulative worker IDs,
complete ESS activity history and group membership agree on exactly one creation.
Replacements, failed/unknown activities and incomplete history are rejected; a
history exceeding the single 50-row snapshot also fails closed, respecting the
[activity API's page-size limit](https://www.alibabacloud.com/help/en/auto-scaling/developer-reference/api-ess-2014-08-28-describescalingactivities).
The writer
publishes an atomic checkpoint and waits. The supervisor kills that process group,
proves termination and checks the writer's cleanup-started marker is absent.
This proves normal resource cleanup was bypassed. It uploads a
sanitized `INTERRUPTED` receipt and exits nonzero; the source workflow is expected
to fail. Before the checkpoint, ordinary failures attempt guarded fallback cleanup.
An unknown writer or failed termination proof never authorizes local deletion.

The interruption receipt also includes a separate `writer` diagnostic. The writer
atomically records its last stage and original failure before attempting cleanup;
the supervisor retains that failure even when fallback cleanup succeeds. Stages
separate authentication, fixture setup, the scale request, worker inventory and
configuration, creation-history verification, checkpoint publication and waiting
for interruption. The writer's cleanup result/error is separate from the
supervisor's existing `cleanup_result`/`cleanup_error` fields. Cloud failures expose
only allowlisted service/action, error code and category; validation failures may
include a fixed reason, and unexpected response shapes have their own category.
Raw messages, CLI replies, resource identities, configuration and writer logs are
never uploaded. Missing or unreadable diagnostics stay explicit and do not change
cleanup authorization, interruption proof, worker budgets or release eligibility.
The parent bounds and filters the diagnostic again before adding it to the public
receipt. Diagnostic file I/O failure does not suppress resource cleanup.

The completed source workflow triggers **Pre-release cloud janitor** on a fresh
runner with fresh OIDC credentials. Recovery discovers resources from cloud tags
and verifies the exact owning attempt is completed; it never downloads the
writer's state or artifacts. This automatic drill recovery requires **exactly one**
recovered fixture and two complete absence inventories. An empty scan fails the
drill proof instead of producing a misleading success. Its public cleanup receipt
binds the source run and attempt, recovered-fixture count and zero-leftover result.
Before deleting a drill fixture, it independently checks the complete creation
history and current member inventory again. An over-budget or unknown history
does not prevent safe cleanup, but sets `drill_budget_verified=false` and fails
the recovery proof. A cleanup-only RAM role needs `ess:DescribeScalingActivities`
for this drill evidence in addition to its normal inventory/deletion permissions.

Successful evidence consists of both the failed source run's `INTERRUPTED`
receipt (`workers_observed=1`, `writer_terminated=true`) and its independent
janitor's `PASS` receipt (`cleaned_fixtures=1`, `leftover_resources=0`,
`drill_budget_verified=true`). Janitor
success never changes the source failure or permits publication. A failed or
missing checkpoint, cleanup failure, empty scan, skipped job or absent receipt
does not establish recovery. The initial drill covers completion-triggered
recovery; scheduled expiry recovery still requires its own execution evidence.

Do not automatically retry a paid drill. If recovery fails, keep cleanup enabled,
inspect the sanitized evidence and invoke the existing exact-run janitor entry.
That manual fallback may report zero after another recovery already completed;
it does not retroactively prove the interrupted drill. ECS auto-release remains
only an additional bound, not proof that the surrounding fixture was deleted.

## Manual use

Use **Cloud acceptance** on main with an exact version and candidate SHA, selecting
one mode or `all`. A single-mode success never supplies publication approval.
Use **Pre-release cloud janitor** to request recovery without publishing.
[CONTRIBUTING.md](../CONTRIBUTING.md) owns executable dispatch commands.
Dispatching Cloud acceptance is a paid cloud operation; dispatching Release also
authorizes publication. Implementing or merging this gate performs neither.
