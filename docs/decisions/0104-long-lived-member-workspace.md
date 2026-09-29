# 0104. A member's workspace is long-lived — a persistent home, many concurrent sessions and git worktrees inside it — not an environment per task

English | [日本語](0104-long-lived-member-workspace.ja.md)

- Status: accepted — recorded retroactively (2026-09); the decision dates from Phase 0–1. Nobody
  wrote it down when it was made. The shape came from the personal setup the product generalised,
  and every phase since has built on it. This record reconstructs the reasons from the ADRs,
  journals, build documents and measurements that depend on it. Each reason names its source.
  **Where the record gives no reason, this ADR says so** instead of supplying one.
- Related: [0001](0001-self-host-vs-saas.md) (bring-your-own seats) /
  [0002](0002-claude-auth-onboarding.md) (the CLI's own login) /
  [0044](0044-workspace-sizing.md) and [0045](0045-ec2-persistent-workspace.md) (what a persistent
  home costs and measures on AWS) / [0055](0055-idle-stop-and-carried-interactions.md) (idle-stop) /
  [0087](0087-efs-metadata-io.md) (metadata I/O of the persistent state) /
  [0036](0036-working-sets.md), [0041](0041-cross-session-messaging.md),
  [0057](0057-member-handoff.md), [0073](0073-session-spawned-sessions.md),
  [0097](0097-session-retention.md) and [0103](0103-branch-naming-rules.md) (built on one shared
  workspace)

## Context

Readers who know task-per-pod designs ask this first: why does each member get one long-lived
workspace, rather than a fresh container per task that is discarded when the task ends? The
developer documentation answers in one row ("User isolation: one container per user",
[build/00](../build/00-project-context.md)), and no decision record explains it.

**Where the shape came from.** The product is a personal fleet setup, generalised
([build/00](../build/00-project-context.md), "What this was built out of"). That setup was
`scripts/tmux-claude.sh`, which started, resumed and generation-managed several Claude CLIs in
detached tmux on one machine, plus a separate `CLAUDE_CONFIG_DIR` per profile. The phases then fixed
the shape without weighing an alternative:

- **Phase 0** ([log/phase0-poc](../log/phase0-poc.md)) tested whether `claude /login` works headless.
  Two of its hypotheses are this decision in small. H3: the credentials persist in the bind-mounted
  home, and claude runs **after the container is recreated without logging in again**. H6: a claude
  in tmux can be detached and **brought back with `--resume`**. The journal records a result for H1–H3
  only: they passed (2026-06-26). H6 stays an unchecked hypothesis with its procedure, and the
  Phase 1 results (§11.10) do not name `--resume` either.
- **Phase 1** ([log/phase1-plan](../log/phase1-plan.md) §11.1, §11.4) built the container for one
  user, with `./data/home/<user>` bind-mounted as `~`. It was still a single workspace (§11.10).
  The Agent inside it lists, starts and stops **several** `claude_*` tmux sessions.
- **Phase 2** made the workspace per user (the roadmap, `roadmap.md`). Its §12.1 later made it per
  membership: one workspace per identity × tenant.

None of these records considers a per-task environment. No measurement compares the two either.

**What "long-lived" means here.** It is the state that lives long, not the machine:

- The home persists across stop and start. A recreate deletes only `~/repos` and keeps the rest
  ([workspace-notes](../../workspace/workspace-notes.md), "What survives").
- The CLI state behind `CLAUDE_CONFIG_DIR` sits on a separate mount that nothing deletes.
- The compute is stopped when idle ([log/p3-9-idle-stop](../log/p3-9-idle-stop.md); ADR 0055).
  On `ecs-ec2` it is not even the same machine each time: a pool of generic slots swaps the
  member's EBS volume in (ADR 0045 decisions 8 and 10).

## Decision

Each membership gets **one workspace**, and it keeps these four properties.

### Decision 1: a persistent home holds the member's state

The home holds the member's CLI logins and settings, tools installed under `~/.local`, package
caches, clones, and each CLI's own conversation store. The logins are kept even where the clones are
not:

- a recreate deletes `~/repos` and keeps the rest;
- where the home is on a per-user disk, the credentials are linked in from storage that is always
  available, so "losing the home disk never costs you your logins"
  ([workspace-notes](../../workspace/workspace-notes.md); the persistence model in
  `workspace/notes/environment.md`; ADR 0087's table of the three volumes).

### Decision 2: many sessions run at once in that one workspace

A session is a slot binding a conversation, a working directory, settings and execution state
([build/04](../build/04-agent.md) §4.2). A member runs many at once, in one container. **The
container boundary separates members, not a member's own sessions.** It is "the only wall"
([build/07](../build/07-security.md) §7.1), and a shell in it runs as the same uid as every session
(§7.2).

### Decision 3: sessions work in git worktrees of clones in that home

A session usually works in its own worktree of a clone under `~/repos`
([build/93](../build/93-worktree-deps.md)). The worktrees share the parent clone's object store and
config.

### Decision 4: the compute is not long-lived

The CP's reaper halts idle sessions and stops idle workspaces
([build/03](../build/03-control-plane.md); by default after 1 hour and 2 hours,
`control-plane/main.go`). A halted session is "resumable, because the transcript is on disk", and
the next access starts the workspace again from its home.
Build output that is cheap to regenerate may live on a disk that dies with the stop: `/scratch`
(ADR 0044 decisions 3 and 5).

## Why — the reasons the record gives

### Reason 1: the credential is the CLI's own login, and the CLI writes it into the home

- **Seats are the member's own.** ADR 0001 kept Claude bring-your-own ("each person runs
  `/login`").
- **The CLI writes the credential into its config directory.** ADR 0002's decision is
  `claude auth login --claudeai`: claude itself writes a `.credentials.json` with a refresh token.
  That is what keeps the interactive TUI authenticated with its subscription features.
- **The injection routes were tried and failed** (ADR 0002, "The chain of corrections";
  [build/08](../build/08-integrations.md) §8.5):
  - a setup token in an environment variable is read by `claude -p` only;
  - a synthesised `.credentials.json` without a refresh token is rejected by the TUI;
  - the API-key variable is billed as API usage and can disable subscription features.
- **codex and opencode keep theirs in the home too**: codex in its own directory, opencode in the
  encrypted store ([build/04](../build/04-agent.md) §4.3, "Credential location"). That table covers
  these three kinds only; this ADR does not claim the same for the others.
- **A copy of the credential goes stale when it rotates.** ADR 0087 found that a credential rotated
  while borrowed stays on the old volume. "With a provider that retires used refresh tokens, the
  user is asked to sign in again." ADR 0095's probes kept the real `XDG_CONFIG_HOME` for the same
  reason: a copied `auth.json` "risks rotating the member's refresh token".

So the credential is state that one directory owns and that has to outlive any one run. Phase 0 made
exactly that (H3) an exit criterion.

### Reason 2: a conversation lives in the CLI's local store, and resuming it reads that store

- **The CLI's own store is the truth**, for the three kinds [build/04](../build/04-agent.md) §4.3
  tabulates: claude and codex keep their own JSONL, opencode its own database. For the managed
  boundary, §4.3 adds that "the conversation body is never copied into a store of our own"
  (ADR 0015).
- **Resuming needs the store and the working directory.** A resumed session reads the store, and
  build/04 states that the agent kinds "cannot resume if the working directory is gone" (§4.2).
  Phase 0's H6 hypothesised this property; the record of it working is the production behaviour
  build/04 describes, not a Phase 0 result.
- **Halting is only cheap because of this.** The idle-stop plan calls halting a claude session
  "effectively hibernate": the conversation is kept in JSONL and `claude --resume` brings it back
  with its context ([log/p3-9-idle-stop](../log/p3-9-idle-stop.md) §19.1). The same plan states
  the invariant: only the infrastructure is stopped; the conversation, the home and the credentials
  persist and are restored on the next access.
- **The features downstream assume this.** ADR 0055 carries a pending interaction over a stop. When
  a stopped session's TTL expires, ADR 0097 archives it instead of deleting it automatically.

### Reason 3: a fresh environment rebuilds what the home keeps warm

This reason stands on weaker ground than reasons 1 and 2, and its parts are of different kinds.
**The start times below are measurements**, but none of them compares a fresh environment with a
persistent one. **The rebuild cost is not measured at all**: the record names it as a benefit and a
concern, never with a number.

**Start time: measured, but it does not separate the two options.** ADR 0045 first quoted 22–27 s
for a hot slot swapping the member's volume in (decision 8). That figure was taken without Service
Connect, which the product uses. Decision 10-5 corrected it to 13–95 s, decision 12 re-measured
43–110 s, and decision 22 gives 43.2 s for a hot free slot. Decision 12 then removed startup time
from the reasons for the EC2 shape: against Fargate's ~105 s warm restart there is almost no
advantage, because ECS placement and container start take most of the time. That compares two
persistent shapes, not a fresh environment with a persistent one. The one fresh-home figure, 144.0 s
for a new user with an empty home on a cold slot (decision 19), differs from the others in the slot's
state as well as in the home. **So the record has no like-for-like start-time comparison, and this
ADR uses start time as a reason neither way.**

**Rebuild cost: recorded as a benefit and a concern, not measured.**

- ADR 0045 decision 2 listed "no regeneration each morning" among what the EC2 shape still offered
  once build output had moved to a disk that dies with the stop (`/scratch`, ADR 0044 decisions 3
  and 5). That is a recorded benefit, not a measured time.
- Decision 5 made regeneration each morning (`npm ci` plus the first build) exceeding 5 minutes one
  of its gates. Decision 5 also states that none of its five gates could be shown by measurement,
  and decision 10 went ahead without meeting them. The 5 minutes is a threshold nobody measured
  against.
- By inference, not measurement: a single-shot environment per task would repeat that rebuild for
  every task that needs its dependencies, and without the warm caches of the next bullet.
- **Warm caches make the Nth copy cheap.** [build/93](../build/93-worktree-deps.md) rests on
  "reinstalling is cheap and duplicating is expensive", because the caches in the home survive. A
  worktree's submodule seeded from the parent clone took 0.23 s for 41 MB with the remote offline,
  and 168 KB of disk ([build/04](../build/04-agent.md) §4.6).

### Reason 4: the setup it generalised was already one place running many sessions

This is a reason of history, not an argument. The product generalised a tmux fleet of several CLIs
on one machine, and the Phase 1 Agent contract kept that shape
([log/phase1-plan](../log/phase1-plan.md) §11.4). The record does not say that anyone chose it
over something else.

### What the record does not give a reason for

- **Why a member's sessions share one isolation boundary.** [build/07](../build/07-security.md)
  §7.1–7.2 states the limit (one wall, one uid) and accepts it. No document weighs it against
  isolating a member's tasks from each other.
- **Why worktrees rather than separate clones or separate environments.** The record measures what
  worktrees share ([build/93](../build/93-worktree-deps.md), [build/04](../build/04-agent.md) §4.6)
  and writes rules for sharing them ([workspace-notes](../../workspace/workspace-notes.md)). It
  never states the choice.

## Rejected: an environment per task

The option weighed here is a **single-shot** container (or pod) per task, built from the image and a
fresh checkout, **with no store shared across tasks**, and discarded when the task ends. **No record
weighs it.** The reasons below are reconstructed from what the recorded decisions depend on. They are
not a rejection anyone wrote at the time.

- **Every task would need the member's login inside it.** The routes that inject a credential do not
  work for the interactive TUI (reason 1). Copying the config directory into each task and throwing
  it away would expose each task to the mechanism ADR 0087 found: a rotation made while the copy was
  in use stays in the copy.
- **The conversation would be discarded with the environment.** Resume, fork, halting for idle-stop
  and carried interactions all read the CLI's local store and working directory (reason 2). Keeping
  them means carrying the store and the working copy out and back in, which is a persistent home
  under another name.
- **Every task that needs its dependencies would rebuild them, and its caches, from cold** (reason
  3; an inference, since the rebuild cost is unmeasured). Start time is not counted against it: the
  record has no like-for-like start-time comparison (reason 3).
- **The features built since assume one home**, and each would need a layer across environments:
  - cross-session messaging (ADR 0041; decision 12 names one workspace as the boundary);
  - sessions spawned by sessions (ADR 0073);
  - working sets (ADR 0036);
  - branch naming from the parent clone (ADR 0103);
  - member handoff (ADR 0057). Its decision 1 is itself the reason in reverse: a session cannot move
    to another member because "the transcript, the worktree, the uncommitted changes and the agent
    CLI's OAuth all live in A's home".

What a per-task environment would have given, and this choice gives up:

- isolation between a member's own tasks;
- no need for worktree discipline;
- a clean state per task;
- nothing left behind to retain or clean up.

**A middle option was not weighed and is not measured:** a container per task that mounts the
member's persistent store (credentials, CLI state, clones and caches) and starts on a warm slot.
It keeps the home and changes only the unit of compute. Reasons 1–3 do not argue against it. The
one thing the record says that bears on it: the features in the fourth bullet above assume a
member's sessions share one container (ADR 0041 decision 12 makes one workspace the boundary).
Nothing was measured against this option.

## Consequences — the costs of the choice

- **Idle-stop has to work, or the bill grows.** With bring-your-own seats, "the operator's cost is
  occupancy, not tokens" ([build/03](../build/03-control-plane.md)).
  - [build/09](../build/09-deploy.md) §9.8.2 estimates from list prices (not from a bill): a
    workspace is about $0.049/hour, so weekdays at 8 hours is $8.7 per person a month and around the
    clock is $36. "That second row is also the bill when scale-to-zero breaks."
  - With idle-stop's deployment defaults at 0 (off), a slot (m7i.large) stayed running 9.4 hours
    overnight because nothing told it to stop. The defaults became 1 hour and 2 hours after that
    ([log/64](../log/64-ec2-persistent-workspace.md) §64.26). ADR 0055 closed another route to the
    same bill: a question left on screen kept a workspace from ever stopping. Its decision 1 lets
    only a working machine keep a container awake.
  - On a single VM, idle-stop saves memory, not money (§9.8.1). There, the risk is that one host's
    RAM runs out and the OOM killer takes down the live fleet
    ([log/p3-9-idle-stop](../log/p3-9-idle-stop.md)).
- **A member's sessions share one container's CPU, memory and disk.**
  - Memory: a live claude session measured 340–435 MB RSS. From that, ADR 0073 estimates that a
    10 GiB cgroup holds roughly 23; its own amendment notes that the RSS sum overestimates. ADR 0073
    caps children per parent well below that, because nothing bounds the number of parents.
  - Builds: the workspace policy's build-memory rules exist because builds sharing one container
    "caused real incidents" ([workspace-notes](../../workspace/workspace-notes.md)).
  - Disk: in this repository, `node_modules` measured 349 MB per worktree unless it is shared
    ([build/93](../build/93-worktree-deps.md)).
  - Shared storage: on `ecs-ec2` the credentials and the CLI state (`CLAUDE_CONFIG_DIR`) are on one
    EFS file system that every workspace uses. When its burst credits ran out on 2026-09-16, the
    whole deployment slowed down; ADR 0087 then measured its cost per session and per open tab.
- **Worktrees in one container need discipline.** The policy every in-container agent reads
  exists because of this ([workspace-notes](../../workspace/workspace-notes.md), "You share one
  container with your other sessions"):
  - never kill by pattern;
  - ports are shared;
  - never check out or fast-forward the parent clone or another session's working copy;
  - the stash stack is shared;
  - a shared `node_modules` link can be emptied for everyone.

  The code guards the same things:
  - ADR 0073 decision 7 refuses `worktree=false` on another live session's directory;
  - ADR 0103 decision 9 serialises writes to the config that every worktree reads;
  - [build/04](../build/04-agent.md) §4.6 passes a submodule URL override on the command line,
    because the config file is shared with every sibling worktree.
- **State accumulates and has to be managed.** Stopped sessions are retained and archived (ADR
  0097), deletes go through a trash (ADR 0101), and throwaway files are given their own place (the
  home-clutter journal, [log/116](../log/116-home-clutter-and-cache-growth.md)). A recreate deletes
  `~/repos` with any uncommitted work in it.
- **On AWS, persistence widens the operational surface.** One workspace went from "a service plus
  an EFS access point" to six kinds of resource (ADR 0045 decision 2, reason 3, which decision 10
  kept as the price).

## What would make us revisit it

- **The CLIs stop keeping their state in the home.** That would take an interactive TUI that
  accepts an injected, refreshable credential without losing subscription features — none of ADR
  0002's attempts got there — and conversations that resume from somewhere other than the local store.
  Reasons 1 and 2 would then weaken.
- **A fresh environment gets its dependency trees and caches as cheaply as the home does now**,
  for example from a shared or pre-seeded cache. Reason 3 would then weaken.
- **A tenant needs a member's own tasks isolated from each other**, for example to run untrusted
  repositories side by side. Today the container is the only wall (build/07 §7.1).
- **Idle-stop cannot be made reliable on some runtime.** There, this shape costs the around-the-clock
  row of build/09 §9.8.2.
- **Contention inside one workspace becomes routine**, with a member's sessions regularly exceeding
  what one container holds (ADR 0073's estimate; ADR 0087's extrapolation to ten sessions per
  user).
