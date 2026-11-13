# 0133. One watch engine for external state: subscriptions, source adapters, push and poll behind one interface

English | [日本語](0133-external-watch-engine.ja.md)

- Status: **accepted** (2026-11-08; proposed 2026-11-08). Steps 1 to 6 of §10 implemented (2026-11-09); the further sources of §9 that step 6 names are not built yet.
- Follow-ups: #3627, #3628, #3629, #3630, #3631, #3632 (steps 1 to 6 of §10); #3709, #3732, #3751, #3752.
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
   - *2026-11-10, Control Plane half and Console (#3631).* Besides five failed calls in a row, 20 replies in a row that
     carry malformed or out-of-namespace entries pause a source; a clean reply resets that count, and a reply that
     delivers its events with a stray entry is not a failed call. The tenant switch is a column of `tenant` (off by
     default), changed by `PUT /api/admin/tenants/{slug}/watch-sources` (tenant admin, audited as
     `tenant.watch_sources`) and sent to the Agent as `watchSources` in the `/internal/mcp-servers` answer, which
     reads as off when it cannot be read. The CP also refuses a member's `POST`/`PUT /api/mcp-servers` that sets
     `targets.watch` while the switch is off (`mcp_watch_disabled`); the Agent enforces the switch on its own.
6. **The `time` source** (§7) with the Control Plane's wake-only schedule target, then further sources from §9 one at
   a time.

   *Note, 2026-11-09 (step 6, #3632).* The time source and the wake-only target are built; the
   further §9 sources are not (follow-ups in the PR). Decisions the text left open:
   the due instant is part of the subject (`time:20261109T150000Z`, UTC) and `watch` takes an RFC 3339
   time with an offset or a wait (`in 20m`), never a bare time of day, because the engine keeps UTC and
   must not guess a zone. A timer's default lifetime is its due time plus 24 hours (at most 30 days
   from now), not the 7 days of other watches, which would end a timer set 20 days out before it
   fired; `until deadline` is refused. The wake-only target is a schedule row with
   `session_mode = wake`, a value the schedule API refuses, booked and cancelled on
   `/internal/wakes`; the list and every by-id handler treat it as absent. The Control Plane
   also deletes the row itself after its fire, and the Agent reads a 404 as "gone", so the clean-up
   is safe to repeat. The engine cancels every booking no live watch needs, working from the set of
   outstanding bookings rather than the places a watch can end, and asks again each minute until the
   Control Plane answers.

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

   *Note, 2026-11-10 (#3751).* The Agent-side flow is built: `af-watch-hook add|list|rm`
   (`workspace/agent/internal/watchhook`), behind Agent routes `/watch/hooks` so the Control Plane token
   (`AF_WATCH_TOKEN`) stays in the Agent and the shell holds only `AGENT_TOKEN`. `add` checks the member's forge
   credential, makes the hook at the Control Plane, installs it at the forge (JSON body, the secret as the
   forge-side signing secret, pull request and check events only) and deletes the Control Plane hook again if the
   install fails; a hook for the same repository already present makes it a no-op. `rm` finds the forge webhook by
   its URL (the Control Plane stores no forge id), deletes it, and retires the Control Plane hook even when the forge
   step fails, since the Control Plane hook is what makes a delivery verify. The secret never leaves the Agent
   process. Not built: a tenant's own GitHub App path, which needs the secret shown to the member once and so
   conflicts with "never print it" (documented in the guide as unavailable; follow-up #3762).

### Note on §9, CI on its own (2026-11-10, #3779)

GitHub Actions runs not tied to a PR are watchable. Decisions the text left open:

- **A source of its own, `github_actions`**, not a second ref form under `github`. The engine checks types per
  source, so a run cannot be asked for `pr.merged` and a PR cannot be asked for a run type with no engine change, and
  every existing PR ref stays as it was. The `watch` handler sends a reference in an Actions form to it when the caller
  left the source at the `github` default (a PR ref never matches those forms). It reads through the `github` source's
  token, conditional requests and 10% share of the hourly limit.
- **Refs.** A run: `https://github.com/o/r/actions/runs/<id>[/attempts/<n>]` or `o/r:run/<id>[/attempts/<n>]`; the next
  run of a workflow: `o/r:<file>.yml[@branch]` or the workflow's URL. Types are `github.workflow_run.completed` and
  `github.workflow_run.failed`; the version is run id and run attempt.
- **"Started after the watch" is a floor taken from GitHub's answer**, not this host's clock. Resolve lists the
  workflow's newest runs and the subject carries the highest run id and the newest creation time seen (or the `Date`
  header when there is no run yet); a run counts only if its id is above the floor (ids only grow) and it was created at
  or after the floor's time. A skewed local clock therefore cannot make an old run look new. Since the floor is in the
  subject, watches made while the workflow is quiet share one subject and one read (with no run yet, the floor time
  is rounded down to the minute so that holds there too). A re-run of a run at or below the floor is never seen. The cost: a run created in the
  moment before the watch but not yet listed by GitHub may be taken as new.
- **Summary.** Built from the repository, run number, attempt, run id and a fixed set of conclusion words; for a
  workflow watch, the file name from the ref. The workflow's own name (written by whoever can edit the repository) and
  the failed jobs go into Data, which the §6 rule 6.5 notice quotes.
- **A subject that is gone.** A source may answer a read with `ErrGone` (a deleted run, a repository the connection
  lost). After three such reads in a row the engine replaces the watch's pending events with a `watch.ended` notice
  and ends it once told; before this a failing subject only backed off until the watch expired.
- Not built: Bitbucket Pipelines (shared `ci.run.*` types once a second provider exists), webhook pokes for runs
  (registrations still list pull request subjects only), paging a workflow that starts more than 20 runs between two
  reads, and `watch.ended` for the PR source. Follow-up: #3780.

*Note, 2026-11-10 (#3780).* Two of the items above are built. (1) A workflow's run list is followed past its first
page (20 runs) while `total_count` says there are more, at most 5 pages (100 runs), each page with its own ETag and
runs deduplicated by id across a boundary; a later page that fails fails the whole read, so the cursor does not
advance on a partial view. The event rule is unchanged (the newest completed run is told, latest state per type); what
paging fixes is a completed run hidden behind 20 newer runs that are still going. (2) The `github` source's read of the
pull request itself now answers `ErrGone` by the Actions source's own definition (404, 403 that is not a rate limit,
410), so a deleted PR or repository ends the watch with `watch.ended` after the usual three reads; 5xx and rate limits
never do. Other reads of the PR (checks of a head SHA) stay plain errors, since a 404 there is a force-push. Bitbucket's
PR source is unchanged. Not built, left in #3780: webhook pokes for runs (a `workflow_run` delivery would have to be
parsed into run subjects and accepted by the poke route, both Control Plane changes; a workflow subject carries a
floor the Control Plane cannot derive), Bitbucket Pipelines, and the live check against a private repository.

*Note, 2026-11-10 (#3780, webhook pokes).* A `workflow_run` delivery now pokes run watches. The Control Plane cannot
derive a workflow subject, which carries a floor only the Agent knows, so the wire names **poke keys** instead: a run
(`github:o/r/actions/runs/<id>[/attempts/<n>]`) and a workflow without its floor
(`github:o/r/actions/workflows/<file>[/branch/<b>]`). For a `completed` delivery the Control Plane names the run with
and without its attempt, the workflow from the basename of `path` (an `@<ref>` suffix dropped), and the workflow
with the run's `head_branch`; every field is checked against the same shape the registration accepts, and a run id,
path or branch that does not fit names nothing. Other actions (`requested`, `in_progress`) name no run key. The Agent
registers each Actions subject as its key (several floors of one workflow share one), and a poke whose subject is such
a key reaches the Actions source: a run key pokes that run, a workflow key pokes every watch of that workflow and
branch whatever its floor (`Engine.PokeMatching`). The poke still carries `(source, subject)` and nothing else, the
source stays `github` on the wire, and a poke only brings the next read forward under the same gap and backoff rules,
so it cannot fabricate a run state. A hook or the App is trusted exactly as for a pull request. Not verified live
against a real repository. Still open in #3780: Bitbucket Pipelines and the live check.

*Note, 2026-11-10 (#3780, Bitbucket vanished PR).* The `bitbucket` PR source now answers `ErrGone` when the read
says there is no such pull request (a 404 on the repository or the PR, or no PR with that number), so the watch ends
with `watch.ended` after the usual three reads. A failed, held-off or rate-limited read is still a plain error and never
ends a watch. Still open in #3780: Bitbucket Pipelines and the live check against a private repository.

*Note, 2026-11-11 (#3875, Bitbucket Pipelines and `ci.run.*`).* Bitbucket Pipelines runs are a watch source, and the
run types are now shared by both providers. Decisions:

- **Shared types: `ci.run.completed` and `ci.run.failed`.** Both the Actions source and the new Pipelines source emit
  only these. The `github.workflow_run.*` names are **aliased, not duplicated**: a source may implement
  `TypeAliaser` (old prefix -> new prefix), and the engine rewrites a pattern that uses the old spelling when the
  watch is made. A pattern naming both spellings collapses to one, so a run is one event per type and is told once
  (the de-duplication key stays `(type, version)`, per subscription). The `list_watches` and notice type is the
  shared name even for a watch asked for with the old one.
- **Existing subscriptions keep firing exactly once.** A subscription stored before this change holds the old names
  in its patterns, its `Told` versions and its pending events; loading state rewrites all three, so a run already
  told as `github.workflow_run.completed` is not told again as `ci.run.completed`, and one still pending is told
  once. The Actions versions (`run id:attempt`) are unchanged. Not changed: the `workflow_run` webhook event of the
  Control Plane is GitHub's own name and has nothing to do with the watch type.
- **A source of its own, `bitbucket_pipelines`**, beside `bitbucket` (pull requests) for the reason `github_actions`
  sits beside `github`. Refs follow the Actions shapes: a run is `https://bitbucket.org/w/r/pipelines/results/<n>` or
  `w/r:run/<n>`; the next run is `w/r:pipelines[/<custom pipeline>][@branch]` (Pipelines has no workflow file; the
  custom pipeline name, `target.selector.pattern`, is its nearest counterpart). A bitbucket.org URL routes by itself;
  the bare forms are the Actions shapes, so they mean Pipelines only with `source: bitbucket`.
- **"Next run" floor** is taken from Bitbucket's answer as for Actions, as the highest build number and newest
  `created_on` among the newest listed runs (the `Date` header when there is none). Build numbers are
  repository-wide and only grow, so a run counts when its number is above the floor and it was created at or after
  the floor time; the branch and custom pipeline are filtered client-side, since the list has no documented filter
  for them. The list is followed through Bitbucket's `next` links (50 per page, 5 pages), stopping at the first page
  that reaches the floor; a link off `api.bitbucket.org` is never followed; a failing later page fails the read.
- **Version and failure.** Version is `build number:completed_on (unix)`, so a run finished again is news. `FAILED`,
  `ERROR`, `STOPPED` (cancelled) and `EXPIRED` also emit `ci.run.failed`, as a cancelled Actions run does; the steps
  that did not succeed are read once per finished run and go into Data with the branch, not into the summary.
- **Budget and auth.** Bitbucket Cloud allows 1,000 requests an hour per account, shared with the pull request list,
  so the source is limited to 5 live watches per workspace and reads at 1 minute (running), 2 minutes (pending), 5
  minutes (waiting for the next run) and 30 minutes (a finished run). A 429 holds the source off until `Retry-After`.
  It uses the same Bitbucket connection as the PR source (OAuth token renewed once on a 401; a pasted API token is
  not renewable and asks for reconnecting), sends it only to `api.bitbucket.org`, and never puts it in an error or a
  log. A 403 is a refusal naming the missing Pipelines read scope.
- Not built: webhook pokes for Pipelines runs (registrations still list pull request and Actions subjects only; no
  Pipelines subject is registered), and `watch.ended` for a deleted *repository* beyond the 404 rule (a 404 on a run
  or on the list ends the watch after the usual three reads). **Not verified live against a real Bitbucket
  repository**: the response fields and the build-number run endpoint are from the API reference and are covered only
  by tests against a fake server.

*Note, 2026-11-13 (#4033, grace window on workflow refs).* A workflow or pipeline watch (`o/r:<file>.yml[@branch]`,
`w/r:pipelines[...]`) used to admit only runs that started after the watch was made. A run chained from the one the
caller had just waited for starts seconds before that run's notice arrives, so the watch made in reaction to the notice
missed it (seen: a dispatched run 3 s before its parent finished, the watch made after). The floor now sits 10 minutes
before the provider's own clock (its `Date` header, never the host's), rounded down to the minute (so the window is 10 to 11 minutes), so quiet watches still share a
subject: the id floor is the highest listed run created before that point, and a run above it that started inside the
window counts, completed or not. A run in the window that had already completed fires at once; a run that started
earlier never does. Each run is still told once (version = run id and attempt), and the engine's latest-state rule
is unchanged, so two runs that are both finished at the first read give one notice for the newer. A repository or
workflow with no run keeps the old floor (the provider's clock). Run-URL refs are unchanged. The tool description, the
guide and `workspace/notes/agent-fleet.md` also say: when what you wait for is a PR the run opens, watch the PR, and when
a run is chained from one you just watched, check `gh run list` first. **Not verified live** against GitHub or Bitbucket;
covered by tests against a fake server.

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

*Note, 2026-11-13 (#3969, the fleet source).* The first of §9's "Agent Fleet itself" rows is built as `source: fleet`: `ref` is a session name, the subject is `fleet:session/<name>`, and the types are `fleet.session.idle`, `fleet.session.stopped` and `fleet.quota.reopened`. Not built: a long background command exiting, other members' sessions. Choices that were open:

- **Scope.** A session resolves only what its own `list_child_sessions` (children: origin session, lineage the caller) or `list_peer_sessions` (the same population, while peer messaging is on) would show it, never itself and never the session that receives the notice. An unknown name and one it may not see get the same refusal. The Workspace belongs to one member, so no other member's session can pass.
- **Idle is the session list's own classification** (`wireSession`, the state `fleetgraph.ObserveState` records), read through the same function as the stalled-tree watch, with no second classifier of its own. The version of `fleet.session.idle` is the session's last turn end, so one turn end is one notice and a restart of the Agent re-reads the same version.
- **"A condition that already holds is reported at once" is kept for `stopped` and not for `idle`.** An idle session's last turn end is recorded as already told when the watch is made (`Resolved.Told`), so only the next turn end fires. Otherwise a parent that messages an idle child and watches it would be told at once for the turn before the message, since the list can read idle for a while after a message was delivered. The cost: a session that is idle and never runs again is not told; `list_child_sessions` shows that state. `stopped` holds until the session is resumed, so reporting it at once is the right answer.
- **`report_back` does not suppress `idle`.** The notice exists for a child that ended its turn without sending a report, and telling the two apart would need a delivery ledger the Agent does not keep. A parent that already has the report ends the watch; the cost of not doing so is one short notice (`reply=none`).
- **Quota** follows `rate_limit_resume.go`'s episode file: the episode is open until its reset instant (or, with none or a spend limit, until the sweep retires it), and `fleet.quota.reopened` carries the episode it ended as its version. A session that was never limited is not told.
- **A deleted or archived session** ends the watch through `ErrGone` (`watch.ended`).

*Note, 2026-11-13 (#3969, review of the fleet source).* Three follow-ups. (1) Access is checked again after Resolve: a source may implement `SubscriptionAuthorizer`, and the engine asks it once per tick before matching or delivering; a refused subscription (the maker no longer sees the session, e.g. peer messaging was switched off) gets one `watch.ended` notice in place of anything pending, and only that subscription ends, since the subject is shared. (2) A hook-less kind's state source can still show the previous turn's idle just after a new input, and the list's poll would stamp it as the new turn's end; the real end then reuses that time (one observation per status Rev), so a watch counts one turn end for two. `recordPolledTurnEnd` now stamps nothing within 3 s of the turn's start; measured limit: a stale idle that outlives that window is still stamped. (3) The usage-limit episode open at subscribe time is seeded into the subject's cursor (`Resolved.Seed`), so a limit that lifts before the first read is still told.

*Note, 2026-11-13 (#3969, second review round).* The settle window of the previous note is 10 s, not 3 s, and it gates both routes that record a polled turn end: the list's stamp and `DriveState`'s notification (`notifyPolledTurnEnd`), so the notification of a turn that really ended inside it comes after the window plus the wait for the next poll. The figure is a heuristic, not a measurement of any kind's source lag; a stale idle that outlives it is still taken as the new turn's end. The exact fix needs a turn identity from the source itself and is #4007. A subscription holding an ended notice is no longer read for, listed with the subject's health, or rewritten by a later `ErrGone`, so a revoked watch learns nothing about the session after the revoke.

*Note, 2026-11-13 (#3970, GitHub releases and tags).* §9's "a release tag published" is built as two sources, `github_releases` (`github.release.published`) and `github_tags` (`github.tag.created`), so the engine's per-source type check refuses a tag type on a releases ref instead of letting it wait forever. `ref` is `o/r:releases[+pre][@pattern]`, `o/r:tags[@pattern]` or a releases/tags URL, routed by shape like the Actions refs. Choices that were open:

- **Floor.** A release is new when its `(published_at, id)` is above the newest published release listed when the watch was made (GitHub's `Date` header, to the minute, when none is listed). The id alone is not the rule: a draft created long ago and published later has a low id and is new. Drafts never fire; pre-releases fire only with `+pre` (they are part of the floor either way). The version (§5) is the release id.
- **Tags have no date and GitHub lists them in name order.** "New" is therefore a set rather than a time: Resolve stores the tag names that exist in the subject's cursor (`Resolved.Seed`, as the fleet source does for its limit episode), and a tag is new when it is listed and not in that set. Reported and non-matching tags join the set, so a reordered list or a page shift is nothing. The set is bounded (600 names) and the list is read to 300 tags; a tag past that is never seen. A name leaves the set after two consecutive complete reads without it, so a deleted-then-recreated tag is created again, while a tag that slipped between two pages during one read is not. The version is the tag name, so a tag already told is not told twice.
- **The pattern** is a glob (`*`, `?`) over printable ASCII, 64 characters, validated strictly at Resolve and written into the subject in hex so the glob characters stay inside the header token set. Tag names outside `[A-Za-z0-9._/+-]` never match, so the Summary (repo and tag only) holds no free provider text; release name and notes go to Data, quoted.
- **Not built:** webhook pokes for releases (reads are every 5 minutes with ETag), registry images and package registries. Not verified against live GitHub: only the fake-server tests ran.

*Note, 2026-11-13 (#3970, review of the releases and tags sources).* Three corrections to the note above. (1) The tag floor must be the whole list: a name past the page cap, seeded or not, is taken for new the day a deletion moves it into view, so a tags watch on a repository with more than 300 tags is refused at Resolve instead of half-seeded (the known-name set is bounded at 2000). (2) The releases cursor keeps a mark, the newest `(published_at, id)` already reported, and a release is new only above both the floor and the mark; otherwise turning the newest release back into a draft made an older new one the best candidate again, and the engine, which remembers only the last version per type, told it twice. (3) A tag created again after being forgotten gets the version `name#<n>` (n counts the forgettings, starting at 2); with the bare name the engine would suppress it whenever that name was the last one told. The contract is now: a deleted-then-recreated tag is told again, and a tag is otherwise told once.

*Note, 2026-11-13 (#3970, second review round).* Raising the known-name bound did not remove the problem it postponed: a bound that drops names lets a still-listed old tag come back as new, and a generation map that stops growing silences a recreated tag. At the bound (2000 known names, or 2000 generation entries) the tags source now ends the watch with `ErrGone` ("tag history grew past what a watch can track; watch its releases instead") and changes nothing in the cursor, so nothing is dropped silently. The three reads that `goneConfirm` asks for come from the unchanged cursor, so the end is deterministic.

*Note, 2026-11-13 (#4007, turn identity for the hook-less kinds).* The 10 s settle window is now only the fallback. Before a TUI prompt is delivered, `captureTurnMark` reads where the kind's own source stands, and the successful working write ties it to its Rev (`markSessionWorkingFrom` → `status.RecordTurnMark`, a store of its own tagged with the record's Rev, so the next status write retires it and an Agent restart keeps it). An idle counts as that turn's end, on both routes (the list's stamp and `DriveState`'s notification), only when the source shows the new input after that position and a turn end after that input, and at once, so a turn that ends before the first poll is told without waiting. Per kind: copilot and cursor use byte offsets in `events.jsonl` / the transcript: a `user.message` / user row at or after the file size at the start, followed by a closing line (a late end of the previous turn, such as a shutdown flush, has no start before it and does not count); agy requires the executor run to end at or after the first user step (`step_type` 14) following the newest step idx at the start, and with no such step in a readable table the end is denied (only an unreadable table or column is unknown and falls back to the window). The start of the JSONL kinds is searched forward from the mark, never in the tail window, and never invented: file growth is not evidence of input. kiro's source is a pane scrape with no position, so it keeps the window. Where the identity cannot be read (no mark, a source smaller than the mark, an agy build without `executor_metadata`) the window applies, never "always idle". The baseline is read before the input is delivered (a turn can end before the working write that follows delivery), and only the two prompt-delivery sites record one. A readable source that denies the end is believed for as long as it does so: no elapsed time turns it into a completion (a tail read can show idle mid-turn after a very large line), so a turn whose input produced nothing leaves the stored "working" until the next input. Not verified against the live CLIs: the shapes come from the recorded fixtures.

*Note, 2026-11-13 (#4054, `pr.checks.failing`).* `pr.checks.failed` waits for every check of the head, so a fast job that fails while a slow one runs told a waiting session nothing. A new type `pr.checks.failing` is told once per head commit (version = the head sha) as soon as at least one check has failed while others are still pending; its Data names the checks failed so far, as for `pr.checks.failed`. `pr.checks.failed` and `pr.checks.completed` are unchanged. Choices:

- **`pr.checks.*` includes `failing`.** The prefix means "every checks event", and a name-only exception would be the one type a pattern silently skips. Cost: an existing `pr.checks.*` watch hears one extra notice per head that fails while checks still run, before its `completed` / `failed`; a session that wants the old behaviour names `pr.checks.completed` and `pr.checks.failed`. A watch with `until: first` now ends on `failing` when that comes first.
- **Told only while checks run.** When every check has finished, `failed` and `completed` carry the failure and `failing` is not emitted, so a finished failure is never three notices. A session that polls only after the finish sees no `failing`; that is `failed`'s job.
- **Once per head.** A second failure on the same head while others run keeps the version and is not told again; its name arrives with `failed` at the end. A re-run on the same head does not repeat `failing`.
- **GitHub and Bitbucket both.** GitHub reads it from the check runs and statuses it already fetches. Bitbucket's cache already folds a failed status into `Checks: failure` with `ChecksPending` set while others run, with the failed names in `FailingChecks`, so it needs no extra request.
- **The built-in PR feedback is unchanged.** It asks for `pr.checks.failed` by name and still tells a failure only after every check finished. `PRConditions` emits `failing` too, but no built-in selects it, a delivered `failing` leaves no tomb, and a watch on it does not stand in for the built-in's failure notice.
- Not verified against live GitHub or Bitbucket: only the fake-server and cache tests ran.
