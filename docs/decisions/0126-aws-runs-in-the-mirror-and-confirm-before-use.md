# 0126. `af-aws-exec` runs show in the session's mirror, and a profile can ask for confirmation before use

English | [日本語](0126-aws-runs-in-the-mirror-and-confirm-before-use.ja.md)

- Status: **accepted** (2026-10-30). The user took decisions E–H below, the recommended option in each. Nothing is built yet.
- Issue: #3050
- Related: [0125](0125-shared-sso-sign-in.md) (shared sign-ins, which this ADR has to land before) /
  [0102](0102-aws-login-through-the-console.md) (the Console login: requests, waits, exit 3, the notice
  path)

## Context

Once a Settings profile is signed in, `af-aws-exec --profile <p> -- <command>` runs without a trace beyond
the tool call's command line in the transcript. That line is easy to miss, and a script hides it
completely. Today the per-profile login also works as an approval: the first use of each profile needs the
member to approve a device code, and an agent cannot do that alone. ADR 0125 lets profiles share one token,
so one approval covers every profile in the sign-in, a production admin profile included.

What exists, and what does not (measured by reading the code):

- **Nothing puts a display-only row into a session's mirror.** The mirror shows the CLI's transcript, plus
  panels the Agent serves under `GET /sessions/{name}/messages`: pending peer messages (a spool under
  `AgentStateDir/pending-peer/<session>/`, `PendingPeersNotice`), discards and handoff proposals.
  Notifications with a session target reach the notification center only, and the CP does not check that the
  session exists.
- **Attribution trusts `AF_SESSION_NAME`.** Every route that maps a process to a session reads it from the
  environment (`listenports.sessionOf`, `aws_exec.go`, `gcloud_exec.go`). An agent sets its own environment,
  so it can name another session or none.
- **Every Agent route is callable with `AGENT_TOKEN`.** The agent's shell holds that token. The CP marks
  what it relays with `X-AF-Relay: cp`, a header the agent can send too. The CP's audit is the only record an
  agent cannot write.
- **The agent runs as the member's user.** It can read `~/.aws/config`, the SSO token cache and the role
  credential cache, and it can run `aws` without `af-aws-exec`.

## Decision

### 1. Each run writes one row to its session's run spool

`af-aws-exec` writes a **run row** before the command starts, and completes it when the command ends:

- **Fields:** time, profile, account, role, region, the sign-in (ADR 0125) and, for a chain, the source profile.
  The account and role are the identity the command runs as: for a chain that is the target role, and the
  source is shown as "via `<source>`". The row also records the program's base name, cut to
  `[A-Za-z0-9._-]{1,32}`. When the command ends it adds the exit status and the duration. The identity comes
  from what `PlanExec` resolved for the run, not from the command line.
- **Service and operation, from known names only.** A pattern cannot tell a service from an option value:
  `aws --profile <lowercase>` and a mistyped `aws ssm <lowercase-secret>` both match `^[a-z0-9-]+$`. The row
  therefore records them only when the program is `aws`, the first argument does not start with `-`, the
  first argument is in a list of service command names, and the second is in that service's list of
  operation command names. Both lists are generated from the pinned aws-cli, and a test fails when they are
  stale. Anything else, including a global option before the service, records `aws` alone.
- **Never** the rest of the command line, the arguments, the environment or the command's output: arguments can
  carry secrets. A test runs commands whose arguments and environment hold a sentinel and asserts that the
  sentinel reaches no spool file, `/messages` answer, notice, Agent log or CP request. The sentinels include a
  lowercase, hyphenated value placed where a service or operation would be (`aws ssm <sentinel>`), and the
  value of an AWS global option (`aws --profile <sentinel> s3 ls`, `--region`, `--endpoint-url`).
- A run that is refused before credentials (exit 1, 2, 3, or 4 below) also writes a row, with the reason code
  and no exit status of a command.
- **Where:** `AgentStateDir/cloud-runs/<session>/`, one file per run, written by the CLI process itself, as the
  login requests are (ADR 0102 decision 1). The directory name is provider-neutral, so `af-gcloud-exec` can
  write there later (not in this ADR's scope). At most 200 rows per session are kept, oldest first out. The
  directory is removed with the session.
- **Which session:** the one `AF_SESSION_NAME` names, when the Agent knows that session. A run with no name, or
  a name the Agent does not know, goes to `cloud-runs/_workspace/`, kept for 7 days.

### 2. The mirror shows the rows; Settings shows every session's

- `GET /sessions/{name}/messages` gains `cloudRuns`, the session's rows. The mirror draws each one as a one-line
  row between the turns, by time: "AWS · `prod-admin` · 123456789012 / AdministratorAccess · ap-northeast-1 ·
  `aws s3 ls` · exit 0 · 3 s". A row is shown in the same place by every kind, and a kind whose transcript has
  no timestamps gets its rows after the last turn.
- Settings > AWS profiles/SSM gets a "Recent runs" list (`GET /aws-runs`, the last 100 across sessions and
  `_workspace`), so a run whose session name was cleared or forged is still visible somewhere.
- No notification and no chat-bridge message per run. A run is not an event that needs a person; a notice for
  each would bury the ones that do.

**What attribution means.** The session is the one the run **reports**. An honest agent, or a script it runs,
shows up in its own mirror. An agent that changes `AF_SESSION_NAME` can move its rows to another session's
mirror or to `_workspace`, but not out of the "Recent runs" list. An agent that does not use `af-aws-exec`
leaves no row at all. The rows are visibility, not an audit. Checking a run's process tree against the
session (decision H) would stop misattribution, but not the bypass.

### 3. SSM sessions

An SSM session's mirror is its terminal. Before `aws ssm start-session`, the pane prints one line:
"`[Agent Fleet] AWS profile <p> · account · role · region · target <instance>`". It writes the same run row to
the SSM session's own spool. The CP already audits the start (`ssm.start_session`).

**The gate does not cover SSM.** An SSM session start and the start modal's instance list
(`POST /ssm/instances`) are the member's presses in the Console, and they count as the member's own use. They
do not go through `PlanExec`. They resolve credentials with the CLI inside the pane or the discovery call,
and the CLI caches the role credentials in `~/.aws/cli/cache`, a gated profile's included. So the claims of
decision 6 ("no role credentials on disk", "not in `~/.aws/config`") hold for `af-aws-exec` only. After a
member has opened an SSM session or listed instances with a gated profile, an agent can use those cached
role credentials until they expire, through a config it writes itself. The guide says so. An agent that
creates an SSM session through the Agent's own route with `AGENT_TOKEN` is the bypass of decision 6, item 1.

### 4. "Confirm before use" is a per-profile Settings field

- `ssm_profile` gains `confirm_use INTEGER NOT NULL DEFAULT 0`. `ssmProfileDTO` and the bridge's
  `awsProfileWire` gain `confirmBeforeUse` (omitted when false). Any profile kind can set it.
- **Which approvals a run needs.** A chain acts as its **target** role (`role_arn`, and that role's account).
  Its source only authenticates: the source's role credentials are used for `sts:AssumeRole` and nothing
  else. So:
  - a run of profile `p` needs an approval of `p` when `p` is gated;
  - a chain also needs an approval of its source when the source is gated, because the source's role is used;
  - an approval of the source never counts for a gated target, and the reverse does not hold either;
  - when both are gated the run needs both, asked on one card with two lines: the target's identity first,
    then "via `<source>` (account / role)".

  The check runs before the chain branch (`exec_nonsso.go`) as well as before the SSO export, so no
  credential path skips it.
- It is a field of the profile, not of the sign-in (ADR 0125). Confirming `prod-admin` confirms nothing else in
  its sign-in, and a login to the sign-in confirms nothing.
- The member's own `~/.aws` profiles have no such field. The Settings list is the only source.

### 5. `af-aws-exec` enforces it, before the credentials

The check runs in `PlanExec` after the profile checks of ADR 0102 decision 1 and **before** any credential
export or login. Confirmation first, then login: a confirmation is cheap, and asking for it first means a
denied run never starts a device code.

- **Approval scope:** one approval covers that session and that profile for 8 hours, or until the session
  ends, whichever comes first (decision F). The record is an Agent state file under
  `AgentStateDir/aws-confirm/`. Names alone are not enough as its key. A profile's account or role can be
  edited after the approval, and a name can be reused by a new profile or a new session. So a grant records,
  and every run compares:
  - the **profile's stable id** (the bridge's `id`; the name is only shown);
  - the **session incarnation**: its name plus the creation time in its meta. A new session of the same name
    does not inherit the grant;
  - the **effective identity**: account, role and region, plus the sign-in id or the portal for an SSO profile,
    or the role ARN and source profile id for a chain.

  Any difference means no grant, and the run asks again. A Settings sync that deletes or changes a gated
  profile drops its grants, and the end of a session drops that session's grants.
- **No attributable session.** A grant needs a session it can be tied to. A gated run without
  `AF_SESSION_NAME`, or with a name the Agent has no live session for, files nothing and exits 4 at once:
  "confirmation needs an Agent Fleet session; run this from one". It never gets a shared, empty-session
  grant.
- **Asking:** a run without a live approval files a **confirmation request**, with the same machinery as a
  login request: one per session and profile, waiters, a random id, a notice of kind `aws-use-confirm` that
  carries only the id. The Console reads the request from the Agent (`GET /aws-confirm`) and shows:
  - a card in the session's mirror, under the latest turn: "`<session>` wants to use `prod-admin`
    (123456789012 / AdministratorAccess)", with **Allow for this session** and **Deny**;
  - the same as a sticky toast when the member is not on that mirror.

  As in ADR 0102, the profile, account and role come from Settings, never from the request. The request
  records the identity it was filed for (the three items above). The card shows that snapshot. An approval
  grants only that snapshot. A Settings change while the request waits drops the request, and the waiting run
  files a new one for the new identity, so the card and what gets exported cannot drift apart.
- **Answering:** `POST /aws-confirm/{id}/approve|deny` on the Agent, relayed by the CP for the member's own
  workspace, and audited as `aws.use.approve` and `aws.use.deny`, with the profile and the session.
- **Waiting:** the run waits as for a login (ADR 0102 decision 5): the per-kind wait, ten minutes at a
  member's terminal. An approval lets it go on. A denial ends it at once with **exit 4** and "use of
  `prod-admin` was denied in the Console". For one minute after that, the same session and profile file no
  new request, so an agent that retries at once does not put the card back. A timeout ends the run with
  exit 4 and "waiting for confirmation in the Console". The request stays pending (15 minutes), so the
  member can still allow it and the agent's next run goes through.
- **Exit 4** is new in `cloudexec`: "the member's confirmation is needed and was not given". Exit 3 keeps its
  meaning. `workspace/notes/aws.md` tells agents to say that the confirmation is waiting in the Console
  and not to retry in a loop.
- **Nobody watching:** the run fails with exit 4 after its wait, and nothing runs. That is the purpose of the
  gate. A scheduled run that needs a gated profile fails in the same way, and the member sees the pending card.

### 6. What the gate does not stop, and what reduces the bypass

The gate stops **accidents and unnoticed use** through `af-aws-exec`. It is not a boundary against an agent
that sets out to get past it, because that agent can:

1. call `POST /aws-confirm/{id}/approve` itself with `AGENT_TOKEN`, header included. The CP audit then lacks
   the approval. The Agent logs it, and the mirror row of the run says "approved without the Console" when the
   approval did not carry the relay header. A forged header hides even that.
2. skip `af-aws-exec` and run `aws --profile prod-admin` directly, or write its own config with
   `sso_session = af-<sign-in>` and the admin role, and use a cached SSO token.

What reduces item 2, and is part of this decision:

- **A gated profile is not written to the managed block of `~/.aws/config`** (decision E). A plain
  `aws --profile prod-admin` then fails instead of quietly working. A tool that needs the profile by name
  runs under `af-aws-exec`, which already wraps any command.

  Today "written to the block" also means "usable". `PlanExec` refuses a profile `~/.aws` does not define
  (`notDefined`). The row's login and logout require `ExportedIn`. A chain requires itself and its source to
  be exported. Simply skipping the block would therefore break every gated profile, and rendering it into an
  isolated config without the checks would drop the protection of the member's own definitions. So the sync
  computes two sets with the **same** rules as today: shadowed by the member's own profile or `[sso-session]`,
  `[DEFAULT]` clash, invalid, incomplete, name or session conflict, broken chain.
  - **usable:** passed every rule;
  - **written:** usable and not gated. A chain whose source is gated is not written either, because its
    `source_profile` would name a profile the file does not have.

  The sync stores both sets under the same lock as the block (`aws-usable.json` beside the Settings cache).
  Everything that today asks "is it exported" asks "is it usable" instead: `PlanExec` (which then renders the
  profile, its sso-session and, for a chain, its source into the run's isolated config), the row's login,
  logout and status, the expiry warning, `af-aws-exec --list` (which shows a gated profile as "usable through
  af-aws-exec only") and the chain checks. A profile that is not usable is refused with today's reason, gated
  or not.
- **`af-aws-exec` keeps no role credentials of a gated profile on disk.** The child gets them in its
  environment, and the CLI's credential cache entry is removed after the export. SSM is the exception
  (decision 3).

What only the member can do: **the only gate an agent cannot pass by itself is a token it does not have.** A
profile kept on its own sign-in and signed out after use still needs a device-code approval each time.
Grouping a gated profile into a sign-in that other profiles keep signed in removes that (ADR 0125). The
Console says so when a gated profile joins a shared sign-in, or gets the gate while it is in one (decision
G).

### 7. Order relative to ADR 0125

The shared sign-in does not ship before this ADR's run rows, and the gate ships with or before it:

1. run rows and mirror display: the Agent, the Console, and on the CP the read relay `GET /api/aws-runs`
   (`relays_sessions.go` registers each route, and without the relay "Recent runs" never arrives), with its
   relay test. `/messages` is already relayed;
2. confirm before use (CP field and migration, Agent, Console);
3. to 5. ADR 0125's three PRs (CP, Agent, Console).

Migrations: the gate's column takes the next numbers (SQLite `0116`, Postgres `0101` on `main` today), and ADR
0125's sign-in table the ones after it. Both are re-checked against `main` when each change is opened.

**Contracts, per PR.**
- PR 1: `cloudRuns` in the `/messages` wire and the `/aws-runs` row, in the Agent's `contract_wire_test.go`
  families, with the Console types bound in the same PR.
- PR 2: `confirmBeforeUse` on `ssmProfileDTO` (the CP's `TestContractFamilies`). On the bridge, `confirmBeforeUse`
  and the profile's stable `id`, which the grants of decision 5 are keyed by, with the CP and Agent sides of
  that wire. The `/aws-confirm` request and answer go in the Agent's families. Their Console types are bound in
  the same PR.
- PRs 3 to 5: as ADR 0125 decision 3 lists. Each PR binds its Console types in the same PR, so no contract
  test waits on a later one.

### 8. Older writers and older readers

- **Absent is not false.** `PUT /api/ssm/profiles/{id}` without `confirmBeforeUse` leaves the gate as it is.
  `false` removes it. A Console that predates PR 2, editing a gated profile, therefore keeps the gate.
- **Older Agent, newer CP.** An Agent that predates PR 2 would ignore `confirmBeforeUse` and write the profile
  to `~/.aws/config`, ungated. The Agent therefore sends `X-AF-AWS-Profiles: 2` when it pulls the bridge. A
  CP answering a pull without it leaves every gated profile, and every chain whose source is gated, out of
  the list. They are then unusable on that Agent, not ungated.
- **Rollback of the CP across PR 2.** An older CP does not know the column. It leaves the stored value alone,
  but the bridge it serves has neither `confirmBeforeUse` nor `id`. Gated ids therefore cannot be mapped onto
  its rows: a profile renamed, deleted or recreated through the older Console would lose the gate. The rule is
  instead about whole answers. The newer CP answers with `X-AF-AWS-Profiles: 2`. **An Agent that has once
  received such an answer never applies an unversioned one.** It keeps the last versioned list, which is the
  block, the usable set and the gates, exactly as it does today when the CP cannot be reached. It logs that
  Settings from an older Control Plane are not applied, and `af-aws-exec --list` says so. Edits made during a
  rollback reach the workspace only after the upgrade. Every gate stays as it was, and nothing ungated
  appears. An Agent that has never seen a versioned answer has no gate to keep and applies answers as
  today. Tests: after a versioned answer, an unversioned one that renames, deletes and recreates a gated
  profile changes nothing on disk, and a gated run still asks.
- **Older Console.** It shows no card. A gated run then waits its time and exits 4. That is safe, and the
  pending request is visible to the newer Console once it loads.

## Rejected

- **A notification per run.** It would bury the notifications that need a person (decision 2).
- **Put the whole command line in the row**, or the first N characters of it. The arguments can carry
  secrets, and redacting them is guesswork.
- **Attribute the run by process tree now.** Managed kinds run tool commands under shared daemons whose
  environment names no session (`listenports` documents this), so a tree walk would lose real runs. It would
  also not touch the bypass of decision 6. Kept as decision H.
- **A CP-signed approval that the Agent verifies.** An agent that wants to get past the gate does not need to
  forge an approval when it can run `aws` directly (decision 6). The signature would protect a door next to an
  open one.
- **Gate the sign-in instead of the profile.** The risk is per role (the admin profile), not per portal.
- **Treat a login as the confirmation.** A login to a shared sign-in is done once for every member. That is
  exactly the coupling this ADR removes.

## Consequences

- Every `af-aws-exec` run that reports a session shows in that session's mirror, with the identity it ran as.
- A gated profile needs the member's press once per session (within the scope of decision F), and a run nobody
  answers fails with exit 4.
- A gated profile is not usable as a plain `aws --profile` name in the workspace (if E is accepted).
- The guide (`guide/member/16-cloud-accounts.md` and `.ja.md`) states the limits of decision 6 in the member's
  terms: the gate catches mistakes, not an agent that sets out to get around it.

## Decisions taken (2026-10-30, by the user)

- **E. Taken: the recommended option.** **Hold gated profiles out of `~/.aws/config`.** Recommended: yes, so a plain `aws --profile` cannot quietly
  bypass the gate. The cost is that a gated profile is usable only through `af-aws-exec`.
- **F. Taken: the recommended option.** **Approval scope.** Recommended: per session and profile, for 8 hours or until the session ends. The
  alternatives are a 1-hour window, or every run (noisy for scripts that call `aws` many times).
- **G. Taken: the recommended option.** **A gated profile inside a shared sign-in.** Recommended: allowed, with the warning of decision 6 in the
  form and in the grouping action. The alternative is to forbid it, which keeps the device code as a real gate
  but blocks the main #3014 case (an admin profile on the same portal).
- **H. Taken: the recommended option.** **Attribution by process tree** as a follow-up. Recommended: not now (decision 2 and Rejected). File an
  issue only if misattributed rows turn up in practice.

## Note — the workspace's spool is `.workspace` (2026-10-30)

Implementation of decision 1 (#3050, PR 1). `_workspace` is a valid session name (`^[A-Za-z0-9_-]{1,40}$`), so a
session called that would share the spool of unattributed runs. The spool is `cloud-runs/.workspace/` instead:
the dot is outside the session namespace. Wherever decisions 1 and 2 say `_workspace`, read `.workspace`.
Nothing else changes.

Two more details the decisions left open:
- The workspace spool also keeps at most 500 rows, so a burst within the 7 days stays bounded.
- A session's spool is removed when the session is purged from the trash, not when it is moved there,
  so a restored session keeps its rows.

## Note — a row records no exit status (2026-10-31)

Implementation of decision 1 (#3050, PR 1, review of #3063). Decision 1 says the row is completed "when the command
ends", with the exit status and the duration. To see the end, af-aws-exec would have to run the command as its
child instead of becoming it. Measured in review, that changes what the caller's signals do:

- a signal sent to the process group (an agent's shell tool, `timeout`) reaches the command twice, once directly
  and once forwarded by the wrapper, when there is no terminal;
- a command that ignores SIGTERM outlives a SIGKILL of the wrapper, holding the credentials. Moving the command
  to a process group of its own fixes the first point but lets a group SIGKILL miss the command's children.

No supervising design keeps exec's behaviour exactly, and af-aws-exec runs deployments. It therefore still
becomes the command, and **a row records the start and the identity, or why the run was refused; it has no exit
status and no duration**. The wrapper's exit status, which the caller already sees, is unchanged. A test runs the
real wrapper and asserts that the command has the wrapper's PID, that one group SIGINT arrives once, and that a
SIGKILL of that PID ends the command.

## Note — a waiting run ends when what it waits for changes (2026-10-31)

Implementation of decision 5 (#3050, PR 2, review of #3072). Decision 5 says that a Settings change drops a pending
request and that "the waiting run files a new one for the new identity". A run cannot do that safely: its plan
(which profile, through which source, from which config) was made from the Settings it read at its start, and
asking about a new identity while running the old plan would let the card and the export drift apart. So **a run
whose session ended, or whose profile Settings changed while it waited, ends with exit 4**, saying which, and the
agent runs the command again, which plans and asks afresh. A request expiring unanswered is still re-filed by a
run that keeps waiting.

Three related details:
- A request or a grant counts only while its session incarnation is live: the Agent neither lists nor accepts an
  answer for an ended session's request, and drops its grants.
- A denial holds each profile it named for a minute, whatever set of profiles the next run asks about.
- Right before credentials are fetched, after the wait, af-aws-exec checks again that the member's own files have
  not taken the profile, its sso-session or its source.
