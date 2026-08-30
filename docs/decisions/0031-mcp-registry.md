# 0031. Register custom MCP servers in a "registry", and have af write them out to each CLI's native configuration

English | [日本語](0031-mcp-registry.ja.md)

- Status: **adopted; P0–P4 implemented** (the registry model, composition, user CRUD, the connection
  test, the Console tab, assistant wiring, session materialisation for claude and codex, and
  **tenant distribution**). The design is [docs/48](../log/48-mcp-registry.md).
- See also: [history/19](../log/19-assistant-chat.md) (the assistant) / [0020](0020-chat-bridge.md) (a CP bridge of the same shape) /
  docs/25 (the built-in ops integrations this generalises) / docs/20 (the egress allowlist)

## Context

Only **three kinds fixed in code** (pagerduty / grafana / cloudwatch) can be used as external MCP
servers (`opsIntegrations` in `assistants.go`). And even those three are **for assistant chat only**;
interactive sessions (claude / codex / opencode, …) have no route at all by which af injects MCP.

For a user to use their own MCP, the only option is to hand-write each CLI's global configuration,
which the Console cannot see and which cannot be managed or distributed. There is no way to give
everyone a company-wide MCP.

## Decision

### 1. Two scopes, user and tenant, with the effective registry composed from them

`effective = builtin(connected) ∪ tenant(enabled) ∪ user`.

- `user` lives in the workspace's encrypted store (`secrets.enc`) — the same key and the same
  lifecycle as connections. No new key management is added.
- `tenant` lives in the CP database. **The CP is the only place that is alive while a workspace is
  stopped**, and distributing an admin's registration to every member has to be held there (the same
  reason as for schedules).
- On a name collision, **tenant wins**. But a user **can disable tenant distribution in their own
  workspace** — leaving an escape hatch for an individual when a broken server takes down everyone's
  session startup.

### 2. Tenant distribution does not allow stdio (it is dropped from the schema)

Tenant-distributed stdio is equivalent to "an admin can run arbitrary commands in every member's
container", and an admin who can distribute a `command` effectively holds root-equivalent power.
Tenant distribution is therefore **remote (Streamable HTTP) only**, and this is enforced not just by
rejecting it in the API but by **not creating `command` / `args` / `env` columns in the CP's table**
at all. The point is not to leave it in a state where it can be loosened later.

Personal-scope stdio is allowed. Running your own command in your own container is no more power
than doing the same thing in a terminal.

**In P4 this single decision was implemented as three independent layers** (so that no one layer
breaking causes real damage):

1. The CP's table has no `command`-family columns (if the column does not exist, it cannot be
   loosened).
2. The CP's API rejects `transport=stdio` with a 400, and the Console's tenant form is **a different
   shape that never sends `command` / `args` / `env`** (a form built by hiding fields from the member
   form would leave "a state in which stdio can be created" theoretically intact).
3. The agent **re-validates and drops** definitions it receives (`mcpreg.acceptTenant`). This works
   even if the CP is compromised or an old row survives. It is **the only check that runs on the very
   machine that would actually execute the command.**

### 3. Sessions receive them by "materialising into the native configuration", not by launch flags

claude's `--mcp-config` has to be used together with `--strict-mcp-config`, which **shuts out the
user's own project `.mcp.json` and global configuration**. A user's MCP disappearing because af
added one is unacceptable.

So af updates each CLI's global configuration file (`.claude.json` / `config.toml` /
`mcp-config.json` / `opencode.jsonc` / `mcp.json`, …) by **read → merge → atomic rename**. Only the
names af wrote are recorded in a separate file, and **deletion is limited to what af wrote**.

There are two prices, both accepted:

- **The configuration contract can break with each CLI version.** It is the same problem as string
  contracts (`false-idle-reverse-heal`), so there is a **drift test** per kind that runs
  `<cli> mcp add` under an isolated HOME and compares the output, turning CI red when it breaks
  (`.github/workflows/mcp-config-contract.yml`; claude and codex wired in P3, opencode/copilot/cursor
  in P5). **Two kinds cannot have this layer**: kiro's `mcp` subcommands all require a login and so
  cannot run on the runner (skipped when not logged in; confirmation is only possible on a logged-in
  machine), and agy will not start on a host without RDRAND. For those two, the measurement record is
  the only corroboration.
- **It takes effect from the next session.** Every CLI reads its configuration at startup. Stated
  explicitly in the Console. Managed sessions alone have a different premise, since the CLI process
  is not restarted; we measured that codex's shared app-server re-reads the config on every
  `thread/start`, and pinned that itself with a drift test (docs/48 §8.3).

Assistant chat is the opposite — **per-launch isolation is right there** — so it keeps using
`--mcp-config` / codex `-c` / an isolated dir. The methods differing between chat and sessions is
deliberate: chat is a throwaway environment af creates, while a session is the user's working
environment.

### 4. Secrets are canonical only inside the store; tenant distribution escapes via `user_secret`

They are always returned to the Console masked (`***`), and a PUT that still contains `***` keeps
the existing value (the existing practice from connections). Becoming plaintext at materialisation
time is unavoidable, so it is limited to `0600` and under home.

Putting a token in a tenant-distributed header means **that token is readable in plaintext in every
member's container**. Isolation cannot prevent this (the person themselves can read it). So there is
a `user_secret` flag: when it is `1`, the tenant distributes **only the URL and the header names**,
and each member puts the value into their own encrypted store. A server with no value entered is not
materialised (better not to emit it than to start it and have it fail).

P4 added this mechanism (defaulting to `0`). Three things settled in implementation:

- When `user_secret=1`, the CP **discards the incoming value on the spot**. Hiding it only on output
  would leave a token an admin pasted before setting the flag sitting in the database, read by
  nobody but still an exposure surface.
- A member can only fill in **the header names the tenant distributed**. Which headers are sent is by
  nature the tenant's prerogative, so an older local value is ignored.
- A member **cannot replace the value** of a row distributed with its value included (`ErrReadOnly`).
  If they could, it would stop connecting with the credentials the tenant intended.

⚠️ **Whether `user_secret=1` should be the default is still an open operational question**
(docs/48 §14-5). Only the mechanism went in; that point was deliberately left alone.

### 5. Run a connection test at registration time (included in P0)

Actually send `initialize` → `tools/list`, and return success/failure and the tool count. Without
it, a user only finds out something is broken when they start a session — and the failure is buried
deep in the CLI's startup log. It is part of the registration flow, in the first phase.

### 6. Normalise the three built-ins into the same type

`opsIntegrations` is folded into `ServerDef` (`Origin=builtin`), and `mcpConfigArgs` becomes **one
list operation** rather than a branch on "built-in or registered". `assistant.Integrations` keeps
using the builtin ids as they are, so **saved assistants need no migration**.

## Options rejected

| Option | Why rejected |
|---|---|
| af acts as an MCP proxy and relays all traffic | af would take on authentication, streaming and OAuth — an order of magnitude more implementation. Egress control already lives in the proxy layer (docs/20), and there is no reason to hold it twice |
| Pass them to sessions with `--mcp-config` too | `--strict-mcp-config` shuts out the user's own MCP (decision 3). And there is no equivalent flag on every kind (agy and cursor have global configuration only) |
| Allow stdio in tenant distribution too, gated on super_admin plus auditing | Gates loosen in operation. If the column does not exist, it cannot be loosened (decision 2) |
| Collect the user scope on the CP | There is no need to gather personal secrets on the CP. Completing it in the workspace's encrypted store is a smaller exposure surface |
| Make tenant distribution fail-closed (if the CP is unreachable, remove the MCP) | A momentary CP outage would make MCP vanish from sessions. Cache it and show it as stale instead |

## Results

- A user can register their own MCP from the Console and use it in both the assistant and sessions.
- An admin can distribute a tenant-wide MCP, but only remote definitions — not commands.
- The three existing built-in integrations sit on the same mechanism, reducing the branches to one.
- Remote definitions mesh with the egress allowlist (docs/20), and an unapproved destination is
  warned about **on the registration screen**. The request route is open to members, but all they can
  create is a `proposed` row; enabling it stays with super_admin (docs/48 §9.1). Making this
  admin-only would leave the person who registered it with no way to learn why.

## Left over

- The old HTTP+SSE transport is out of scope for v1 (deprecated in the specification, and the
  connection test would need a different client).
- MCPs requiring OAuth (each CLI's `mcp login`) are not driven from af.
- Per-tool permissions (agy's `mcp(server/tool)`, copilot's `--allow-tool`) are not in v1.
- Accounting for MCP tool calls (docs/46's remaining P5) is not decided here.
- ~~kiro's and cursor's remote configuration shapes are unmeasured~~ → settled on real hardware in P5
  (docs/48 §8.1). Both were confirmed to carry custom headers over remote, through a real turn and a
  real `mcp list` against a header-recording listener. **Since tenant distribution is remote-only
  (decision 2) and authenticates by header**, a failure here would have broken in the shape of "only
  the distributed servers do not work".
- **Validating a definition happens twice, on the CP and on the agent** (code cannot be shared
  between separate Go modules). The error codes are deliberately identical and drift shows up as the
  agent's re-validation marking something "distributed but unused", but a new rule has to be added in
  both places.
- `localCustodian`'s KEK derives from the master, so `key_ref` is **not cryptographic tenant
  separation** (a property already stated in `custodian.go`). The real isolation rests on the store's
  `tenant_id` scoping and on the distribution surface resolving token → membership → tenant. True
  per-tenant revocation is a matter for the Vault/KMS adapter and is not changed here.
