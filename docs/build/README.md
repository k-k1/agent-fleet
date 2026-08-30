# Building Agent Fleet

English | [日本語](README.ja.md)

Audience: someone changing the code — a new contributor, a future maintainer, or an agent session
Source of truth: the code (this shelf is the map and the design intent)
Updated: 2026-08

This shelf answers **"how does it work?"**: the three processes and what each owns,
the two authentication layers, the API boundaries, the data model, the threat model,
the integrations, and how to build and test.

## What belongs here

- Responsibilities and wire contracts: which process owns what, and what the
  externally visible contract is.
- The shapes that repeat — how an agent kind is integrated, how a deployment adapter
  is structured — so the next one is added by following a pattern rather than by
  reverse-engineering the last one.
- Build, reflect and test practices.

## What does not

- **Line numbers, ever.** They are wrong within a week. Point at a grep-able anchor
  instead — an endpoint path, an environment variable name, an error code string — or
  at the code map, which is the one file allowed to enumerate paths and is expected
  to go stale.
- **Procedures for running a deployment** — [operate/](../operate/README.md) owns
  those, and this shelf links rather than copies.
- **User-facing instructions** — [use/](../use/README.md).
- **Journals.** A measurement, an incident post-mortem or a round-by-round
  investigation is not a design document. Put the durable conclusion here and the
  reasoning in [decisions/](../decisions/).

## Update trigger

| You changed | Update |
|---|---|
| An API group or path | the API contract map + the component chapter |
| A migration | the data model chapter |
| Authentication, crypto, isolation or audit | the security chapter |
| An external provider or agent CLI | the integrations chapter (+ the agent-kind pattern) |
| A deployment target, adapter or variable | the deploy chapter |
| Build, reflect or test mechanics | the development chapter |
| Where files live (a refactor) | the code map only — nothing else should move |
| A feature users can see | the relevant chapter, plus [ref/](../ref/README.md) and the reader's shelf |

## Migration in progress

Not written yet. Until it is, `../dev/` is the source of truth and already follows
most of these rules. Phase P4 of the documentation rebuild rewrites it here,
bilingually, and adds the two guides the old shelf never had: how to add an agent
kind, and how to add a deployment adapter.
