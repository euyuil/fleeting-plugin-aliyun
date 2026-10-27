# Third-party obligations

The project is MIT. The Alibaba OpenAPI SDK, Tea runtime, and credentials
library are Apache-2.0. Dependencies are pinned in `go.mod` and `go.sum`; no
dependency sources have been modified. Preserve the upstream license and any
applicable notices when redistributing linked binaries.

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
