# 0102. `af-aws-exec` asks the Console for the SSO login, and the device code starts only on the member's press

English | [日本語](0102-aws-login-through-the-console.ja.md)

- Status: **proposed** (2026-09-27). Implementation follows in a separate change.
- Issue: #1026
- Follow-ups: #1028, #1029
- Related: #1010 (acceptance of `af-aws-exec`, where the gap was found) / #1025 (the SSM resume
  button skipped the login modal; the device-code view is shared with it)

## Context

`af-aws-exec` runs a command with the credentials of one Settings > SSM profile. When the cached IAM
Identity Center login is missing or expired, it logs in only if a person is at a terminal
(`awsx.PlanExec`, `Login: "auto"`). An agent's shell has no terminal, so the command exits 3 and prints
`aws sso login --profile <name> --use-device-code --no-browser`. The agent then has to ask the member to
open a terminal and run it. In the #1010 acceptance run this was the one step the member had to leave the
Console for.

Most of what the Console needs already exists:

- **One token cache.** SSM sessions and `af-aws-exec` use the same `[sso-session af-<name>]` (rendered
  from Settings by `awsx.renderProfile`; `ssoOnlyConfig` keeps the name on purpose) and the same
  `~/.aws/sso/cache`. A login done anywhere serves both.
- **A device-code view.** `SsmLoginModal` shows the verification URL and code of an SSM session's login.
  The Agent reads them off the session's tmux pane (`parseSSMLogin`).
- **A channel to the Console.** The notification outbox (`internal/notice`). A CLI subprocess can write to
  it directly, as `workspace-agent notify-arch-residue` does. The Control Plane stores any kind and any
  payload it drains, so a new kind needs no CP change.

One fact changes the design the issue sketched. **An agent's shell holds `AGENT_TOKEN`.** It is passed
down so that the af MCP server can reach the Agent (`mcpreg/attach.go`), and it is the same token the
Control Plane uses. Any process in an agent session can therefore call any Agent route. "Only the Console
may start the device authorization" cannot be enforced by who is allowed to call the route.

The rule it has to keep is the one `workspace/notes/aws.md` teaches: **approve only a code you started
yourself.** A device code is a phishing primitive. Whoever holds the code can have the member approve a
login that then belongs to them.

### What this design protects, and what it does not

Agents run as the same user as the Agent. Anything that user can write, an agent can write: the outbox,
the request files, `~/.aws/sso/cache`, and the `aws` binary itself (`install-awscli` puts it in
`~/.local/bin`). A hostile agent in the member's own workspace is therefore **not** inside what this
design can defend. It can read a token from the cache after any login, and it can replace `aws` so that
the member's own press prints a code of its choosing.

What the design does defend:

- A code started by anyone other than the member's press — an agent calling the start route, a code
  printed in a chat, a code from outside the workspace — never appears in the Console's modal or toast.
- The account, role and verification URL the Console shows come from Settings and are checked against
  it, not from text a caller wrote.

The guide's rule stays the member's protection for everything else.

## Decision

### Decision 1 — without a terminal, `af-aws-exec` files a login request instead of only failing

When the login is needed, `--no-login` is not given and nobody is at a terminal, `af-aws-exec` registers a
**login request** and then waits (decision 5).

- The request is filed only after every check `PlanExec` already makes before a login has passed: the
  profile is exported, not shadowed by the member's own definition, not incomplete, not held back by
  `[DEFAULT]`. Otherwise the daemon could log in to an `af-<name>` key that a section of the member's own
  also uses.
- The request is a file under the Agent's state directory (`paths.AgentStateDir()/aws-login/`), written
  under a lock by the CLI process itself. No Agent route is needed to file it. A request survives an
  Agent restart, and filing one does not depend on the Agent being up.
- **One request per sso-session.** A second `af-aws-exec` for the same sso-session joins the existing
  request as another waiter. Today each Settings profile has its own sso-session (`af-<name>`), so in
  practice this means one request per profile. Each request has a random id, new each time a request is
  created.
- The request records the profile name, when it was first and last asked for, and the waiters: the
  requesting session's name from `AF_SESSION_NAME` when it is set, and the command's first word. Both are
  text an agent wrote. They are cut to a short length and a restricted character set before they are
  stored, and they are shown as "who asks", never as a statement about the account.
- The **account and role shown to the member are not taken from the request** (decision 2).
- Only **Settings profiles** can be logged in this way. For a profile the member defined in their own
  `~/.aws` (run with `--account`), the Agent has no validated source for the account and role to show.
  That case keeps today's exit 3 and the terminal command.
- A request expires 15 minutes after the last `af-aws-exec` asked for it, but never while an attempt for it
  is running (decision 3).

### Decision 2 — the Console shows a sticky toast at the bottom, with a "Log in" button

Filing the request also writes a notification of a new kind, `aws-login-required`, to the outbox. It is
one notification per request, not one per waiter: `notice.PutOnce` keyed by the request's random id, so a
new request for the same profile later gets a notification of its own. Its target is the requesting
session when known, and the workspace otherwise.

**The notification is only a trigger.** Anyone running as the member's user can write the outbox with any
payload, so the Console reads nothing from it but the request id. On delivery it asks the Agent through
`GET /api/aws-login`. The Agent joins each pending request with its Settings profile and returns the
profile, the account and role from Settings, the waiters of decision 1, and whether the request is still
pending. An id the Agent does not know shows nothing.

If the request is still pending, the Console shows a **sticky toast at the bottom of the screen**, the same
form as the "new version available" toast (`UpdateToast`, `duration: 0`). The toast names the profile, the
account and role, and who asks. It has one button, **Log in**, which opens the login modal (decision 3).

- The notification center alone is not enough. The request waits for a person, and a row in the center is
  easy to miss while an agent's command is waiting on it.
- The Console does not open the modal by itself. A modal that appears while the member types in a terminal
  pane takes their keystrokes.
- **The Agent decides whether a request is resolved.** A request is resolved when the token cache of its
  sso-session (`~/.aws/sso/cache/<sha1 of the sso-session name>.json`) holds a token that has not expired
  — however the login happened, from this Console, another tab or a terminal — or when it is cancelled or
  expired. Reading that file's `expiresAt` costs a file read, not an `aws` run. The Agent drops resolved
  requests.
- While a toast is up, that tab asks `GET /api/aws-login` every few seconds and removes the toast once the
  request is no longer pending. The Console also asks once when it starts, so a reload does not lose a
  pending request. Nothing polls while no toast is up.
- The toast's close button hides it in that tab only. It comes back on the next reload, or when a new
  request's notification arrives. The request itself stays pending.

### Decision 3 — the device code is started by the press and shown only to the tab that pressed

"Log in" in the modal calls `POST /api/aws-login/{id}/start`. Only then does the Agent daemon run
`aws sso login --use-device-code --no-browser` for that sso-session. It runs detached from any pane, with a
config file that holds only the sso-session rendered from Settings (the same shape as `ssoOnlyConfig`).
The Agent reads the verification URL and the code from the command's output, with the same patterns as
`parseSSMLogin`.

- **The URL is checked before it is shown.** Its host must be the device-authorization host of the
  profile's Settings `sso_region` or the host of its Settings start URL. Anything else ends the attempt as
  failed, and nothing is shown. (`parseSSMLogin`'s pattern accepts any https host.)
- The attempt succeeds when the command exits 0 and the token cache of decision 2 then holds a token that
  has not expired. The patterns have been proven only against a terminal's output, so before the
  implementation is accepted it is measured that the CLI writes the URL and code to a pipe before it
  blocks.

`start` returns an **attempt id**. The modal polls `GET /api/aws-login/{id}/attempts/{attempt}` and shows
the URL and code of that attempt only. Every `start` begins a new attempt and ends the one running before
it. Because of this:

- A code reaches the screen only in the modal of the tab whose press created it.
- An agent that calls `start` itself gets an attempt that no modal shows. What it can do with that code —
  print it in the chat — it can already do today by running `aws sso login` in its own shell. The guide's
  rule covers that case, and this design does not weaken it.
- If another `start` ends the member's attempt, the modal says the attempt was replaced and offers to start
  again. It never switches to the other attempt's code.
- An agent that keeps calling `start` can keep replacing the member's attempt, so the member can never
  finish. This is accepted: it is visible (the modal says it was replaced), it logs nobody in, and an agent
  that hostile can already do worse as the same user.
- `GET /api/aws-login` never returns attempt ids, URLs or codes. Only the attempt route returns them, and
  only for the attempt id asked for.

Attempts live in the Agent's memory. If the Agent restarts during one, the `aws sso login` child ends with
it, `GET …/attempts/{attempt}` answers that the attempt is gone, and the modal offers to start again. The
request itself is on disk and stays pending.

"Cancel" calls `POST /api/aws-login/{id}/cancel`. It ends any running attempt and drops the request. The
waiting `af-aws-exec` runs then exit 3 at once, saying that the login request was **cancelled** (not that
the member declined it: an agent can call the route too). For 10 minutes after a cancel, a new
`af-aws-exec` for that profile files no request and shows no toast. It exits 3 at once, saying that the
login was cancelled in the Console and to ask the member. Without this, the next run would put the toast
straight back.

The device-code presentation (code, "Sign in" button that the member opens by hand, the warning) is
factored out of `SsmLoginModal` into one component that both modals use. `SsmLoginModal`'s props and the
`ssmResume` store stay as they are.

### Decision 4 — the Control Plane relays four routes and audits the start and the cancel

The CP adds `GET /api/aws-login`, `POST /api/aws-login/{id}/start`, `GET /api/aws-login/{id}/attempts/{attempt}`
and `POST /api/aws-login/{id}/cancel` to the member's agent proxy, for the member's own Workspace only. It
audits `start` and `cancel` as `aws.login.start` / `aws.login.cancel` with the profile name.

That audit covers only what came through the CP. An agent can call the same routes on the Agent directly
with `AGENT_TOKEN`. The Agent therefore also writes every start and cancel to its own log, with the
profile name and whether the call came through the CP. The code and the URL are never logged or audited,
on either side.

### Decision 5 — `af-aws-exec` waits about 90 seconds, then exits 3 with the request still pending

As soon as the request is filed, `af-aws-exec` prints that the login was requested in the Console. It
prints this first, before waiting, so that a run killed by its caller's timeout has still said it. It then
waits up to 90 seconds.

- While waiting it reads the request file and the `expiresAt` of the sso-session's token cache every two
  seconds, which is cheap. It asks the CLI for the credentials again (`exportCreds`, a whole `aws` start)
  only when that cache file shows a token that has not expired.
- If the credentials come back, the command runs as if the login had been there from the start.
- If the request is cancelled, it exits 3 at once (decision 3).
- On timeout it exits 3. The message says the login was requested in the Console and to run the command
  again after approving it. The request stays pending, so the toast stays up.

The 90 seconds assume the agents' command timeouts are longer. Before the implementation is accepted, each
agent kind's default timeout for a shell command is measured and recorded in the implementation's journal.
If a kind's timeout is shorter, the immediate line above is what tells that agent where the login is; the
wait is not shortened for it.

### Decision 6 — the flags and the terminal case do not change

- `--login` at a real terminal keeps the in-terminal device-code login.
- `--no-login` never files a request and never waits.
- Exit 3 keeps its meaning: "SSO login required and not completed here".

`workspace/notes/aws.md` (the `af-aws` skill) and the member guide's integrations chapter
(`guide/member/10-integrations.md` and `10-integrations.ja.md`) describe the new exit-3 wording. They also
tell the agent to say that the login is waiting in the Console rather than asking the member to open a
terminal.

## Rejected

- **The CLI calls an Agent route to file the request.** Filing works without the Agent through the state
  directory, as the outbox already does. A route would add a failure mode and give no extra protection,
  since the caller holds `AGENT_TOKEN` either way.
- **Hide `start` from agents.** Every route is callable with `AGENT_TOKEN`. Separating the Console's token
  from the one agents hold is a larger change to CP↔Agent authentication. Binding the code to the press
  (decision 3) gives the property that matters without it.
- **The Console opens the modal by itself** (the issue's first sketch). It interrupts typing (decision 2).
- **Notification center only.** Too easy to miss for something a command is waiting on (decision 2).
- **Keep the device code running in the caller's shell and scrape it**, as SSM sessions do. An agent's
  shell has no pane that the Console shows, and the code would start before anybody pressed anything.
- **Build the toast's text from the notification's payload.** The outbox is writable by every agent, so
  the account shown could be anything (decision 2).

## Consequences

- The member stays in the Console for the whole `af-aws-exec` flow. The agent sees either a completed
  command or an exit 3 that says the login is waiting in the Console.
- A new sticky toast kind exists. The toast system has no way to withdraw a toast from code today, so the
  implementation adds one.
- A tab with a login toast up polls one cheap Agent route every few seconds until the request is resolved.
- Not covered here, each a separate issue: a "Log in" action on a Settings > SSM profile row that opens the
  same modal, and warning before an SSO session ends. The acceptance run against a real IAM Identity Center
  also stays open until a member runs it.
