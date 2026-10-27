# ADR 0002: Serialize absolute targets and specified release

Status: accepted.

## Expansion

Use ScaleWithAdjustment(TotalCapacity, SyncActivity=false), with one random
ClientToken per logical operation. Its asynchronous mode changes desired
capacity; it does not certify creation or readiness. Clamp targets to the
validated MaxSize before writing. Serialize reads and mutations with a
context-aware gate. A successful mutation retains its target until cloud desired
capacity confirms it. A later Increase starts at that target, not an older cloud
snapshot. Never write a lower target during expansion. An observed larger target
without accepted local intent is a competing-writer error.

Before the next mutation, confirm previous intent within a bounded deadline.
Queries never replace pending intent with stale values. Retry transient transport,
throttle and activity-conflict failures with the same token and bounded jittered
backoff. If a write outcome remains ambiguous, keep its token and target, resolve
it before allowing a different write, and fail closed on persistent ambiguity.
No automatic compensation creates a second logical request.

Cloud desired capacity and the non-propagating group-tag operation journal are
recoverable operation data. Persist token/original desired/IDs before writes,
then activity and accepted-mask facts after acceptance. If persisting accepted
removal facts fails, retry that persistence before activity progression; do not
lose this stage merely because the API activity ID is already known. Verify the journal hash
and invariants before mutation; incomplete records stop writes. If journal write
acceptance or confirmation is lost, retain the same local operation/token until
it is confirmed. On restart, compute unobserved capacity from cloud desired intent (or a prepared
journal target) minus discovered non-removing members. This also applies when no
journal remains. Cap that credit as members are discovered and consume it before
new writes; never treat already discovered capacity as a new requested slot.
Normal accepted requests do not replenish this restart-only credit.


After restart, the plugin reconstructs desired capacity and members from complete
snapshots and does not replay a prior Increase. A crash before reporting accepted
capacity can leave pending capacity visible to the host; initialization adopts
that actual cloud target. The sole-writer requirement also applies during
restart; a second concurrent Manager is unsupported.

## Deletion

RemoveInstances(IDs, RemovePolicy=release, DecreaseDesiredCapacity=true,
IgnoreInvalidInstance=true) is the only normal scale-in operation. Validate
membership and automatic creation origin, deduplicate, batch at 20 and wait for
each activity before submitting the next. Record ignored IDs separately. Return
only accepted logical removals. Already absent IDs succeed without another
capacity decrement only after ECS absence is verified. Never substitute a lower
desired assignment that lets ESS select an unrelated running worker.

Track the accepted activity and token while it is unresolved. Reconcile Warning
per ID; fail activity errors and keep still-present members available for retry.
Update emits deleting before final absence, and deleted only after complete ESS
and ECS queries confirm disappearance. API failures do not emit missing states.
A removed member that still exists in ECS is an error requiring operator cleanup.

## Interruption and health replacement

Desired capacity already contains replacement intent. Disappearance does not
increment it. Complete successful snapshots and ECS absence establish deletion;
transient query failure or unknown states do not establish health failure.
Readiness is the Runner connector's bounded SSH/readiness command, separate from
ECS Running and ESS InService.

## API evidence

- [ScaleWithAdjustment](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-scalewithadjustment): TotalCapacity, ClientToken, asynchronous desired changes and clamping.
- [RemoveInstances](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-removeinstances): Active/no-running-activity prerequisite, 20-ID limit, ignored IDs, release policy and decrement.
- [ModifyScalingGroup](https://help.aliyun.com/zh/auto-scaling/developer-reference/api-ess-2014-08-28-modifyscalinggroup): lowering desired may remove cloud-selected members, so it is not normal scale-in.

Tests must cover pending increases, interleaving, lost replies, batching, warning
subsets, activity failure, restart, replacement and stale/incomplete snapshots.

## Conservative partial-failure boundary

A terminal removal can be retired when cloud desired equals original desired
minus the IDs proven released. Failed IDs can then be retried with a new token.
If partial desired rollback differs, do not guess a debit or issue a downward
absolute assignment that could choose other workers. Retain the durable journal
and require scoped investigation; the API documents do not establish the needed
per-ID rollback guarantee. This semantic remains live-unverified. Unknown removal
responses older than the 30-second recovery replay window also fail closed; no
unlimited server idempotency-retention promise is inferred from ClientToken.
