# Third-party obligations

The project is MIT. The Alibaba OpenAPI SDK, Tea runtime, and credentials
library are Apache-2.0. Dependencies are pinned in `go.mod` and `go.sum`; no
dependency sources have been modified. Preserve the upstream license and any
applicable notices when redistributing linked binaries.

CI uses the unmodified MIT-licensed
[checkout v7.0.1](https://github.com/actions/checkout/tree/v7.0.1),
[setup-go v7.0.0](https://github.com/actions/setup-go/tree/v7.0.0),
[upload-artifact v7.0.2](https://github.com/actions/upload-artifact/tree/v7.0.2) and
[download-artifact v8.0.2](https://github.com/actions/download-artifact/tree/v8.0.2)
Actions, pinned to full commits in `upstream.lock.json` and the workflows. All
run on Node.js 24 on GitHub-hosted runners. They are CI tools and are not bundled
in the plugin packages.

Cloud acceptance downloads the unmodified official Alibaba CLI 3.5.2 Linux amd64
archive, pinned by SHA256 in `upstream.lock.json`. It is Apache-2.0 licensed;
retain its license/notices if redistributing it. It is an ephemeral validation
tool and is not bundled in the plugin's native or OCI packages. Its source and
license are available at the [official tag](https://github.com/aliyun/aliyun-cli/tree/v3.5.2).

The ESS endpoint table is adapted from alibaba-cloud-sdk-go v1.63.107. Its source
URL and version are included in `internal/aliyun/endpoints.go`, and its
Apache-2.0 text is retained in `LICENSES/Alibaba-SDK-Apache-2.0.txt`.

The remaining transitive dependencies of this SDK transport use MIT, BSD, or
Apache-2.0 licenses. Future binary distributions must include the licenses and
notices of the modules actually linked into each target, including Go's
standard library license and patents notice.

Client diagnostics use HashiCorp go-hclog v1.6.3 under MIT. Preserve its copyright
and license text when redistributing linked binaries. The dependency is
unmodified; its source is available at the
[locked upstream tag](https://github.com/hashicorp/go-hclog/tree/v1.6.3).

The provider imports Fleeting's MIT-licensed protocol and connector. Its linked
go-plugin v1.8.0 and yamux v0.1.2 dependencies use MPL-2.0. Preserve their notices
and provide their corresponding unmodified source versions with distributions.
Fleeting also has a root NOTICE for connector material; retain it alongside its
license. Release packaging must inventory all actually linked modules before
publishing binaries. No dependency source is modified in this repository.

`tools/dist.py` inventories the union of modules actually compiled into the Linux
amd64 and arm64 plugin binaries. Archives include their LICENSE/COPYING/NOTICE
files, the locked source-archive URLs, and Go's LICENSE/PATENTS. Missing linked
module licenses stop the build. The `_licenses` directory is excluded from Go
package discovery. Unused module declarations and test/release-tool dependencies
are not described as linked plugin code. Release tooling imports the MIT-licensed
fleeting-artifact and Apache-2.0 go-containerregistry; it is not copied into the
plugin executable. Worker images and job containers need separate license review.

OCI platform payloads keep only root-level files for the pinned Runner installer.
Their `licenses.tar.gz` preserves the complete `LICENSES/`, `_licenses/`, and
`dependencies.json` tree, including the original texts and source archive URLs.
A root `LICENSE` and readable `NOTICE` explain how to access that material.
Native archives retain the original unpacked license tree.

Manager configuration parsing uses the unmodified MIT-licensed
[BurntSushi/toml v1.6.0](https://github.com/BurntSushi/toml/tree/v1.6.0).
Its license is included by the linked-module inventory in native, OCI and Debian
packages. Debian packaging uses the host's `dpkg-deb`; it is not bundled.

Development-only security tools are pinned in `upstream.lock.json` and
`tools/requirements-dev.txt`: [Gitleaks 8.30.1](https://github.com/gitleaks/gitleaks/tree/v8.30.1)
and [Ruff 0.17.3](https://github.com/astral-sh/ruff/tree/0.17.3) are MIT;
[ShellCheck 0.11.0](https://github.com/koalaman/shellcheck/tree/v0.11.0) is GPL-3.0;
[govulncheck 1.9.0](https://github.com/golang/vuln/tree/v1.9.0) uses the Go
BSD-style license. The unmodified executables run locally/in CI and are not
bundled in plugin distributions. ShellCheck is a separate lint process, not
linked into the plugin. Retain the applicable license and source obligations if
redistributing these tools; this repository does not redistribute their binaries.
The x/net security update to v0.60.0 retains its existing Go BSD-style license
and is included in the generated linked-module inventory when applicable.
