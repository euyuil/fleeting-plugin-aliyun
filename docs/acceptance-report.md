# Implementation and acceptance report

Updated on 2026-10-29 (Asia/Shanghai). This report separates implemented code, offline evidence,
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
verifies the source tag before publishing. The corrected prerelease passed
anonymous public installation on both Linux architectures, as described below.

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
| `make test` | Provider scenarios T01–T18, API signing/errors, journal/restart regressions, process/SSH, fixture and official OCI installer tests, and Python guards. |
| `make test-component` | Actual plugin process and SSH, helper file behavior, and synthetic fixture CLI requests. |
| `make lint` | Formatting, vet, pinned actionlint, finite configuration checks, and tracked whitespace. No configured hook or security scanner is claimed. |
| `go test -race ./...` | Offline Go packages passed race-enabled execution. |
| `go build -tags live -o bin/aliyun-acceptance ./cmd/aliyun-acceptance` | Optional live CLI compiled; enabling live cloud operations was not part of this check. |
| Live CLI with `RUN_LIVE_ACCEPTANCE` unset | Nonzero exit and `NOT_RUN_MISSING_CONFIGURATION` result; no SDK construction. |
| `make test-live-extended` with live inputs absent | Tagged suite compiled and explicitly skipped with a NOT_RUN artifact; this is not a live PASS. |
| `make dist` | Both Linux architectures cross-compiled and archive checksums/licenses generated. On native Linux, the complete generated OCI payload is installed and loaded through the official library. Non-Linux hosts skip execution of cross-built binaries. |
| `make test-oci` | Official releaser-to-installer loopback round trip, retained notices, exact installed bytes, version/source and plugin protocol checks. External publication evidence is recorded separately below. |
| Pinned Runner config parsing | All three examples parsed by the real checksum-verified Runner. No GitLab registration or job ran. |
| Staged review | Full staged scope, new-file whitespace, and `git diff --cached --check` checked per commit. Significant implementation slices received independent review. |

[CONTRIBUTING.md](../CONTRIBUTING.md) owns the executable procedures. CI runs the
same offline checks, builds the tagged CLI, packages both architectures, executes
the Linux amd64 asset, and retains release candidates for seven days. CI success
is not evidence that a live job or publication happened.

## Live acceptance and resource outcomes

Plugin-only cloud smoke, two-worker scale-in, ready-worker restart and
accepted-creation and accepted-removal restart recovery: **PASS** on 2026-10-28. Real plugin processes completed
**0→1→authenticated SSH→0**, **0→2→1→0**, and same-instance rediscovery/SSH after
plugin termination. An instrumented expansion-response-loss restart also passed.
Released-plugin Linux amd64 **VM and Docker GitLab job cycles passed** with
Runner 19.5.1, as recorded [below](#released-plugin-vm-and-docker-jobs). Earlier
preparation failures remain recorded separately. One temporary prepared image
and its snapshot were created for these jobs and deleted afterward. Those local
runs did not create persistent Manager or cloud OIDC configuration. Their task
cloud fixtures were deleted and absence was verified twice per fixture. The later
dedicated-account GitHub OIDC gate is recorded separately below.

Raw evidence, resource identities, account information and local runtime paths
remain outside version control. The summary below records the selected facts
needed for maintenance; it is not production acceptance or a publication claim.
The live tools retain private JSON receipts under the selected output directory.

### Dedicated-account pre-release cloud gate

On 2026-10-29 (Asia/Shanghai), the manual
[Cloud acceptance run](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40724709276)
passed its build, all six serial scenarios, and final artifact/receipt gate.
The run used fresh GitHub OIDC STS credentials in the dedicated test account.
No GitLab job or release publication was performed by this run.

The accepted candidate is `ea3879ab92e1ec71822b73f0c13a96d920ef3de9`, version
`0.1.0-dev`, run attempt `1`. Its manifest SHA-256 is
`71dd14b46094a3b7404d9df99c9724d0625aa187f98122d44eb92505c5ef3dd9`.
The artifact is `cloud-candidate-40724709276-1` (ID `12831758870`); its archive
SHA-256 is `3b2b4451b275bb24ebe96e2ee618b5063506e4ff30a929cc0159415c5370151b`.
These identify different layers: the archive digest is not the manifest digest.
GitHub retains the payload for seven days. The selected public receipts are
archived in `pre_release_cloud_gate` in the machine-readable status above.

| Mode | Scenario | Fixture cleanup | Leftovers |
| --- | --- | --- | --- |
| `cloud-smoke` | PASS | PASS | 0 |
| `cloud-scale` | PASS | PASS | 0 |
| `cloud-restart` | PASS | PASS | 0 |
| `cloud-create-restart` | PASS | PASS | 0 |
| `cloud-remove-restart` | PASS | PASS | 0 |
| `cloud-increase-response-loss` | PASS | PASS | 0 |

All receipts bind the same candidate, version, manifest, run/attempt and fixture
configuration digest. The first five modes used the normal Linux amd64
distribution binary; response loss used its separately hashed `responsegate`
test binary. The [workflow-completion janitor](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40728720450)
checked this exact owning run/attempt and passed with zero fixtures to recover.
The acceptance switch was restored to false after verification. This proves the
completion-triggered scan, not hourly recovery of abandoned fixtures.

Earlier attempts exposed fixture tag visibility during creation/deletion and
cross-group reuse of the driver's cleanup idempotency token; these were corrected
in PRs #54, #55 and #59. The token collision was reproduced in an offline
regression. Its exact historical cloud error code was not retained. An earlier
SSH-check-stage failure also lacks sufficient evidence for a precise root cause;
the successful run does not retroactively turn failed attempts into passes.

This evidence covers the exact candidate above. It does not cover the later
Debian/Manager addition in `72e2628` or later source changes, Linux arm64 cloud
execution, full GitLab jobs, STS renewal, or broader lifecycle scenarios. Future
publication must run its own gate against the version and artifacts it publishes.

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

### Real two-worker specified scale-in

A separate `cloud-scale` run on 2026-10-28 passed **0→2→1→0** without GitLab.
It used release-build source
[`94cfed170949721bafae39b39eb02a746f5046f3`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/94cfed170949721bafae39b39eb02a746f5046f3),
squash-merged with identical content as
[`dcc736d50701e625aea5a191bd7c520d7526b955`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/dcc736d50701e625aea5a191bd7c520d7526b955).
The executed plugin SHA-256 was
`9c7663ee8e2f5f89f951be454a3fac33a8279aedcd5493edb8fceb997181f6de`.
The real gRPC plugin, fixed type/image/zone, public Ubuntu 26.04 image,
20 GiB ESSD PL0 disk per worker and narrow SSH ingress match the preceding smoke.
Both actual workers and disks were individually checked. The configured swap
size remained 512 MiB per worker; runtime swap state was not measured.

One attempt created exactly two Spot instances, with concurrency and cumulative
creation limits both fixed at two, no replacements and no on-demand fallback.
The synthetic labels below map to the same two private instance identities
throughout this run; cloud IDs and raw receipts remain outside Git.

| Transition | Observed evidence (UTC) |
|---|---|
| 0→2 | Plugin `Increase(2)` reached desired/total=2 with two actual Spot workers at 03:38:58. |
| SSH before scale-in | Worker A returned its fresh nonce at 03:39:30; worker B returned a distinct nonce at 03:39:39. |
| 2→1 | `Decrease` accepted exactly A. Two stable snapshots confirmed desired/total=1, pending/removing=0, A absent from ESS/ECS with its disk/ENI gone, and original B still `InService`/`Running`. B then returned a third fresh SSH nonce at 03:40:16. |
| 1→0 | Plugin released B; ESS/ECS zero and both workers' disk/ENI absence were verified at 03:40:47 before fallback cleanup. |
| Full teardown | Driver/plugin stopped, fixture deleted, and two complete independent inventories found no owned resources across all ten checked resource kinds. Temporary client/server keys and encoded server-key user data were removed. |

The driver recorded `PASS`, `specified_release=true`, `normal_release=true`
and completed authenticated SSH. Independent inventories matched exactly two
instance IDs and two actual disks to the driver receipts. No retry creation was
needed. This verifies target-only deletion and retained-worker SSH for this
bounded two-worker case. Larger pools, multi-batch deletion, restart recovery,
real Spot interruption and GitLab executor jobs remain unverified. The upstream
server-host-key limitation still applies; no OIDC or restricted-role acceptance
is claimed. See [the reproducible mode](testing.md#two-worker-specified-scale-in-without-gitlab).

### Real plugin restart recovery

A separate `cloud-restart` run on 2026-10-28 passed
**0→1→plugin termination→same-instance rediscovery/SSH→0** without GitLab.
It executed release-build source
[`7e44c4e518206bd74139fee9d0a9e2b1e76b76bc`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/7e44c4e518206bd74139fee9d0a9e2b1e76b76bc),
whose tested implementation was merged in
[`d62dddd9b4324c214f5dc2f28b0fe6c7fe7e4b09`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/d62dddd9b4324c214f5dc2f28b0fe6c7fe7e4b09).
The executed plugin SHA-256 was
`e55db9a3be347b93c819f8451ff87a5d70e301aa7f8a0ec1bc9466a6d216a957`.
The single worker used the same `us-east-1b`, `ecs.e-c4m1.large`, Ubuntu 26.04
public image and 20 GiB ESSD PL0 configuration. Actual ECS/disk configuration
was checked. Swap was configured at 512 MiB; runtime swap state was not measured.

One attempt created exactly one Spot instance, with concurrency and cumulative
creation limits both one, no replacements and no on-demand fallback. The driver
terminated the owned plugin process without provider `Shutdown`, reaped its
transport, proved the old PID had exited, and launched the same verified binary
under a distinct PID. The new plugin received only the same group configuration
and connector settings; it discovered the worker through fresh `Init`/`Update`.

| Transition | Observed evidence (UTC) |
|---|---|
| 0→1 | Plugin `Increase(1)` reached desired/total=1 on the actual Spot worker at 03:59:18. |
| SSH before restart | Original plugin returned the first nonce from that worker at 04:00:13. |
| Restart and recover | New plugin rediscovered the same instance ID and returned a second fresh SSH nonce at 04:00:27. Capacity remained one and the complete set of two existing scaling activity IDs was unchanged, confirmed at 04:00:28. |
| 1→0 | New plugin normally released the worker; ESS/ECS zero and disk/ENI absence were verified at 04:00:57 before fallback cleanup. |
| Full teardown | Driver/plugin stopped, fixture deleted, and two complete independent inventories found no owned resources across all ten checked resource kinds. Temporary client/server keys and encoded server-key user data were removed. |

An `increase` operation journal was still present before termination. The run
recorded `restart_recovered=true`, `normal_release=true` and `PASS`; the independent
resource audit also passed. Both SSH commands and all observations bound to the
same single instance ID, and no additional scaling activity appeared across the
restart. Synthetic labels retain that relationship in the public JSON without
publishing cloud IDs or local PIDs.

This verifies restart after a ready worker with accepted increase intent. The
accepted-creation and accepted-removal cases are recorded separately below. Lost
removal API replies, multi-page cloud snapshots and other recovery states remain unverified. GitLab, restricted
roles/OIDC and upstream SSH server-host-key verification remain outside this
run. See [the reproducible restart mode](testing.md#plugin-restart-recovery-without-gitlab).

### Real restart during accepted creation

A separate `cloud-create-restart` run on 2026-10-28 passed
**0→accepted creation→plugin termination→one ready worker/SSH→0** without GitLab.
The successful run executed source
[`211678527cf7f1e4e04e6ac03badf8224a9d2f2d`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/211678527cf7f1e4e04e6ac03badf8224a9d2f2d),
integrated as
[`4e17e8d6664d8f4a3b7bd8925ea3a0bed34b45d9`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/4e17e8d6664d8f4a3b7bd8925ea3a0bed34b45d9).
The plugin SHA-256 was
`b56c17fae55381671371fc0ecf82debb88c3263c0d176922667d50d0efd97936`.
It used the same minimum `us-east-1b` Spot type/image and 20 GiB ESSD PL0 disk.
Actual instance/disk configuration and SSH passed. Configured swap remained
512 MiB; runtime swap state was not measured.

| Event | Observed evidence (UTC) |
|---|---|
| Request accepted | Real plugin `Increase(1)` returned accepted at 04:35:01.511043. No second Increase was issued. |
| Before termination | At 04:35:02.292083, the persisted increase journal targeted 1; the unique provisioning activity was `Ess/InProgress`, and the member was `Init`. Neither activity nor membership reported an instance ID; the probe therefore recorded `NotAssigned`, without an ECS-ID-based power-state query. |
| After old process exited | At 04:35:03.112724, the same activity and full activity-ID set remained; the member was still `Init` with no reported ID. The old PID was proven gone before a distinct new PID was launched. |
| New process recovery | Fresh Init completed at 04:35:08; one actual running worker was observed at 04:35:13 and returned a fresh SSH nonce at 04:35:43. |
| Original activity result | At 04:35:46, the original provisioning activity was successful with exactly that one created instance ID; no additional activity IDs had appeared. |
| Normal 1→0 | New plugin released the worker; ESS/ECS zero and disk/ENI absence were verified at 04:36:35 before fallback cleanup. |
| Full teardown | Driver/plugin stopped; all owned resources across ten checked kinds were absent in two complete independent inventories. Temporary keys and encoded server-key user data were removed. |

The successful attempt created exactly one Spot instance with concurrency and
cumulative creation limits both one, no replacements and no on-demand fallback.
The driver and independent resource audit recorded `PASS`, with
`restart_recovered=true` and `normal_release=true`. The two timing captures were
persisted before recovery continued; they establish the pre-readiness cutpoint
without treating an unassigned ID as a normal membership or cleanup identity.

There were two isolated attempts and two Spot instances in total, one each.
The first probe rejected the completed API desired-capacity record accompanying
the actual provisioning activity, before it terminated the plugin. That failed
attempt was fully cleaned and confirmed absent twice before the successful
retry. The corrected probe distinguishes the successful zero-creation `Api`
record from the `Ess/InProgress` provisioning activity, while retaining all
activity IDs in comparisons. The [official activity contract](https://help.aliyun.com/en/auto-scaling/developer-reference/api-ess-2014-08-28-describescalingactivities)
defines those trigger sources. Tooling and the observed-contract fix were
merged in [#30](https://github.com/euyuil/fleeting-plugin-aliyun/pull/30) and
[#31](https://github.com/euyuil/fleeting-plugin-aliyun/pull/31).

This covers an acknowledged expansion during provisioning. Lost removal replies,
multi-page cloud snapshots and actual Spot interruptions
remain unverified. GitLab, restricted roles/OIDC and upstream SSH server-host-key
verification remain outside this run. See [the reproducible mode](testing.md#plugin-restart-during-accepted-creation).

### Real restart during accepted removal

A separate `cloud-remove-restart` run on 2026-10-28 passed
**0→one worker/SSH→accepted removal→plugin termination→0** without GitLab.
The run executed source
[`5fd41f6cdb0930f140533ae8232b081549e5ac0b`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/5fd41f6cdb0930f140533ae8232b081549e5ac0b),
integrated as
[`8f62bb6b6e054468876cc8955cbb542172d8eb14`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/8f62bb6b6e054468876cc8955cbb542172d8eb14).
The plugin SHA-256 was
`b0d0d32cf6622815eea919dda3c415b03299fe130a1be61cf9dd008746d82928`.
One isolated attempt created exactly one Spot instance. It retained the same
`us-east-1b`, 2 vCPU / 512 MiB, Ubuntu 26.04 and 20 GiB ESSD PL0 configuration,
with concurrent and cumulative creation limits both one. Actual instance and
disk configuration passed. Configured swap remained 512 MiB; runtime swap state
was not measured.

| Event | Observed evidence (UTC) |
|---|---|
| Ready worker and SSH | One running worker was observed at 06:13:18; authenticated SSH returned a fresh nonce at 06:14:46.242667. |
| Removal accepted | Target-specific Decrease returned accepted at 06:14:52.793213. Its persisted journal held the exact target and activity, Before=1, Target=0 and AcceptedMask=1. |
| Before termination | At 06:14:53.814667, the removal activity was `Api/InProgress`, the sole member was `Removing`, and an ECS query found the exact target still `Stopping`. Desired/total/removing capacity was 0/1/1. |
| After old process exited | At 06:14:55.180112, the same target still existed as `Removing` / `Stopping`, with the same activity and complete activity-ID set. The old PID was proven gone before a distinct PID launched. |
| New process recovery | Fresh Init completed at 06:14:57.427263; the accepted target-specific removal journal remained intact. Only empty Decrease calls advanced reconciliation. No second target-specific Decrease or Increase was issued by the driver. |
| Normal zero | ESS/ECS zero and disk/ENI absence were verified at 06:15:21.534047 before fallback cleanup. At 06:15:22.103512, the original activity was verified successful, the journal was clear, and the full activity-ID set remained unchanged. |
| Full teardown | Driver/plugin stopped; all owned resources across ten checked kinds were absent in two complete independent inventories. Temporary keys and encoded server-key user data were removed; the independent cleanup guard disarmed. |

The driver and independent resource audit recorded `PASS`, with
`restart_recovered=true`, `normal_release=true`, and one cumulative instance
identity. Both timing captures were persisted in private evidence before the
new process began recovery. No lifecycle hook, artificial deletion delay or
direct ECS deletion was used to obtain the cutpoint. Synthetic labels in the
public JSON preserve identity relationships without cloud IDs or local PIDs.
The reproducible tool and its regressions were merged in
[#36](https://github.com/euyuil/fleeting-plugin-aliyun/pull/36).

This covers an acknowledged removal while the target still exists. Lost removal
responses, multi-page cloud snapshots and actual Spot interruptions remain
unverified. GitLab jobs, restricted roles/OIDC and upstream SSH server-host-key
verification remain outside this run. See [the reproducible mode](testing.md#plugin-restart-during-accepted-removal).

### Real expansion-response loss and restart

A separate `cloud-increase-response-loss` run on 2026-10-28 passed
**0→ESS accepts expansion/response withheld→plugin termination→retry/one worker/SSH→0**.
One isolated attempt created exactly one Spot instance without GitLab or Runner.
It executed source
[`a27d9887f18fc5b8a5ede075c810b1a59223ec79`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/a27d9887f18fc5b8a5ede075c810b1a59223ec79),
integrated as
[`87c1c742e25943db3d78895d63b8ee5a05d6ed53`](https://github.com/euyuil/fleeting-plugin-aliyun/commit/87c1c742e25943db3d78895d63b8ee5a05d6ed53).
The plugin SHA-256 was
`ddf2757d3c213981648f481a5f9dce320300f35484323929ec562bb174c98ea3`.

This was fault-injected real-cloud acceptance using the explicitly marked
`responsegate` test build. Its real SDK kept the official URL, Host, SNI,
signing, parsing and retry logic. A per-process dial/trust override routed ESS
through a numeric-loopback TLS gate, whose upstream independently verified
official ESS HTTPS. The gate withheld the selected successful response before
any HTTP status, headers or body could reach the old plugin. It changed no
system trust store. Unmodified release bytes and natural packet loss were not
tested by this run. The tool and isolation regressions were merged in
[#40](https://github.com/euyuil/fleeting-plugin-aliyun/pull/40).

| Event | Observed evidence (UTC) |
|---|---|
| Real expansion accepted | The gate received the request at 06:52:52.070006 and validated ESS HTTP 200, a nonempty RequestId and no error Code at 06:52:52.892838. The request's token matched the persisted 0-to-1 journal. |
| Before termination | At 06:52:53.763819, the same provisioning activity was `Ess/InProgress` and membership was `Init`. The selected response remained withheld and the original Increase was still pending. |
| Termination and peer exit | The kill signal began at 06:52:54.050830; the original peer disconnected at 06:52:54.054729. The old PID was proven gone. The gate did not deliver the response or count a local drop as peer exit. |
| After old process exited | At 06:52:54.886589, the same activity and complete activity-ID set remained, with `Init` membership. Neither timing snapshot reported an instance ID; `NotAssigned` is a probe sentinel, not an ECS power-state query. |
| New process and retry | A distinct PID completed fresh Init and retried Increase(1) by 06:52:59.295804. It returned zero additional capacity; the gate recorded exactly one expansion attempt and one forwarding to ESS. |
| Original worker and SSH | One actual running worker was observed at 06:53:10; authenticated SSH returned a fresh nonce at 06:53:35.397993. By 06:53:39.554049, the original activity had successfully produced exactly that one ID and its activity-ID set was unchanged through recovered SSH, before requested scale-in. |
| Normal zero | At 06:54:16.962891, ESS/ECS zero and disk/ENI absence passed before fallback cleanup. |
| Full teardown | Driver/plugin and gate stopped; all resources owned by this run across ten checked kinds were absent in two complete independent inventories. Temporary SSH keys, encoded server-key user data and the gate CA file were removed; the independent guard disarmed. |

The instance retained `us-east-1b`, 2 vCPU / 512 MiB, Ubuntu 26.04 and a 20 GiB
ESSD PL0 system disk, verified against actual cloud inventory. Concurrent and
cumulative creation budgets were both one, with no on-demand fallback.
Configured swap remained 512 MiB; runtime swap state was not measured.
Driver and independent resource audit both recorded `PASS`, including
`restart_recovered=true` and `normal_release=true`. Private persisted cutpoints
bind the held response, journal token, activity, process and eventual worker;
public JSON uses synthetic identities and omits credential-bearing traffic.

Lost removal responses, other failure timings, multi-page snapshots and genuine
Spot interruptions remain unverified. GitLab jobs, restricted roles/OIDC and
upstream SSH server-host-key verification remain outside this run. See
[the reproducible fault-injection mode](testing.md#plugin-restart-before-delivery-of-an-expansion-response).

The following gates remain specific and visible:

| Gate | Completed work | Next evidence required |
|---|---|---|
| Dedicated-account pre-release gate | Six serial OIDC scenarios, matching candidate receipts, zero leftovers and workflow-completion janitor passed on `ea3879a` | Later candidates, STS renewal and scheduled abandoned-fixture recovery need separate evidence. |
| Plugin-only 0→1→SSH→0 | Real plugin, actual Spot/disk checks, authenticated SSH, normal specified release and full fixture cleanup passed | Broader lifecycle scenarios remain separate. |
| Plugin-only 0→2→1→0 | Target-only release, original retained-worker SSH and final normal release passed; full fixture cleanup confirmed twice | Larger release batches and recovery remain separate. |
| Ready-worker plugin restart | Old process exit, distinct new PID, original instance rediscovery, fresh SSH, unchanged activity IDs and normal release passed; fixture cleanup confirmed twice | Lost removal replies and multi-page cloud snapshots remain unverified. |
| Accepted-creation plugin restart | Nonready snapshots before/after old exit, one eventual worker from the original activity, SSH, normal release and full cleanup passed | Lost removal responses and multi-page snapshots remain separate. |
| Accepted-removal plugin restart | SSH before removal, same still-existing target/activity around old exit, new-process recovery, journal clearance and normal zero passed; full cleanup confirmed twice | Lost removal responses and multi-page snapshots remain separate. |
| Lost expansion response | Real ESS success withheld before SDK delivery; pending old call, peer exit after kill, fresh-process retry with one cloud expansion, original worker/SSH and normal zero passed; full cleanup confirmed twice | Instrumented test build only; lost removal responses, other fault timings and multi-page snapshots remain separate. |
| Basic 0→1→job→0 | Released 0.1.0-rc.2 VM and Docker jobs passed on separate Linux amd64 Spot workers; artifact identity, normal zero and full cleanup verified | Other images, full arm64 jobs, larger pools and broader job features remain separate. |
| Extended scale/replacement | Real-plugin two-worker scale-in passed; fixed budgets and tagged recovery suites implemented | Larger scales and replacement need a separate prepared fixture and explicit mode. DeleteInstance injection does not demonstrate a genuine Spot notice. |
| Dynamic SSH | Public-key delivery, expiration, helper and reacquisition implementation/tests | Prepared Cloud Assistant image, restricted RunCommand identity, and actual key delivery/SSH timing. |
| Windows | Connection logic, example and offline checks | Licensed image, network route and an actual Windows executor job. |
| Formal suspend/resume | Standard unsupported production methods; isolated controller and offline tests | Same-ID ESS/ECS terminal states, unchanged capacity, fresh SSH after resume, interruption recovery and verified cleanup before capability activation. |
| External publication | Corrected 0.1.0-rc.2 published; anonymous Runner install/list, exact native bytes, version/source and protocol checks passed on Linux amd64 and arm64 | Installation acceptance does not establish full GitLab job or production acceptance. The rc.1 failure remains recorded below. |

M0 and the offline M1 implementation are complete. M2 tooling is implemented.
Its plugin-only smoke, scale-in, ready-worker, accepted-creation, accepted-removal
and instrumented lost-expansion-response restarts passed. Both released VM/Docker
job cycles passed after the tooling and configuration fixes below. M3 retains
formal suspend and broader live acceptance gates;
full official capability parity is not complete. M4 local distribution and
maintenance tooling are delivered; corrected public Runner installation passed
on both supported Linux architectures. These limits
follow the specification's no-configuration path.

## First prerelease publication

The [release workflow](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40632137153)
published `v0.1.0-rc.1` from `a5a7bd65ddf2eb344e7597ae509f5c8ecaffc197`.
GitHub confirms `prerelease=true`, both native archives, checksums and the source
inventory. The exact GHCR manifest is public; an independent anonymous request
returned HTTP 200 with digest
`sha256:6a638b1da483e14b24678bd0c01276d8f9816787540b30d995a99cf295d682d2`.

The [real Runner installation workflow](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40632320865)
failed on both Linux amd64 and arm64. The pinned installer skips directory
headers and cannot create parent directories for the nested license files in
this package. It did not reach installed-plugin protocol loading. The published
version and assets remain unchanged, and its Release notes identify this issue.
The corrected producer layout and full-payload regressions were published as
`0.1.0-rc.2` and passed fresh public installation checks below. No cloud resources
or GitLab jobs were created by these publication and installation checks.

## Corrected prerelease installation

The [release workflow](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40639393805)
published `v0.1.0-rc.2` from
`795f07f5c2771bf7f84154c6892d90ba77f26b8b` on 2026-10-28. GitHub confirms
`prerelease=true`, the exact source tag, both native archives, checksums and the
dependency inventory. An independent request without registry credentials
returned HTTP 200 for `ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.2`, with
index digest
`sha256:9df40a99e6b5197a1eea0191b98bc79c84386b0dcb2b1533f6f50752ba09795a`.

The [public installation workflow](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40639528094)
passed on fresh native Linux amd64 and arm64 hosts. Each used the
checksum-verified Runner 19.5.1, an empty plugin directory and an explicit empty
Docker auth configuration. Registry and cloud credentials were absent from the
installation step. Each job downloaded the native archive and checksum file
anonymously, verified its checksum, and used the real Runner CLI to install and
list the exact OCI reference.

The verification tool confirmed identical installed/native binary bytes, exact
version and source revision, and real Fleeting protocol loading. Empty plugin
configuration was rejected before credential construction or cloud API calls.
Both jobs completed successfully and retained JSON receipts; their selected
fields are preserved in [`acceptance/status.json`](../acceptance/status.json).

| Platform | Installed executable SHA-256 |
|---|---|
| Linux amd64 | `1317b5063669c750d87d23916db4cd2730c5085d9863d858572d187225b864bd` |
| Linux arm64 | `4c70c9977ef3861effbaaf8b2d4d7f3c863edb2f3876421bd588ed38c2d1efed` |

The flat OCI payload retains complete licenses/notices in `licenses.tar.gz`.
This is public installation and pre-cloud protocol acceptance. It does not
establish a successful GitLab job, Docker/Windows execution or production
acceptance, and it created no cloud workers.

## Released-plugin job preparation attempts

On 2026-10-28, a dedicated private GitLab fixture and one locked project runner
were configured. The fixture contains only the reviewed smoke files; other
runners are disabled for that project. Credentials and private project/job
identities remain outside this repository.

The first Instance-executor attempt used the published `0.1.0-rc.2` OCI package,
Runner 19.5.1, and a checksum-verified native Release binary. Installed bytes
matched SHA-256
`1317b5063669c750d87d23916db4cd2730c5085d9863d858572d187225b864bd`, with release
source `795f07f5c2771bf7f84154c6892d90ba77f26b8b`. The dedicated ESS group started
at zero and created exactly one verified Spot worker. The job reached
`prepare_executor` but timed out after five minutes, before checkout or the
fixture script. No successful job artifact or normal-release PASS was obtained.

Fallback cleanup rejected the real API's uppercase `TCP` protocol spelling.
The driver result remains `CLEANUP_FAILED`; the independent outer teardown does
not turn this failed attempt into a pass. Protocol comparison and exact expected
CIDR validation were fixed in [#39](https://github.com/euyuil/fleeting-plugin-aliyun/pull/39).
After ECS queries reported no instance, its primary ENI remained visible; an
additional exact-owned-ID instance deletion confirmation was issued before the
ENI disappeared. Full fixture absence was then confirmed in two complete
inventories.

A separate one-instance image-preparation attempt used the same fixed
`ecs.e-c4m1.large` type (2 vCPU, 0.5 GiB), Ubuntu 26.04 public image and 20 GiB ESSD
PL0 disk. Its console recorded the kernel killing `cloud-init` for lack of memory
at about 13 seconds of uptime. A staged Runner transfer timed out, image bootstrap
did not start, and no custom image was created. Its full cloud fixture was also
deleted and confirmed absent twice. Requested swap in cloud-init is not evidence
that swap was configured before the process was killed.

These initial attempts created **two Spot instances in total**, one in each isolated
fixture. Their disks, ENIs, keys and networks are gone; the failed job's ESS group
and launch template are also gone. Temporary private SSH keys and encoded
server-key user data were removed after teardown. The private GitLab project and
dedicated runner are retained for reuse. These failures did not establish a
usable full-job worker. The explicitly approved 2-GiB prepared-image retry below
subsequently passed both execution modes.

## Released plugin VM and Docker jobs

On 2026-10-28, both selected Linux amd64 execution modes completed a real
**0→1→job→0** cycle with the unchanged published `0.1.0-rc.2` plugin and Runner
19.5.1. Each used a fresh, exclusively owned ESS group starting at desired/total
capacity zero, MaxSize=1 and a cumulative creation budget of one worker. Both
workers were verified Spot instances; no on-demand fallback was enabled.

The explicitly approved worker type was `ecs.e-c1m1.large` (2 vCPU, 2 GiB) in
`us-east-1b`, with a 20 GiB ESSD PL0 system disk and configured 1 GiB swap. A separate Spot
preparation VM installed the checksum-verified Runner, tools from the signed
Ubuntu `20261027T000000Z` snapshot and Docker Engine. It cached the job image and
version-tagged helper, ran an actual container probe, then cleared preparation
SSH keys, cloud-init/machine/network state and the readiness marker before a
stopped-instance image capture. Fresh workers installed their own keys and
checked tools before becoming ready. Preparation compute was deleted before
starting either job.

The Manager was a temporary local Linux amd64 container under Docker Desktop
amd64 emulation; the actual job workers were native Linux amd64 ECS instances.
Runner installed the OCI plugin with an empty registry-auth configuration and
compared its bytes with the Release binary. Both jobs used plugin SHA-256
`1317b5063669c750d87d23916db4cd2730c5085d9863d858572d187225b864bd` and release
source `795f07f5c2771bf7f84154c6892d90ba77f26b8b`. The acceptance driver source was
`1f55016bf8416a87f80c53d8119ff047227362a2`.

| Evidence | Instance executor | Docker Autoscaler |
|---|---|---|
| GitLab job | Success, 121.725 seconds | Success, 146.765 seconds |
| Checkout and fixture command | Passed | Passed |
| Artifact container marker | `false` | `true` |
| Nonce, fixture SHA, runner ID and metadata instance ID | Matched the selected job and actual Spot worker | Matched the selected job and actual Spot worker |
| Complete disk/ENI inventory and normal release | Passed before fallback cleanup | Passed before fallback cleanup |
| Full fixture cleanup | Passed, two absence inventories | Passed, two absence inventories |

Durations are GitLab's job durations, including preparation. Docker used
`docker.io/library/buildpack-deps@sha256:57c2de9a151180bf9ba8eea8bbebf8c546ccaa629f4fec8ea9b18330226af31b`.
The cached helper used Runner's `x86_64-v19.5.1` tag; its observed repository digest
was `sha256:ca7cc8f66a6c6931281aa2ae974b62f80111a4f578cabc442b1d39388a0dee91`.
The job image was digest-pinned; the helper used the pinned Runner version tag.

One earlier 2-GiB VM job connected over SSH but failed during shell preparation
in 96.710 seconds, before checkout. Runner's default Bash-style escaping was
incompatible with the configured `sh`. Explicit POSIX escaping fixed the
examples, Quick Start and acceptance Manager in
[#46](https://github.com/euyuil/fleeting-plugin-aliyun/pull/46). Preparation also
exposed a working-directory issue fixed in
[#43](https://github.com/euyuil/fleeting-plugin-aliyun/pull/43). The observer could
erase a previously complete inventory when resources detached before ECS
disappeared; [#45](https://github.com/euyuil/fleeting-plugin-aliyun/pull/45)
preserves historical proof while retaining exact final resource-ID checks.
That failed run remains `CLEANUP_FAILED`; its separate full teardown passed.
These tooling and configuration fixes leave the published plugin unchanged.

Across all released-job preparation and job attempts, **six Spot instances**
were created: two initial 0.5-GiB attempts, one 2-GiB image-preparation VM, one
failed 2-GiB job worker, and the two successful job workers. All were deleted.
The temporary image, its one snapshot, all task ESS groups/templates, disks,
ENIs, key pairs and networks were deleted; every fixture was confirmed absent
twice. Temporary private SSH keys, encoded host-key request data and Manager
containers were removed. The private GitLab project and locked runner remain
for reuse, with no pending/running jobs. Private project/job identities and raw
receipts remain outside this repository.

This establishes the selected single-worker Linux amd64 job cycles. Windows,
full arm64 jobs, other images, larger pools, interruption and production
Manager deployment are not covered. The separate GitLab `Live acceptance`
workflow and its fixed-pool janitor remain unconfigured and unrun; the plugin-only
pre-release cloud gate above does not configure those workflows.

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
