# Agent Fleet documentation

English | [日本語](README.ja.md)

Start with the shelf that matches who you are. Each shelf is written for one reader,
and the shelf is also the unit we ship: the Control Plane hands a container only the
shelves that user's role is allowed to see.

| You are | Shelf | What it answers |
|---|---|---|
| Someone using Agent Fleet to run agents | [use/](use/README.md) | How do I do this? |
| A tenant administrator | [admin/](admin/README.md) | How do I run this for my team? |
| Someone who installs and operates a deployment | [operate/](operate/README.md) | How do I stand it up and keep it alive? |
| Someone changing the code | [build/](build/README.md) | How does it work? |

Two shelves are read by everyone:

- **[ref/](ref/README.md) — what the product can do.** Capabilities per feature, per
  agent, per repository provider, per deployment target and per role. Every other
  shelf links here instead of repeating it; when prose and a table disagree, the
  table wins.
- **[decisions/](decisions/) — why it is like this.** Decision records, append-only,
  including the options we discarded so nobody retries them by accident.

## Writing here

[CONVENTIONS.md](CONVENTIONS.md) is the norm for every file on every shelf: the
three-lifetime rule, the required file header, the vocabulary each shelf may use, and
the bilingual rule (English is canonical, Japanese lives beside it as `X.ja.md`).
`scripts/docs-check.py` enforces it, in CI and locally.

## Not a shelf

- **[log/](log/README.md)** — a frozen archive of the work journals that used to be
  `docs/NN-*.md` and `docs/history/`. Not maintained, not shipped. It exists so we can
  look up **measurements, production-incident causes, upstream CLI contracts, and the
  reasons we abandoned things** — facts that live nowhere else. Living documents never
  link into it.
- [HANDOFF.md](HANDOFF.md) — the development host's runtime state and host-specific
  practices. Changes daily.
- [roadmap.md](roadmap.md) and [CHANGELOG-handoff.md](CHANGELOG-handoff.md) — forward
  plans, and a dated work log.

## Migration in progress

`dev/` and `guide/` are the previous layout and are still the source of truth until
the new shelves are written. Each new shelf's README says what it already covers and
what still lives in the old place.
