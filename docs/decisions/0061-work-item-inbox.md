# 0061. External work items are "fetched by the Agent, with the CP holding only non-confidential metadata" — no CLI and no MCP for the listing

English | [日本語](0061-work-item-inbox.ja.md)

- Status: **adopted; P0–P2 implemented** (2026-08-26. Within P2, automatic working-set creation is **not adopted** = decision 11, and PR creation is **withdrawn** = decision 6.1).
  **2026-08-27: decisions 14–16 added after looking at real data (41 Jira items)** — partly withdrawing decision 8's "a saved query is the only filter" ([docs/80](../log/80-work-item-inbox.md) §80.18).
  **2026-08-27: Bitbucket pull requests added, with decisions 17–19** ([docs/80](../log/80-work-item-inbox.md) §80.19). The design and the background are in [docs/80](../log/80-work-item-inbox.md).
- See also: [0031-mcp-registry.md](0031-mcp-registry.md) (MCP means "each CLI speaks it directly and af only distributes the definitions"; OAuth MCP is a non-goal) / [0036-working-sets.md](0036-working-sets.md) (the unit of "a piece of work") /
  [0055-idle-stop-and-carried-interactions.md](0055-idle-stop-and-carried-interactions.md) (do not keep it warm) / [0052-tenant-git-oauth.md](0052-tenant-git-oauth.md) (the CP passes secrets through and does not hold them) / [0059-repo-import-jobs.md](0059-repo-import-jobs.md) (the relationship between self-running work and the busy check)

## Context

af's launch route starts **from a repo** (pick a repository → launch), but a person's work starts
**from a ticket**. The request is to show Jira / GitHub Issues in the left pane and start a session
from there with the context attached.

Producing a listing is not itself difficult. What is difficult is that **where the secrets live and the
workspace's power state** collide head-on:

- The tokens (GitHub / Jira) exist **only in the container's `secrets.enc`**. Only the Agent can fetch
  with them.
- But "deciding which ticket to work on" happens **before a session is created**, i.e. while the
  workspace is stopped.
- Waking the workspace just to display it would reopen, somewhere else, the "workspace that never
  stops" [0055](0055-idle-stop-and-carried-interactions.md) closed (only tier 2 affects billing).

## Decision

**1. The Agent fetches; the CP caches.** While the workspace is running, the Agent runs the saved
queries and pushes the results into the CP's `work_item_cache` (membership scoped). While stopped, the
CP's cache is what is drawn, and **each row always carries "last fetched hh:mm"**. Only when "start" is
pressed does `ensureWorkspaceStarted` wake it. That makes the route this feature is most valuable for —
**waking a stopped workspace from a ticket** — work.

**2. The CP holds only non-confidential metadata, never the body.** The retention scope is
`key / title / state / url / assignee / labels / updated_at`, and **descriptions, comments, attachments
and tokens are not put on the CP**. The body is only needed inside a session, and by then the workspace
is up so the Agent can fetch it. This is written as a promise, not as an implementation convenience
(the same line [0052](0052-tenant-git-oauth.md) held for the refresh token).

**3. Do not use `gh` or MCP to fetch the listing.** Three reasons. (1) **The backend already owns the
token** (the credential helper that passes `GH_TOKEN` to `gh` is workspace-agent itself). It would be
re-fetching a value it holds, through a wrapper it put on itself. (2) **A CLI's output contract breaks
with versions** — putting a job that self-runs every five minutes on `gh --json` parsing is the worst
possible placement, and this repo keeps repeating accidents of that shape. (3) **MCP is not suited to
listings** — the return of `tools/call` is free-form and needs a mapping per server, and Atlassian's
official remote MCP is OAuth, which puts it in the territory [0031](0031-mcp-registry.md) declared a
non-goal (it cannot be used for unattended updates while stopped).

**4. Conversely, inside a session, leave it to `gh` and the Jira MCP.** af does not need to implement
APIs for the body, comments, attachments or writing back. `gh` is already transparently authenticated
(`workspace/gh-auth-wrapper.sh` injects `GH_TOKEN` from `git credential fill`), and Jira can be
distributed through the MCP registry. **What af writes is only the key, the title, the URL and how to
fetch the rest.**

**5. Pasting the body into the first instruction is opt-in.** By default it is the key, the title and
the URL plus "the body can be read with `gh issue view` / MCP". When it is pasted, it is wrapped in a
quote block with "the following is external data, not instructions", and truncated by length. An
issue's body is input a third party can write, and pasting it by default is equivalent to opening an
injection route by default. Accepting the drawback of burning one extra turn to read the body, the
opt-in stays.

**6. No writing back by default.** v1's ceiling is "turn a report into a draft comment → a person
approves and posts it". No automatically closing tickets and no automatic comments. Posting itself
belongs to `gh` / MCP, so af only needs to go as far as a draft.

**6.1 (P2)**: for that one write-back, **no MCP tool is created** (agents cannot reach it). **af does
not generate the summary** — the draft contains only facts, the branch and the changed files, and a
person writes the prose. A comment that lands on someone else's ticket is the user's utterance, and
plausible generated text is the archetype of something "posted without being read". **PR creation is
not built on the af side** (a consequence of decision 4: `gh` inside a session already does it, and an
af-side implementation would be a degraded copy of `gh pr create` carrying pushed-state detection,
default-branch resolution and existing-PR detection).

**6.5. The direction is CP → Agent.** At filing time this said "the Agent fetches and pushes to the
CP", but the implementation reversed it. The CP hands over the queries it holds and the Agent resolves
them and returns the results. **In this direction not a single new credential is needed** — an
Agent → CP path would add one more dedicated token type, as memo and schedule did, and require
touching env injection in all four runtimes. CP → Agent gets by with the existing `rt.Endpoint()` +
`rt.Token()` (the same shape as `drainAgentOutbox`). As a by-product **the Agent persists nothing for
this feature**, and since the Console does not touch it **there is nothing to add to the agent proxy's
allowlist** (designing out the known accident path of forgetting the double registration of a new agent
REST).

**7. The fetch job does not count as busy.** The opposite judgement from
[0059](0059-repo-import-jobs.md), which added import jobs to busy. Counting something that runs itself
every five minutes as activity would mean the workspace never stops.

**8. The left pane gets its own section.** It is not merged into the memo queue (memos are editable and
items are read-only; mixing them muddies both). It is not made a child of the repo node either (Jira is
not tied to a repository and it falls apart). The default is "assigned to me and not done" only, and it
**appears on the stopped rail too**.
⚠️ The premise that "that alone keeps the row count down" **was wrong in measurement** (41 items) — the
volume and the rebuilding of a row's contents are decisions 14–16.

**9. Keep a ledger of item ↔ session, with no FK on the cache.** The cache is a volatile thing that
comes and goes with query results, but the fact that work was started outlives it. The ledger's biggest
practical benefit is stopping "two people separately starting the same ticket" before launch.

**9.5. Read Jira's state from the status *category* (P1).** Status names get renamed per project, but
the `statusCategory` behind them (new / indeterminate / done) is fixed, so that is what maps onto the
provider-independent vocabulary. An implementation that judges by name breaks on the first custom
workflow. Also, Jira's credentials make **the email a secret too** (one half of Basic auth), and they
are **validated with `/rest/api/3/myself` before saving** (three fields means three ways to mistype,
and without validation the first sign of trouble is an error on a rail row minutes later, which looks
like the feature is broken). Search tries `/rest/api/3/search/jql` first and falls back to the old
`/search` only on 404/410 (never on 401).

**10. v1 is GitHub only; Jira is P1.** GitHub can use the existing connection token as is, so it needs
zero extra authentication and works as a feature with P0 alone. Jira needs a new connection kind
(email + API token) and so comes next.
⚠️ "We have MCP, so we do not need a Jira connection" does not follow — MCP works only inside a
conversation and cannot produce a listing, so it does not satisfy the request at all.

**11 (P2). Do not split working sets; let the user choose where to launch.** "One ticket = one working
set" does not mesh with [0036](0036-working-sets.md)'s **repository-granularity** membership (putting
in only the session makes it vanish from the tree; putting in the base pulls in every session of that
repository). Rather than widening the membership model, **being able to choose the repository and "a
new worktree / an existing working copy"** from the ticket is what was actually wanted, and the group
list does not grow by one per ticket. ★ The Start hub lists only base clones, and the launch dialogue's
Location cannot point at another existing copy, so neither of these two choices can be substituted by
existing UI.

**12 (P2). The branch name defaults to `feature/{key}`.** The title is not mixed in — the key alone
identifies the work, a Japanese title produces an empty slug, and an English one gets long. `{slug}` can
be added in the template.

**13 (P1.5). Jira accepts OAuth too. The same pattern as Bitbucket, but not the same app.**
Pasting an API token means three fields, and the email is half of Basic auth, i.e. a credential.
Atlassian's 3LO is added, **riding `tenant_git_oauth` as a third provider** (no migration needed).
⚠️ It cannot share Bitbucket's app (a different authorisation server and a different place to register),
so the tenant admin registers one more in the Developer Console. **The API token route stays** — a
tenant with no app registered has no other entrance, and every deployment starts in that state. The
scope **includes write:jira-work** at the user's choice (for posting comments, §80.10). **No new bridge
token type is added**: what the existing git-oauth bridge authorises is "update this member's token",
and the provider is just part of the path.

**13.1 (P1.5). The 3LO app's access type is Resource-level.** The authorisation then covers only the
one site the user chose. Account-level would handle every site with one authorisation, but it would
permanently hand over permissions to sites they did not choose, which does not mesh with this feature's
consistent stance of keeping what af holds minimal (no bodies on the CP, read by default, write-back
only when a person presses). The site-selection UI only appears when several are returned, so with
Resource-level it simply does not appear.

**13.2 (P1.5). Serialising refreshes is a requirement of the rotation.** Atlassian's rotating refresh
token is single-use, and **re-presenting a spent one is treated as theft and can revoke the whole
authorisation**. The rail's fetch and a comment post noticing the expiry at the same time is perfectly
normal, so it is serialised within the process, re-checking the expiry after taking the lock and not
exchanging if it has already been refreshed.

**14 (P2.5). Redraw "no filter UI" on the basis of the measured 41 items.**
At filing time we decided **a saved query is the only filter** (decision 8,
[docs/80](../log/80-work-item-inbox.md) §80.1 / §80.12). The real data was **41 items for
`assignee = currentUser() AND statusCategory != Done`**. A query decides **what you look at**, not
**how many rows you look at at once**. For a person there is no query narrower than "my incomplete
items", so the query side has no further answer.

The new line: **if an action on the af side causes a request to the provider it is the saved query's
job; if it only changes how already-fetched rows are shown it is the rail's job.** That permits exactly
two things, neither of which hits the provider, saves anything, or changes the ordering:

- **10 rows by default plus "show more (N remaining)".** The count badge still says 41 (it is folded,
  not hidden). The order is as before (incomplete first, then most recently updated).
- **A one-line filter within the rail** (shown only when `count > 10`). Substring matching on key,
  title, assignee, label and repository. No operators.

⛔ Still not built: a UI for composing queries, a sort UI, a detail pane, grouping (§80.18.5).

**15 (P2.5). What is folded is the display, not the server.** A stopped rail only draws the CP's cache
and cannot fetch "more"; fetching would collide head-on with decision 1's **do not wake the workspace to
display something**. So the fetch cap (50 per query) stays and the fold lives in the Console's state.
But the order it cuts in is made correct — **if the JQL has no `ORDER BY`, send it with
`ORDER BY updated DESC`** (GitHub has `sort=updated&order=desc` from the start). If the display claims
to be "the top N by recency", an indeterminate order among the fetched 50 makes that a lie. ⚠️ "The
50-item cap was hit" is not surfaced on the rows (widening a three-layer DTO for it is not worth it when
the measured 41 does not hit it). Add it once real data hits it.

**16 (P2.5). Put on a row only information that differs per row.** On real hardware, the second line of
all 41 rows was **the same one display name** (the person who connected), and titles were heavily
elided because of it. The rule: **within one query's results, metadata whose distinct count is 1 or
fewer (assignee, repository) is dropped from the rows, and a row whose metadata has become empty loses
the metadata line entirely and becomes one line.** The freed width goes back to the title, and the freed
height is not refilled.

The same rule applies to **the relative update time**: **shown only on rows that have not moved for 24
hours or more**. ⚠️ Originally this said "always, at the right of the heading line", but **measuring the
real rendering changed it** — at the default rail width the title has only 130px and the chip is 38px,
i.e. **it permanently costs 23% of the title**. The top rows already say they are recent by virtue of
the list being ordered by update, so there is no reason to pay; it is enough to show it only for what
position cannot say — **"untouched for three months"**. Measured, the title width went from 124px to
168px (+35%) ([docs/80](../log/80-work-item-inbox.md) §80.18.7).

★ **"Hide it when it matches the connected account" was not adopted.** The stopped rail draws only the
CP's cache, so it would mean entrusting "who am I" to the CP = widening the DTO by one layer and passing
a migration in two dialects — and it would not fix the identical symptom of "**all 41 rows are one
teammate**". The problem is not "the assignee is me" but "**41 rows say the same thing**", and the rule
that judges by distinct count is that description itself (zero server changes, provider-independent, and
it kicks in automatically on a team query). "Let the user choose per query whether to show it" is also
rejected — **outsourcing a display judgement to the user's settings is the same as not having made the
judgement**.

**17 (P2.6). For Bitbucket, make the saved query start with "where to look".** GitHub's
`/search/issues` and Jira's JQL can each be written as one account-wide query, but **Bitbucket Cloud API
2.0 has no cross-account search** (the official schema's `paths` contain only "one repository" and "a
workspace × the PRs that person created"). ⚠️ A 404 from an unauthenticated call is no evidence —
Bitbucket hides routes that require authentication behind the same 404 (measured).

So a saved query is read as `<workspace>[/<repo>] [filter expression]`. **This does not break decision
8's "a saved query is the only unit of fetching"**, and it needs no new column, no DTO change and no
migration — a GitHub query already carries `repo:owner/name` inside it and JQL carries `project = X`, so
**"write 'where' in that provider's own way in the query field" is an existing rule**. Alongside that,
`@me` expands to the connected account's UUID (Bitbucket's filter expressions have no equivalent of
`currentUser()`, and without it nobody can write "PRs where I am a reviewer").
⛔ **Sweeping N repositories to imitate a cross-account search is rejected**: one saved query would
become N round trips, and the budget that `workItemFetchQueries` × `workItemFetchPerQuery` was supposed
to bound becomes unbounded, decided by the query's contents.

**18 (P2.6). The scope is insufficient. But existing connections are not touched.**
Reading PRs needs `pullrequest` / `read:pullrequest:bitbucket`, which a connection made for clone/push
does not have. ⚠️ **Bitbucket does not put the scope in the authorisation URL**, so permissions are
decided by the consumer's Permissions, and adding them later leaves the old permissions baked into
existing tokens — which would normally mean the same weight as decision 13 (Jira's 3LO): "everyone
re-authorises".

**The reason it does not is that this feature fetches nothing by default** (create no saved query and
Bitbucket is never called). So: **do not add it** to the required scopes at connection time (calling a
clone-only person's perfectly normal connection "insufficient" would be a lie); a 403 names **the
missing permission, who can add it, and the reconnection afterwards** (a generic "please reconnect"
puts people in a loop that re-pasting will not fix); and the guidance goes **on the screen of whoever
adds it** (the Bitbucket row in tenant administration, and the API token explanation).

**19 (P2.6). Bitbucket is read-only.** Decision 6's write-back (the report comment) is not added —
posting needs `pullrequest:write`, which is exactly the "everyone re-authorises" decision 18 avoided,
and not a price worth paying for a listing. **The Console does not show a report button on bitbucket
rows** (never show an interactive element whose destination will always refuse). For the same reason,
"how to read the body" in the first instruction is the URL only — the container has no equivalent of
`gh` for Bitbucket, and **we do not write the name of a tool that is not there**.

**19.1 (P2.6). §80.16-3's "should PRs appear as items?" closes as "yes, with no setting".** At filing
time it said "start off by default", but **whether you created a saved query is already off by
default** (on GitHub too, PRs do not appear unless you write `is:pr`). The worry that "a PR is the
continuation of work that already has a session" **only applies to your own PRs** — reviewing someone
else's PR is work for which there is no session on this side yet.

**20 (§80.20). Do not line buttons up on the rows. Pressing a row opens a detail modal, and the actions
gather there.** The user's comment was "**the Start buttons lined up are frightening**". Decision 14
(§80.18) got "only information that differs per row goes on a row" through, yet **the buttons were the
same on every row** — the same mistake had survived on the interactive side. A list is a surface for
reading, and if an element that creates a worktree when pressed is permanently resident on every row,
"frightening" is a correct reading.

What stays on a row is **🔗 (the lightest action, which does not go through af, and only on hover) and
the started badge**. ★ **The badge is not an action but the information "somebody already has this
row"**, so it stays (the biggest practical benefit of decision 5). `Start` and 💬 Report moved into the
detail modal. ⚠️ **Never stack two modals** (Report opens after Detail closes) — both the Esc layering
and the focus trap assume one at a time.

**20.1. The detail does not show the body (the description).** It is the first thing you want when
someone says "detail", but the CP does not hold the body (decision 2), so showing it would need a
single-item fetch on every open, and that **always fails if the workspace is stopped** = holding
decision 1 (do not wake the workspace to display something) in the listing and breaking it in the
detail. A modal that opens with "only the detail empty" while stopped looks like a broken feature. The
body is needed inside a session, and there `gh` / the Jira MCP are available (decisions 3 and 4). The
detail lays out **only the CP's cache**, and **adds neither a new fetch route nor a new retention
scope** — so "we do not build a ticket viewer" (decision 1's proviso) still holds.

**20.2. One modal fewer.** The old `WorkItemStartModal` (decision 16's launch-target picker) was folded
into the detail. Its original shape offered two selects with nowhere to confirm "what is this row?"
before pressing; folded in, **confirming and choosing are on the same single sheet**.

**22 (§80.22). One row per ticket. Overlapping queries do not duplicate it.** The comment came from
having "saved the same JQL twice" on real hardware, but the fix is not on the registration side —
**overlapping queries such as `assignee = currentUser()` and `project = G3M` are normal usage**, and the
same row lines up once per overlap. This is not "a shelf that lays out query results" but "a shelf of my
work items" (decision 1), and **how many queries a row matched is not information about the row** (the
same line as decision 14). There is real harm too: the badge says **82** for 41 items, the default
10-row fold fills with duplicates, and a plain row for the same ticket sits next to one with a started
badge, so **decision 5 (stop the second person) becomes a lie on that row**.

The folding is **in the Console's display layer only**; the CP's cache stays per query —
`ReplaceWorkItems` is built on per-query replacement (one 401 must not empty the shelf). The row kept is
chosen deterministically by **incomplete → most recently updated → ascending `queryId`**. ⚠️ Without
that last `queryId`, the CP's `ORDER BY updated_at DESC, item_key` does not decide the order among ties,
so the winning row changes on each fetch and **the `repoHint` used for launching silently wobbles**.

**23 (§80.23). Bitbucket's query alone is not typed by hand but assembled from the connection's
listing.** Decision 17 fixed the stored form (`<workspace>[/<repo>]` at the front of the query) but did
not extend to **making a person type it**. On real hardware the default value
`workspace/repo reviewers.uuid="@me"` was saved as is, and the 404 was read as "some other error".
★ The plan of "put the words to be replaced in the default value and the error will say so itself" does
not work — **an error message can only make up for something that was writable in the first place**.
Bitbucket's query was not writable: the target at the front is **af's invention** (not Bitbucket's
syntax), and the awaiting-review expression demands a UUID the person does not have (`@me` expansion
exists, but you cannot get there without knowing that notation).

The shape is "**what to show (three, multi-select) × the target**", and the three come exactly from
§80.19.1's API limits (awaiting review / a repository's PRs / my PRs). ★ The three are not mutually
exclusive, so they are **checkboxes** — "awaiting review" and "my PRs" are both things you want to see,
and adding one at a time would make you pick the same target twice. What is checked becomes several
queries in one addition (the display name only works for a single one; with several, rows with the same
name make it unreadable which is which). The target reuses the repository list used for cloning, so
**no new API and no new credentials**. ★ If there is only one candidate, do not ask (someone with one
workspace has it filled in the moment they press). What is saved is still one string, and both the CP
and the Agent are unchanged. ⚠️ That listing has to reach the Agent, so **it cannot be fetched while
stopped** — in that case it falls back to typing by hand (blocking it in the settings would break
decision 1's "usable while stopped" from behind).

★ **GitHub and Jira do not change.** There, the user writes the real dialect they use every day, and
passing it straight through is correct. Aligning them would invert it into "you can only write what
af's screen lets you write".

## Options rejected

- **Have the CP fetch directly (holding the token under envelope encryption on the CP).** Freshness
  would be preserved while stopped, and it would suit running a shared team service account. But it is
  discarding the "the CP does not hold secrets" principle — the very open point
  [docs/25](../log/25-ops-monitoring.md) §4.3 deferred to an ADR. **A listing being a few minutes stale
  troubles nobody**, so it does not balance against overturning the principle.
- **Fetch on the Agent only and put nothing on the CP.** The most faithful to the principle and the
  smallest implementation, but the left pane is empty while stopped. It becomes a feature that is
  unavailable at exactly the moment it matters, erasing two thirds of its value.
- **Use `gh` / MCP to fetch the listing.** Decision 3's three points. In particular, the fact that
  **the backend owns the token** means going through gh has **zero** benefit.
- **Have an agent (an LLM) generate the listing.** Non-deterministic, slow and billed. A listing is a
  program's job.
- **Merge it into the memo queue.** Decision 8.
- **Put the body in the cache and offer full-text search.** The retention scope for sensitive
  information widens a whole level, and what is gained is a degraded copy of the vendor's own web UI.
- **Sync everything.** A cache of thousands of items only fattens the CP and nobody reads it. Narrow it
  with a query.
- **Group by project key and collapse** (the alternative to decision 14). It adds headings so **the
  vertical grows**, and the order changes from "most recently updated" to "by project", breaking the
  rail's primary use. The project is already in the key (`G3M-897`), so typing `G3M` into the filter
  suffices.
- **Default to "only recently updated"** (a time window; the alternative to decision 14). It would
  silently drop rows the query asked for on the grounds of a clock — impossible for the same reason we
  do not hide `done` rows. A ticket untouched for three months is precisely the one being forgotten.
- **Give a saved query a row limit** (the alternative to decision 15). It buries the fact that there are
  41 items in the query's settings where it cannot be seen, and neither the count badge nor "show more"
  can be produced.
- **Ride Jira on Bitbucket's OAuth app.** Even within Atlassian, the authorisation server
  (`bitbucket.org` vs `auth.atlassian.com`) and the place to register (Bitbucket workspace settings vs
  developer.atlassian.com) are different, and one client_id cannot authorise the other's scopes.
- **Retire the API token route now that OAuth exists.** A tenant with no app registered would be unable
  to connect. The maintenance cost of two authentication routes is accepted in the same shape as
  GitHub's having both device flow and pasting.
- **Add `workspace` / `repo` columns to Bitbucket's saved queries** (the alternative to decision 17).
  For the cost of a three-layer DTO and a migration in two dialects, it adds two columns that stay empty
  for GitHub and Jira. Writing it in the query string keeps **the change inside the adapter**.
- **Make `pullrequest` a required scope on the Bitbucket connection** (the alternative to decision 18).
  It would display "insufficient" on the connection of someone who uses no work items at all, making
  them fix something that does not need fixing.
- **Show the issue's body in the detail modal** (the alternative to decision 20.1). Since the CP does
  not hold the body, it would have to hit the provider on every open, and **that always fails while the
  workspace is stopped**. It would break in the detail the line decision 1 held in the listing — and the
  party that most needs the body (the agent) can already read it with `gh` / MCP.
- **Show the row's buttons only on hover** (the alternative to decision 20). An action whose location is
  unknown until you touch it merely replaces "frightening" with "cannot find it", and on a coarse
  pointer (touch) it ends up permanently visible anyway.
- **Support Bitbucket Issues as well.** They are barely used, and a Bitbucket-using team's issues are in
  Jira (already covered by decision 13). When someone asks.
- **Cover GHE (GitHub Enterprise Server) in v1.** Both the `gh` wrapper and the direct calls are fixed
  to github.com, and giving the connection a host is separate work. When someone asks.
