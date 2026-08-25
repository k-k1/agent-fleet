# Changelog

Release notes index for [Agent Fleet](https://github.com/k-k1/agent-fleet-dist). Each entry links
to the release, where the full notes are. 日本語は [CHANGELOG.ja.md](CHANGELOG.ja.md)。

## [0.12.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.12.2) — 2026-08-25

Agent memory can be imported with the history it came with, workspace resource figures
appear on ECS deployments, and the admin Egress screen works on Postgres.

## [0.12.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.12.1) — 2026-08-25

Three fixes found while bringing a real `ecs-ec2` deployment up on 0.12.0: a Graviton
Control Plane that quietly came up on x86_64, per-member cost figures that never
settled on a member account of an AWS organization, and agent memory that could not be
imported into a workspace that had just been created.

## [0.12.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.12.0) — 2026-08-25

A workspace no longer stays up — and billed — because a session is waiting for a
person. Questions, plan approvals and permission requests are kept when the session is
folded and answered afterwards in the session list, tool approvals are a choice rather
than always skipped, and a session can be handed to another member of the tenant.

## [0.11.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.11.0) — 2026-08-23

Tenant administrators register their own GitHub and Bitbucket OAuth apps, the Control
Plane can run on Graviton, and a set of `ecs-ec2` workspaces that hung on "starting" or
quietly kept costing money now behave.

## [0.10.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.10.0) — 2026-08-22

The machine a workspace runs on is something a tenant chooses per member, including
Graviton, and vendor icons in `.drawio` diagrams now draw. Administrators can delete
what they used to be able to create only, and a handful of sessions that looked broken
were not.

## [0.9.3](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.9.3) — 2026-08-21

One fix for administrators: you can now take yourself off a tenant's roster, as long as
it is not the last roster you are on.

## [0.9.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.9.2) — 2026-08-21

A follow-up to the EC2-backed persistent workspaces of 0.9.0, from running one as a
real deployment: a home created from the pre-baked *golden* snapshot could not start at
all, and baking a golden by hand ran into a step that never completes.

## [0.9.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.9.1) — 2026-08-21

A maintenance release for AWS deployments, from standing one up end to end: read-aloud
never worked on ECS at all, a workspace created after the Control Plane started could
not be reached from it, and the two screens a brand-new deployment shows first were
blank.

## [0.9.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.9.0) — 2026-08-21

Signing in is no longer tied to Google: any OpenID Connect provider or GitHub can be
enabled, and one deployment can be divided into departments that own their own sign-in
page, roster and permitted networks. On AWS there is a new runtime that keeps each
person's home on a disk that survives a stop, with the cloud bill shown per member
beside it. In the Console, `.drawio` files open as diagrams, every session lists the
files it changed, and passages of a conversation can be highlighted for the people you
shared it with.

## [0.8.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.8.0) — 2026-08-13

A session's conversation can be shown to another member of your tenant, a new session
can branch from any past message with the conversation copied as-is, the main area
lays out as a tabbed grid as well as split panes, and sessions can send each other a
one-line message without going through you.

## [0.7.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.7.0) — 2026-08-07

OpenCode signs in with your opencode.ai account from the Console and lets you choose
which plan its sessions run on, interrupted Claude turns pick themselves back up
without an assistant in the loop, and AWS's Agent Toolkit joins the built-in MCP
integrations.

## [0.6.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.6.0) — 2026-08-02

An agent can hand you the browser page it is driving, so you take over for the final
click, and the Console speaks English end to end instead of falling back to Japanese.
**Docker Compose installs change in this release: the container images are pulled from
GHCR instead of being loaded from an image tarball.**

## [0.5.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.5.1) — 2026-08-01

A rebuild that removes an internal string the Console bundle had been carrying, plus
the gate that will keep it from happening again. **The downloads for 0.1.0 through
0.5.0 have been withdrawn — please use this release instead.**

## [0.5.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.5.0) — 2026-08-01

Working sets split the Console's left pane by project, you can register your own MCP
servers, an agent's memory is versioned and portable, and the editor can propose a
change you review as a diff.

## [0.4.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.4.0) — 2026-07-27

Token usage is broken down by feature, files open for edit in the Console, things you
lock stay put, and a turn that dies mid-flight resumes itself.

## [0.3.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.3.0) — 2026-07-25

Kiro joins the agent CLI lineup, the composer offers reply suggestions, and the fleet
operator can answer questions and review plans on your behalf.

## [0.2.3](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.3) — 2026-07-24

Memos can be dragged straight into a session, plus a set of smaller fixes.

## [0.2.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.2) — 2026-07-24

Panes can be popped out into their own browser tab, schedules gained a detail and edit
view, and Cursor can back the assistant chat.

## [0.2.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.1) — 2026-07-23

Bitbucket connection fixes and a unified session handoff modal.

## [0.2.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.0) — 2026-07-23

The largest release so far: sessions can be driven from Discord and Slack, prompts can
run on a schedule, Cursor CLI joins the lineup, Subversion working copies are
supported, and the `native` host binary updates itself.

## [0.1.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.2) — 2026-07-21

A hotfix for the `native` runtime, where Claude sessions could become completely
unable to accept input.

## [0.1.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.1) — 2026-07-21

A documentation and packaging follow-up to the first release, plus one Console change.

## [0.1.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.0) — 2026-07-21

First public release. Agent Fleet is a self-hosted console for running AI coding
agents as a fleet: each member gets an isolated environment and drives agent sessions
from the browser.
