# Contributing to Agent Fleet

Thanks for your interest. This is a small project with a single maintainer, so a
few conventions keep things smooth.

## License of contributions

By submitting a contribution you agree it is licensed under the project's
**Apache License, Version 2.0** (see `LICENSE`). Apache-2.0 includes an explicit
patent grant; no separate CLA is required.

## Ground rules

- **Never commit secrets.** OAuth secrets, `AF_MASTER_KEY`, cookie secrets, and
  allowlists live in git-ignored files (`deploy/compose/.env`,
  `deploy/local/oauth.env`, `allowed-emails.txt`). Double-check `git diff` before
  committing. Compiled binaries are git-ignored too.
- **Keep the core deploy-agnostic.** Don't bake Docker/compose assumptions into
  the Control Plane. Deployment specifics belong behind the ports (Runtime,
  KeyCustodian, MetadataStore, AuthGateway) — see `docs/dev/09-deploy.md`.
- **Match the surrounding code.** Go: `gofmt` + `go vet` clean, `go test ./...`
  passing. Console: `npm run build` clean.
- **Run `gofmt` before every commit that touches Go — this is a hard gate.**
  `ci.yml` runs `gofmt -l .` per Go module and fails the build on *any* unformatted
  file, so `go build` / `go vet` / `go test` passing is not enough (editors and
  auto-format often skip `_test.go` files, which is exactly what has slipped through).
  Before committing, run in each touched module and make sure it prints nothing:
  ```bash
  (cd control-plane && gofmt -l .)      # lists unformatted files; empty = OK
  (cd workspace/agent && gofmt -l .)
  ```
  Fix with `gofmt -w <file>` (or `gofmt -w .`). Same for `go vet ./...`.

## Building & running

Local dev (host process): `deploy/local/run-dev.sh`.
On-prem container deploy: `deploy/compose/` (see its `README.md`).

Build paths on the dev host are non-standard: Go is at `$HOME/.local/go/bin`,
Node via nvm. The compose image build is self-contained (multi-stage) and needs
neither on the host — only Docker.

## Commits & PRs

- Small, focused commits with a clear message (Japanese is fine — the codebase
  uses it).
- Note any schema migration (`control-plane/migrations/`) and confirm it is
  forward-compatible: the embedded migrator applies on CP start, and downgrades
  are not supported.
- Describe how you verified the change (tests, and for behavior changes, a real
  run — this project is verified against a live fleet, not just unit tests).
