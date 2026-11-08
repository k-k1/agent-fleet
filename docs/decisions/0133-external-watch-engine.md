# 0133. One watch engine for external state: subscriptions, source adapters, push and poll behind one interface

English | [日本語](0133-external-watch-engine.ja.md)

- Status: **proposed** (2026-11-08). The user asked for the abstraction; nothing is built yet.
- Related: [0021](0021-scheduled-execution.md) (scheduled execution) / [0061](0061-work-item-inbox.md) (work item inbox)

## Context

Sessions increasingly wait on things outside the workspace: a PR's CI finishing, a PR going into conflict, a merge,
a ticket moving, a deployment settling. Today each of these is watched in one of two ways, and both are poor.

**1. Each agent polls by hand.** An agent that needs to know when CI finishes runs a one-liner in the background
(`gh pr checks <N> --watch --interval 60`) and waits for the shell to exit. Observed on 2026-11-08 in one issue round
(PRs #3589, #3590, #3595, #3599, #3600, #3605, #3606): the parent session ran such a watcher for every PR, a red run had
to be diagnosed and re-armed by hand, and a conflict was found only because the parent ran `git merge-tree` itself.
The watcher dies with the shell, cannot wake a stopped session, knows only one provider, and every session that cares
about the same PR polls it again.

**2. Each Agent feature carries its own watcher.** The workspace Agent already has several, each with its own
schedule, de-duplication and delivery:

| Feature | What it watches | How |
|---|---|---|
| `internal/branchpr` | GitHub (GraphQL, batched) and Bitbucket PR state | poll; open PRs every 2 min (`openTTL`), settled ones every 10 min, backoff 5 / 15 min |
| PR feedback watch (#3129, `sessionx/pr_feedback.go`) | CI failure and requested changes on a session's own PR | reads the `branchpr` cache; injects a notice into the owning session; off by default |
| PR notices (#3131, `sessionx/session_pr_notices.go`) | ready to merge, merged, closed | reads the same cache; writes the notification outbox for the member |
| Stalled-tree watch (#2397, `sessionx/stalled_tree.go`) | a parent and all its children idle | its own loop; injects a notice |
| Work item inbox (ADR 0061, `internal/workitems`) | GitHub / Bitbucket / Jira items for the member | its own queries and caches |

None of them can be asked for by a session ("tell me when #3606's checks finish"), none tells a session about success
or a conflict (#3129 tells only failures, and tells the member about conflicts), and none can be reached by a parent
that does not own the PR. Each new provider or condition so far has meant another loop, another store and another
once-only marker.

More watches are coming. Besides CI and PRs, the user named deployments; other candidates are listed in §9.

## Decision

Build **one watch engine** with a stable, provider-neutral interface, and put every provider behind an adapter.
Sessions subscribe through one MCP tool; the engine schedules, de-duplicates and delivers; adapters only know how to
read or receive one kind of external state.

### 1. Layers

```
 session / member  <-- Sink (notice into a session, notification centre, forward to a parent)
        ^
   Watch engine (shared): subscriptions, authorisation, scheduling, rate limits and backoff,
                          de-duplication, expiry, persistence across restarts
        ^  normalised Event
   Source adapters (per provider): github, bitbucket, jira, aws-cfn, http, ...
        ^                   ^
   Poll (Agent side)    Webhook (Control Plane side)
```

### 2. Interfaces (shape, not final names)

```go
// One per provider. Capabilities says what it can do; the engine combines them.
type Source interface {
    Kind() string                // "github", "jira", "aws-cfn", ...
    Capabilities() Caps          // Poll, Webhook, conditional fetch (ETag), batching
    // Resolve normalises a reference ("k-k1/agent-fleet#3606") and checks that the subscriber's
    // connection may read it. It is the only place an adapter decides access.
    Resolve(ctx context.Context, who Principal, ref string) (Subject, error)
}

type Poller interface {
    // cursors carry the last ETag / updated_at per subject; an unchanged subject returns no event.
    // Hint tells the engine when this subject is worth asking about again.
    Poll(ctx context.Context, subs []Subject, cursors map[SubjectID]Cursor) ([]Event, map[SubjectID]Cursor, map[SubjectID]Hint, error)
}

type WebhookParser interface { // Control Plane side
    Verify(r *http.Request, secret []byte) error
    Parse(r *http.Request) ([]Event, error)
}

type Event struct {
    Source  string          // "github"
    Subject SubjectID       // "github:k-k1/agent-fleet#3606"
    Type    string          // shared vocabulary, §3: "pr.checks.completed"
    Version string          // de-duplication key: head SHA, updated_at, delivery ID
    Summary string          // one line a session can act on
    Data    json.RawMessage // provider detail (failed jobs, URLs)
    At      time.Time
}

type Subscription struct {
    Owner   SessionRef      // who asked
    Subject SubjectID
    Types   []string        // patterns: "pr.checks.*"
    Until   UntilRule       // first match | subject closed | deadline
    Notify  SinkRef         // self | parent | member
}
```

The adapter knows the provider; the engine knows time. Polling intervals, backoff after errors or rate limits, and the
choice between push and poll belong to the engine, guided by the adapter's `Hint` (for example "checks running: ask
again in 15 s", "settled: 10 min", "rate limited until T").

### 3. A shared event vocabulary

Event types are namespaced and shared across providers, so a session subscribes to `pr.checks.completed` whether the
PR is on GitHub or Bitbucket. The first set: `pr.checks.completed` (with the overall result and the failed checks),
`pr.checks.failed`, `pr.conflict`, `pr.mergeable`, `pr.merged`, `pr.closed`, `pr.review.changes_requested`,
`issue.transitioned`, `deploy.completed`, `deploy.failed`. A provider-specific condition gets a provider-prefixed type
(`github.workflow_run.completed`) until a second provider needs it. Adding a type is a code review decision, not
something an adapter does on its own.

### 4. Where it runs

- **Polling runs in the workspace Agent.** The credentials (Settings > Connections), the sessions and the existing
  `branchpr` cache are there, so a poll costs no new hop.
- **Webhooks are received by the Control Plane**, verified and parsed into the same `Event`, and forwarded to the
  workspaces that hold a subscription for that subject. A webhook only shortens the wait: the engine still polls a
  subscribed subject at a long interval, because webhooks can be lost.
- A session sees the same `Event` either way and cannot tell, and need not care, which path delivered it.

### 5. Delivery

- **Into a session**: a notice in the same form as the stalled-tree and PR feedback ones
  (`[agent-fleet:notice kind=watch source=github type=pr.checks.completed subject=…]` and the one-line summary),
  injected only when the session is idle and can take input. A stopped session is started and told once ready, one at a
  time, as #3129 already does. An archived session's subscriptions end.
- **To a parent**: a child may subscribe with `Notify: parent`, so an issue round's parent hears about its children's
  PRs without owning them.
- **To the member**: the notification outbox (`internal/notice`), as #3131 does today.
- **Once**: an event is delivered once per subscription and `Version`, recorded on disk, so a restart repeats nothing.
- **While the workspace is stopped** nothing polls, because the engine lives in the Agent. Subscriptions stay on disk;
  when the workspace starts, the engine polls every live subscription once before it returns to its schedule, and
  delivers only the latest state per subject. Webhook events for a stopped workspace are held by the Control Plane
  (newest per subject, bounded) and handed over at start; they never wake the workspace. Anything that must act while
  the workspace is stopped is an ADR 0021 schedule (§7).

### 6. Rules the engine enforces

1. **Authorisation at subscribe time and at every delivery**: `Resolve` checks that the subscriber's own connection
   can read the subject. A subject in another tenant, or one the member's token cannot see, is refused. A webhook
   event is forwarded only to workspaces whose subscriptions were authorised that way.
2. **Limits**: at most 20 live subscriptions per session and 200 per workspace (`watch` refuses the next one and
   says which limit was hit). The engine's own requests may use at most 10% of a connection's hourly request budget
   (one token is one GitHub rate limit, whoever uses it); replies that do not count against the provider's limit,
   such as GitHub's `304`, do not count against this budget either. All three numbers are settings with these
   defaults.
3. **Expiry**: every subscription has a deadline, 7 days by default and at most 30 days, and ends with its owner.
4. **Fail quietly, not wrongly**: an adapter error backs off and is visible in `list_watches`; it never produces an
   event that did not happen.
5. **Provider text is data, not instructions.** A notice is read by an agent, and titles, comments, job logs and
   anything an external source (§8) returns can be written by someone other than the member. `Summary` is composed
   by the adapter from fields it controls (state, result, counts, numbers, the subject's reference); free text goes
   only into `Data`, is cut to a fixed length, and the notice marks it as untrusted data.

### 7. Time, and how it relates to ADR 0021

The engine's `time` source holds timers that live inside a running workspace ("in 20 minutes", "at 15:00") and fires
`time.fired` through the same sinks as any other event. It **feeds** the ADR 0021 scheduler and does not replace it:
only the Control Plane is alive while a workspace is stopped, and the 0021 scheduler already wakes workspaces and
drives sessions there.

- A time watch asked for with `wake` is not kept by the engine: the Agent registers it as an ADR 0021 schedule through
  the existing schedule bridge, and its delivery comes back through the watch sinks.
- A time watch without `wake` that falls due while the workspace is stopped is delivered at the next start, marked
  late, or dropped when its subscription expired in the meantime.
- The 0021 scheduler stays the only component that wakes a workspace.

### 8. Sources a member adds

A member may register an MCP server from Settings > Connections > MCP servers as a watch source. The feature is off
for a tenant until an administrator enables it.

- **Contract**: the server exposes two tools with the shape of §2: `watch_resolve(ref)` returns a subject or a
  refusal, and `watch_poll(subjects, cursors)` returns events, cursors and hints. External sources poll only; the
  Control Plane receives no webhooks for them.
- **Caller**: the engine in the Agent calls these tools, never a session. The server receives subject references and
  its own cursors, nothing else: no session content, and no credentials other than those of its own MCP server
  connection.
- **Namespace**: its event types must be `ext.<source>.*` and its subjects `ext:<source>:…`. Anything else is dropped
  and counted as an error, so an external source cannot pose as `pr.merged` or speak about another source's subject.
- **Reach**: its events go only to subscriptions owned by the registering member's sessions (self, parent) or to that
  member. They are never forwarded to the Control Plane, another member or another workspace.
- **Output**: `Summary` is capped at 200 characters and `Data` at 8 KiB, and both are treated as provider free text
  under rule 6.5.
- **Budget**: a call times out after 10 s; a subject is polled at most once a minute whatever the `Hint` says; one call
  carries at most 50 subjects and returns at most 20 events. Five consecutive failures pause the source; the pause is
  shown in `list_watches` and lifted by the member.
- **Isolation**: a local (stdio) server runs as a child of the Agent with the same user and limits as the MCP servers
  sessions start, so the workspace container is its sandbox, as it is for every process the member runs; being a
  source gives it no extra privilege. A remote server is reached the way sessions reach it.

### 9. Sources this is meant to absorb

| Area | Examples |
|---|---|
| Code hosting | GitHub, Bitbucket, later GitLab: checks, conflicts, merge, review, a new commit on the base branch |
| Trackers | Jira, GitHub / Bitbucket issues: transitions, assignment, a new comment that mentions the session's item |
| CI on its own | GitHub Actions / Bitbucket Pipelines runs not tied to a PR (nightly, release workflows), a release tag published |
| Deployments | CloudFormation stack status, ECS service steady state, a Kubernetes rollout, a container image becoming available in a registry |
| Agent Fleet itself | a peer session going idle or finishing, a child's turn ending, quota windows reopening (`limitepoch`), a long background command exiting |
| Endpoints | an HTTP health check turning green after a deploy, a certificate nearing expiry |
| Time | "at 03:00" and "in 20 minutes" as a time source that feeds scheduled execution (ADR 0021) rather than replacing it (§7) |
| Security feeds | a new advisory for a dependency, a new finding in the published images' vulnerability scan (#3339) |

The table is a direction, not a commitment; each source is its own change with its own review.

### 10. Steps

1. **Engine + `github` source + MCP tools** (`watch`, `unwatch`, `list_watches`). The `github` poller adds REST with
   conditional requests (`If-None-Match`; a `304` does not count against GitHub's primary rate limit) and adaptive
   intervals for subscribed PRs, next to the batched GraphQL that `branchpr` uses for the session list.
2. **`bitbucket` source** (Bitbucket has no ETag: adaptive intervals only).
3. **Webhook ingress on the Control Plane** (GitHub App first; repository webhooks where an App cannot be installed;
   Bitbucket repository webhooks).
4. **Move #3129 and #3131 onto the engine** as built-in subscriptions, keeping their member-facing switches.
5. **Sources a member adds** (§8), behind the tenant switch.
6. **The `time` source** (§7), then further sources from §9 one at a time.

## Rejected

- **A dedicated `watch_pr` tool and a faster `branchpr`.** Solves the PR case quickly but adds the sixth hand-rolled
  watcher; the next provider repeats the work.
- **Webhooks only.** Needs an App or hook-admin rights in every repository and a Control Plane reachable from the
  internet; a lost delivery is a silent miss. Kept as the fast path, not the only path.
- **Polling from the Control Plane.** The member's credentials and the sessions live in the workspace; polling from
  the Control Plane would mean holding member tokens centrally for a feature that does not need them there.
- **Replacing the ADR 0021 scheduler with the `time` source.** The engine runs in the Agent, which is not alive while
  a workspace is stopped; replacing the scheduler would mean moving the engine, or a copy of it, onto the Control
  Plane, and rewriting a scheduler that already works.
- **Built-in sources only.** Every niche system (an in-house deploy tool, a team's status page) would need a change to
  Agent Fleet itself. The namespace, reach and budget rules of §8 keep a member's source from affecting anyone else.
- **Letting adapters invent event types.** Leads to one name per provider for the same condition, and to sessions that
  must know which provider they are talking to.

## Consequences

- Skills that run `gh pr checks --watch` in the background (issue-rounds, issue-to-pr) can replace it with one
  `watch` call once step 1 ships.
- The engine becomes a shared dependency of several Agent features; its store and scheduler need their own tests and
  measurements (requests per hour per connection, delivery delay).
- Webhook ingress adds a public, unauthenticated-by-session endpoint to the Control Plane, guarded by signature checks;
  it gets its own security review in step 3.
- Sources a member adds put member-chosen code in the engine's path. §8 confines what they can emit and to whom; step 5
  gets its own security review, like step 3.

## Open questions

None left. The three raised in the draft are answered in §6 (limits and expiry), §7 (time and ADR 0021) and §8
(sources a member adds).
