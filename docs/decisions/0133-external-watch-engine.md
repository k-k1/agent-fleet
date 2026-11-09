# 0133. One watch engine for external state: subscriptions, source adapters, push and poll behind one interface

English | [日本語](0133-external-watch-engine.ja.md)

- Status: **accepted** (2026-11-08; proposed 2026-11-08). Nothing is built yet.
- Follow-ups: #3627, #3628, #3629, #3630, #3631, #3632 (steps 1 to 6 of §10).
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
| `internal/branchpr` | GitHub (GraphQL, batched) and Bitbucket PR state | a cache filled on demand (when the session list is read, or by #3129), not a loop of its own; GitHub: open PRs 2 min (`openTTL`), settled 10 min, backoff 5 / 15 min; Bitbucket: open PRs 5 min, conflict checks 15 min, backoff 10 min after a 403 |
| PR feedback watch (#3129, `sessionx/pr_feedback.go`) | CI failure, requested changes and new unresolved review threads on a session's own PR (at most 3 review notices per PR); merge or close of PR links the session holds | reads the `branchpr` cache; injects a notice into the owning session, or tells the member when that session is archived or cannot take prompts; off by default (the `prFeedback` switch) |
| PR notices (#3131, `sessionx/session_pr_notices.go`) | ready to merge (`pr-ready`), merged, closed | reads the same cache whenever it is recorded; writes the notification outbox for the member; no switch |
| Stalled-tree watch (#2397, `sessionx/stalled_tree.go`) | a parent and all its children idle | its own loop; injects a notice |
| Work item inbox (ADR 0061, `internal/workitems`) | GitHub / Bitbucket / Jira items for the member | the Control Plane owns the schedule, saved queries and cache; the Agent only resolves the queries with the member's tokens |

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
        ^
   Poll (Agent side)  <--  poke (subject only) <-- Webhook (Control Plane side)
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
    Parse(r *http.Request) ([]SubjectID, error) // a poke, never an Event: §4
}

type Event struct {
    Source  string          // "github"
    Subject SubjectID       // "github:k-k1/agent-fleet#3606"
    Type    string          // shared vocabulary, §3: "pr.checks.completed"
    Version string          // de-duplication key: a fingerprint of the state, §5
    Summary string          // one line composed from fields the adapter controls, rule 6.5
    Data    json.RawMessage // provider detail (failed jobs, URLs)
    At      time.Time
}

type Subscription struct {
    Owner   SessionRef      // owning session: who asked, or the parent when Notify is parent (§5)
    Subject SubjectID
    Types   []string        // patterns: "pr.checks.*"
    Until   UntilRule       // first match | n deliveries | subject closed | deadline
    Notify  SinkRef         // self | parent | member
    Fallback SinkRef        // built-in subscriptions only: where to deliver when the session cannot take it (§5)
}
```

The adapter knows the provider; the engine knows time. Polling intervals, backoff after errors or rate limits, and the
choice between push and poll belong to the engine, guided by the adapter's `Hint` (for example "checks running: ask
again in 15 s", "settled: 10 min", "rate limited until T").

### 3. A shared event vocabulary

Event types are namespaced and shared across providers, so a session subscribes to `pr.checks.completed` whether the
PR is on GitHub or Bitbucket. The first set: `pr.checks.completed` (with the overall result and the failed checks),
`pr.checks.failed`, `pr.conflict`, `pr.mergeable`, `pr.ready` (open, not draft, checks green, mergeable, and approved
or no review required), `pr.merged`, `pr.closed`, `pr.review.changes_requested`, `pr.review.threads_unresolved`,
`issue.transitioned`, `deploy.completed`, `deploy.failed`. `watch` refuses a type the subject's source cannot produce rather than accepting a watch that never fires. On
Bitbucket that is `pr.review.threads_unresolved`: its pull request has no resolved-thread state, and reading every
comment page of every subscribed PR does not fit its hourly budget (rule 6.2). A provider-specific condition gets a provider-prefixed type
(`github.workflow_run.completed`) until a second provider needs it. Adding a type is a code review decision, not
something an adapter does on its own.

### 4. Where it runs

- **Polling runs in the workspace Agent.** The credentials ("Settings > Connections"), the sessions and the existing
  `branchpr` cache are there, so a poll costs no new hop.
- **Webhooks are received by the Control Plane** and verified, and are only a **poke**: the Control Plane forwards
  `(source, subject)` and nothing of the payload to the workspaces that registered that subject, and the engine polls
  the subject at once with the member's own token. Events therefore always come from a poll, so the data a session
  sees is always what the member's own connection can read, and a workspace that registered a subject it cannot read
  learns no more than that something changed. The Agent registers a subject with the Control Plane when its first
  subscription is made and withdraws it with the last. The GitHub App's webhook secret is the deployment's own
  setting; a repository webhook gets a secret the Control Plane generates when the member adds the hook, so no member
  credential is needed on the Control Plane to verify a delivery.
- A webhook only shortens the wait: the engine still polls a subscribed subject at a long interval, because webhooks
  can be lost.

### 5. Delivery

- **Into a session**: a notice in the same form as the stalled-tree and PR feedback ones,
  `[agent-fleet:notice kind=watch source=github type=pr.checks.completed subject=… reply=none]`, then the one-line
  `Summary`, then `Data` under a line that labels it as data, not instructions (rule 6.5). Header values are tokens
  from a fixed ASCII set (letters, digits, `.:/#_-`); a value outside it is refused when the subscription is made or
  the event is dropped, so nothing can close the header early or start a new line. The notice is injected only when
  the session is idle and can take input. A stopped session is started and told once ready, one at a
  time, as #3129 already does. An archived session's subscriptions end.
- **To a parent**: a child may subscribe with `Notify: parent`, so an issue round's parent hears about its children's
  PRs without owning them. The parent is the session that started the child (`Origin` is a session, not a handoff).
  Such a subscription belongs to the parent: it counts against the parent's limit, is listed and ended by the parent,
  and ends when the parent is archived, not when the child is.
- **To the member**: the notification outbox (`internal/notice`), as #3131 does today.
- **When the session cannot take it**: a subscription a session made ends with that session. A built-in subscription
  (step 3) names a `Fallback`, the member, as #3129 does today for an archived session or a kind that cannot take
  prompts.
- **Once**: an event is delivered once per subscription, `Type` and `Version`, recorded on disk, so a restart repeats
  nothing. `Version` is a fingerprint of the state that produced the event: for checks, the head SHA, the result,
  and each check's identity and completion time (the run attempt where the provider has one; Bitbucket statuses and
  GitHub status contexts have only an update time); for conflicts and change requests, an episode counter that moves
  each time the condition returns; for unresolved review threads, the count, so a rise from 1 to 2 is a new event. A
  re-run on the same commit or a second conflict is therefore a new event, and the same state seen twice is not.
- **The first poll after `watch`** evaluates the current state: a condition that already holds (the checks on the
  current head have completed) is delivered at once, so a `watch` made a moment too late does not wait forever.
  Built-in subscriptions instead take the first state they see of a terminal condition (`pr.merged`, `pr.closed`)
  as the baseline, because a PR first seen already merged is history, not news. Their other conditions (a failed
  CI run, a change request, a conflict, ready to merge) are delivered on first sight, as #3129 and #3131 did; what
  those features already told is carried into the engine's record when they move, so nothing is told again.
- **While the workspace is stopped** nothing polls, because the engine lives in the Agent. Subscriptions stay on disk;
  when the workspace starts, the engine polls every live subscription once before it returns to its schedule, and
  delivers only the latest state per subject and type. That start-up poll covers whatever pokes would have said, so
  the Control Plane drops pokes for a stopped workspace, and a poke never wakes one. Anything that must act while
  the workspace is stopped is an ADR 0021 schedule (§7).

### 6. Rules the engine enforces

1. **Authorisation at subscribe time and at every delivery**: `Resolve` checks that the subscriber's own connection
   can read the subject. A subject in another tenant, or one the member's token cannot see, is refused. Every event
   comes from a poll made with that connection (§4), so a revoked token or a lost repository access stops delivery
   at the next poll.
2. **Limits**: at most 20 live subscriptions per session and 200 per workspace (`watch` refuses the next one and
   says which limit was hit). Where the provider states an hourly limit, the engine's own requests may use at most
   10% of it per connection (one token is one GitHub rate limit, whoever uses it); replies that do not count against
   the provider's limit, such as GitHub's `304`, do not count against this budget either, and secondary-limit replies
   (`Retry-After`) are obeyed. Bitbucket's 1,000 requests an hour are already mostly spent by `branchpr`, so its
   source reads through the `branchpr` cache and shortens the cache's interval for subscribed PRs within that budget
   instead. A provider without a stated hourly limit (Jira, CloudFormation, an HTTP endpoint) gets 30 requests a
   minute per connection. All these numbers are settings with these defaults.

   Sharing `branchpr` with the engine needs four changes to it, made in steps 1 and 2:
   - entries indexed by PR number as well as by branch, so a watch and the session list share one entry;
   - a refresh the engine can wait for, for the first poll after `watch` and for a poke, since a lookup today never
     waits on the network;
   - a fingerprint of all the head's checks (each check's identity, state and completion or update time, which it
     does not request from Bitbucket today), because §5's `Version` cannot be computed from the failing checks alone;
   - a shorter conflict-check interval for subscribed Bitbucket PRs.
3. **Expiry**: every subscription has a deadline, 7 days by default and at most 30 days, and ends with its owner.
4. **Fail quietly, not wrongly**: an adapter error backs off and is visible in `list_watches`; it never produces an
   event that did not happen.
5. **Provider text is data, not instructions.** A notice is read by an agent, and titles, comments, job logs and
   anything an external source (§8) returns can be written by someone other than the member. `Summary` is composed
   by the adapter from fields it controls (state, result, counts, numbers, the subject's reference); free text goes
   only into `Data`. In the notice, `Data` is at most 2 KiB; each value is quoted, stripped of control and
   line-separator characters and cut to 100 characters; only `https://` URLs are kept.

### 7. Time, and how it relates to ADR 0021

The engine's `time` source holds timers that live inside a running workspace ("in 20 minutes", "at 15:00") and fires
`time.fired` through the same sinks as any other event. It **feeds** the ADR 0021 scheduler and does not replace it:
only the Control Plane is alive while a workspace is stopped, and the 0021 scheduler already wakes workspaces and
drives sessions there.

- Every time watch is kept by the engine, fires from the engine while the workspace runs, is listed by
  `list_watches` and ends like any other subscription.
- A time watch asked for with `wake` also gets a counterpart on the Control Plane: a schedule whose only action is to
  start the workspace if it is stopped. It sends no prompt and no message to the Agent; on start the engine fires
  every time watch that fell due from its own store, as it does for any other subscription (§5). Every schedule today
  ends in a prompt (`rate_limit_resume.go` books a reuse-mode prompt into the stopped session), so a wake-only
  schedule is a new target on the Control Plane, made in step 6, and is not shown in the member's
  scheduled-execution list. The engine records the counterpart's ID and deletes it when the watch fires, is ended
  with `unwatch` or ends with its owner, as `rate_limit_resume.go` does with its schedule ID. Delivery then follows §5
  (idle sessions only, `Notify: member` included). Where the Control Plane's scheduler is not running, `watch` refuses `wake` instead of accepting a timer that
  would never fire.
- A time watch without `wake` that falls due while the workspace is stopped is delivered at the next start, marked
  late, or dropped when its subscription expired in the meantime.
- The 0021 scheduler stays the only component that wakes a workspace.

### 8. Sources a member adds

A member may register an MCP server from "Settings > Connections > MCP servers" as a watch source. The feature is off
for a tenant until an administrator enables it.

- **Contract**: the server exposes two tools with the shape of §2: `watch_resolve(ref)` returns a subject or a
  refusal, and `watch_poll(subjects, cursors)` returns events, cursors and hints. External sources poll only; the
  Control Plane receives no webhooks for them.
- **Caller**: the engine in the Agent calls these tools, never a session on the engine's behalf. Being a watch source
  is its own target on the definition (`Targets.Watch`), independent of `Targets.Session`: a member may hand one
  server to both, and a session that can reach the server can already send it anything. `Resolve` checks the
  server's `Kinds` against the subscribing session's kind. A `ref` is one line of printable ASCII, at most 200
  characters; when the subscribing session cannot reach the server itself (`Targets.Session` off, or its kind not in
  `Kinds`), it is further limited to the header token set (§5), so `watch` cannot carry session text to a server the
  session could not reach. The resolved subject, source and type always use the header token set.
- **What the server receives**: subject references and its own cursors, nothing else: no session content, and no
  credentials other than those of its own MCP server connection. A member's local (stdio) server is started with an
  environment built from an allowlist (`PATH`, `HOME`, `LANG`, `TMPDIR`), the variables `mcpreg.ForwardEnvNames`
  forwards to every definition (the isolation flag) and its own configured variables, never the Agent's own
  environment, which holds the secret-store keys and the Agent's internal tokens. The existing stdio dialer and probe
  pass the Agent's environment as is to every server; step 5 builds it per definition instead. The same step makes the
  Console's connection test (`HandleServerTest`) take the origin from the store alone: a stored built-in is tested
  exactly as stored, and anything else is a member's server, whatever the request says, so a request cannot have an
  arbitrary command started with the built-in environment. Built-in servers keep
  what `ForwardEnvNames` gives them (the built-in `af` server needs the Agent's token, the ops servers the
  secret-store keys), so only member-added servers lose access.
- **Namespace**: its event types must be `ext.<source>.*` and its subjects `ext:<source>:…`. Anything else is dropped
  and counted as an error, so an external source cannot pose as `pr.merged` or speak about another source's subject.
- **Reach**: its events go only to subscriptions owned by the registering member's sessions (self, parent) or to that
  member. They are never forwarded to the Control Plane, another member or another workspace.
- **Output**: the engine, not the server, composes `Summary` from `source`, `type` and `subject`. Whatever summary the
  server returns goes into `Data` with the rest, and all of it is provider free text under rule 6.5.
- **Budget**: a call times out after 10 s; a subject is polled at most once a minute whatever the `Hint` says; one call
  carries at most 20 subjects and returns at most 20 events. The server returns a new cursor for a subject only when
  every pending event of that subject is in the reply; a subject without one is asked again from its old cursor.
  The engine sends due subjects least recently polled first, so one busy subject cannot starve the rest. Five consecutive failures pause the source; the pause is
  shown in `list_watches` and lifted by the member.
- **Isolation**: beyond the environment above, a local server runs with the same user and limits as the MCP servers
  sessions start, so the workspace container is its sandbox, as it is for every process the member runs; being a
  source gives it no extra privilege. A remote server is reached the way sessions reach it.

### 9. Sources this is meant to absorb

| Area | Examples |
|---|---|
| Code hosting | GitHub, Bitbucket, later GitLab: checks, conflicts, merge, review, a new commit on the base branch |
| Trackers | Jira, GitHub / Bitbucket issues: transitions, assignment, a new comment that mentions the session's item |
| CI on its own | GitHub Actions / Bitbucket Pipelines runs not tied to a PR (nightly, release workflows), a release tag published |
| Deployments | CloudFormation stack status, ECS service steady state, a Kubernetes rollout, a container image becoming available in a registry |
| Agent Fleet itself | a peer session going idle or finishing, a child's turn ending, quota windows reopening (`sessionx/rate_limit_resume.go`), a long background command exiting |
| Endpoints | an HTTP health check turning green after a deploy, a certificate nearing expiry |
| Time | "at 03:00" and "in 20 minutes" as a time source that feeds scheduled execution (ADR 0021) rather than replacing it (§7) |
| Security feeds | a new advisory for a dependency, a new finding in the published images' vulnerability scan (#3339) |

The table is a direction, not a commitment; each source is its own change with its own review.

### 10. Steps

1. **Engine + `github` source + MCP tools** (`watch`, `unwatch`, `list_watches`). For subscribed PRs the `github`
   poller adds REST with conditional requests (`If-None-Match`; a `304` does not count against GitHub's primary rate
   limit) and adaptive intervals. The PR itself does not change its ETag when a check finishes, so each PR takes
   several endpoints: the PR (`pulls/{n}`), its head's check runs and combined status, and its reviews. What it
   fetches is written back into the `branchpr` cache (with the `branchpr` changes of rule 6.2), so the same PR is not
   fetched twice. Until step 3, a session's
   own subscription to its PR's checks or reviews suppresses #3129's notice for that session and PR, so it is not told
   twice.
2. **`bitbucket` source** (Bitbucket has no ETag: adaptive intervals within the `branchpr` budget and the `branchpr`
   changes of rule 6.2; mergeability and approval filled in from the conflict and participant reads `branchpr`
   already makes, so `pr.mergeable` and `pr.ready` work on Bitbucket; `pr.review.threads_unresolved` stays refused,
   §3).
3. **Move #3129 and #3131 onto the engine** as built-in subscriptions with the member as `Fallback`. #3129 keeps its
   `prFeedback` switch, its review-notice cap (`Until`: 3 deliveries) and its watch on held PR links; #3131 has no
   switch and keeps none. This needs no webhooks, so it comes before them. A built-in is not polled by a source: its
   feature states the set it wants on each sweep (the owner can change, the delivery record stays with the PR) and
   feeds the PR it already read from the session list's `branchpr` caches, so it costs no request of its own and
   counts against no watch limit. A session that watches the same PR and type itself hears it from its own watch,
   and the built-in stays quiet.
4. **Webhook ingress on the Control Plane** (GitHub App first; repository webhooks where an App cannot be installed;
   Bitbucket repository webhooks), forwarding pokes only, with the secrets of §4.
5. **Sources a member adds** (§8), behind the tenant switch, with the environment allowlist for stdio servers.
   - *2026-11-09, Agent half (#3631).* A member's source is addressed as `ext.<name>` (the definition's name, lower
     case, `-` as `_`; a name that starts with a digit cannot be one), so it can never shadow a built-in source; its
     types are `ext.<name>.*` and its subjects `ext:<name>:…`. A subject is read through an optional batch interface of
     the engine (up to 20 subjects per call, least recently read first) and at most once a minute whatever the hint.
     The Agent reads the tenant switch from the CP's `/internal/mcp-servers` answer (`watchSources`, absent = off); the
     CP side and the Console follow in a second change, so until then the switch cannot be on. Only a member's own
     definition is a source: a tenant-distributed one never is, and `Targets.Watch` is cleared when it arrives.
     The narrowed stdio environment applies to the engine's own connections and to the connection test of a definition
     with `Targets.Watch`; existing session-target servers keep the Agent's environment until that is decided
     separately. Pausing is kept in memory, so an Agent restart lifts it, and editing or re-enabling the definition
     lifts it too. An OAuth server is reached through the relay, as sessions reach it.
6. **The `time` source** (§7) with the Control Plane's wake-only schedule target, then further sources from §9 one at
   a time.

### Step 4 note (2026-11-09, #3630)

Built as one change: `internal/webhookpoke` (signature check, parsers that return subjects only), the
endpoints `POST /webhooks/github` (the deployment App, `AF_GITHUB_WEBHOOK_SECRET`) and
`POST /webhooks/hooks/{id}` (a tenant-owned hook), the Agent's `PUT /internal/watch-subjects` and
`/internal/watch-hooks` under its own `AF_WATCH_TOKEN`, and `POST /watch/poke` on the Agent.

- A hook's secret is derived (HMAC of the token signing root and the hook id), not stored, so the database holds
  nothing that verifies a delivery. Deleting the hook retires it.
- Tenant isolation: a hook belongs to one tenant and its pokes reach only that tenant's registrations. The App is
  shared by every tenant, so what it forwards is limited to public repositories (`repository.private` false; a
  payload that does not say counts as private). Private repositories use a tenant hook or the long poll.
- Registrations are a replaced set per workspace (at most 200), expire after an hour, and the Agent renews every
  20 minutes. A poke reaches only workspaces the database shows as running and never starts one.
- A replayed or forged delivery costs at most one extra read: the Control Plane pokes a (workspace, subject) pair at
  most every 5 s, and the engine reads a poked subject at most every 10 s and keeps a failing subject's backoff.
- Not built: the Agent-side flow that installs a hook at GitHub or Bitbucket with the member's token, and
  `status`-style events that name a commit and not a pull request (the long poll covers them).

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
  it gets its own security review in step 4.
- Sources a member adds put member-chosen code in the engine's path. §8 confines what they can emit and to whom; step 5
  gets its own security review, like step 4.

## Open questions

None left. The three raised in the draft are answered in §6 (limits and expiry), §7 (time and ADR 0021) and §8
(sources a member adds).
