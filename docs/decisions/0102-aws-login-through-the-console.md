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
  it directly, as `workspace-agent notify-arch-residue` does.

One fact changes the design the issue sketched. **An agent's shell holds `AGENT_TOKEN`.** It is passed
down so that the af MCP server can reach the Agent (`mcpreg/attach.go`), and it is the same token the
Control Plane uses. Any process in an agent session can therefore call any Agent route. "Only the Console
may start the device authorization" cannot be enforced by who is allowed to call the route.

The rule it has to keep is the one `workspace/notes/aws.md` teaches: **approve only a code you started
yourself.** A device code is a phishing primitive. Whoever holds the code can have the member approve a
login that then belongs to them.

## Decision

### Decision 1 — without a terminal, `af-aws-exec` files a login request instead of only failing

When the login is needed, `--no-login` is not given and nobody is at a terminal, `af-aws-exec` registers a
**login request** and then waits (decision 5).

- The request is a file under the Agent's state directory (`paths.AgentStateDir()/aws-login/`), written
  under a lock by the CLI process itself. No Agent route is needed to file it. A request survives an
  Agent restart, and filing one does not depend on the Agent being up.
- **One request per sso-session.** A second `af-aws-exec` for the same sso-session joins the existing
  request as another waiter. Today each Settings profile has its own sso-session (`af-<name>`), so in
  practice this means one request per profile.
- The request records the profile name, when it was first and last asked for, and the waiters (the
  requesting session's name from `AF_SESSION_NAME` when it is set, and the command's first word). The
  **account and role shown to the member are not taken from the request.** The Agent looks them up in the
  Settings profile when it shows the request, so a caller cannot put a misleading account on the screen.
- Only **Settings profiles** can be logged in this way. For a profile the member defined in their own
  `~/.aws` (run with `--account`), the Agent has no validated source for the account and role to show.
  That case keeps today's exit 3 and the terminal command.
- A request expires 15 minutes after the last `af-aws-exec` asked for it.

### Decision 2 — the Console shows a sticky toast at the bottom, with a "Log in" button

Filing the request also writes a notification of a new kind, `aws-login-required`, to the outbox (one per
request, not one per waiter: `notice.PutOnce` keyed by the request). Its target is the requesting session
when known, and the workspace otherwise.

On delivery the Console shows a **sticky toast at the bottom of the screen**, the same form as the "new
version available" toast (`UpdateToast`, `duration: 0`). The toast names the profile, the account and role,
and who asks. It has one button, **Log in**, which opens the login modal (decision 3).

- The notification center alone is not enough. The request waits for a person, and a row in the center is
  easy to miss while an agent's command is waiting on it.
- The Console does not open the modal by itself. A modal that appears while the member types in a terminal
  pane takes their keystrokes.
- The toast goes away when the request is resolved: logged in (from any tab or from a terminal),
  cancelled, or expired. Showing it also does not depend on seeing the notification live: when the Console
  starts it asks for the pending requests once, so a reload does not lose them.

### Decision 3 — the device code is started by the press and shown only to the tab that pressed

"Log in" in the modal calls `POST /api/aws-login/{id}/start`. Only then does the Agent daemon run
`aws sso login --use-device-code --no-browser` for that sso-session. It runs outside any pane, with a config
file that holds only the sso-session rendered from Settings (the same shape as `ssoOnlyConfig`). The Agent
reads the verification URL and the code from the command's output, with the same patterns as
`parseSSMLogin`.

`start` returns an **attempt id**. The modal polls `GET /api/aws-login/{id}/attempts/{attempt}` and shows
the URL and code of that attempt only. Every `start` begins a new attempt and ends the one running before
it. Because of this:

- A code reaches the screen only in the modal of the tab whose press created it.
- An agent that calls `start` itself (it holds `AGENT_TOKEN`) gets an attempt that no modal shows. What it
  can do with that code — print it in the chat — it can already do today by running `aws sso login` in its
  own shell. The guide's rule covers that case, and this design does not weaken it.
- If an agent's `start` ends the member's attempt, the modal says the attempt was replaced and offers to
  start again. It never switches to the other attempt's code.

"Cancel" calls `POST /api/aws-login/{id}/cancel`. It ends any running attempt and drops the request. The
waiting `af-aws-exec` runs then exit 3 at once, saying the member declined the login in the Console.

The device-code presentation (code, "Sign in" button that the member opens by hand, the warning) is
factored out of `SsmLoginModal` into one component that both modals use. `SsmLoginModal`'s props and the
`ssmResume` store stay as they are.

### Decision 4 — the Control Plane relays four routes and audits the start and the cancel

The CP adds `GET /api/aws-login`, `POST /api/aws-login/{id}/start`, `GET /api/aws-login/{id}/attempts/{attempt}`
and `POST /api/aws-login/{id}/cancel` to the member's agent proxy, for the member's own Workspace only. It
audits `start` and `cancel` as `aws.login.start` / `aws.login.cancel` with the profile name. The code and
the URL are never logged or audited.

### Decision 5 — `af-aws-exec` waits about 90 seconds, then exits 3 with the request still pending

After filing, `af-aws-exec` prints that the login was requested in the Console and waits up to 90 seconds.
90 seconds fits agents' tool-call timeouts.

- While waiting it reads the request file, which is cheap. When the Agent records the attempt's success, or
  every 15 seconds in any case (a login done in a terminal does not touch the request), it asks the CLI for
  the credentials again (`exportCreds`).
- If the credentials come back, the command runs as if the login had been there from the start.
- On timeout it exits 3. The message says the login was requested in the Console and to run the command
  again after approving it. The request stays pending, so the toast stays up.

### Decision 6 — the flags and the terminal case do not change

- `--login` at a real terminal keeps the in-terminal device-code login.
- `--no-login` never files a request and never waits.
- Exit 3 keeps its meaning: "SSO login required and not completed here".

`workspace/notes/aws.md` (the `af-aws` skill) and `guide/member/10-integrations.md` describe the new exit-3
wording. They also tell the agent to say that the login is waiting in the Console rather than asking the
member to open a terminal.

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

## Consequences

- The member stays in the Console for the whole `af-aws-exec` flow. The agent sees either a completed
  command or an exit 3 that says the login is waiting in the Console.
- A new sticky toast kind exists. The toast system has no way to withdraw a toast from code today, so the
  implementation adds one.
- Not covered here, each a separate issue: a "Log in" action on a Settings > SSM profile row that opens the
  same modal, and warning before an SSO session ends. The acceptance run against a real IAM Identity Center
  also stays open until a member runs it.
