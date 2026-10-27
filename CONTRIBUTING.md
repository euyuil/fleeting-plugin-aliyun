# Contributing

Use Go 1.27.2 and Git. Exact upstream versions are pinned in
`upstream.lock.json`; Go dependencies are locked by `go.mod` and `go.sum`.
No cloud credentials are needed for local development.

```sh
go mod download all
go mod verify
go test ./...
go test -race ./...
go vet ./...
```

Tests replace the API boundary or use signed requests against a loopback HTTP
server. They do not invoke the standard credential chain or contact cloud APIs.
Do not disable Go checksum verification.

Before committing, review `git diff`, stage only intended files, review the
complete `git diff --cached`, and run `git diff --check` and
`git diff --cached --check`. Use Conventional Commits and approved contributor
identity. GitHub CI runs the same checksum, race, vet, formatting, and whitespace checks.
This repository has no configured commit hooks or security scanner.
Do not claim that an unconfigured check ran.

See [third-party obligations](docs/third-party.md) before adding dependencies.
