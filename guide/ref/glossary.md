---
audience: "everyone, and especially anyone translating between a screen and the code"
source_of_truth: "the Console's own strings for the screen column; the code for the implementation column"
updated: "2026-09"
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
| Inference engine | engine / role (`llm`, `image`) | A model server the deployment runs, points at, or borrows — one for chat, one for images. What the top-bar pills report on |
| Engine row | engine row / `lifecycle` | One declared engine: the deployment's own GPU, a ComfyUI on your network (external), one borrowed from another deployment (remote), or any OpenAI-compatible image server. Which row draws a picture is decided by the row, not by the server's type |
| Borrowed engine | remote engine | An engine another deployment runs and lends through its gateway. Its catalogue is a read-only copy here; starting, stopping and editing happen over there |
| Model catalogue | catalog | The checkpoints, LoRAs and VAEs the engines can load, one row per model, one catalogue per deployment |
| Family | family / `base_model` | The checkpoint's lineage — SD 1.5, SDXL, SD 3.5, FLUX.1, FLUX.2 klein, Z-Image, Anima, Krea 2. It decides the workflow, the default size and which settings are read. The image-generation pane's card for it |
| Plan card | ingest plan / `plan_token` | The quote shown before a model is taken in: every file it needs, what each costs (a download in MiB, or nothing when the bytes are already held), the licence and the warnings. Not the chat plan card a session asks you to approve |
| S3 Bucket | ledger / object store | The tab ("Bucket") that lists what the deployment's storage actually holds — including objects no catalogue row declares — and lets you register, move or delete them |
| Image generation (pane) | imagegen studio | The pane that makes pictures on the deployment's ComfyUI without an agent: trial run, batches, seeds, LoRAs |
| Image gallery | gallery pane | A folder's pictures as cards, with folders, covers, counts and an enlarged view |
| Fleet graph | fleetgraph pane | One lane per session, time running left to right: which session started which, what passed between them, and when each was working, waiting or idle. The elapsed counterpart of the sessions overview |
| LoRA | LoRA adapter | A small add-on trained against one family that steers a checkpoint's style or subject. Listed only for the family it matches |
| Trigger words | trigger words | The words a LoRA needs in the prompt to do anything. Shown on its row before you pick it, and added as chips when you do |
