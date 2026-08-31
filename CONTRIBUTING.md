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
  KeyCustodian, MetadataStore, AuthGateway) — see `docs/build/09-deploy.md`.
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

**`develop` is the trunk** (and the default branch). Day-to-day work is pushed
straight to `develop` and merged as it lands (single maintainer, no review gate);
**"done" means merged into `develop`**. Don't create branches on your own — the
Console hands each worktree session its own branch. Remote:
`git@github.com:k-k1/agent-fleet.git`.

**`main` is the always-green stable branch** and is updated only through
`develop` → `main` pull requests (a release train, once or twice a week or at the
end of a phase). Hosted CI comes in two tiers. `ci.yml` (gofmt / vet / test /
build) runs on **every push to `develop`** as well as on the `main` gate — the
repository is public, so its runners are free, and gating it on `main` alone left
the trunk unverified for a month at a time. `e2e.yml` and the contract workflows
do spend an external LLM quota, so those stay concentrated on the `develop` →
`main` PR, on pushes to `main`, and on their nightly / weekly cron over `develop`.
Either way, the local run above is still the per-commit check — hosted CI is the
safety net, not the first line. See `docs/log/35-packaging.md` for the billing
rationale behind this two-tier split. Hotfixes branch from `main` → PR → `main`,
then back-merge into `develop`. Release tags,
release builds and public distribution are all cut from `main`.

Keep commits small and focused, and follow the format below.

### Commit message format

Conventional Commits with a **Japanese subject**. **Both the subject and the body
are written in Japanese** — that is this repository's convention, and its commit
history doubles as the design record, so it stays in one language.

> Contributing from outside and don't write Japanese? **English is fine** — send
> the PR in English and the maintainer will not ask you to rewrite it. The rule
> above is the maintainer's own working convention, not a barrier to entry.

```
<type>(<scope>): <summary>      ← first line, no trailing period, imperative, ~50 chars

<body>                          ← after one blank line. What changed and why. For a
                                   behaviour change: root cause → fix → verification.
                                   Wrap at ~72 columns.

Co-Authored-By: <model name> <noreply@<vendor>>   ← trailer, separated by a blank line
```

- **type**: `feat` / `fix` / `docs` / `style` (formatting only, no behaviour change) /
  `refactor` / `perf` / `test` / `build` (baked-in CLI or dependency version bumps) /
  `chore` / `ci`.
- **scope**: the main subject of the change. Real examples: `console`, `chat`,
  `cp` (= control-plane), `agent`, `mirror`, `tts`, `workspace`, `deploy`; docs use
  `docs(NN)` with the chapter number. Add one whenever you can.
- **body**: for bug fixes and behaviour changes, record **root cause → fix →
  verification (how you actually checked)**. This project is verified against a live
  fleet, and the commit history is the only design record of that.
- **migration**: schema changes (`control-plane/migrations/`) must be forward
  compatible, and say so in the body — the embedded migrator applies them
  automatically at Control Plane startup and there is no downgrade path.

### Attribution (the Co-Authored-By trailer)

This project is built with Claude Code, Codex and opencode side by side. A commit an
agent wrote carries a co-author trailer after a blank line, naming **the model that
actually generated it** — attribution is by model, not by CLI.

| Environment | Example trailer |
|-------------|-----------------|
| Claude Code | `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>` (or `Claude Fable 5` etc., matching the running version) |
| Codex | `Co-Authored-By: GPT-5.6 <noreply@openai.com>` |
| opencode | Attributed to the running model (Claude family → `@anthropic.com`, GPT family → `@openai.com`) |

- The address is the vendor's `noreply@` domain (Anthropic = anthropic.com,
  OpenAI = openai.com, otherwise the model vendor's domain). Name the model version
  actually running — don't hardcode one.
- **No session URL line.** The old `Claude-Session:` trailer is retired. Claude Code
  adds one by itself when driven over Remote Control; that is harmless and accepted
  (suppress it in Claude Code's own settings, not here).
- **Always add `Co-Authored-By:`** when an agent was involved — at least one line, and
  several lines when several models or people were. It may be omitted only for a
  commit written purely by a human.
