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
Do not disable Go checksum verification. `make lint` installs pinned actionlint
1.7.12 and validates the configuration examples. CI also parses them using the
pinned, checksum-verified Runner binary.

Before committing, review `git diff`, stage only intended files, review the
complete `git diff --cached`, and run `git diff --check` and
`git diff --cached --check`. Use Conventional Commits and approved contributor
identity. GitHub CI runs the same checksum, race, vet, formatting, and whitespace checks.
This repository has no configured commit hooks or security scanner.
Do not claim that an unconfigured check ran.

See [distribution](docs/distribution.md) for native and OCI packaging, installation,
and explicit release gates. See [third-party obligations](docs/third-party.md) before adding dependencies.
