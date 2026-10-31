# ESS/ECS API boundary

The client uses the official signed transport and checks exact region/group
identity. ESS membership is fully paginated; ECS queries are divided into at
most 100 requested IDs and require complete count-checked responses. Missing
counts, duplicate or unrequested identities, and changing pagination totals
invalidate the snapshot instead of reporting absent workers. The official
[DescribeScalingInstances contract](https://www.alibabacloud.com/help/en/auto-scaling/developer-reference/api-ess-2014-08-28-describescalinginstances)
defines the page number, page size and total count used by these checks.

A missing member instance ID or group ID, a changed total, an early empty page,
or more rows than the total invalidates the entire membership snapshot. After
the first such read, the client may reread from page one within one shared
30-second window, waiting one second between attempts and honoring any earlier
caller deadline. Every attempt discards prior pages; no partial members or
invented group binding can escape. Persistent inconsistency remains an error.
Missing/null/negative counts, foreign groups, duplicate identities, API errors
and the pagination limit fail immediately. Hard identity violations take
precedence over retryable defects in the same response. Diagnostics include
page/count or missing-field information without instance or group values.
This read-only recovery does not repeat writes, retry ECS/history queries, or
extend readiness, release, case or resource-lifetime budgets.

Connection metadata maps the catalog's `CpuArchitecture` values `X86` and `ARM`
to Fleeting's `amd64` and `arm64`. Existing `x86_64`, `x64`, `arm64` and `aarch64`
aliases remain accepted. Empty, unknown or mismatched-type metadata fails unless
the connector architecture is explicitly configured. See the official
[DescribeInstanceTypes contract](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-describeinstancetypes).

Admission requires an Active ECS group, desired capacity, MinSize=0, finite
MaxSize, unit weights, a pinned template version, and no conflicting rules,
schedules, hooks, refresh or replacement configuration. Automatically created
members and ECS members explicitly entrusted to ESS for release are accepted.
The caller must validate the group before mutating it and serialize writes.

Expansion uses `ScaleWithAdjustment` with `TotalCapacity`; specified deletion
uses `RemoveInstances`, `RemovePolicy=release`, and a deliberate desired-capacity
decrement. Every logical write must keep the same ClientToken on retry. API
acceptance does not prove provisioning or release; callers must reconcile cloud
state and activity results. See [capacity decisions](adr/0002-capacity-reconciliation.md).

The non-propagating `fleeting-operation` group tags store one operation's token,
original and target capacity, specified IDs, activity, and accepted subset. The
hash and typed invariants detect incomplete records. A writer verifies persisted
intent before any capacity change and clears it only after reconciliation.
The dedicated group must have room within its 20-tag limit; the journal uses up
to 13 tags. This is a single-writer recovery mechanism, not a distributed lock.
Do not edit these tags while an operation is pending.

`ValidateSpotSmoke` is an additional, conservative live-fixture check: empty
0-to-1 capacity, fixed image/type/zone/key, actual Spot policy, no on-demand
fallback, release-on-delete disks/interfaces, and explicit bounded traffic.
It does not replace verification of the actual created instance. No test in this
slice contacts live services. Provider protocol and real-process tests follow
with the provider implementation.
