---
audience: "someone changing the Agent Fleet code"
source_of_truth: "the code — if this tree disagrees with it, the code is right"
updated: "2026-09"
---

# Agent Fleet developer documentation

English | [日本語](README.ja.md)

**This tree is for developers.** The procedures for people *using* Agent Fleet live in a
separate tree, [`guide/`](../guide/README.md), which is what ships into a container.
Nothing here is shipped to anybody.

| Shelf | What is in it |
|---|---|
| [build/](build/README.md) | How it works — wire contracts, responsibilities, data flow, the shape of an extension |
| [decisions/](decisions/) | Why it is like this. Append-only decision records, **including the options that were rejected**, so nobody retries them by accident |
| [CONVENTIONS.md](CONVENTIONS.md) | The writing conventions for every tree. `scripts/docs-check.py` enforces the mechanical parts in CI and locally |

## Three readers

Documents are split by reader ([ADR 0064](decisions/0064-docs-three-audiences.md)).

| Reader | Where | Shipped |
|---|---|---|
| Someone deciding whether to try it | root [README.md](../README.md) | GitHub only |
| Someone using it | [`guide/`](../guide/README.md) | **into every container**, not cut by role |
| Someone changing the code | `docs/` (here) | **to nobody** |

**The directory boundary is the distribution boundary**, which is why `guide/` may not
link into `docs/` (the other direction is free). See [CONVENTIONS §2](CONVENTIONS.md).

## Not shelves

- **[log/](log/README.md)** — the frozen archive of the work journals that used to be
  `docs/NN-*.md` and `docs/history/`. Not maintained, not shipped. It exists only so you
  can look up the things recorded nowhere else: **measurements, the causal chain of a
  production incident, an upstream CLI's contract, why something was abandoned.** Living
  documents never link here.
- [HANDOFF.md](HANDOFF.md) — the development host's own runtime state and local
  conventions. Changes daily.
- [roadmap.md](roadmap.md) / [CHANGELOG-handoff.md](CHANGELOG-handoff.md) — the
  forward-looking plan, and the dated work log.
