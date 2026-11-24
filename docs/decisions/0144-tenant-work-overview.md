# 0144. A tenant work overview: who is running which ticket, published by the member, projected per viewer on the server, and never stored as history

English | [日本語](0144-tenant-work-overview.ja.md)

- Status: **proposed** (2026-11-24). A study for #3136; nothing is built. The decisions below are
  recommendations, and the owner confirms them through the open questions (#5057). Nothing was
  measured: every `file:line` was read at `051711e1d`, no tenant or live fleet was queried, and the
  cost figures are bounds read from code, not observations.
- Follow-ups: #5057 (open questions), #5058 (the build, parts 1 to 3).
- Related: [0111](0111-member-collaboration.md) (decisions 5, 6, 12 and addendum 3 and 4 are the rules
  this follows) / [0061](0061-work-item-inbox.md) (decisions 1, 2, 9, 26, 27) /
  [0078](0078-sessions-overview-pane.md) / [0055](0055-idle-stop-and-carried-interactions.md) /
  [0097](0097-session-retention.md) / [0104](0104-long-lived-member-workspace.md). ADR 0143 (the personal
  work-item board, #3135, proposed in PR #5055 and not merged when this was written) is the sibling study:
  it decides what a card and a lane are for one member, this decides what a member may let others see.
  Issues: #3134 (links), #5052 (PR facts on links), #1962 (the limit board, same opt-in model).

## Context

A tenant of 2 to 30 members (ADR 0111 addendum 5) has no way to see that two of them started the same
ticket, what the team is moving today, or who to hand a ticket to. The inputs now exist for one member's
own work: #3134 gave every session typed links to work items and pull requests. This study asks what the
CP may show *other* members, and how. What the tree has now, and what it lacks (all read at `051711e1d`):

1. **Links are mirrored tenant-wide, in a table meant for this.** `session_work_link` holds one row per
   (workspace, session, provider, folded key) with role, title, URL and, for pull requests, the last seen
   state (`control-plane/internal/store/migrations/0118_session_work_link.sql`, `0119_…_state.sql`). Its
   index is `(provider, item_fold)`, added "so the Control Plane can say which sessions are on an item
   without asking every Agent". `ReplaceSessions` swaps a workspace's sessions and links in one
   transaction (`internal/store/store_session_index.go:59-91`), and `DeleteWorkspace` removes them.
2. **The mirror is refreshed by more than the reaper.** ADR 0111 decision 6 named the reaper's
   `GET /sessions` pull. The reaper reaches only a running workspace of a tenant where
   `tierClocks.anyOn()` holds (`reaper.go:664`, `:722`). The showback usage sampler also reads the list of
   every running workspace and calls `mirrorSessions` (`usage.go:216`). It is on by default every five
   minutes (`main.go:532`; `AF_USAGE_SAMPLE_INTERVAL=0` switches it off), and it does not depend on any
   idle feature. The Console's list handler and the admin stats are the other two writers
   (`workspace_handlers.go:1189`, `admin_stats.go:181`). `mirrorSessions` orders them
   (`session_mirror.go:47`). Every replace stamps `last_seen` with the time of the read
   (`store_session_index.go:77`), so a running workspace's rows are at most about five minutes old, and a
   stopped workspace's rows keep the time of the last read while it ran.
3. **The mirror holds no activity.** `sessionRows` keeps `Alive` (as `running` | `stopped`) and `Carried`,
   and drops `sessionWire.State` (`workspace_handlers.go:1148-1164`, state at `:880`). Working, waiting
   for an answer and idle exist only in the live Agent list. The limit board already shows the pattern
   for reducing them: `limitSessionsOf` keeps a state and drops the name (`limit_board.go:193`).
4. **A key is a tenant-wide identity for GitHub and Bitbucket Cloud, and not for Jira.** The issue says
   work-item keys are already tenant-wide. `owner/repo#45` is, within one provider (a link is identified
   by provider and key, and the Agent keeps `github` and `bitbucket` keys apart on purpose,
   `links.go:206-213`). A Jira key `PROJ-12` carries no site (`jiraKeyRe`, `links.go:95`), and ADR 0061
   lets each member choose the one Jira site their connection uses. Two members on two sites can hold
   `PROJ-12` for two different tickets. The link's URL does carry the host when the Agent knew it
   (`WorkItemLink.URL`, optional).
5. **A key is not harmless.** `acme/payroll#45` or `SEC-9` tells a viewer that a private repository or a
   Jira project exists and that someone is working on it. ADR 0061 decision 2 keeps bodies off the CP,
   but a title or a key the CP holds is exactly what a viewer without access must not receive. The CP
   cannot ask the tracker whether a viewer may see an item: the tokens live in the Agents.
6. **Titles already have a per-viewer path.** Each member's inbox cache (`work_item_cache`) is filled with
   that member's own credentials, so a title in it proves that member could read the item. ADR 0061
   decisions 26 and 27 read a reference that is not in the cache live, with the member's own credentials,
   and store nothing.
7. **There is a share ACL for session names.** `effectivePermission` over
   `ListSessionSharesByRecipient` (`session_share.go:401`, `:535`) is what lets a member see another's
   session, and ADR 0111 decisions 5 and 12 require a session's name, title or link in any derived view to
   pass it.
8. **The limit board is the model for audience and reduction.** Publication is hidden by default, has an
   audience (the tenant or named members) and a level, and one function reduces a member's data for a
   viewer (`limitVisibleTo`, `boardEntry`; `limit_board.go:360`, `:378`). A hidden member and a
   never-observed member look the same. Tenant admins can switch the board off, and no SSE or export path
   carries it (ADR 0111 addendum 4 and the 2026-10-12 note).

The question is which of these to reuse, what a member may publish, and what has to be added.

## Decisions

### 1. A row is one driving link of one session of one publishing member.

The overview answers "who is running which ticket". A session counts for an item when it holds a link
with a *driving role*: `started`, `created` or `working` (`links.go:21-25`; the same definition as
ADR 0143 decision 1). `watching` and `reviewing` links are not published in P1: a child that watches a
ticket is not running it, and ADR 0111 decision 6 already said that a state says nothing beyond itself.
A session with no driving link has no row. The overview groups rows by item, by member and by repository
(decision 9). It stores no group, no lane and no claim.

### 2. An item is identified by provider, site and folded key.

- **Site** is the host of the link's URL when it has one, and otherwise the provider's cloud host
  (`github.com`, `bitbucket.org`). A Jira link with no URL has an *unknown* site.
- Two rows are the same item only when provider, site and folded key are equal. A row whose site is
  unknown is its own group, never merged into a group with a known site and never dropped. The Console
  says "site unknown" on it. This is the answer to fact 4: a wrong merge reads as "two members are on one
  ticket" and sends them to talk about different tickets.
- The CP derives the site at write time from the URL it already stores and keeps only the host. No URL is
  published (decision 4).

### 3. The source is the session mirror. No new pull, no new push path.

- ADR 0111's rejection of an Agent-to-CP push stands, and nothing is added to the Agent.
- **One additive column, `activity`,** on the session mirror, written by `sessionRows` from
  `sessionWire.State` through the same writers as today (fact 2). It holds one of `working`, `waiting`,
  `idle`, `unknown` (decision 4). The migration adds a defaulted column and drops or rewrites nothing, so
  it ships the way `0119` did. The replace already runs for every writer; this adds one bound value per
  row and no write.
- **The reaper is not the source.** It would leave out every tenant with no idle feature on. The mirror
  covers every running workspace while the sampler runs, and says how old each row is (decision 8).
  When the sampler is off, only a Console list read refreshes a workspace, and rows go stale and are
  labelled so.
- A stopped workspace draws from the mirror, as of its last run. A workspace that was deleted has no
  rows.

### 4. What a member publishes, field by field. Nothing else.

| Field | Source | Notes |
|---|---|---|
| member | the membership | already visible in the tenant roster |
| provider, key, site | the link (decision 2) | a key is published only for a namespace the member allowed (decision 5) |
| role | the link | one of the three driving roles |
| `activity` | the mirror column | `working` (working, compacting), `waiting` (question, plan, permission, blocked, auth), `idle` (idle, limited, spend_limit), `unknown`; `stopped` when the session is not alive |
| `observedAt` | the row's `last_seen` | only at the *state* level (decision 5) |
| PR number and state | a PR link's key and `state` | P3, after #5052 gives the state an observation time |

Never published, whatever the level:

- **the session's name, title, label, directory, branch, kind, model or origin;** a name appears only
  through the share ACL (decision 7).
- **the item's title and URL.** The CP's `title` and `url` columns exist for the owner's own Console and
  are never part of the projection (decision 6).
- **what a session is waiting for.** `waiting` carries no question text, no tool, no permission kind.
  `limited` and `spend_limit` read as `idle`: a viewer must not learn about billing trouble, as the limit
  board's "state visible by default" rejection says.
- **`carried` interactions, conversation, changed files, check names or reviewer names.**

### 5. Publication is opt-in, per tenant membership, with an audience, a level and a consent per namespace.

This is ADR 0111 decision 6's rule set applied to a new payload. A limit-board audience does not imply it,
as decision 12 says for edit-overlap.

- **Hidden until chosen.** Without a publication row, a member has no row in anyone's overview.
- **Audience:** the tenant, or named members. One setting per tenant membership (addendum 3); what is
  published to one tenant never reaches another.
- **Level:** `tickets` (who is on which item, with "current" or "stale" only) or `state` (adds `activity`
  and `observedAt`). The default on opt-in is `tickets`. A time stamp on a row is a working-hours signal,
  which is why it is a level and not a given.
- **Consent per namespace.** A namespace is `owner/repo` on GitHub and Bitbucket, and the project key on
  Jira, each with its site. A key is published only when its namespace is on the member's list. When the
  member opts in, the Console lists the namespaces their sessions link to now, with a plain preview of
  what a viewer would see (the same function as decision 6, run for a chosen viewer), and the member
  ticks the ones to publish. A namespace that appears later is not published until the member has
  decided; the settings screen shows "N new projects waiting". This is decision 12's "two consents per
  member and repository", and it is why a private repository's name does not travel on a default.
- **A session can be hidden by itself.** One flag per session that removes its rows whatever the
  namespaces say. It is for a ticket that must not be talked about (a security incident, a personnel
  matter). Open question 2 asks whether it is needed in the first part.
- **Tenant switch.** Admins can switch the overview off for the tenant, on by default, like the limit
  board (addendum 4). **Admins get no wider view**: an admin is a viewer like any other, and admin pages
  (docs/67, docs/83) do not gain a "who runs what" column. This answers the issue's first question, and
  the answer stays the one ADR 0111 gave.

### 6. One function reduces a member's rows for a viewer, and the Console never receives the rest.

- `overviewEntry(publication, found, viewerMembershipID, rows, now)` is the one place another member's
  rows are reduced, written the way `boardEntry` is. It returns the empty view without reading anything
  else when the viewer is not in the audience, and **that empty view is what a hidden member, a member
  who never published, a member whose tenant switch is off and a member with nothing in the allowed
  namespaces all look like.** A viewer cannot tell them apart.
- **A member always sees their own rows**, for every namespace and at the `state` level. Own rows need no
  publication. The publisher's preview is this function run with another member as the viewer.
- **No title, no URL, no session name leaves the CP in the projection.** The Console joins the published
  key against what it already has:
  1. its own inbox cache, which holds the viewer's own readable items with their titles and URLs;
  2. a press-to-read through ADR 0061 decisions 26 and 27, which reads the reference live with the
     viewer's own credentials, stores nothing, and works only while the viewer's workspace runs;
  3. otherwise the bare key.
  The access check is the tracker's, with the viewer's own token, never the CP's or the publisher's.
  The cost is that most rows show a bare key. That is accepted: it is also the honest display of "the CP
  does not know whether you may see this".
- **No history.** The projection reads the mirror as it is. There is no log of who was on which item, and
  no "last week" view (decision 10).
- **Bounded.** One request returns at most 500 rows, with `truncated: true` when it cut. The mirror caps a
  session at 51 links (`SessionLinksFromJSON`), and a tenant has 2 to 30 members, so the cap is a guard,
  not a limit a team reaches.

### 7. A session's name and link follow the share ACL, and the overview never grants.

A row carries an opaque session reference only when the viewer already has access under
`effectivePermission` on the catalog entry for that (workspace, session). The Console then shows the name
and an "open" action through the existing shared-session path. Without access the row says "a session of
<member>" and carries nothing that identifies the session. The overview creates, widens and implies no
share (ADR 0111 decision 5). The ACL is evaluated on every read, so an unshared session turns back into
the anonymous form on the next one.

### 8. Honest about age: three times, and a horizon.

- **`observedAt` is the mirror's `last_seen`:** when the CP last read this workspace's list, not when the
  session last did something. A stopped workspace keeps its last-read time, and its rows carry
  `activity: stopped`.
- **Stale after 60 minutes** (the limit board's threshold, addendum 6). A stale row keeps its content and
  is marked, in the Console as "last seen N min ago" at the `state` level and as "not current" at the
  `tickets` level.
- **A horizon of 7 days.** A row whose `last_seen` is older is not projected at all. Otherwise a member
  who stopped working a month ago would sit on a ticket forever, and the ticket would read as taken. The
  7 is a first number chosen without data; ADR 0143 open question 5 makes the same point about a
  threshold. Open question 3.
- **Age never orders rows or moves one between groups.**

### 9. Three views, one hint, and no claim of absence.

- **By ticket** ("who is on PROJ-123"), **by member**, **by repository** (the namespace of decision 5).
- **"Two members on one ticket"** is shown when at least two *different members'* driving rows for the
  same item (decision 2) are in the viewer's projection. It is derived from
  the rows the viewer may see and from nothing else, so it never reveals a session the viewer's audience
  does not cover. Two sessions of one member (a parent and its child, ADR 0073) are not a hint.
- **The overview never says "nobody else is on this".** Unpublished members, hidden sessions, stale rows,
  watchers, and sessions that never recorded a link are all missing from it, and an empty group reads
  "nobody *shown* here". It is named a hint, never a lock or a conflict (decision 12's "never says no
  overlap").
- **No ranking, no count of tickets per person, no time-per-ticket.** Ranking members is what ADR 0111
  decision 6 declined for limits and it is declined here for the same reason.
- A nudge at launch (ADR 0061 decision 9's duplicate-start guard, "B is on this ticket") may reuse the
  projection later. It is not part of this study.

### 10. Retention: nothing beyond the mirror, and the mirror already has an end.

This answers the issue's second question.

| Thing | Ends when |
|---|---|
| A row | the Agent stops listing the session or its link (the next replace removes it), or the horizon passes |
| A stopped workspace's rows | they stay as of the last run until the horizon (decision 8), then drop out of every projection |
| A deleted or archived session | the next list from the Agent no longer has it |
| A deleted workspace | `DeleteWorkspace` removes the links with it |
| The publication, namespace list, viewers and hidden flags | the membership is removed; unpublishing deletes the publication, and leaves the mirror, which is the member's own data |
| A removed viewer | the audience row goes, and the next read is the empty view |

There is no table of past rows, so "who worked on PROJ-123 last month" has no answer here. A history
would be a working-hours log, and it would need its own consent and its own ADR.

### 11. A new ADR, not a phase of ADR 0111, and not a Console-side derivation.

- ADR 0111 P2 lists `workItem` / `pr` *attachments*: a message in the inbox that cites an item. This is a
  different object (a standing publication read by many), and it needs decision 12's audience model,
  which 0111 left to a separate ADR. This ADR is that one for tickets. Nothing in 0111 is amended; when
  this is accepted, its Follow-ups line gets a pointer.
- ADR 0143 derives lanes in the Console from the member's own data. Another member's data must be reduced
  on the server before it leaves (decision 6, and 0111 decision 12). The two share vocabulary (driving
  roles, PR state), not code. When a tenant card needs 0143's lane for a rollup, it takes the lane rule
  as a table, not as an import.
- Delivery is REST read while the view is open. No SSE and no export path carries it in P1; any such path
  has to go through `overviewEntry`.

## Phases

Each part leaves `main` releasable alone, with the new behaviour unreachable or hidden.

1. **Part 1, backend.** The `activity` column and its writer; the publication, namespace, viewer and
   hidden-session tables; `overviewEntry`; the REST read and the publication routes behind the tenant
   switch. Nobody has published, so every read is the empty view. The tests in "Acceptance" below.
2. **Part 2, the member's side.** Settings: opt-in, audience, level, the namespace list with the preview,
   the per-session hide.
3. **Part 3, the viewing side.** The three views, the hint, the title join of decision 6. PR chips wait
   for #5052 (the facts and their observation time) and #5053 (held PRs observed), so a merged PR does
   not read as open.

### Acceptance (part 1)

- A hidden member, a never-published member, an outside-the-audience viewer, a tenant switch off and an
  empty namespace list return byte-identical answers.
- A sentinel in each of title, URL, session name, label, directory, branch, kind and `carried` of a
  seeded session never appears in any projection, at either level (a golden, the way the limit board's
  tests do it).
- A key outside the allowed namespaces is absent; a new namespace stays absent until it is allowed.
- A removed viewer, a left member and a deleted workspace stop appearing on the next read.
- Two Jira rows with the same key and different sites are two groups; a row with an unknown site is a
  third and merges with neither.
- A row past 60 minutes is marked and past 7 days is gone; a stopped workspace's rows read `stopped`.
- A session the viewer has no share on carries no session reference; sharing it adds one on the next read
  and unsharing removes it.
- The hint appears for two members and not for one member's two sessions, and never from a row outside the
  viewer's projection.
- An admin who is not in an audience gets the empty view.

## Open questions

These are the owner's, collected on #5057. Each has a recommendation above.

1. **Namespace consent: per namespace, or publish all and exclude** (decision 5)? Recommended: per
   namespace. The alternative is simpler to set up and leaks a new private repository's name until the
   member notices.
2. **Is the per-session hide part 1 or later?** Recommended: part 2, with the settings screen. Without it
   a member who must hide one ticket has to take its whole namespace off.
3. **The numbers** (decision 8): 60 minutes to stale, 7 days to the horizon. Recommended: ship them and
   look at a real tenant after part 3.
4. **Where the view lives.** Recommended: a *Team* view beside the limit board's home, not inside the
   personal overview pane, so ADR 0143's board stays one member's.
5. **Do `watching` and `reviewing` ever show** on the tenant view? Recommended: no, not in this ADR.

## Alternatives set aside

- **Publish titles from the CP.** The mirror holds them, and it is one `SELECT`. It sends a private
  repository's or Jira project's titles to someone who never had access. Decision 6 joins on the viewer's
  side instead.
- **Ask the tracker, from the CP, whether the viewer may see the item.** The CP holds no member's tracker
  token (ADR 0061 decision 3: the Agent owns it). Adding one is a credential path this feature does not
  justify.
- **Take the reaper's pull as the source** (ADR 0111 decision 6's wording). It misses every tenant with no
  idle feature on and runs serially once a minute. The mirror it also feeds is already tenant-wide.
- **A new Agent→CP push or a new Agent field.** Rejected in ADR 0111 for the same reason it is not needed.
- **State visible by default, or all namespaces by default.** The first is ADR 0111's own rejection. The
  second writes a private repository's name to the tenant the first time a session links to it.
- **Group by working copy or by changed paths.** Needs repository identity across members, which
  `workingCopyId` (per owner) does not give (ADR 0111 decision 12). Item keys do not need it.
- **Source rows from `work_item_session`.** It records the Console's launches only and holds no PR, no
  child's link and no session that started elsewhere (ADR 0143 fact 6).
- **Keep a history** so "who worked on this last month" can be answered. A working-hours log: decision 10.
- **Stronger tenant admin visibility.** The answer in ADR 0111 has been no, and nothing here changes why.
- **Derive a "conflict" or "taken" claim.** The data cannot support it (decision 9).

## Consequences

- Another member's work is visible only as far as the member chose, per namespace, per audience, per
  level, and the viewer cannot tell a choice not to publish from nothing to publish.
- The overview is as fresh as the usage sampler (five minutes by default) or the last Console list read,
  and says so; it is blank for a member who never published, even when that member is on your ticket.
- Titles are mostly absent for tickets outside the viewer's own queries. That is the price of never
  sending a title the viewer may not read.
- Jira keys without a known site never merge, so two members on one Jira ticket whose links carry no URL
  are not told so. A hint that fails quietly is the side to err on.
- The mirror gains one defaulted column and no new write. Nothing else in the Agent or the Console changes
  before part 2.
