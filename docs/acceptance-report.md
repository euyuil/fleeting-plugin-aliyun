# Implementation and acceptance report

Reviewed on 2026-10-28. This report separates implemented code, offline evidence,
live acceptance, and external publication. The machine-readable status is
[`acceptance/status.json`](../acceptance/status.json); exact source versions are
in [`upstream.lock.json`](../upstream.lock.json).

## Delivered implementation

The provider implements the pinned nine-method Fleeting interface using the
Alibaba OpenAPI SDK and standard credential abstraction. It validates a dedicated
ESS group, serializes capacity changes, journals recoverable intent, reconciles
pending capacity after restart, and releases only specified members in batches
of at most 20. Full ESS/ECS snapshots protect membership from partial responses.
API errors, disappearance, replacement, and worker readiness remain distinct.

Linux static SSH is the default. Explicit connector settings take precedence
over ECS OS, architecture, user and address metadata. Docker Autoscaler uses the
same provider with a separately prepared image. Windows amd64 has static WinRM
HTTPS connection information. Dynamic Linux SSH is opt-in: Cloud Assistant gets
only the expiring public key; the worker helper uses a separate authorized-key
file and bounded, identity-checked command completion.

The production provider advertises no suspend/resume capability. Its methods
return the standard unsupported error. An isolated controller journals exact
IDs, enters ESS standby before stopping, starts the same VM before exiting
standby, preserves desired capacity, and rejects unrelated standby members.
Formal promotion requires independent live state and resumed SSH evidence.
See the [capability matrix](upstream-parity.md) and the six ADRs for constraints.

The explicit live tools include a fixed GitLab fixture, checksum-verified Runner
Manager, runner-token/project binding, nonce/fixture/job correlation, strict Spot
and creation budgets, durable resource inventories, normal-release assertions,
and three cleanup layers. The manual workflow defaults to smoke. Extended scale,
controlled deletion/replacement and suspend suites use separate explicit modes.
The independent janitor requires configured roles and explicit enablement.

Native Linux amd64/arm64 archives include version information, checksums, linked
module licenses/notices, Go notices, and source locations. The OCI writer and
installation test use the upstream Fleeting artifact implementation. The manual
release workflow checks immutable versions/tags, serializes publication, and
verifies the source tag before publishing. No release was published here.

## Selected baseline

- Runner 19.5.1 is the initial minimum and primary version.
- Fleeting is pinned to `6ed75a33d303` as selected by that Runner release.
- Reference providers: AWS 1.3.1, Google Cloud 1.3.1, Azure 1.2.0.
- Go 1.27.2, OpenAPI SDK 2.2.4, credentials library 1.4.14, Alibaba CLI 3.5.2.
- Full commits, executable hashes, Action commits and tool versions remain in the
  lock file rather than being independently maintained in this report.

## Actual offline validation

The following documented commands passed during delivery. No cloud or GitLab
credentials were used. API-boundary fakes, signed loopback HTTP, real plugin
processes, authenticated loopback SSH, temporary files and a loopback OCI registry
supply the controlled boundaries.

| Command or check | Evidence and limit |
|---|---|
| `make deps` | Locked complete module graph downloaded and verified; repeated installation left checksums stable. |
| `make build` | Actual native plugin binary built with revision/version information. |
| `make test` | Provider scenarios T01–T18, API signing/errors, journal/restart regressions, process/SSH, fixture and official OCI installer tests; seven Python guards. |
| `make test-component` | Actual plugin process and SSH, helper file behavior, and synthetic fixture CLI requests. |
| `make lint` | Formatting, vet, pinned actionlint, finite configuration checks, and tracked whitespace. No configured hook or security scanner is claimed. |
| `go test -race ./...` | Offline Go packages passed race-enabled execution. |
| `go build -tags live -o bin/aliyun-acceptance ./cmd/aliyun-acceptance` | Optional live CLI compiled; enabling live cloud operations was not part of this check. |
| Live CLI with `RUN_LIVE_ACCEPTANCE` unset | Nonzero exit and `NOT_RUN_MISSING_CONFIGURATION` result; no SDK construction. |
| `make test-live-extended` with live inputs absent | Tagged suite compiled and explicitly skipped with a NOT_RUN artifact; this is not a live PASS. |
| `make dist` | Both Linux architectures cross-compiled and archive checksums/licenses generated. Linux amd64 `version` also executed in CI; arm64 execution is unverified. |
| `make test-oci` | Official releaser-to-installer loopback round trip and exact installed bytes. External GHCR and Docker executor execution are unverified. |
| Pinned Runner config parsing | All three examples parsed by the real checksum-verified Runner. No GitLab registration or job ran. |
| Staged review | Full staged scope, new-file whitespace, and `git diff --cached --check` checked per commit. Significant implementation slices received independent review. |

[CONTRIBUTING.md](../CONTRIBUTING.md) owns the executable procedures. CI runs the
same offline checks, builds the tagged CLI, packages both architectures, executes
the Linux amd64 asset, and retains release candidates for seven days. CI success
is not evidence that a live job or publication happened.

## Live acceptance and resource outcomes

Plugin-only cloud smoke: **PASS** on 2026-10-28. The real plugin process completed
**0→1→authenticated SSH→0** against Alibaba Cloud. Full GitLab job acceptance is
still **NOT_RUN_MISSING_CONFIGURATION**. No GitLab project/job, custom worker
image, persistent Manager or cloud OIDC configuration was created. All temporary
cloud resources from this smoke and the earlier connectivity preflight were
deleted and their absence was verified.

Raw evidence, resource identities, account information and local runtime paths
remain outside version control. The summary below records the selected facts
needed for maintenance; it is not production acceptance or a publication claim.
The live tools retain private JSON receipts under the selected output directory.

### Direct ECS connectivity preflight

On 2026-10-27, one explicitly authorized Spot instance ran in `us-east-1b` with
`ecs.e-c4m1.large` (2 vCPU, 0.5 GiB RAM), a 20 GiB ESSD PL0 system disk, and the
public image `ubuntu_26_04_x64_20G_alibase_20260916.vhd`. The guest reported
Ubuntu 26.04.1 LTS and kernel `7.0.0-31-generic`. A 512 MiB swap file used space
inside the existing system disk. The instance had a 45-minute automatic release
deadline and was manually released after the checks.

- Boot and direct CLI SSH passed with a dedicated client key and strict checking
  against a separately provisioned temporary server host key.
- Guest HTTPS requests to GitHub, its API, raw content and codeload returned 200.
- A real GitHub-hosted runner reached the US East ECS, ESS and STS public HTTPS
  endpoints. Their unsigned requests returned 400, proving connectivity rather
  than cloud authorization. The runner also matched the scanned SSH public host
  key after only its current egress /32 was allowed. This scan does not prove
  possession of the server's private host key or SSH client authentication.
- The dedicated instance, system disk, primary ENI, key pair, security group,
  vSwitch and VPC were deleted. Two complete, identity-checked inventories
  confirmed no owned resources remained. Temporary GitHub secrets/artifacts and
  local keys were removed; private runtime receipts remain outside Git.

The [network workflow run](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40531496389)
completed successfully. This preflight used direct ECS APIs and SSH; it did not
exercise the Fleeting plugin, ESS scale-in, a GitLab job, dynamic SSH or recovery.

### Real plugin-only scaling smoke

The successful run used release-build source
[`100df4fce0c2c3acb136672ba97e2b8fea91ab6c`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/100df4fce0c2c3acb136672ba97e2b8fea91ab6c),
subsequently squash-merged with identical content as
[`7feb6f0912aa3a86f1ee0bc378df2cbfbf9382d7`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/7feb6f0912aa3a86f1ee0bc378df2cbfbf9382d7).
The executed plugin SHA-256 was
`940b958bbfeb0803594899778c8b9871a16198f89ae0e711b52f1eec0e8afc7f`.
The local driver launched that binary over the real Fleeting plugin protocol;
it did not directly create the worker with ECS APIs. The fixture used the
same `us-east-1b`, `ecs.e-c4m1.large`, Ubuntu 26.04 public image, 20 GiB ESSD PL0
and 512 MiB in-disk swap configuration as the preflight. This smoke did not
measure the guest's runtime swap state. Actual ECS and disk
responses verified the fixed type/image/zone, 2 vCPU, 512 MiB RAM, Spot purchase,
disk size/category/performance level and delete-with-instance policy. Each
isolated attempt allowed only one worker, with no on-demand fallback.

| Successful transition | Observed evidence (UTC) |
|---|---|
| Initial 0 | Empty ESS group, desired and total capacity both zero. |
| Plugin `Increase(1)` | Exactly one actual Spot member and desired/total capacity 1 at 03:03:40. |
| Plugin `ConnectInfo` and SSH | Pinned upstream connector authenticated using the temporary client key and returned the exact per-run nonce at 03:04:19. |
| Plugin specified `Decrease` | Desired/total/pending/removing capacity zero, no ESS/ECS members, and associated disk/ENI absence confirmed at 03:05:13, before fallback cleanup. |
| Fixture teardown | Driver/plugin stopped; all owned instances, disks, ENIs, snapshots, ESS group, launch template, cloud key pair, security group, vSwitch and VPC absent in two complete independent inventories. |

Three isolated plugin attempts created three Spot instances in total, one per
attempt. The first failed on incomplete ESS member identity and was fully
cleaned. Later diagnostics captured `LifecycleState=Init` with an empty
`InstanceId` and a correct group ID; the client now rereads the complete snapshot
within a bounded deadline. A synthetic raw-response regression includes this
observed shape. The second reached actual scale-out but failed SSH readiness.
A subsequent read-only catalog query confirmed `CpuArchitecture=X86`, which the
old default `ConnectInfo` path rejected; the driver had not retained its last
connection error. The final attempt passed after the catalog mapping fix.
Fixture preparation also exposed the required `SpotInstancePools` value.
These fixes were delivered in [#18](https://github.com/euyuil/fleeting-plugin-aliyun/pull/18),
[#20](https://github.com/euyuil/fleeting-plugin-aliyun/pull/20) and
[#21](https://github.com/euyuil/fleeting-plugin-aliyun/pull/21).

Both failed attempts received independent cleanup and two absence checks before
the next attempt. Final fixture cleanup encountered one transient connection
reset during a read-only snapshot inventory; rerunning cleanup after the driver
was confirmed stopped completed the deletion and the final two inventories.
All temporary local client/server keys and encoded server-key user data were
removed after cleanup. No cloud credentials were placed on the worker or in
GitHub secrets. Local CLI OAuth supplied the SDK credentials; this did not test
GitHub OIDC, restricted RAM roles or STS renewal.

SSH proves client authentication and command execution. The pinned upstream
connector does not verify the server host key, so this run does not establish
that separate identity property. It also does not exercise a GitLab job, all nine
provider methods, multi-instance retention, restart recovery, real Spot
interruption, dynamic SSH, Windows, Docker or suspend/resume.

The following gates remain specific and visible:

| Gate | Completed work | Next evidence required |
|---|---|---|
| Plugin-only 0→1→SSH→0 | Real plugin, actual Spot/disk checks, authenticated SSH, normal specified release and full fixture cleanup passed | Broader lifecycle scenarios remain separate. |
| Basic 0→1→job→0 | Plugin-only cloud smoke passed; real job driver and fixed GitLab fixture implemented | Authorized configured run with the pinned Manager, exact GitLab job/nonce, actual Spot identity, normal release, and zero verified leftovers. |
| Extended scale/replacement | Tagged SDK/provider/SSH suites and fixed budgets | Separate prepared fixture and explicit mode. DeleteInstance injection does not demonstrate a genuine Spot notice. |
| Dynamic SSH | Public-key delivery, expiration, helper and reacquisition implementation/tests | Prepared Cloud Assistant image, restricted RunCommand identity, and actual key delivery/SSH timing. |
| Windows and Docker | Connection logic, examples and offline checks | Prepared images, network route, and actual Windows/Docker executor jobs. |
| Formal suspend/resume | Standard unsupported production methods; isolated controller and offline tests | Same-ID ESS/ECS terminal states, unchanged capacity, fresh SSH after resume, interruption recovery and verified cleanup before capability activation. |
| External publication | Reproducible native/OCI code and manual release workflow | Explicit version/publication authorization, suitable GitHub package/release access, actual GHCR visibility and anonymous Runner installation. |

M0 and the offline M1 implementation are complete. M2 tooling is implemented;
its plugin-only cloud smoke passed, while the GitLab job cycle is unrun. M3
retains formal suspend and broader live acceptance gates;
full official capability parity is not complete. M4 local distribution and
maintenance tooling are delivered; external publication is unrun. These limits
follow the specification's no-configuration path.

## One-time configuration and cost responsibilities

For full GitLab job acceptance, follow [infrastructure preparation](../infra/README.md), the
[fixed GitLab fixture](../tests/fixtures/gitlab/README.md), and
[testing setup](testing.md). Supply an exact region/group, fixed type/image/zone,
a prepared worker user/public key, matching private key secret, GitLab HTTPS
project/ref/SHA/tag/runner ID, runner authentication token and API token. Configure
separate scoped OIDC roles/provider, actual trust claims, and the independent
cleanup role; enable the janitor only after reviewing that scope. The exact
variable names are listed in `acceptance/status.json` and the setup guide.

GitHub runs offline CI and a temporary Manager only in explicit live jobs.
GitLab owns the fixed project/runner, checkout, job scheduling and artifact.
Alibaba Cloud supplies the selected ESS/ECS/Spot/network resources. Ordinary PRs
receive no cloud OIDC identity or GitLab secrets. No paid GitLab project token,
persistent Manager VM, Kubernetes, or new paid network service is required by
default. Provider, fixture-bootstrap and cleanup permissions are separate.

Default smoke permits MaxSize=1 and one cumulative observed worker ID, including
replacements. It has a 20-minute stage and 40-minute workflow, with bounded
acquisition and fresh-identity cleanup. Extended modes select their own fixed
budgets before claim. ESS observation is not an atomic cumulative billing cap.
Compute, disks, images, public bandwidth/traffic and billing granularity can still
incur charges. The tools do not switch to on-demand, change regions/types, or
raise budgets on failure. Infrastructure teardown is explicit and separate from
worker cleanup; reusable VPCs, templates, images and fixed GitLab runners are not
deleted automatically.

The pinned upstream SSH connector does not verify SSH host keys, and its WinRM
connector skips server certificate verification. Use the isolated authenticated
route described in [operations](operations.md); this is an upstream identity
boundary, not a claim of verified server identity.
