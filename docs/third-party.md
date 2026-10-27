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
