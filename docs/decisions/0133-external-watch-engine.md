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

More watches are coming. Besides CI and PRs, the user named deployments; other candidates are listed in §7.

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

### 6. Rules the engine enforces

1. **Authorisation at subscribe time and at every delivery**: `Resolve` checks that the subscriber's own connection
   can read the subject. A subject in another tenant, or one the member's token cannot see, is refused. A webhook
   event is forwarded only to workspaces whose subscriptions were authorised that way.
2. **Limits**: subscriptions per session and per workspace, and request budgets per connection (one token is one
   GitHub rate limit, whoever uses it).
3. **Expiry**: every subscription has a deadline (default 7 days) and ends with its owner.
4. **Fail quietly, not wrongly**: an adapter error backs off and is visible in `list_watches`; it never produces an
   event that did not happen.

### 7. Sources this is meant to absorb

| Area | Examples |
|---|---|
| Code hosting | GitHub, Bitbucket, later GitLab: checks, conflicts, merge, review, a new commit on the base branch |
| Trackers | Jira, GitHub / Bitbucket issues: transitions, assignment, a new comment that mentions the session's item |
| CI on its own | GitHub Actions / Bitbucket Pipelines runs not tied to a PR (nightly, release workflows), a release tag published |
| Deployments | CloudFormation stack status, ECS service steady state, a Kubernetes rollout, a container image becoming available in a registry |
| Agent Fleet itself | a peer session going idle or finishing, a child's turn ending, quota windows reopening (`limitepoch`), a long background command exiting |
| Endpoints | an HTTP health check turning green after a deploy, a certificate nearing expiry |
| Time | "at 03:00" and "in 20 minutes" as a time source, which lets scheduled execution (ADR 0021) and "remind me" share the same delivery |
| Security feeds | a new advisory for a dependency, a new finding in the published images' vulnerability scan (#3339) |

The table is a direction, not a commitment; each source is its own change with its own review.

### 8. Steps

1. **Engine + `github` source + MCP tools** (`watch`, `unwatch`, `list_watches`). The `github` poller adds REST with
   conditional requests (`If-None-Match`; a `304` does not count against GitHub's primary rate limit) and adaptive
   intervals for subscribed PRs, next to the batched GraphQL that `branchpr` uses for the session list.
2. **`bitbucket` source** (Bitbucket has no ETag: adaptive intervals only).
3. **Webhook ingress on the Control Plane** (GitHub App first; repository webhooks where an App cannot be installed;
   Bitbucket repository webhooks).
4. **Move #3129 and #3131 onto the engine** as built-in subscriptions, keeping their member-facing switches.
5. Further sources from §7 one at a time.

## Rejected

- **A dedicated `watch_pr` tool and a faster `branchpr`.** Solves the PR case quickly but adds the sixth hand-rolled
  watcher; the next provider repeats the work.
- **Webhooks only.** Needs an App or hook-admin rights in every repository and a Control Plane reachable from the
  internet; a lost delivery is a silent miss. Kept as the fast path, not the only path.
- **Polling from the Control Plane.** The member's credentials and the sessions live in the workspace; polling from
  the Control Plane would mean holding member tokens centrally for a feature that does not need them there.
- **Letting adapters invent event types.** Leads to one name per provider for the same condition, and to sessions that
  must know which provider they are talking to.

## Consequences

- Skills that run `gh pr checks --watch` in the background (issue-rounds, issue-to-pr) can replace it with one
  `watch` call once step 1 ships.
- The engine becomes a shared dependency of several Agent features; its store and scheduler need their own tests and
  measurements (requests per hour per connection, delivery delay).
- Webhook ingress adds a public, unauthenticated-by-session endpoint to the Control Plane, guarded by signature checks;
  it gets its own security review in step 3.

## Open questions

- Whether the time source (§7) should replace the scheduler of ADR 0021 or only feed it.
- Whether a member can add their own source as an external process (for example an MCP server exposing `poll`), and
  how such a source is sandboxed.
- The default subscription limits and the default expiry.
