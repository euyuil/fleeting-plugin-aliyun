# ADR 0005: Native Cloud Assistant delivery of expiring public keys

Status: implemented, opt-in; live validation not run.

The documented ali-instance-cli send_public_key path offers a roughly 60-second
login window but depends on Session Manager/configuration plugins. Its official
public API catalog does not expose SendPublicKey. StartTerminalSession exposes
session commands/connection URLs rather than a stable public-key-registration
method. AttachKeyPair needs reboot and is not an immediate substitute. An
unversioned proprietary CLI download is not introduced into the provider.

Use the public ECS RunCommand/DescribeInvocationResults APIs through the native
SDK to invoke an explicitly prepared helper as the configured POSIX job user.
The helper installs an OpenSSH public key carrying an absolute 60-second
expiry-time. OpenSSH enforces expiry independently of Manager lifetime. The
provider returns an earlier refresh deadline and creates a fresh key on every
ConnectInfo. No private key or cloud credential is sent to the worker or command
history. KeepCommand=false avoids retaining a reusable cloud command object.

Prerequisites are Linux, a compatible OpenSSH, Cloud Assistant, synchronized UTC,
flock/getent/awk/base64, the root-owned helper, and an AuthorizedKeysFile setting
that includes its dedicated user-owned file. WorkingDir is /, and the helper
resolves the current account's real home from passwd rather than trusting HOME.
It locks updates, retains other unexpired keys, prunes expired entries and
replaces the file atomically with private permissions. It does not alter the
static authorized_keys file.

A bounded 30-second command delivery window leaves at least 20 seconds to begin
SSH. Acquisition requires an explicit zero exit code and the helper's fixed
`fleeting-key-ready` output. API/invocation failure or a shortened login window fails acquisition;
callers reacquire, rather than reusing expired data. Existing established SSH
sessions continue. Network reachability is still required; this is not a private
network tunnel. Windows is not supported by this dynamic path.

RunCommand's current RAM metadata requires broad account resource scope, despite
some rendered documentation showing instance resources. Enforce CommandRunAs for
the dedicated job user and use an isolated account/reviewed boundary. Do not
claim unverified instance/tag restrictions. Static SSH remains the default.

Sources reviewed 2026-10-27:

- [Temporary public-key CLI](https://help.aliyun.com/en/ecs/user-guide/register-a-public-key-and-connect-to-an-instance-with-the-key-by-using-ali-instance-cli).
- [RunCommand](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-runcommand) and its API/RAM metadata.
- [OpenSSH authorized_keys expiry-time](https://man.openbsd.org/sshd.8).

Offline evidence covers key generation/refresh, public-only command payloads,
invocation identity, helper expiry pruning, concurrent writes, file permissions
and incorrect inherited HOME. Actual Cloud Assistant delivery and SSH timing on
Alibaba Cloud remain separate live acceptance work.
