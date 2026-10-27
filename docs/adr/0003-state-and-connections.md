# ADR 0003: Publish complete state and refresh connections

Status: accepted.

ESS membership is the group boundary; ECS describes its concrete machines. Read
all membership pages and ECS ID batches. Validate identities, required counts,
duplicates and stable pagination totals. Never publish a partial snapshot.
Malformed/null responses and API failures leave the previous published set
intact. Unknown lifecycle/status combinations are creating, not implicitly
running. Deletion requires absence from both complete ESS and ECS queries; an
ECS instance outside ESS is a residual resource error rather than a successful
release.

Capacity writers use one cancellable gate. Update publication uses a separate
cancellable gate and short memory locks, so asynchronous deletion does not block
health/address queries and Update callbacks cannot reorder snapshots. Caller
callbacks must return; the official gRPC stream observes its context. Service
requests use the official context-aware SDK call, bounded transport timeouts,
limited retry attempts and jitter. The credentials abstraction has its own
refresh/network timeouts; it does not expose a universal context API. Static
STS environment credentials avoid network refresh, but require a fresh identity
when they expire.

ConnectInfo refreshes ECS on each request, supplies both addresses and preserves
explicit connector settings. OS and architecture inference fills only omitted
fields. Static Windows requires amd64/WinRM HTTPS and a prepared certificate and
password. Cloud Running/InService is not proof of SSH or worker readiness;
Runner owns bounded connection and readiness commands.

The pinned Fleeting WinRM connector creates its HTTPS endpoint with the
insecure flag enabled, and its provider protocol has no server CA verification
setting. The static Windows path therefore encrypts transport but does not
verify the server certificate. Use an isolated authenticated network and treat
verified server identity as an upstream limitation. This has not been live
validated. See the [pinned connector source](https://gitlab.com/gitlab-org/fleeting/fleeting/-/blob/6ed75a33d303/connector/winrm.go).

The pinned SSH connector also uses `ssh.InsecureIgnoreHostKey`: it authenticates
the client but does not verify SSH server host keys. Restrict Manager routing and
worker ingress to the intended dedicated network boundary. See the
[pinned SSH source](https://gitlab.com/gitlab-org/fleeting/fleeting/-/blob/6ed75a33d303/connector/ssh.go).
