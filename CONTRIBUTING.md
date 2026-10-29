# Contributing

The [design specification](docs/specification.md) records the architecture and
requirements. Use the [compatibility matrix](docs/upstream-parity.md) for upstream
capabilities and the [testing guide](docs/testing.md) for reproducible acceptance
scenarios and their limits.

Use the exact Go toolchain from `go.mod`, GNU Make, Python 3.11+, and Git.
Other upstream versions are pinned in `upstream.lock.json`; Go dependencies
are locked by `go.mod` and `go.sum`.
No cloud credentials are needed for local development. Fixture safety tests
use an OpenSSH client for public-key validation and OpenSSL for a synthetic
loopback TLS server. CI also exercises the pinned CLI against that server using
synthetic STS credentials; real fixture creation requires
the separately pinned Alibaba CLI documented in [infrastructure](infra/README.md).

```sh
make verify
make dist
```

`make verify` runs the shared code checks and security scans. PR CI and release
candidate builds call `make verify-code` plus the same reusable security job.
Code checks include an early toolchain check, metadata/documentation consistency,
`go mod tidy -diff`, checksum verification, normal/race/responsegate tests,
read-only formatting, vet/actionlint, Runner example parsing, and a deliberately
disabled live-tag entry. No live SDK or credentials are enabled by this target.
The live-tag diagnostics execute; the cloud scenario explicitly reports NOT_RUN.
For focused work, the `test`, `test-component`, `test-race`, `test-responsegate`,
`test-live-offline`, `test-configs`, `lint`, and `security` targets remain available.

`make dist` builds archives and calls `make verify-dist`. The platform workflow
uses `make verify-dist DIST_DIR=PATH` to check existing candidate checksums and
load the exact native OCI payload without rebuilding the plugin. Use
`make test-debian-lifecycle DISPOSABLE_HOST=true RUNNER_BINARY=PATH DIST_DIR=PATH`
only on a disposable Linux systemd host. This target installs/removes packages.

`make lint` reports unformatted Go files without editing them. Apply `gofmt -w`
explicitly when needed. Module checks use the tidy graph: `make deps` downloads
selected dependencies without adding unrelated full-graph zip checksums.
`check-metadata` verifies locked module/Action/tool references, published receipt
identities, capability status structure, and local Markdown file links. It does
not treat historical acceptance evidence as evidence for the current commit.
When upgrading dependencies or Actions, update their authoritative pins together;
Go has one version in `go.mod`, and every setup-go action reads that file.

Tests replace the API boundary, sign loopback HTTP requests, or load the real
plugin process and execute authenticated SSH commands on a loopback server.
The component-only endpoint override is excluded from release binaries. They do not invoke the standard credential chain or contact cloud APIs.
On a native Linux host, `make dist` also publishes the complete generated OCI
payload to a loopback registry and installs it with the pinned official library.
Other hosts cross-build the Linux archives but skip their execution check.
On Linux, `make dist` also builds amd64 and arm64 `.deb` packages using
`dpkg-deb` from `dpkg-dev`, with the same binaries and license inventory.
macOS distribution builds skip Debian packaging. For a **disposable Linux host**
with systemd, Python 3.11+, OpenSSH, `dpkg-dev`, and the package dependencies installed:

```sh
python3 tools/fetch-runner.py
sudo python3 tools/test-debian-lifecycle.py --disposable-host \
  --runner .cache/gitlab-runner-linux-amd64 --packages dist
```

Use the arm64 Runner filename on an arm64 host. This test installs and removes
packages and creates a temporary service account. It refuses existing installations.
It uses real dpkg, systemd and pinned Runner parsing; a local fake busy job tests
SIGQUIT draining. It never calls GitLab or Alibaba Cloud, and needs no credentials.
Do not run it on a shared manager. Normal Go tests exercise configuration rejection,
retry, private file handling and preservation without root or systemd.
Do not disable Go checksum verification. `make lint` installs actionlint at the
version in `upstream.lock.json` and validates the configuration examples. CI also parses them using the
pinned, checksum-verified Runner binary.

Before committing, review `git diff`, stage only intended files, review the
complete `git diff --cached`, and run `git diff --check` and
`git diff --cached --check`. Use Conventional Commits and approved contributor
identity. GitHub CI runs the same checksum, race, vet, formatting, and whitespace checks.
CI fetches the full Git history and checks committed PR changes with
`git diff --check "$BASE_SHA...$HEAD_SHA" --`, using the PR event's base and head
SHAs. The three-dot range checks changes since their merge base, including when
the target branch has advanced. For pushes to `main`, CI checks the event's
before and after SHAs with `git diff --check "$BASE_SHA" "$HEAD_SHA" --`.
The `main` ruleset requires the aggregate `offline` check from GitHub Actions
before merging. It rejects failed, canceled, or skipped build/platform/security jobs.
CI uploads the candidate, downloads it by artifact ID, and compares
every archive, Debian package, checksum file, and dependency inventory byte for
byte. This exercises the pinned Node.js 24 artifact Actions without cloud access.
The same sealed candidate also runs through `verify-candidate.yml` in PR CI and
before cloud acceptance. General CI jobs use explicitly pinned Ubuntu 26.04
hosts. The platform matrix retains Ubuntu 24.04 and adds Ubuntu 26.04, each on
native amd64 and arm64. All four jobs verify the candidate source and manifest,
install the full OCI payload, parse examples with the pinned Runner, and exercise
the Debian package lifecycle. The Ubuntu 26.04 amd64 job additionally boots an
isolated Debian 12 userspace with systemd and no network, then runs the lifecycle
test on the same package. `tools/test-debian-system.sh` requires a disposable
GitHub-hosted amd64 runner; debootstrap downloads authenticated Debian packages
before the isolated test starts. No cloud or GitLab credentials are used.

Security checks run on every PR/main CI and before release candidate builds.
They use checksum-pinned Gitleaks and ShellCheck, hash-locked Ruff wheels, and a
pinned govulncheck module. A weekly/manual credential-free scan also checks main
against newly disclosed Go vulnerabilities. PR secret scans cover commits since
the PR base; main/manual/weekly scans cover all fetched history. Findings block
the required aggregate check. Dependabot proposes Go, Action, and Python tooling
updates; updates still require normal review and CI.

```sh
make security
```

This installs the exact tools into `.cache/` and runs script checks, a redacted
history secret scan, and Go vulnerability analysis. `SCAN_RANGE=BASE..HEAD` can
limit the history scan locally. Before committing, also scan the staged diff:

```sh
.cache/tools/gitleaks git --pre-commit --staged --redact --no-banner .
```

`.gitleaksignore` contains only a reviewed historical public Action SHA false
positive, identified by its exact commit/file/rule/line fingerprint. Do not add
broad exclusions for real findings. ShellCheck's local SC2016 exception preserves
intentional expansion inside the target user's `su` shell. No commit hooks are
configured; the commands above are explicit developer checks.

## Inspect the OIDC subject

Before configuring the RAM role's trust policy, read the repository's current
OIDC subject template and prefix. Replace `OWNER/REPOSITORY` with the target
repository; this request does not change its OIDC settings:

```sh
gh api repos/OWNER/REPOSITORY/actions/oidc/customization/sub
```

Use the result as described in the [RAM setup guide](docs/pre-release-cloud.md#ram-setup-walkthrough).
Do not infer the subject from the repository name alone: immutable subjects also
include owner and repository IDs.

## Manual cloud validation

After configuring the new account's OIDC role and initial read-only policy,
check identity and inventory access while paid acceptance remains disabled:

```sh
gh workflow run cloud-config-check.yml --ref main
```

This manual check neither creates resources nor proves write permissions or
cloud scenario success. Follow the [RAM setup guide](docs/pre-release-cloud.md#ram-setup-walkthrough).

After separately authorizing the paid operation and configuring the dedicated
new account, run one plugin-only scenario without publishing:

```sh
gh workflow run cloud-acceptance.yml --ref main \
  -f version=0.1.0-dev -f candidate_sha=FULL_MAIN_COMMIT_SHA -f mode=cloud-smoke
gh workflow run cloud-janitor.yml --ref main
```

Select another documented mode or `all` for the full suite. Release invokes all
six modes as a mandatory pre-publication dependency. Configuration, ownership,
budgets and recovery are documented in [pre-release cloud acceptance](docs/pre-release-cloud.md).
For immediate recovery of a completed run, add `-f run_id=RUN_ID -f run_attempt=ATTEMPT`
to the janitor command; otherwise it scans only expired fixtures.

To exercise independent cleanup after a deliberate process loss, separately
authorize the paid drill and enable both creation and cleanup switches, then run:

```sh
gh workflow run cloud-cleanup-drill.yml --ref main -f confirm_abandon=true
```

The source workflow must fail after producing an `INTERRUPTED` receipt. Follow its
completion-triggered **Pre-release cloud janitor** run to a terminal result and
check the two bound receipts as described in the
[controlled interruption drill](docs/pre-release-cloud.md#controlled-interruption-drill).
No cloud drill runs in ordinary CI. Offline tests include the actual SIGKILL
checkpoint/cleanup-bypass check on Linux; macOS skips that procfs-specific test.

See [distribution](docs/distribution.md) for native and OCI packaging, installation,
and explicit release gates. See [third-party obligations](docs/third-party.md) before adding dependencies.

Release automatically calls the public installation workflow after publication,
binding the published tag to the source commit accepted by the cloud gate. Both
native Linux architecture jobs must pass before the Release workflow succeeds.
The verifier receives read-only repository access and no inherited cloud secrets.
After a failed verification, inspect the existing immutable release; do not
repeat publication. The same workflow can verify an already published version
on fresh GitHub-hosted Linux amd64 and arm64 hosts:

```sh
gh workflow run verify-release.yml --ref main -f version=0.1.0-rc.3
```

Wait for both jobs to finish successfully and inspect their OCI and Debian JSON
receipts. The Debian check downloads the public package and runs the disposable-host
lifecycle test above against its checksum-verified native binary. Older tags
without Debian packaging retain OCI-only checks.
To run OCI verification on your own Linux hosts, prepare the clean
credential and plugin directories described in [distribution](docs/distribution.md#public-installation-check),
an OCI Runner config, the exact source commit, and the checksum-verified native
Release binary. With `DOCKER_CONFIG` and `FLEETING_PLUGIN_PATH` set for that check:

```sh
gitlab-runner fleeting --config PATH_TO_CHECK_CONFIG install
gitlab-runner fleeting --config PATH_TO_CHECK_CONFIG list
go run ./tools/distribution \
  -verify-ref ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.3 \
  -expected-version 0.1.0-rc.3 -expected-revision SOURCE_COMMIT \
  -expected-binary PATH_TO_VERIFIED_RELEASE_BINARY
```

Replace the placeholders and use the exact published version. Retain the JSON
receipt after a successful check. Verification reads public packages and local
files; it does not publish or create cloud workers.

The credential-free CI also compiles the optional live command:

```sh
go build -tags live -o bin/aliyun-acceptance ./cmd/aliyun-acceptance
```

See [testing and acceptance](docs/testing.md) for explicit live configuration,
`make live`, and `make cleanup`. These commands require separate cloud-operation
authorization and do not run as part of normal PR validation.

The response-loss acceptance mode uses a separate `responsegate` test build.
Its credential-free checks are also required in CI:

```sh
go test -race -tags=responsegate ./internal/acceptance ./internal/aliyun ./internal/provider
go build -tags=responsegate -o bin/fleeting-plugin-aliyun-responsegate ./cmd/fleeting-plugin-aliyun
```

This build retains the real SDK and permits a per-process loopback TLS route.
Ordinary release builds exclude that factory and ignore its environment variables.

`make test-live-extended` compiles the tagged extended suites and reports a
NOT_RUN skip unless explicit live switches and configuration are supplied.
See the testing guide for modes and fixed budgets.
