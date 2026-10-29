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
`fleeting-plugin-aliyun`. For this native installation path, replace the OCI
reference in the [Instance example](../examples/instance/config.toml) with
`plugin = "fleeting-plugin-aliyun"` in `runners.autoscaler`. `version` and `-version`
print build information. Do not replace a public immutable version's files.

Linux amd64/arm64 are distribution targets. Local darwin/arm64 protocol testing
is development evidence, not a macOS Manager release/support claim. Windows
workers do not require a Windows Manager build. Cross-compilation is distinct
from execution and live executor acceptance.

## Debian

For native Debian package installation and service management, see the
[Debian guide](debian-installation.md). Linux `make dist` also produces `.deb`
packages for amd64 and arm64 with `dpkg-deb`. These contain the exact native/OCI
plugin binary, local `manager` commands, service and example files, and retained
licenses. Debian versions map prerelease `-` to `~`; package checksums are included
in `SHA256SUMS`. Packages do not bundle GitLab Runner or start a service on install.
Download packages from the
[0.1.0-rc.3 Release](https://github.com/euyuil/fleeting-plugin-aliyun/releases/tag/v0.1.0-rc.3).
CI also produces development artifacts for contributors. There is no hosted APT
repository. The same explicit release gates apply to every published package.

## OCI

### Current prerelease

Use `ghcr.io/euyuil/fleeting-plugin-aliyun:0.1.0-rc.3` for evaluation. Follow the
[manual OCI service guide](installation.md#linux-service-setup) for installation.
For `.deb` installation and local `manager` commands, use the
[Quick Start](../README.md#quick-start-run-a-job-on-a-linux-vm).
The [rc.3 public release check](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40815916873)
passed anonymous OCI and Debian downloads, exact binary comparison with the native
Release archive, and the Debian lifecycle on native Ubuntu amd64 and arm64 hosts.
The [acceptance report](acceptance-report.md#debian-prerelease-publication-and-installation)
records the six-scenario cloud gate, installation receipts, and independent cleanup.

### Earlier 0.1.0-rc.2 verification

Anonymous
download, real Runner 19.5.1 installation and listing, exact Release binary
comparison, version/source checks, and plugin protocol loading passed on native
Linux amd64 and arm64 in the
[public installation workflow](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40639528094).
The [acceptance report](acceptance-report.md#corrected-prerelease-installation)
records the exact source, manifest and installed binary checksums. These checks
did not run a GitLab job or create cloud workers.

### Known issue in 0.1.0-rc.1

The first prerelease is public, and anonymous GHCR manifest download passed.
Actual Runner 19.5.1 installation failed on both Linux amd64 and arm64: the OCI
payload contains nested license files, but the pinned installer skips directory
entries and cannot create their parents. See the
[installation run](https://github.com/euyuil/fleeting-plugin-aliyun/actions/runs/40632320865).
The published version and assets are retained unchanged. Do not use its OCI
reference as a working installation path. Native archives keep their existing
layout. Version `0.1.0-rc.2` fixes this issue; full GitLab job acceptance remains
separate.

### Packaging and publication

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
config, then run `gitlab-runner fleeting --config <config> install`, list the
installed plugin and load its protocol through the pinned Fleeting library.
Earlier `0.1.0-rc.2` results are recorded above. The offline loopback test uses
the identical releaser/installer module version and compares installed bytes.
It also rejects a second publication of the same version. Local registry routing
uses a test transport because the pinned upstream path escaper does not support
registry ports in plugin install paths.

Corrected builds keep `plugin`, `LICENSE`, `NOTICE`, and `licenses.tar.gz` at the
OCI platform root. The archive preserves the complete license/source inventory
tree; read `NOTICE` for extraction instructions. Native archives retain their
original directory tree. The installer regression exercises this full generated
payload, rather than only the plugin executable.

The pinned upstream publisher also updates floating OCI aliases, including for
prereleases. Pin an exact version when installing a release candidate; GitHub's
prerelease flag does not control OCI aliases.

## Public installation check

The Release workflow calls this check after publication and waits for both
architectures. It passes the exact accepted source revision, so a different
published tag fails before installation. A verification failure makes the
workflow fail but does not remove or overwrite already published assets. The
manual verifier remains available for diagnosis and recovery after publication.
The direct reusable-workflow call does not depend on events from `GITHUB_TOKEN`
creating another workflow run.

After publication and public visibility are confirmed, use a fresh Linux host
for each supported architecture. Configure a new Docker directory with an
explicit `{"auths":{}}` config file, a new plugin directory, and no registry or
cloud credentials. Use the real pinned Runner to install and list the OCI plugin.
The distribution tool's verification mode compares it with the checksum-verified
Release archive, executes its version command, and loads its real Fleeting
protocol. An empty configuration must fail before credential construction or
cloud API calls.

The tool emits a small JSON receipt with the source revision, platform, manifest
digest, binary checksum, and verification results. It does not run a GitLab job
or create a worker. For releases with Debian packages, the workflow also downloads
the architecture's `.deb` without credentials, verifies its checksum and native
binary, and runs the existing dpkg/systemd lifecycle test on that host. It saves
a separate Debian receipt. Older releases retain OCI-only checks.
See [contributor commands](../CONTRIBUTING.md) for the
verification command.

## Release gate

### Candidate integrity

`tools/release_candidate.py` binds an unpublished `dist/` directory and a separate
`responsegate/plugin` test binary to one full source SHA and version. Its manifest
records every file checksum. Keep the manifest digest outside the downloaded
candidate payload. The `verify` operation rejects modified, missing, extra or
linked files and mismatched source/version metadata. Go build information must
identify the exact clean source SHA, Linux target architecture and expected test
tags before any cloud identity is obtained.

The `gate` operation additionally requires one passing receipt for each of the
six plugin-only cloud scenarios, with the same candidate digest, binary checksum,
GitHub run and attempt. It rejects skipped scenarios, cleanup failures, leftovers
and receipts from another candidate. The response-loss scenario must identify its
instrumented binary; the other five must identify the ordinary Linux amd64
distribution binary. Run the offline regression tests with `make test`.

### Candidate platform checks

PR CI and the pre-release cloud workflow call the same credential-free platform
verifier with the artifact ID, manifest digest, source SHA and version. Native
Ubuntu 24.04 and 26.04 hosts, each on amd64 and arm64, verify the downloaded
manifest and build metadata, then install the complete OCI payload and run the
real package lifecycle against the exact candidate bytes. The Ubuntu 26.04 amd64
host also boots an isolated Debian 12 userspace with systemd for the package
lifecycle. It has no network during the test. No candidate is rebuilt by these
checks, and no registry is published.
A failed, canceled or skipped platform job blocks both the PR aggregate check
and the start of cloud scenarios. Public download checks remain a separate
post-publication requirement. The public verifier uses the same four Ubuntu
combinations; receipt filenames and artifact names include the runner image and
architecture so results cannot overwrite each other.

### Publication

Use the trusted main branch, review final changes and CI, select a new semantic
version, and invoke Release explicitly. Preflight requires the first line of
`docs/release-notes.md` to be `# VERSION`, matching the requested version, before
querying remote state. It also refuses existing Releases and source tags.
The [pre-release cloud gate](pre-release-cloud.md) must then pass all
six plugin-only scenarios and complete fixture cleanup in the separately
configured test account. Missing configuration blocks publication. The workflow
reuses the exact accepted artifacts without rebuilding, creates the candidate's
source tag before OCI publication and requires that tag when creating the Release. Prerelease
versions are marked as prereleases and cannot become GitHub's latest release.
If publication fails after creating a tag or package, inspect and recover that
partial release deliberately; do not overwrite or blindly rerun the same version.
Publication is distinct from code
implementation. Check all release steps and the actual assets/package after the
workflow completes; an accepted dispatch is not proof of publication. Confirm
anonymous retrieval and Runner install before advertising public installation.
Before publication, update `docs/release-notes.md` with the exact version and
validated scope. After public installation checks, record their outcome, source
revision and receipts in the acceptance report and `acceptance/status.json`.

For baseline upgrades, read the new Runner go.mod and provider signatures,
update `upstream.lock.json`, compile against that exact Fleeting version, rerun
protocol/SSH/installer/race/config tests, review license changes and update the
parity matrix. Run targeted live suites for affected cloud behavior when
configuration is available. Do not extend old live evidence to new capabilities.
