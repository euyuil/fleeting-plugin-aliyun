# Contributing

Use Go 1.27.2, GNU Make, Python 3.11+, and Git. Exact upstream versions are pinned in
`upstream.lock.json`; Go dependencies are locked by `go.mod` and `go.sum`.
No cloud credentials are needed for local development. Fixture safety tests
use an OpenSSH client for public-key validation; real fixture creation requires
the separately pinned Alibaba CLI documented in [infrastructure](infra/README.md).

```sh
make deps
make build
make test
make test-component
make lint
go test -race ./...
make dist
make test-oci
```

Tests replace the API boundary, sign loopback HTTP requests, or load the real
plugin process and execute authenticated SSH commands on a loopback server.
The component-only endpoint override is excluded from release binaries. They do not invoke the standard credential chain or contact cloud APIs.
On a native Linux host, `make dist` also publishes the complete generated OCI
payload to a loopback registry and installs it with the pinned official library.
Other hosts cross-build the Linux archives but skip their execution check.
Do not disable Go checksum verification. `make lint` installs pinned actionlint
1.7.12 and validates the configuration examples. CI also parses them using the
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
The `main` ruleset requires the `offline` check from GitHub Actions before merging.
This repository has no configured commit hooks or security scanner.
Do not claim that an unconfigured check ran.

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

See [distribution](docs/distribution.md) for native and OCI packaging, installation,
and explicit release gates. See [third-party obligations](docs/third-party.md) before adding dependencies.

After an authorized release is published and its package is public, check
anonymous installation on both supported Linux architectures. The manual
workflow uses fresh GitHub-hosted Linux amd64 and arm64 hosts:

```sh
gh workflow run verify-release.yml --ref main -f version=0.1.0-rc.2
```

Wait for both jobs to finish successfully and inspect their JSON receipt
artifacts. To run the same verification on your own Linux hosts, prepare the clean
credential and plugin directories described in [distribution](docs/distribution.md#public-installation-check),
an OCI Runner config, the exact source commit, and the checksum-verified native
Release binary. With `DOCKER_CONFIG` and `FLEETING_PLUGIN_PATH` set for that check:

```sh
gitlab-runner fleeting --config PATH_TO_CHECK_CONFIG install
gitlab-runner fleeting --config PATH_TO_CHECK_CONFIG list
go run ./tools/distribution \
  -verify-ref ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.2 \
  -expected-version 0.1.0-rc.2 -expected-revision SOURCE_COMMIT \
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
