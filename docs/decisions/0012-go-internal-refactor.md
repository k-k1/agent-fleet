# 0012. Go backend internal refactor — layer into `internal/`, keep the two binaries separate, defer a shared module

English | [日本語](0012-go-internal-refactor.ja.md)

- Status: decided (2026-07-08)
- See also: [23-go-refactor.md](../log/23-go-refactor.md) (the design proper) /
  [0011-console-rebuild.md](0011-console-rebuild.md) (the precedent on the Console side)

## Context

The CP (about 13.8k lines, 54 files) and the Workspace Agent (about 16k lines, 61 files) are
both a flat single `package main`, so no boundary is enforced by the compiler. The consequences:
the CP has two god objects (`config` with 133 handlers; `manager` with too many
responsibilities and a lock held across DB I/O), and the Agent carries three parallel copies of
the same implementation for the three CLIs (transcript / auth / usage) plus 11 islands of global
locks. There is no CI, and the API contract with the Console is kept in sync by hand across three
places: TS types, Go structs and error-code strings. The logic itself, though, is healthy, and
good abstractions already exist in the Store / Runtime / Agent interfaces — unlike the Console
(docs/22), where nothing but a rebuild would do, **this is purely a structural problem** and
refactoring solves it.

## Decision

1. **Layer into `internal/` packages within each module** (behaviour unchanged, wire API fully
   compatible). Both Dockerfiles COPY the whole module directory, so splitting within a module
   passes the build unchanged. `//go:embed` targets (migrations, knowledge) move with their
   package.
2. **Keep the CP↔Agent split into two binaries.** The code in `preview` and the SSM family that
   "looks duplicated" is the two ends of a protocol, and it is designed and documented as a trust
   boundary (the Agent is inside the VPC only). Do not merge them.
3. **Defer a shared Go module (go.work).** The genuine cross-module duplication amounts to
   `writeJSON` and ID generation, which does not justify the cost of changing the context of two
   Dockerfiles. Revisit when we seriously take on typing the contract (generating TS types with
   tygo or similar).
4. **Put the safety net in first** (P0): CI (gofmt / vet / test / build plus the Console build),
   extracting `buildMux()` out of `main()` plus an httptest smoke test, and turning error-code
   strings into constants. golangci-lint is left out of the initial scope because it would start
   red against the existing code (opt in later).

## Consequences

- The phase order is P0 (safety net) → P1 (Agent: fold up runGit / fileStore[T] / decodeJSON,
  then slice the CLIs vertically into `internal/agents/{claude,codex,opencode}` and so on) →
  P2 (CP: the preamble wrapper, distributing route registration, breaking up `config` and
  `manager`, Store sub-interfaces) → P3 (optional: typing the contract). Details in docs/23.
- Exactly one change touches behaviour — fixing the lock scope of `manager.mu` — and it goes in
  its own PR. Every other wave is pure relocation and folding; move waves and logic waves are
  never mixed.
- The three transcript parsers are not merged. They are aligned to the same package boundary and
  the same output type, so the parallelism is fixed structurally (things like opencode's missing
  usage become visible as an unimplemented part of the interface).
