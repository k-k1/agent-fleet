# 0132. The user guide is read on agent-fleet.org/docs/ only; the copy shipped into containers goes

English | [日本語](0132-site-only-guide.ja.md)

- Status: **proposed** (2026-11-07). The user chose this; it is carried out in phases after the site is live.
  Follow-ups: #3463.
- Amends: [0064](0064-docs-three-audiences.md) (the "Shipped: into every container" column for `guide/`) /
  [0131](0131-public-docs-site.md) §2 (the Console's user guide and the agents' knowledge reading the shipped files)

## Context

ADR 0131 publishes `guide/` as a site at `https://agent-fleet.org/docs/` and keeps the shipped copy: the Control Plane
bakes `guide/` (plus the runbooks and release notes, `deploy/release/stage-docs.sh`) into its image, docker and native
mount it into each workspace at `/usr/local/share/agent-fleet/docs`, and ECS pulls it over the docs bridge. The Console's
**Guide** menu opens that copy in the file viewer, and the fleet policy tells every agent to grep it.

Keeping both means two ways of reading the same text, a converter for one and a staging script for the other, and a
Console viewer that renders the guide less well than the site (no search, no sidebar, no alerts). The user asked for
one place to read it.

## Decision

1. **People read the guide on the site.** The Console's **Guide** menu opens the matching page of
   `https://agent-fleet.org/docs/` (Japanese under `/ja/`) in a new browser tab.
2. **The shipped copy is removed** from the Control Plane image, the native tarball, the docs bridge and the workspace
   agent. `stage-docs.sh` stays only as the site's input.
3. **`guide/` in the repository stays the single source.** Nothing about how it is written, checked or reviewed
   changes; `docs-check.py` keeps checking it.
4. **The agents' fleet policy and topic notes stay shipped** (`/usr/local/share/agent-fleet/notes/`). They are not the
   guide, they are small, and an agent needs them before it can reach anything. Where they send an agent to the guide,
   they give the site URL instead of a path in the container.
5. **The order is fixed by availability**: the site is deployed (ADR 0131, the deploy phase of #3463) before the
   Console and the policy point at it, and the shipped copy is removed last.

## Consequences

The user accepted these when choosing this option:

- **Agents need the network to read the guide.** Where outbound traffic is closed, an agent can answer from the policy,
  the topic notes and the built-in knowledge, not from the guide.
- **The site shows the latest release.** A deployment that has not upgraded can read pages that describe newer
  behaviour.
- **An air-gapped deployment has no guide in the product.** Its operators read the repository at their release tag,
  or mirror the site themselves.

Also:

- The `docs-check.py` checks that tie the policy to guide paths change to check site URLs against `guide/`.
- The docs bridge (`control-plane/docs_bridge.go`, `workspace/agent/docs_sync.go`), `workspace_docs.go`, the docs
  mounts in the runtimes, `AGENT_DOCS_DIR` / `docsRoot`, the CP image's `docs` stage and the release gate's
  shipped-docs assertion are deleted in the last phase.
- Runbooks remain readable on GitHub next to their scripts, and on the site.

## Rejected options

- **Keep the shipped copy for agents only, and send people to the site.** Two consumers stay, and the one the user
  wanted gone (the stage, bake and bridge) stays with them.
- **Open the site inside the Console** (an iframe or the browser pane). The site sets `frame-ancestors 'none'`, and the
  browser pane is for the member's own apps.
