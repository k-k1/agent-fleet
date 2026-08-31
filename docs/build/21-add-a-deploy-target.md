# 21. Adding a deployment target

English | [日本語](21-add-a-deploy-target.ja.md)

Audience: someone adding a way to run workspaces
Source of truth: the existing `runtime_*.go` adapters — read all four before writing one
Updated: 2026-08

Four adapters exist: Docker, ECS on Fargate, ECS on an EC2 pool, and containerless host
processes. **They differ far more from each other than a new one usually needs to**, so
read them first — the containerless one is the smallest and shows the minimum, and the
EC2 pool one shows what "hard" looks like.

The user-facing comparison is
[ref/deploy-targets.md](../ref/deploy-targets.md); the operator's view is
[operate/01](../operate/01-choose.md).

## 21.1 What a target actually is

**One interface, one profile value, and nothing else.** The core — Console, CP logic,
agent, workspace image — is the same artefact everywhere ([01 §1.6](01-architecture.md)).
If you find yourself needing to change the core to add a target, that is the signal that
something belongs behind a port rather than in the adapter.

An unknown profile **fails fast at boot** rather than defaulting to Docker. Keep that:
a deployment that silently ran the wrong substrate would be far worse than one that
refuses to start.

## 21.2 The contract to implement

Beyond starting and stopping a container, these are the parts that are easy to miss:

| Obligation | Why it matters |
|---|---|
| **Adopt, do not recreate** | On a CP restart the database row is the truth and an existing workspace must be **adopted by inspection**. Recreating would discard a running user's session |
| **Report `starting` honestly** | It exists for substrates where a cold start takes minutes. **While it is starting, callers must neither re-start nor idle-stop it.** An adapter that comes up in seconds simply never reports it |
| **Two-stage graceful stop** | Signal, wait, then kill — and hand the agent a **shorter** grace than your own, so it can interrupt the pane and let tmux exit before the hammer falls |
| **Do not put the DEK in plaintext where the substrate can show it** | On a substrate with a secret store, the task definition must carry **a reference**, not the value ([07 §7.6](07-security.md)) |
| **Declare whether you can be bind-mounted into** | The role-scoped documentation is staged and mounted for adapters that have a host seam, and **pulled over an internal endpoint by the container for those that do not** ([03](03-control-plane.md)). Claiming the wrong one either copies megabytes nobody can read, or leaves the guide empty |
| **Per-user isolation, or refuse to run shared** | The containerless adapter **requires the single-user auth mode** and refuses otherwise, because without a container boundary there is nothing separating users ([07 §7.2](07-security.md)) |

## 21.3 What is not the adapter's job

- **Idle decisions.** The logic is common; the adapter only supplies liveness and
  performs the stop ([03 §3.7](03-control-plane.md)).
- **Authentication and tenancy.** Those are resolved long before the runtime is built.
- **The workspace image.** If your target needs a different image, **stop** — that
  breaks the one property that makes any of this portable.

## 21.4 Cost and latency are part of the design

Two lessons from the existing targets, both of which cost real money to learn:

- **A standing floor changes the shape of the bill, not just its size.** A target with
  per-task billing needs idle-stop to actually work for its numbers to hold — and **the
  bill when idle-stop breaks is the number you should quote to yourself**, not the happy
  path ([09 §9.8](09-deploy.md)).
- **Measure before optimising the start.** A cold start was assumed to be image pull and
  a lazy-loading scheme was nearly adopted for it; the actual first-start failure turned
  out to be **a synchronous wait longer than the ingress idle timeout**, which was fixed
  independently. **Break the number down before choosing a remedy for it.**

## 21.5 Verification

- **The role-scoped docs tests** encode which adapters have a host seam. Adding one
  without touching them means the new adapter is silently in the wrong group.
- **The end-to-end suite runs against a real container** through the public API only, so
  it will exercise your adapter if it can be selected by profile.
- **`ref/deploy-targets.md` rows are compared against the profiles the factory accepts**
  — CI fails if you add a profile and not a row.
- Nothing replaces standing one up and running a real session on it. **"It compiles and
  the tests pass" has never been sufficient evidence for a substrate**; every target so
  far found something only on real infrastructure.

## 21.6 Finishing

1. The factory accepts the profile, and rejects unknown values loudly.
2. [ref/deploy-targets.md](../ref/deploy-targets.md) has its column, including the
   honest "—" cells.
3. [operate/01](../operate/01-choose.md) says **when to choose it** — and, if it is not
   the obvious choice, when not to.
4. A runbook exists next to the scripts it operates, and
   `deploy/release/stage-docs.sh` copies it in for the container.
5. [decisions/](../decisions/) records why it exists as a separate target rather than a
   flag on an existing one — that question is asked every time.
