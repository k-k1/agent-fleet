# Changelog

Release notes index for [Agent Fleet](https://github.com/k-k1/agent-fleet-dist). Each entry links
to the release, where the full notes are. 日本語は [CHANGELOG.ja.md](CHANGELOG.ja.md)。

## [0.6.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.6.0) — 2026-08-02

An agent can now hand you the browser page it is driving, so you take over for the
final click; the Console speaks English end to end instead of falling back to
Japanese. **Docker Compose installs change in this release: the container images
are pulled from GHCR instead of being loaded from an image tarball.**

## [0.5.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.5.1) — 2026-08-01

A rebuild that removes an internal string the Console bundle had been carrying,
plus the gate that will keep it from happening again. **The downloads for 0.1.0
through 0.5.0 have been withdrawn — please use this release instead.**

## [0.5.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.5.0) — 2026-08-01

Working sets split the Console's left pane by project, you can register your own
MCP servers for the assistant and for sessions, an agent's memory is versioned and
portable, and the editor can propose a change you review as a diff.

## [0.4.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.4.0) — 2026-07-27

Token usage is broken down by feature, files open for edit in the Console, and
things you lock stay put — plus auto-resume when a turn dies mid-flight.

## [0.3.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.3.0) — 2026-07-25

Kiro joins the agent CLI lineup, the composer gains reply suggestions, and the
fleet operator can now answer questions and review plans on your behalf.

## [0.2.3](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.3) — 2026-07-24

Memos can be dragged straight into a session, and a set of smaller fixes.

## [0.2.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.2) — 2026-07-24

Panes can be popped out into their own browser tab, schedules gained a detail and
edit view, and Cursor can now back the assistant chat.

## [0.2.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.1) — 2026-07-23

Bitbucket connection fixes and a unified session handoff modal.

## [0.2.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.2.0) — 2026-07-23

The largest release so far: sessions can now be driven from Discord and Slack,
prompts can run on a schedule, Cursor CLI joins the lineup, Subversion working
copies are supported, and the `native` host binary updates itself.

## [0.1.2](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.2) — 2026-07-21

A hotfix release for the `native` runtime, where Claude sessions could become
completely unable to accept input, plus much fuller documentation in the
distribution repo.

## [0.1.1](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.1) — 2026-07-21

A documentation and packaging follow-up to the first release, plus one Console
change.

## [0.1.0](https://github.com/k-k1/agent-fleet-dist/releases/tag/v0.1.0) — 2026-07-21

First public release. Agent Fleet is a self-hosted console for running AI coding
agents as a fleet: each member gets an isolated per-user environment, and starts,
drives and manages agent sessions from the browser.
