# 0146. Google Workspace MCP servers: a tenant-distributed OAuth MCP with one sign-in, and confirmations answered by a person in the Console

English | [日本語](0146-google-workspace-mcp.ja.md)

- Status: **proposed** (2026-11-25). Nothing is built yet. The facts below were measured on
  2026-11-25 against Google's public metadata and an unauthenticated `tools/list`; nothing has
  signed in to a real Workspace tenant.
- Tracking: #5179
- Follow-ups: #5173, #5175, #5174, #5177, #5176, #5178
- Related: [0119](0119-mcp-client-oauth.md) (OAuth for registered MCP servers and the relay this
  builds on, and whose "the relay does not parse MCP" decision 6 narrows) /
  [0031](0031-mcp-registry.md) (the registry and tenant distribution) /
  [0032](0032-mcp-2026-07-28.md) (the protocol revision Google's servers speak) /
  [0015](0015-agent-managed-driver.md) (the managed drivers' interaction channel) /
  [0107](0107-google-cloud-profiles.md) (the wrapper shape decision 8 borrows) /
  [0122](0122-egress-gateway.md) (the egress allowlist)

## Context

Google made dedicated remote MCP servers for Workspace (Gmail, Drive, Docs, Sheets, Slides,
Calendar, Chat, People, Tasks, Classroom, universal search) and the Workspace CLI `gwscli` generally
available on 2026-11-25. From **2027-02-01** Google technically requires AI agents to use those
instead of the Workspace REST APIs. Ordinary apps keep the REST APIs. Usage-quota billing starts on
**2027-04-01** and is charged to the Cloud project that owns the OAuth client. The Workspace AI Tools
Addendum forbids permanent storage of raw data, extended retention of derived data, model training
and bulk scraping.

What Google asks of a client:

- **An agent-flagged OAuth client.** "Use this client for an AI-powered agent" is a checkbox on an
  ordinary client ID, and the Workspace servers reject calls authorized with any other client. The
  setup guide names a **Web application** client for hosted MCP clients.
- **Form elicitation.** Before sending mail, changing sharing, trashing or bulk edits (and whatever
  admin policy adds), a `tools/call` answers `resultType: "input_required"` with a form
  `elicitation/create`. The client retries the identical call with the opaque `requestState` and
  `inputResponses`. Options are `ALLOW_ONCE` / `DENY_ONCE`, and `ALLOW_TOOL_ALWAYS` where policy
  allows it. A client that does not declare `elicitation.form` gets JSON-RPC `-32021`. Each server
  also serves `/mcp/v1self`, which omits the tools that always need approval, for hosts without
  elicitation.
- **Protocol.** Stateless Streamable HTTP, POST only, MCP 2026-07-28.

What af already has (ADR 0119): `auth: oauth` definitions, tenant distribution, a tenant
pre-registered confidential client whose secret stays in the CP, the CP callback
`{PUBLIC_BASE_URL}/oauth2/mcp/callback`, and a relay in the workspace that injects the token and passes
bytes through.

Measured gaps (2026-11-25):

1. The PRM at `https://gmailmcp.googleapis.com/.well-known/oauth-protected-resource/mcp/v1` names
   `https://accounts.google.com/`; the OIDC metadata says `issuer: https://accounts.google.com`.
   `probe_oauth.go` refuses the byte mismatch.
2. An unauthenticated `tools/list` answers 200 with the full tool list. No 401 challenge names a
   scope, so `selectScope` asks for every `scopes_supported` of the PRM: `https://mail.google.com/`,
   `gmail.send`, `gmail.settings.*` and more.
3. Google's metadata lists no `offline_access`; a refresh token needs `access_type=offline` (and
   `prompt=consent` for a user who consented before). Without it the grant dies after an hour.
4. One grant per server: ten services, ten sign-ins.
5. Nothing in the tree handles elicitation. The relay is transport-only; the codex managed driver
   leaves unknown server requests unanswered; the ACP drivers answer `-32601`; `mcpc` declares empty
   capabilities. Which CLIs answer `input_required` themselves is unmeasured.
6. The hosts are `gmailmcp.googleapis.com`, `drivemcp.googleapis.com`, …, and People is
   `people.googleapis.com`. A dot-suffix rule cannot cover them; `.googleapis.com` is deliberately
   kept off the allowlist (`egress_policy.go`).

## Decision

### 1. Workspace MCP is a tenant-distributed OAuth MCP server, with the tenant's own client

af ships no Google OAuth client. The tenant admin creates an agent-flagged Web application client in
the tenant's own Cloud project, with the redirect `{PUBLIC_BASE_URL}/oauth2/mcp/callback`, and
enters it as ADR 0119's tenant pre-registered confidential client. The consent screen, Google's
app verification, the Addendum and the quota bill all belong to that project, which is where Google
puts them. A member may still register a server for themselves with their own client (0119 decision
3.1).

### 2. OAuth compatibility (#5173)

- **Issuer equivalence.** An issuer whose path is exactly `/` equals the same issuer with an empty
  path (RFC 3986 §6.2.3, scheme-based normalization). Nothing else is normalized; `iss` in the
  authorization response is compared with the issuer that the AS metadata states. A server that
  validates only through this allowance ranks after every exactly matching one when the first
  valid server of the PRM is selected (0119), so a PRM that already worked keeps its server.
- **Scopes on the definition.** A definition may carry an explicit scope list. When it does, the
  list replaces the challenge and `scopes_supported`. A Workspace definition always carries one.
- **Authorization parameters per issuer.** For `https://accounts.google.com`, the authorization
  request adds `access_type=offline` and `prompt=consent`. Consent is asked on every sign-in, not
  only when the stored grant has no refresh token: Google returns a refresh token only on a
  consented authorization, and a sign-in (a step-up, a reconnect) replaces the stored grant, so one
  made without consent would leave a grant that dies after an hour. This is a table keyed by
  issuer, not a free-form field an admin fills in.
- **Probe.** A 200 probe that finds a PRM reports the OAuth findings anyway, so the form can offer
  OAuth for a server whose tool list is public.

### 3. A Workspace preset in tenant MCP settings (#5175)

One form takes the client (id, secret), the services to offer and a scope bundle. It writes one
distributed definition per service:

- **read-only:** `*.readonly`, `drive.readonly`, `calendar.events.readonly`, …
- **standard:** adds `gmail.compose`, `drive.file`, `documents`, `spreadsheets`, `presentations`,
  `calendar.events`, `chat.messages.create`

`https://mail.google.com/`, `gmail.send` and `drive` (full) are never in a bundle. The preset is a
writer of ordinary definitions; nothing downstream knows it exists.

### 4. One grant per client and issuer

Definitions with the same pre-registered client at the same issuer share a grant. Signing in to any
of them requests the union of their scopes, and every one of them is then connected. The relay's
check that the token goes only to a URL the grant names (0119 P3 note) becomes "a URL in the grant's
set". Adding a definition to the set, or a scope to one, shows "needs more permission" (0119's
step-up state) rather than a silent second sign-in.

### 5. `/mcp/v1self` until decision 6 ships

The preset writes the `/mcp/v1self` URLs while the relay cannot answer elicitation. The tools that
always need approval are then absent instead of failing with `-32021`. When decision 6 ships, the
preset rewrites them to `/mcp/v1`; that changes the URL, so per 0119 the grant follows the set
change of decision 4 rather than ending.

### 6. The relay answers elicitation, and only a person can say yes (#5174)

For definitions flagged `elicitation: relay` (the preset sets it), the relay parses JSON-RPC:

- It adds `elicitation.form` to `_meta["io.modelcontextprotocol/clientCapabilities"]` of each request.
- When a `tools/call` result is `input_required`, the relay holds the CLI's request open. It files an
  approval with the Agent, carrying the server, the tool, its arguments (as the server will run
  them) and the form's options. The Console shows it as an approval card on that session's pane,
  in Managed and Terminal (CLI) alike, and as a notification.
- The member's choice is sent back as `inputResponses` with the unchanged `requestState`, and the
  final result goes to the CLI. A decline, a closed card or the time limit is sent as `decline`; the
  server then answers "Action canceled", which the agent sees.
- `ALLOW_TOOL_ALWAYS` is offered only when the server offers it.
- The relay never answers on its own. Whatever the CLI declared, the CLI does not answer either:
  the result the CLI sees is always `complete`. An agent in bypass mode therefore cannot confirm its
  own action, which is the point of Google's confirmation.
- Each answer is an audit row `mcp.elicitation` (server, tool, choice, who). It carries no arguments
  and no content.

This narrows 0119's rejection of "a full MCP proxy that parses messages" to flagged definitions.
Static-header and other OAuth definitions stay transport-only. The time limit must fit the CLIs'
tool-call timeouts, which is the first measurement of #5174.

### 7. Guide and policy (#5177)

- **Admin guide:** creating the client, enabling the `*mcp.googleapis.com` services, the exact egress
  hosts, and who pays from 2027-04-01.
- **Member guide:** signing in once, and what the approval card means.
- **Notice:** from 2027-02-01, anything that calls the Workspace REST APIs as an agent stops working.
  That includes community MCP servers built on REST and REST calls through `af-gcloud-exec`.
- **Addendum and stored data:** session transcripts and agent memory can hold mail and file content.
  Whether af needs a retention setting for them is the question #5177 carries.

### 8. The Workspace CLI later (#5176)

`gwscli` signs in through a loopback redirect, which a member's browser cannot reach in a workspace.
Its confirmations wait on stdin, and its token flag shows in the process list. It gets an on-demand
install and a wrapper in `af-gcloud-exec`'s shape when MCP is in use and someone needs the CLI.

## Rejected

- **Each CLI's own elicitation.** Support is unmeasured and differs by kind. In bypass mode the agent
  is the one asked, and a confirmation the agent can give itself is not the human confirmation
  Google requires.
- **An af-owned Google client shared by every deployment.** One verification, one quota and one
  Addendum party for all customers' data, and af has no Cloud project to hold them.
- **Only `/mcp/v1self`, permanently.** Sending a reviewed draft is a core use, and v1self omits it.
- **Requesting `scopes_supported` as 0119 does.** That asks every member for full mailbox control to
  read a thread.
- **Fronting the servers with Google Cloud Agent Gateway or Model Armor from af.** Those are the
  customer's Cloud controls. af's egress gateway already covers the hosts, and the relay is af's
  request path.

## Consequences

- **Agent (`mcpreg`):** issuer equivalence, definition scopes, the per-issuer parameter table, probe
  reporting, grant sets, and the parsing relay path with approvals.
- **CP:** the definition fields (scopes, `elicitation`), the preset API, and the `mcp.elicitation`
  audit.
- **Console:** the preset form, the approval card wired to the relay's approvals, and the row state
  for a grant set.
- **`mcpc`** (lcpp, muse) is reached through the relay, so it needs no elicitation code of its own.
- **Live run:** #5178.

## Notes

- **Sources read on 2026-11-25:**
  - Google Cloud Blog, "Deploying agents safely in your enterprise"
  - developers.google.com/workspace/guides/configure-mcp-servers, …/mcp-elicitation,
    …/workspace-cli
  - support.google.com/cloud/answer/15549257 (agentic client)
- **Measured on 2026-11-25:**
  - PRMs for Gmail and Drive at the path-inserted well-known URL. The root form answers 404.
  - `accounts.google.com`'s `oauth-authorization-server` document answers 404, and its OIDC document
    answers 200 with `S256`, `authorization_response_iss_parameter_supported: true`, no
    `registration_endpoint`, and no CIMD.
  - An unauthenticated `tools/list` with `Mcp-Method` answers 200. Without `Mcp-Method` it answers
    `-32020` "Header mismatch".
- **Not verified:** whether Google accepts or ignores RFC 8707 `resource`; the setup guide's line
  that the Developer Preview Program is required, which may predate GA.
