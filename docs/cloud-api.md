# ESS/ECS API boundary

The client uses the official signed transport and checks exact region/group
identity. ESS membership is fully paginated; ECS queries are divided into at
most 100 requested IDs and require complete count-checked responses. Missing
counts, duplicate or unrequested identities, and changing pagination totals
fail the snapshot instead of reporting absent workers.

A missing member instance ID or group ID invalidates the entire snapshot. The
client may reread it from page one for up to 30 seconds, bounded by the caller's
deadline, without publishing partial members or filling in an absent group ID.
A nonempty foreign group ID is rejected immediately. Diagnostics include the
request ID, page/row and missing-field flags without instance or group values.

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
