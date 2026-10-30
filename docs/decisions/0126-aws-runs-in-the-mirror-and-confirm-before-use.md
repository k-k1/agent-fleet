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

- **Fields:** time, profile, account, role, region, the sign-in (ADR 0125) and the source profile of a chain.
  It also records the program's base name, cut to `[A-Za-z0-9._-]{1,32}`. For `aws <service> <operation>`
  it records the service and operation, only when both match `^[a-z0-9-]{1,64}$`. When the command ends it
  adds the exit status and the duration. The account, role and region come from what `PlanExec` resolved
  for the run, not from the command line.
- **Never** the rest of the command line, the arguments, the environment or the command's output: arguments can
  carry secrets. A test runs a command whose arguments and environment hold a sentinel, and asserts that the
  sentinel reaches no spool file, `/messages` answer, notice, Agent log or CP request.
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

### 4. "Confirm before use" is a per-profile Settings field

- `ssm_profile` gains `confirm_use INTEGER NOT NULL DEFAULT 0`. `ssmProfileDTO` and the bridge's
  `awsProfileWire` gain `confirmBeforeUse` (omitted when false). Any profile kind can set it.
- A run **needs confirmation** when its profile has the field set, or, for a chain, when its source has it:
  a chain runs on its source's role.
- It is a field of the profile, not of the sign-in (ADR 0125). Confirming `prod-admin` confirms nothing else in
  its sign-in, and a login to the sign-in confirms nothing.
- The member's own `~/.aws` profiles have no such field. The Settings list is the only source.

### 5. `af-aws-exec` enforces it, before the credentials

The check runs in `PlanExec` after the profile checks of ADR 0102 decision 1 and **before** any credential
export or login. Confirmation first, then login: a confirmation is cheap, and asking for it first means a
denied run never starts a device code.

- **Approval scope:** one approval covers that session and that profile for 8 hours, or until the session
  ends, whichever comes first (decision F). The record is an Agent state file under
  `AgentStateDir/aws-confirm/`, keyed by session and profile.
- **Asking:** a run without a live approval files a **confirmation request**, with the same machinery as a
  login request: one per session and profile, waiters, a random id, a notice of kind `aws-use-confirm` that
  carries only the id. The Console reads the request from the Agent (`GET /aws-confirm`) and shows:
  - a card in the session's mirror, under the latest turn: "`<session>` wants to use `prod-admin`
    (123456789012 / AdministratorAccess)", with **Allow for this session** and **Deny**;
  - the same as a sticky toast when the member is not on that mirror.

  As in ADR 0102, the profile, account and role come from Settings, never from the request.
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
  `aws --profile prod-admin` then fails instead of quietly working, and `af-aws-exec` renders it into the
  run's isolated config only. A tool that needs the profile by name runs under `af-aws-exec`, which already
  wraps any command.
- **`af-aws-exec` keeps no role credentials of a gated profile on disk:** the child gets them in its
  environment, and the CLI's credential cache entry is removed after the export.

What only the member can do: **the only gate an agent cannot pass by itself is a token it does not have.** A
profile kept on its own sign-in and signed out after use still needs a device-code approval each time.
Grouping a gated profile into a sign-in that other profiles keep signed in removes that (ADR 0125). The
Console says so when a gated profile joins a shared sign-in, or gets the gate while it is in one (decision
G).

### 7. Order relative to ADR 0125

The shared sign-in does not ship before this ADR's run rows, and the gate ships with or before it:

1. run rows and mirror display (Agent and Console; no CP change);
2. confirm before use (CP field and migration, Agent, Console);
3. to 5. ADR 0125's three PRs (CP, Agent, Console).

Migrations: the gate's column takes the next numbers (SQLite `0116`, Postgres `0101` on `main` today), and ADR
0125's sign-in table the ones after it. Both are re-checked against `main` when each change is opened.

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
