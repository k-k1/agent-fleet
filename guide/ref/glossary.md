---
audience: "everyone, and especially anyone translating between a screen and the code"
source_of_truth: "the Console's own strings for the screen column; the code for the implementation column"
updated: "2026-08"
---

# Glossary

English | [日本語](glossary.ja.md)

Two columns on purpose. The reader-facing shelves may use only the **screen** word;
`docs/build/` uses the **implementation** word. Keeping the mapping in
one place is what lets a support conversation and a stack trace be about the same
thing.

| Screen | Implementation | Means |
|---|---|---|
| Workspace | container / task | One person's private environment: their repositories, work in progress and sessions. Runs as a dedicated container, or as sandboxed host processes on the Docker-less target |
| Workspace action bar | — | The strip that starts and stops the workspace and holds Start, Preview and pane splitting |
| Session | session | One task's conversation, working location and execution state. **It does not imply a terminal** |
| Execution method | driver | How Agent Fleet runs an agent and delivers instructions to it |
| Managed | paneless / managed driver | Runs on a shared runtime, operated from the chat view. No terminal |
| Terminal (CLI) | tmux pane / PTY | You drive the agent's own interactive screen in a terminal |
| Agent | kind | A CLI coding AI: claude, codex, opencode and the rest. See [agents.md](agents.md) |
| Assistant | assistant chat | A purpose-specific chat that uses no repository. Not a session |
| Mirror | transcript / mirror | The rendered view of a running or stopped conversation |
| Working copy | working copy / dir | The folder of a repository inside the workspace that you actually edit |
| worktree | git worktree | An independent working copy of the same repository, so parallel work does not collide |
| Parent | parent clone | The working copy a worktree was made from, and what status displays compare against |
| Pane | pane | One subdivision of the main area |
| Working set | working set | A named grouping of repositories, conversations, sessions and schedules that narrows the left pane. **It moves and copies nothing** |
| Shared session | session share | Showing a conversation read-only to another member of the same tenant |
| Handoff | handoff | Passing a conversation to a new session, or to another member |
| Fork | fork at message | Starting a new session from a past point in an existing conversation |
| Work item | work item | An issue, ticket or pull request pulled in from a provider. See [repos.md](repos.md) |
| Memo queue | memo | Instructions parked now and sent to a session later, in a batch |
| Cleanup / trash | cleanup / shelf | The sweep of stopped sessions, stale worktrees and merged branches. What it removes is stashed and can be restored |
| Browser pane | browser pane | The workspace's own Chromium rendering `127.0.0.1:{port}` into a pane. See [browser-pane.md](browser-pane.md) |
| Lightweight preview | preview proxy | The same service opened in a new tab under a `/preview/{port}/` sub-path. WebSocket and SSE pass through, but an app emitting absolute-path assets breaks |
| Preview subdomain | preview subdomain | A `https://<random>-<port>.<domain>/` URL issued every time the workspace starts. The app is served at the root and several ports are open at once. Not issued on every deployment |
| Shared preview | shared preview | A preview a member of your tenant turned "Show it to your tenant" on for. It opens after you sign in, and not while their workspace is stopped |
| Connection | connection / secrets | A credential you attached from the Console — an agent, a git provider, a tracker |
| Tenant | tenant | One team or department. Members of different tenants are invisible to each other |
| Slot | slot | On the EC2 target, one pooled instance a workspace can be placed on |
| Deployment | deployment | One installation of Agent Fleet. One company runs one |
