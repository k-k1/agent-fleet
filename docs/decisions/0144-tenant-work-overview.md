# 0144. A tenant work overview: who is running which ticket, published by the member, projected per viewer on the server, and never stored as history

English | [日本語](0144-tenant-work-overview.ja.md)

- Status: **proposed** (2026-11-24). A study for #3136; nothing is built. The decisions below are
  recommendations, and the owner confirms them through the open questions (#5057). Nothing was
  measured: every `file:line` was read at `051711e1d`, no tenant or live fleet was queried, and the
  cost figures are bounds read from code, not observations.
- Follow-ups: #5057 (open questions), #5058 (the build, parts 1 to 4).
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
2. **The mirror has three writers, and the reaper is not one of them.** ADR 0111 decision 6 named the
   reaper's `GET /sessions` pull as the limit board's source. The reaper reads the list but keeps only
   limit observations from it (`reaper.go:752-759`, `limit_board.go:228`), and only for a running
   workspace of a tenant where `tierClocks.anyOn()` holds (`reaper.go:664`, `:722`). The mirror is written
   by the showback usage sampler (`usage.go:216`), by the Console's list handler and the events stream that
   shares its read (`workspace_handlers.go:1189`, `events.go:227`), and by the admin session list
   (`admin_stats.go:181`). The sampler visits every running workspace whatever idle features the tenant
   has. It is on by default every five minutes (`main.go:532`; `AF_USAGE_SAMPLE_INTERVAL=0` switches it
   off), and it writes only when the Agent answers, so a slow or silent Agent leaves its rows older than
   five minutes. `mirrorSessions` orders the writers by the start of their read (`session_mirror.go:47`).
   `last_seen` is stamped with `NowTS()` when each row is inserted (`store_session_index.go:77`), after the
   read and after any wait for the database, not with the read's start.
3. **The mirror holds no activity.** `sessionRows` keeps `Alive` (as `running` | `stopped`) and `Carried`,
   and drops `sessionWire.State` (`workspace_handlers.go:1148-1164`, state at `:880`). Working, waiting
   for an answer and idle exist only in the live Agent list. The limit board already shows the pattern
   for reducing them: `limitSessionsOf` keeps a state and drops the name (`limit_board.go:193`).
4. **A key is a tenant-wide identity for GitHub and Bitbucket Cloud, and not for Jira.** The issue says
   work-item keys are already tenant-wide. `owner/repo#45` is, within one provider (a link is identified
   by provider and key, and the Agent keeps `github` and `bitbucket` keys apart on purpose,
   `links.go:206-213`). A Jira key `PROJ-12` carries no site (`jiraKeyRe`, `links.go:95`), and ADR 0061
   lets each member choose the one Jira site their connection uses. Two members on two sites can hold
   `PROJ-12` for two different tickets. The link's URL does not settle which site: the launch item's link
   carries none (`LinksOf`, `links.go:125-131`), and a hand-entered URL is checked only for https, a host
   and no userinfo (`sessionx/session_work_links.go:105-111`), not for pointing at that provider and key.
   The Jira site a member uses lives in their Agent's connection (`workspace/agent/connections_jira.go`),
   not in the CP.
5. **A key is not harmless.** `acme/payroll#45` or `SEC-9` tells a viewer that a private repository or a
   Jira project exists and that someone is working on it. ADR 0061 decision 2 keeps bodies off the CP. A
   title is exactly what a viewer without access must not receive, and a key is a smaller disclosure of
   the same kind. The CP cannot ask the tracker whether a viewer may see an item: the tokens live in the
   Agents. A key can therefore be shown on the publisher's consent and chosen audience, not on the
   viewer's access (decision 5), which is the footing of ADR 0111 decision 12's paths.
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

### 2. An item is identified by provider, site and folded key, and an unknown site never merges.

- **Site** for GitHub and Bitbucket is the provider's cloud host, which the provider fixes. For Jira it
  is the site of the *publisher's own Jira connection*, recorded by the Agent on the link. The Agent
  writes no such field today (fact 4), so until it does (part 4) every Jira site is *unknown*.
  **A link's URL is not a source of the site**: it is optional, absent on the launch item, and not
  verified against the key.
- **The Agent records a site only where the item and the site are fixed together:** a launch from an
  inbox row, and a link made from an item the Agent has just read through its connection. A key typed by
  hand, a key inferred from a branch or a pull request title (`session_inferred_links.go:63`) and every
  link that already exists stay *unknown*, and a later change of the member's Jira connection never
  re-labels them. Otherwise a member who switches from site A to site B and types A's `PROJ-12` would
  publish it as B's.
- Two rows are the same item only when provider, folded key and a *known* site are equal. A row whose
  site is unknown is its own group by ticket, merged with no other row (not even another unknown one), and
  never dropped from its owner's view. The Console says "site unknown" on it. A wrong merge reads as "two members are on one ticket"
  and sends them to talk about different tickets, which is worse than a missing hint.
- **A Jira row whose site is unknown is shown to its owner only,** and to no other member, whatever the
  publication says. A consent given for a project key cannot say which site it covers, and a member who
  switches Jira connection would publish the new site's `SEC-9` under the consent given for the old
  site's `SEC`; the link carries nothing that tells the two apart. So in parts 1 to 3 Jira has no
  cross-member rows, grouping or hint at all, and GitHub and Bitbucket have all three. Part 4 adds one
  additive field to the Agent's link, `site`, taken from the Agent's own Jira connection and never from
  a URL; only a Jira row with a known site can be published, and only under a (site, project) consent.
  A row whose provenance stays unknown after part 4 stays owner-only. An earlier, explicit consent for
  "this project key on whichever site I use" is the alternative, and is open question 8.
- **Display groups use the same identity.** By ticket: (provider, known site, folded key), and an
  owner's own unknown-site Jira row is a group of its own, one per row. By repository: `provider` plus
  `owner/repo` for GitHub and Bitbucket; for Jira, the known site plus the project key, and an owner's
  unknown-site rows grouped by (owner, project key), a group that never includes a known-site row. Neither
  view ever puts two members' unknown rows together.

### 3. The source is the session mirror. No new pull, no new push path.

- ADR 0111's rejection of an Agent-to-CP push stands. Parts 1 to 3 add nothing to the Agent; only
  cross-member Jira grouping needs one additive link field (decision 2, part 4).
- **Two additive columns** on the session mirror, written through the same writers as today (fact 2):
  - `activity`, from `sessionWire.State` in `sessionRows`: one of `working`, `waiting`, `idle`,
    `unknown` (decision 4).
  - `observed_at`, the start of the read behind the write: the `readStart` that `mirrorSessions` already
    holds, handed to `ReplaceSessions`. `last_seen` stays as it is for its other readers, because it is
    stamped at insert and can be later than the observation. A row written before the migration has an
    empty `observed_at` and is not projected until the next replace, one refresh later.

  The migration adds defaulted columns and drops or rewrites nothing, so it ships the way `0119` did. The
  replace already runs for every writer; this adds two bound values per row and no write.
- **The reaper is not a source** (fact 2). The sources are the sampler, the Console's list reads and the
  admin list. Five minutes is the sampler's default attempt interval, not a promise: a failed read
  leaves rows as they were. With the sampler off, the other two still refresh a workspace, and every row
  says how old it is (decision 8).
- **A stopped workspace is judged at projection, not from its rows.** When the workspace's recorded state
  (`store.Workspace.State`) is not running, every row of it projects as `stopped` whatever activity the
  last read saw, and `observed_at` stays the last read, never moved by the stop. A session that was
  `working` in the last mirror before its workspace stopped therefore reads `stopped`. A workspace that
  was deleted has no rows. **`stopped` wins over `unknown`.** A row is *stopped* when the workspace's recorded state is not running
  or its session was not alive in the last list read. The activity of a stopped row is `stopped`, whatever the age of its
  observation (an observation two hours old in a workspace recorded as stopped, or of a session that was not alive, reads `stopped`);
  otherwise `unknown` when the row is not `current`; otherwise the mirror's activity. The recorded
  state is what the CP writes at start and stop
  (`workspace_handlers.go:735`, `:744`, `:787`; the stop path drops a failed database update, and a
  start can record `running` while the runtime is still `starting`), so it is not a guarantee. Two rules
  keep that from showing an old `working`: the activity last observed in the mirror is used only while the
  row is `current` (decision 8), and a row older than 60 minutes that is not stopped shows
  `unknown`. Across a quick stop and restart, a row
  can show the last observed activity for up to 60 minutes, with its `observedAt` at the `state`
  level. That is a known limit, not a claim of liveness. Part 1 tests a stop with a `working` last
  mirror, and a restart before the first new read.

### 4. What a member publishes, field by field. Nothing else.

| Field | Source | Notes |
|---|---|---|
| member | the membership | already visible in the tenant roster |
| provider, key, site | the link (decision 2) | a key is published only for a namespace the member allowed (decision 5); a Jira site is unknown until part 4 |
| role | the link | one of the three driving roles |
| `activity` | the mirror column | `working` (working, compacting), `waiting` (question, plan, permission, blocked, auth), `idle` (idle, limited, spend_limit), `unknown` (a row that is not `current` and not stopped); `stopped` when the session is not alive or its workspace is not running, which wins over `unknown` (decision 3); only at the *state* level |
| `observedAt` | the row's `observed_at` | only at the *state* level (decision 5) |
| `current` | derived: `observed_at` within 60 minutes | at the *tickets* level this is the only time-shaped field |
| PR number and state | a PR link's key and `state` | P3, after #5052 gives the state an observation time |

**A key is validated on the CP before anything else.** The CP accepts a row only for a known provider
and a key that matches that provider's whole shape (`owner/repo#N` for GitHub and Bitbucket, a project
key and number for Jira, the shapes of `links.go:95-98`). The mirror does not guarantee this
(`store_session_links.go:37`) and the launch key is only trimmed and clipped (`cleanWorkItemRef`,
`session_handlers.go:693`). A bare number, a key with free text after it, or an unknown provider is
never published and never matches a namespace, so text that follows an allowed repository name cannot
travel as a key.

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

- **Hidden until chosen.** Without a publication row, a member has no row in any *other* member's
  overview (their own are decision 6's).
- **Audience:** the tenant, or named members. One setting per tenant membership (addendum 3); what is
  published to one tenant never reaches another.
- **Level:** `tickets` (who is on which item, with `current` only) or `state` (adds `activity` and
  `observedAt`). The default on opt-in is `tickets`. A time stamp on a row is a working-hours signal,
  which is why it is a level and not a given. Issue #3136 asks for `observedAt` on every row; `tickets`
  departs from that on purpose. It is not time-free: a row stays `current` while the CP's reads of the
  workspace succeed, and a viewer who watches it lose `current` learns, to within their polling interval,
  roughly when the last successful read began. That is not the moment the workspace stopped, and a
  transient failure of the read has the same effect. The preview says so ("viewers can tell roughly when
  your workspace was last seen"). Open question 7.
- **Consent per namespace.** A namespace is `owner/repo` on GitHub and Bitbucket (per provider), and for
  Jira the project key *together with the site*, so no Jira namespace exists before part 4 records sites,
  and a Jira row with an unknown site is never published (decision 2). A consent given for one site
  never covers another: a member who switches from site A to site B publishes nothing of B's until they
  allow the (B, project) pair. A key is published only when its namespace is on the member's list. When the
  member opts in, the Console lists the namespaces their sessions link to now, with a plain preview of
  what a viewer would see (the same function as decision 6, run for a chosen viewer), and the member
  ticks the ones to publish. A namespace that appears later is not published until the member has
  decided; the settings screen shows "N new projects waiting". This is decision 12's "two consents per
  member and repository", and it is why a private repository's name does not travel on a default.
- **A key is disclosed on this consent, not on the viewer's tracker access.** Nothing can check the
  viewer's access (fact 5), so everyone in the audience sees the key `acme/payroll#45` of an allowed
  namespace whether or not they can open it. The preview says "everyone in <audience> will see these
  keys, whether or not they can open them". Open question 6 asks the owner to confirm this footing.
- **A session can be hidden by itself.** One flag per session that removes its rows from other members'
  views whatever the namespaces say (the owner still sees it). It is for a ticket that must not be talked
  about (a security incident, a personnel matter). Open question 2 asks whether it is needed in the first
  part.
- **Tenant switch.** Admins can switch the overview off for the tenant, on by default, like the limit
  board (addendum 4). **Admins get no wider view**: an admin is a viewer like any other, and admin pages
  (docs/67, docs/83) do not gain a "who runs what" column. This answers the issue's first question, and
  the answer stays the one ADR 0111 gave.
- **Order of precedence,** for a read by viewer V of member M's rows:
  1. the tenant switch is off: the empty view, for everyone, V's own rows included;
  2. every row, whoever the viewer, is dropped when it has no `observed_at` or is past the horizon
     (decision 8), and when its key fails the validation above;
  3. V is M: all the remaining rows, every namespace, hidden sessions included, at the `state` level;
  4. M has no publication, or V is not in M's audience: the empty view;
  5. otherwise a row is shown only if its namespace is allowed and its session is not hidden.
- **Withdrawing consent takes effect on the next read, for rows and for everything derived from them.**
  Hiding a session, removing a namespace, removing a viewer and unpublishing each take their rows out,
  and the groups and the hint (decision 9) are then *recomputed from the rows that remain*. Two members
  becoming one removes the hint; three becoming two keeps it; the last row leaving removes the group.
  Unpublishing deletes the publication, its namespaces, its viewers and its hidden flags. Publishing
  again starts from nothing and shows the preview afresh. The server enforces all of this; the
  settings screen only edits the same records, and **the publication write routes ship with that
  screen (part 2)**, so nobody can publish before a preview exists.

### 6. One function reduces a member's rows for a viewer, and the Console never receives the rest.

- `overviewEntry(publication, found, viewerMembershipID, rows, now)` is the one place another member's
  rows are reduced, written the way `boardEntry` is. It returns the empty view without reading anything
  else when the viewer is not in the audience, and **that empty view is what a hidden member, a member
  who never published, and a member with nothing in the allowed namespaces all look like.** A viewer
  cannot tell them apart. (The tenant switch off is the same view for everyone, decision 5.)
- **A member sees their own rows** by the precedence in decision 5: no publication is needed, and the
  switch still wins. The publisher's preview is this function run with another member as the viewer.
- **No title, no URL, no session name leaves the CP in the projection.** The Console joins the published
  item against what it already has, on decision 2's identity:
  1. its own inbox cache, which holds the viewer's own readable items with their titles and URLs;
  2. a press-to-read through ADR 0061 decisions 26 and 27, which reads the reference live with the
     viewer's own credentials, stores nothing, and works only while the viewer's workspace runs;
  3. otherwise the bare key.
  The access check is the tracker's, with the viewer's own token, never the CP's or the publisher's.
  **A Jira row joins only when its site is known and equals the site of the viewer's own connection.**
  Otherwise a bare `PROJ-12` could pick up the title of a different ticket of the viewer's site. In parts
  1 to 3 every Jira site is unknown, so Jira rows show the bare key and offer no press-to-read. The cost is
  that most rows show a bare key. That is accepted: it is also the honest display of "the CP does not know
  whether you may see this".
- **No history.** The projection reads the mirror as it is. There is no log of who was on which item, and
  no "last week" view (decision 10).
- **Bounded, after the projection.** One request returns at most 500 rows. The cap counts only rows the
  viewer may see (after the rules of decision 5), keeps them in a fixed order, and sets `truncated: true`
  only when visible rows were cut. **The order uses no time at all:** one fixed order for every row, (member, session, key),
  because a response mixes rows published at `tickets` and at `state`. A re-observation alone therefore
  never changes which keys a viewer gets, as long as the same rows are inside the horizon.
  Groups and the hint are computed from the returned rows. A hidden or
  expired row therefore cannot push a visible one out or flip `truncated`. The mirror caps a session at
  51 links (`SessionLinksFromJSON`), and a tenant has 2 to 30 members, so the cap is a guard. It can be
  reached (ten members with fifty links each), and then it decides which rows are cut, by the fixed
  order and never by age.

### 7. A session's name and link follow the share ACL, and the overview never grants.

A row carries an opaque session reference only when the viewer already has access under
`effectivePermission` on the catalog entry for that (workspace, session). The Console then shows the name
and an "open" action through the existing shared-session path. Without access the row says "a session of
<member>" and carries nothing that identifies the session. The overview creates, widens and implies no
share (ADR 0111 decision 5). The ACL is evaluated on every read, so an unshared session turns back into
the anonymous form on the next one.

### 8. Honest about age: three times, and a horizon.

- **`observedAt` is the mirror's `observed_at`** (decision 3): when the CP's read of this workspace's list
  began, not when the session last did something. A stopped workspace keeps its last-read time, and its
  rows read `stopped`.
- **Stale after 60 minutes** (the limit board's threshold, addendum 6). A stale row stays listed and is
  marked, as "last seen N min ago" at the `state` level and "not current" at the `tickets` level, and its
  activity reads `unknown` unless the row is stopped (decision 3). **A stale row does not count toward the hint** (decision 9): two
  members linked on one ticket, one of them seen an hour ago, is not "two members on one ticket".
- **A horizon of 7 days on the age of the observation.** A row whose `observed_at` is older is not
  projected at all. Otherwise a workspace that stopped a month ago would leave its members on tickets
  forever. This is not a judgement that someone stopped working: a running workspace keeps refreshing,
  so a forgotten session in it stays listed as `idle`, and the overview does not guess abandonment. A
  member who has put a ticket down says so by hiding the session or removing the link. The 7 is a first
  number chosen without data; ADR 0143 open question 5 makes the same point about a threshold. Open
  question 3.
- **Age never orders rows, picks which are cut by the cap, or moves one between groups.** The only effect
  of age on the set of rows is the horizon itself: a row past 7 days is not a candidate until a new
  observation brings it back.

### 9. Three views, one hint, and no claim of absence.

- **By ticket** ("who is on PROJ-123"), **by member**, **by repository**, grouped by the display keys of
  decision 2 (not by the consent namespace alone, which has no site while Jira sites are unknown).
- **"Two members on one ticket"** is shown when at least two *different members'* current (not stale)
  driving rows for the same item (decision 2, known site) are in the returned rows. It is derived from the
  rows the viewer may see and from nothing else, so it never reveals a session the viewer's audience
  does not cover. Two sessions of one member (a parent and its child, ADR 0073) are not a hint, and
  neither are two Jira rows whose site is unknown.
- **The overview never says "nobody else is on this".** Unpublished members, hidden sessions, rows past
  the 7-day horizon, watchers, and sessions that never recorded a link are all missing from it, and an
  empty group reads
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
| A stopped workspace's rows | they stay as of the last run, projected as `stopped`, until the horizon (decision 8), then drop out of every projection |
| A deleted or archived session | the next list from the Agent no longer has it |
| A deleted workspace | `DeleteWorkspace` removes the links with it |
| The publication, namespace list, viewers and hidden flags | the membership is removed, or the member unpublishes: all four are deleted together, and the mirror stays, which is the member's own data |
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

1. **Part 1, backend.** The `activity` and `observed_at` columns and their writers; the publication,
   namespace, viewer and hidden-session tables; the `overviewEntry` function with the server-side
   enforcement of decisions 4 to 6 and 8; the REST **read** behind the tenant switch. It ships **no
   write route for a publication**, so nobody can publish before part 2's preview exists; the tests
   seed the tables directly. No Console shows anything, and a read returns the caller's own rows and
   nothing of anyone else's.
2. **Part 2, the member's side.** The publication write routes and the settings: opt-in, audience, level,
   the namespace list with the preview, the per-session hide. Question 2 is about when the hide's *screen*
   comes; the server's enforcement of it is in part 1.
3. **Part 3, the viewing side.** The three views, the hint, the title join of decision 6. PR chips wait
   for #5052 (the facts and their observation time) and #5053 (held PRs observed), so a merged PR does
   not read as open.
4. **Part 4, Jira site.** The additive `site` field on the Agent's link, recorded only where item and
   site are fixed together (decision 2), mirrored into `session_work_link`. Only then can a Jira row be
   published, under a (site, project) consent (decision 5), and group across members and join titles. The
   Agent and the mirror identify a link by provider and key (`links.go:146`,
   `store_session_index.go:103`), so one session cannot hold the same key from two sites: a known limit
   of this part, which concerns separate sessions.

### Acceptance

Part 1 (server):

- Byte-identical answers for one fixed viewer V who has the same own rows in each case: a member who is
  hidden, one who never published, one whose audience excludes V, and one with an empty namespace list.
  The tenant switch off is compared separately: it answers the empty view to everyone, V's own rows
  included.
- A row with no `observed_at`, a row past 7 days, or a key that fails decision 4's validation (a bare
  number, free text after an allowed repository, an unknown provider) is absent for every viewer,
  the owner included.
- A sentinel in each of title, URL, session name, label, directory, branch, kind and `carried` of a
  seeded session never appears in any projection, at either level (a golden, the way the limit board's
  tests do it).
- A key outside the allowed namespaces is absent; a new namespace stays absent until it is allowed.
- The precedence of decision 5, one case each: the tenant switch off (empty for everyone, the owner
  included); the owner with no publication (own rows, every namespace, hidden sessions too); another
  viewer of the same member (empty); an audience member (allowed namespaces, no hidden session).
- With visible rows seeded, hiding a session, removing its namespace, removing the viewer and
  unpublishing each take the withdrawn rows out of the viewer's *next* response, and the groups and the
  hint are recomputed from what remains: two members becoming one removes the hint, three becoming two
  keeps it, the last row leaving removes the group. After unpublishing and publishing again, nothing is
  shown until consent is given anew.
- A removed viewer, a left member and a deleted workspace stop appearing on the next read.
- A Jira row with an unknown site is shown to its owner only: another viewer in the audience, with the
  project key allowed or not, never receives it. In neither view is it merged with another member's row
  or grouped with a known-site row. It is tested with the same key from two members (never together), and
  with two different keys of one project from one member: two groups by ticket, one group by (owner,
  project) by repository. Two GitHub rows with the same key from two members are one
  group.
- Activity precedence, for a stopped workspace and, separately, for a stopped session in a running
  workspace: a stopped row reads `stopped` at 5 minutes and at 2 hours of age; a row that is not stopped
  reads its activity at 5 minutes and `unknown` at 2 hours.
- `observed_at` is the read's start, not the insert time: a write delayed in the database does not make
  a row look newer.
- A row past 60 minutes is marked, reads `unknown` unless it is stopped, and does not count toward the
  hint; past 7 days it is gone. A workspace that stopped while its last mirror said `working` projects as `stopped` with its
  old `observedAt`; after a restart and before the first new read, the row shows no activity once it is
  past 60 minutes.
- The cap is applied after the projection: adding hidden, unallowed or expired rows to a tenant that has
  500 visible ones changes neither the returned rows, nor `truncated`, nor the hint. With 501 visible
  rows at `tickets`, refreshing one workspace's `observed_at` without changing any `current` changes
  neither the returned keys nor the hint.
- At `tickets` the response carries no `activity` and no `observedAt`; at `state` it carries no waiting
  reason, limit value, session name or title.
- A session the viewer has no share on carries no session reference; sharing it adds one on the next read
  and unsharing removes it.
- The hint appears for two members and not for one member's two sessions, and never from a row outside the
  viewer's projection.
- An admin who is not in an audience gets the empty view.

Part 3 (Console): the title join attaches no title to a Jira row with an unknown site, nor to one whose
site differs from the viewer's connection.

Part 4 (Agent and mirror): the site is recorded for a launch from an inbox row and for a link made from
an item just read, and for no other link; the same key on two sites in two sessions stays two items; a
switch of the member's Jira connection re-labels no existing link and publishes nothing of the new
site's under a consent for the old one.

## Open questions

These are the owner's, collected on #5057. Each has a recommendation above.

1. **Namespace consent: per namespace, or publish all and exclude** (decision 5)? Recommended: per
   namespace. The alternative is simpler to set up and leaks a new private repository's name until the
   member notices.
2. **When does the per-session hide get its screen?** The server enforces it from part 1. Recommended:
   the screen comes with part 2's settings. Without it a member who must hide one ticket has to take its
   whole namespace off.
3. **The numbers** (decision 8): 60 minutes to stale, 7 days to the horizon. Recommended: ship them and
   look at a real tenant after part 3.
4. **Where the view lives.** Recommended: a *Team* view beside the limit board's home, not inside the
   personal overview pane, so ADR 0143's board stays one member's.
5. **Do `watching` and `reviewing` ever show** on the tenant view? Recommended: no, not in this ADR.
6. **Is the publisher's consent enough to show a key to a viewer who may have no tracker access** (decision
   5)? The CP cannot check that access. Recommended: yes, with the plain preview saying so, because the
   audience is the member's own choice and a namespace is allowed one by one.
7. **The `tickets` level's time signal** (decision 5): a viewer can tell roughly when the CP's last
   successful read of the workspace began (not when it stopped; a transient failure looks the same).
   Recommended: accept it and say so in the preview. The alternative is to drop `current` at `tickets`,
   which leaves a row of up to 7 days old with no mark.
8. **Jira before the site is known** (decision 2). Recommended: a Jira row with an unknown site is shown
   to its owner only, before and after part 4; only a row whose site is known is published, and only
   under a (site, project) consent. The alternative is an
   earlier, explicit consent for "this project key on whichever site I use", whose preview says it
   covers a second site if the member switches.

## Alternatives set aside

- **Publish titles from the CP.** The mirror holds them, and it is one `SELECT`. It sends a private
  repository's or Jira project's titles to someone who never had access. Decision 6 joins on the viewer's
  side instead.
- **Ask the tracker, from the CP, whether the viewer may see the item.** The CP holds no member's tracker
  token (ADR 0061 decision 3: the Agent owns it). Adding one is a credential path this feature does not
  justify.
- **Take the reaper's pull as the source** (ADR 0111 decision 6's wording). It keeps nothing of the
  session list but limit observations, and misses every tenant with no idle feature on. The mirror is
  already tenant-wide.
- **A new Agent→CP push.** Rejected in ADR 0111 for the same reason it is not needed. The one Agent field
  proposed here is the Jira site on a link (decision 2), and it rides the list the CP already reads.
- **Take the Jira site from the link's URL.** It is absent on the launch item and not checked against the
  key, so a pasted URL could put a row into the wrong site's group or a consented namespace.
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
- The overview is as fresh as the usage sampler's last successful read (every five minutes by default),
  the Console's list reads or the admin list, and says so; it is blank for a member who never published,
  even when that member is on your ticket.
- Titles are mostly absent for tickets outside the viewer's own queries. That is the price of never
  sending a title the viewer may not read.
- Until part 4, no Jira row has a known site, so Jira rows are seen by their owner only: Jira does not
  appear in the tenant view at all, and two members on one Jira ticket are not told so. A hint that
  fails quietly is the side to err on.
- A key reaches everyone in the audience, including members who cannot open the repository (open
  question 6).
- The mirror gains two defaulted columns and no new write. Nothing else in the Agent or the Console
  changes before part 2, and part 4 adds one Agent link field.
