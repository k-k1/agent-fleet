---
audience: "someone integrating a new CLI coding agent"
source_of_truth: "the existing `internal/agents/<kind>` packages — copy the closest one"
updated: "2026-08"
---

# 20. Adding an agent kind

English | [日本語](20-add-an-agent.ja.md)

Seven kinds have been added this way. **The surfaces are the same every time**, and the
mistakes are the same every time too — this chapter is both lists.

Before writing code, read [04 §4.3](04-agent.md) for the shape and
[ref/agents.md](../../guide/ref/agents.md) for what the existing kinds actually support.

## 20.1 Decide three things first

**1. Which drivers?** Terminal only, managed only, or both. Managed means a structured
API and no pane; Terminal means driving the CLI's own screen through tmux. Both is more
work but is what most kinds ended up needing.

**2. How is the conversation id held?** This is the decision that causes silent
breakage later, so make it deliberately ([04 §4.2](04-agent.md)):

- **Captured** — the CLI mints the id, and a hook, a plugin or a disk scan re-records it
  **on every event**. If the CLI moves to a different session, the next event follows.
  **Prefer this.**
- **Imposed** — you mint it and pass it in. Everything downstream then assumes the CLI
  is still using it, **and it breaks silently the day it is not.** If you must impose,
  **ship the recovery path in the same change** — the rule is: only when the imposed id
  exists nowhere on the CLI's side, only when **exactly one** candidate matches, and
  **do nothing when it is ambiguous.**

**3. What proves it works?** Not the CLI's own status output, and not a banner. **A real
prompt producing a real answer.** This has been wrong enough times to be a rule
([08 §8.5](08-integrations.md)).

## 20.2 The surfaces to fill

| Surface | Where | Notes |
|---|---|---|
| The kind constant and its capabilities | `internal/session`, and the `Caps()` of your package | **Do not set a capability you have not driven end to end.** [ref/agents.md](../../guide/ref/agents.md) is checked against this by CI |
| Launch | your package's launch builder | Environment goes **prefixed onto the command**, never through the tmux session environment (§20.3) |
| Live state | hooks, a plugin, or runtime events | Normalise to working / idle / question ([04 §4.4](04-agent.md)) |
| Transcript | a reader for the CLI's own storage | **Read its native store; never copy conversations into a store of ours.** The parsers stay separate |
| Sign-in | the connections API | Prefer a method needing **no callback** ([08](08-integrations.md)) |
| Credential location and the filesystem denylist | your package, plus the denylist | Anything the CLI writes credentials into must be **hidden from the file browser** |
| MCP materialisation | `internal/mcpreg` | Each CLI has its own config shape and placeholder dialect |
| Agent instructions | the instruction distributor | If the CLI has no per-user place for them, **say so in the UI** rather than silently dropping them |
| Console descriptor | the agent registry | One descriptor; **the UI branches on capabilities, not on the kind** |
| Version pin | the image build arguments and the version manifest | [10 §10.2.1](10-development.md) |
| A contract workflow | `.github/workflows/<kind>-contract.yml` | **One file per agent** — §20.5 |

## 20.3 The traps that have actually bitten

Every one of these cost real debugging time.

- ⚠️ **`tmux new-session -e` does not reach the process.** It sets the session
  environment, not the child's. **Prefix the command.**
- ⚠️ **Reap your children.** The agent is not PID 1. `Start()` without a matching wait
  leaks a PID **forever**, and the path that leaks is always the failure path — "kill it
  on a start timeout and return". Two runtimes shipped that bug.
- ⚠️ **Nested hook schemas parse when written flat, and then never fire.** No error, no
  log — resume silently starts a new conversation instead.
- ⚠️ **tmux target matching is a prefix match.** Always use the exact form, or you will
  eventually kill the wrong session.
- ⚠️ **A model that only exists in a picker is not a model id.** Resolve against the live
  catalogue at creation and **refuse before the clone or worktree happens** — an invalid
  model that only fails after launch leaves debris behind.
- ⚠️ **Free plans are a different product.** One CLI's free tier has no model catalogue
  *and* rejects the reasoning-effort flag — so passing that flag unconditionally fails to
  start **for exactly the users least able to diagnose it**.
- ⚠️ **A trust or onboarding prompt is not authentication.** A CLI can be signed in and
  still show a wizard, which looks identical to being signed out
  ([08 §8.5](08-integrations.md)).
- ⚠️ **Answer modals by key sequence, never by typing the label**, and verify **through
  the delivery layer** — a sequence that is right in a probe can still not reach the
  agent ([92](92-driving-a-tui.md)).

## 20.4 Do not set a capability you have not driven

`Caps()` is not documentation — the Console shows or hides controls by it, and
[ref/agents.md](../../guide/ref/agents.md) is compared against it in CI. The rule this repository
learned: **a capability is set only when the path has been driven end to end on the real
CLI.** The specific case: allowing the permission prompt to be skipped requires that
**a pending approval can actually be answered from the Console**. Removing the flag is
easy for any kind — but a session stopped at a dialog **the user cannot see or answer**
is, from their side, indistinguishable from a hang.

The same applies to the other direction: **when a capability is genuinely absent, do not
render the control at all.** A button that does nothing is worse than no button.

## 20.5 Verification, and why the workflow is per agent

Local tests are not enough, for two reasons that are documented in
[10 §10.4](10-development.md): CI only ever sees **the pinned version**, while a
workspace that opted into self-update runs `@latest`; and the headless smoke test draws
no TUI, so nothing about the interactive screen is exercised. **Breakage in state
detection escaped a green CI three times.**

So a new kind needs a **contract workflow of its own**. It must be its own file:
path filters and dispatch inputs are per workflow, and sharing one caused a single
dispatch to spend **two different agents' quotas**.

Register it with the daily drift watcher as well, so a published version change
dispatches the contract automatically — but only if its credentials can be supplied
unattended. **A credential that rotates through an interactive refresh is recorded as
"seen" and dispatched by hand**; conflating "we noticed a new version" with "we tested
it" is how a regression ships.

## 20.6 Finishing

A kind is not done when it runs. It is done when:

1. `Caps()` matches what you actually drove;
2. [ref/agents.md](../../guide/ref/agents.md) has its column filled — **CI compares the fork and
   permission rows against `Caps()` exactly**, so a mismatch fails the build;
3. [use/06-agents](../../guide/member/06-agents.md) tells a user how to connect it, using the
   Console's own words;
4. the contract workflow exists and has passed against the real CLI;
5. if anything was settled that could plausibly be reopened — why this driver, why this
   id strategy — [decisions/](../decisions/) has the record.
