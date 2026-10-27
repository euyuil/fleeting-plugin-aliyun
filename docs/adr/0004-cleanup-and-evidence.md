# ADR 0004: Exclusive run ownership and verifiable cleanup

Status: accepted.

A fixture is an exact project + region + group, initially empty and idle. Claim
it only when no previous run marker exists. Preserve existing local receipts and
use a separate run/attempt evidence directory. Capture the complete activity-ID
baseline before claiming; persist its digest on the group to authenticate a
recovered ledger's baseline. Group tags are ownership/expiry metadata, not an
assumption that ECS/disks/ENIs inherited tags.

The live driver is the Manager's parent. It stops and waits for that exact process
before fallback deletion. A separate local cleanup verifies PID/config identity
through /proc. An independent janitor obtains fresh credentials, verifies exact
run ownership and TTL, and confirms that the original GitHub run/attempt has
completed. An active run is not reclaimed only because its expiry elapsed.
Repository concurrency is additional serialization, not a distributed lock.

Inventory current auto-created members and associated disks/ENIs before
releasing them. A temporary zero disk/ENI result is pending evidence, not a
complete empty inventory. Require unique nonempty IDs, exact instance attachments,
one system disk and one primary ENI. ECS visibility lag while ESS still owns the
member remains pending; a missed lifetime without inventory stays unverified. Refresh while the VM exists. Unsafe disk deletion
policy fails ordinary observation; cleanup still cancels replacement intent and
releases provably owned compute, then reports retained disks as leftovers.
Unknown associated-resource evidence never becomes PASS.

Cleanup first zeros the exclusive disposable group's desired capacity. It may
then release remaining specified members with DecreaseDesiredCapacity=false,
because intent is already zero. This includes an explicitly tagged suspend
experiment's standby members, which ordinary desired scale-in does not remove.
Normal production Decrease continues to use true and its durable journal.
Cleanup never deletes unrelated account resources by name, detaches billed VMs,
or destroys reusable infrastructure. It verifies ESS, ECS, disks, ENIs and owned
narrow ingress rules before clearing markers.

A default smoke has a cumulative budget of one new instance ID. Count new group
members and new activities after the exact baseline. Normal provider-driven
release must complete before PASS; fallback cleanup cannot make a broken
Decrease pass. Recheck budget and resource verification after cleanup. Evidence
writes are atomic, synchronized and checked. Failure/cancellation/credential
expiry keeps useful identifiers and cannot yield a fabricated pass.

Three layers are process defer cleanup, an always-run workflow step with fresh
identity, and a trusted independent hourly/completion/manual janitor. If a crash
loses the activity baseline or pre-release resource inventory, cleanup can release
known members but retains a failed verification result and ownership markers.
Recover a scope-validated prior ledger artifact or perform an explicit scoped
inventory before certifying release. No missing evidence is guessed away.
