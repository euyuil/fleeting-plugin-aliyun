# Dynamic SSH image preparation

Static SSH is the default. To enable short-lived keys, prepare a Linux image
with Cloud Assistant, synchronized UTC, OpenSSH supporting `expiry-time`, and
`flock`, `getent`, `awk`, `base64`, and the standard shell utilities.

Install `infra/fleeting-authorize-key` root-owned and executable at
`/usr/local/bin/fleeting-authorize-key`:

```sh
sudo install -o root -g root -m 0755 infra/fleeting-authorize-key /usr/local/bin/fleeting-authorize-key
```

Configure sshd's `AuthorizedKeysFile` to
include `.ssh/fleeting-keys` for the dedicated job user, while retaining the
image's intended static key path. Validate the sshd configuration before baking
the image. The helper resolves the user's actual passwd home, writes only its
dedicated key file, locks concurrent updates, and preserves other unexpired keys.
It accepts one generated public-key line and never receives private keys.

Set `ssh_credentials = "cloud_assistant"` in `[runners.autoscaler.plugin_config]`
and `use_static_credentials = false` in the connector configuration. Specify a
POSIX username. This mode supports Linux SSH only. Static key/password settings
are not an alternative fallback for failed dynamic authorization.

The Manager calls `ecs:RunCommand` and `ecs:DescribeInvocationResults` through the
native SDK. Current RunCommand RAM metadata requires broad account resource
scope; enforce CommandRunAs for the dedicated job user and isolate/review the
account boundary. Do not assume unverified instance-tag restrictions. The
command uses `WorkingDir=/`, a bounded invocation timeout, and KeepCommand=false.
No additional proprietary CLI is needed.

Each ConnectInfo generates a new key with an absolute 60-second expiry and
returns an earlier refresh time. A completed command must have an explicit zero exit code and the fixed
`fleeting-key-ready` output, with at least 20 seconds left to begin SSH; shortened windows fail and require reacquisition. Existing
SSH sessions can continue after the login key expires. Network reachability
still needs a public or private route from the Manager.

See [ADR 0005](../docs/adr/0005-dynamic-ssh.md) for API alternatives, limitations,
and evidence. Offline tests cover payloads, refresh, helper file operations,
expiry pruning, concurrent writes, and invalid input. Real Cloud Assistant
execution and SSH timing have not been validated on Alibaba Cloud.
