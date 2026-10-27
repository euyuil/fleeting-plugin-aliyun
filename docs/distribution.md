# Distribution

## Native

Use `make build` for the host binary and `make dist VERSION=0.1.0` for Linux
amd64/arm64 archives. Archives contain `fleeting-plugin-aliyun`, the project
license, retained notices, actual linked-module licenses and a versioned module
inventory. Checksums are in `dist/SHA256SUMS`. Build information includes version,
revision, toolchain, platform and source commit timestamp. Binary paths are
trimmed and archive ownership, timestamps, and file modes are normalized.

Verify the downloaded SHA256 against the release checksum and copy the executable
to a directory on the Manager's PATH under the exact name
`fleeting-plugin-aliyun`. Set `plugin = "fleeting-plugin-aliyun"` in `runners.autoscaler`, as in the
[Instance example](../examples/instance/config.toml). `version` and `-version`
print build information. Do not replace a public immutable version's files.

Linux amd64/arm64 are distribution targets. Local darwin/arm64 protocol testing
is development evidence, not a macOS Manager release/support claim. Windows
workers do not require a Windows Manager build. Cross-compilation is distinct
from execution and live executor acceptance.

## OCI

The pinned Runner uses fleeting-artifact `1b7c6957ad1d`. Packages are built using
its actual releaser library with `dist/packages/<os>/<arch>/plugin` inputs. It
produces the layer annotations and bootstrap layout its matching installer
expects. A generic arbitrary Docker image is not substituted.

```sh
make dist VERSION=0.1.0
go run ./tools/distribution -ref ghcr.io/OWNER/fleeting-plugin-aliyun:0.1.0 -dir dist/packages
```

This command is a publication operation and requires registry authorization.
It requires an exact semantic version (without `v` or build metadata), refuses existing versions and refuses to proceed if version-absence checks
fail for authentication/network reasons. The upstream library updates semantic
aliases; the semantic version itself is immutable. The release workflow
serializes all repository releases, uses the repository GITHUB_TOKEN with
packages:write, and creates a matching GitHub Release after tests/builds. Local
CLI publishers must also operate as a single writer for that registry repository;
the upstream alias update is not a distributed compare-and-swap operation. No live cloud test is
required by a routine build or installation-format test.

Before claiming a public package, check GHCR package settings: a new package can
be private even when source is public. Set public visibility deliberately, then
verify without registry credentials:

```toml
[[runners]]
  [runners.autoscaler]
    plugin = "ghcr.io/OWNER/fleeting-plugin-aliyun:0.1.0"
```

Use a temporary empty DOCKER_CONFIG, FLEETING_PLUGIN_PATH and a temporary Runner
config, then run `gitlab-runner fleeting install --config <config>`, list the
installed plugin and load it through Runner. This external publication and
anonymous installation have not been performed. The offline loopback test uses
the identical releaser/installer module version and compares installed bytes.
It also rejects a second publication of the same version. Local registry routing
uses a test transport because the pinned upstream path escaper does not support
registry ports in plugin install paths.

## Release gate

Use the trusted main branch, review final changes and CI, select a new semantic
version, and invoke Release explicitly. Preflight refuses existing Releases and
source tags. After validation, the workflow creates the exact source tag before
OCI publication and requires that tag when creating the Release. Prerelease
versions are marked as prereleases and cannot become GitHub's latest release.
If publication fails after creating a tag or package, inspect and recover that
partial release deliberately; do not overwrite or blindly rerun the same version.
Publication is distinct from code
implementation. Check all release steps and the actual assets/package after the
workflow completes; an accepted dispatch is not proof of publication. Confirm
anonymous retrieval and Runner install before advertising public installation.
Update `docs/release-notes.md` with the exact live scope; the initial text states
that live tests have not run and no earlier live-verified release exists.

For baseline upgrades, read the new Runner go.mod and provider signatures,
update `upstream.lock.json`, compile against that exact Fleeting version, rerun
protocol/SSH/installer/race/config tests, review license changes and update the
parity matrix. Run targeted live suites for affected cloud behavior when
configuration is available. Do not extend old live evidence to new capabilities.
