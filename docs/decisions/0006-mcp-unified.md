# 0006. MCP — expose the admin surface and the working surface as one (PAT auth; E is the point)

English | [日本語](0006-mcp-unified.ja.md)

- Status: decided. Implemented = stage 1 (member/drive + PAT + `/mcp`) plus admin read/write, both live-E2E green (2026-07-01) / the dangerous stage remains (waiting on the groundwork for key rotation and idle detection)
- See also: [roadmap P3-6](../roadmap.md#p3-6-mcp-による-agent-fleet-制御管理面--作業面を一体で) / [history/p3-6-mcp](../log/p3-6-mcp.md) / [build/01 §1.4 Authentication is two layers](../build/01-architecture.md) (formerly architecture, "authentication scope") / [build/07 Security](../build/07-security.md) (formerly security)

## Context

The CP has REST plus the Console, but that means it **can only be driven by the client a human
built (the Console)**. Operators and members want to drive and observe the fleet in natural
language from their own Claude (Claude Code / Desktop / claude.ai). In particular the very thing
this project started from — **one Claude at hand driving the several claude/opencode/codex
sessions running inside its own workspace** — is the point of putting the fleet on MCP. You
cannot get there by wrapping REST in a hand-written client.

Hence the decision (2026-06-29) to expose **both** the admin surface (the old P3-6 idea) and the
working surface (a member driving their own sessions remotely) from a single MCP server.

## Decision

### 1. Only the entrance is unified (not the service layer)

Only the **transport, the authentication/RBAC and the audit** of `/mcp` are unified. There are
still two backends behind it: admin tools go to the CP's management service layer, member tools
proxy through `manager.resolve` to the per-container Agent (`/sessions`, `/repos`, `/fs`). The
principle of **a thin wrapper that adds no new logic** holds. "One service layer" was a
statement about the admin surface; the working surface is the Agent's surface and is not merged
into it. Do not conflate the two.

### 2. Authentication = PAT (issued by the CP, inheriting the issuer's role)

- **Each user issues their own PAT in the Console (CP).** No separate service principal.
- The token references an **identity + membership**. The **role is resolved live from the store
  on every call** (never baked into the token), so a demotion or a deleted membership disarms
  existing tokens immediately.
- **The role is a ceiling on capability.** A token issued by an admin can reach admin tools; a
  member's token reaches only the working tools for their own membership. This satisfies "an
  admin's PAT for admin things".
- **The scope is chosen at issue time (≤ role)**: `read` (default) / `write` /
  `admin:dangerous`. Even an admin gets `read` by default. This makes **read/write separation
  fall out naturally in the form of "one person holds several tokens"**: a reading Claude gets a
  read token, destructive operations need a different one.
- Revocation, TTL and rotation from the start (issue/list/revoke UI in the Console). The string
  is shown once at issue time; only a hash is stored.
- **The tenant is fixed in the token.** A client-supplied `X-AF-Tenant` is not accepted over MCP
  (cross-tenant access is closed off).
- The on-prem oauth2-proxy (Google forward-auth) does not mesh with MCP clients' OAuth2.1/DCR,
  so PAT is primary. Native OAuth2.1 support is secondary, for the stage where we go after
  claude.ai/Desktop (AWS onwards).

### 3. Transport = Streamable HTTP

One new route `/mcp` on the CP (no new process). **Streamable HTTP**, not the old HTTP+SSE
spec. The Go SDK from the official project is the first choice, with a pinned version (the MCP
transport has already split once).

### 4. One tool registry, filtered by capability from the principal

Which tools are visible is decided by role + scope. The posture is deliberately asymmetric
across roles:

- **member/drive (E, the main goal)**: `list_my_sessions` / `send_to_session` /
  `get_session_status` / `get_session_output`. Your own BYO claude driving your own workspace is
  **the same trust domain and self-contained**; the blast radius is your own workspace, so it is
  not subject to strict read/write separation.
- **admin/read**: `get_usage` / `list_*` / `tail_audit`.
- **admin/write**: `start_workspace` / `stop_workspace` / `stop_session` / `set_user_quota`,
  etc. (the `write` scope).
- **admin/dangerous**: `rotate_key` / `recreate_workspace` / `stop_all_idle` (the
  `admin:dangerous` scope, plus a `confirm` argument, plus `dry_run` defaulting to true — these
  cut across the fleet, so they are strongly gated).

RBAC is **always re-checked in the service layer**; the MCP layer's capability filter is UX, not
the authority on authorisation. Every call is written as
`AuditLog(actor_kind=mcp, principal, token_id)` (the schema already has `mcp`).

### 5. E (member remote drive) is a first-class goal, moved earlier

E rides on assets that already exist: the state hook (`working|idle|question`,
`session_status.go`) can decide when a send has completed, tmux `send-keys` injects a prompt,
the deterministic sid names the target, and the jsonl transcript yields the reply. **The only
new thing needed is one Agent endpoint, `get_session_output` (a tail of the jsonl or of
capture-pane).** The loop on the Claude at hand is `send → poll status until idle|question →
get_output` (→ answer if it is a question). N sessions in parallel = driving a fleet. So E is
promoted into Phase 1 (changed from the old plan, where it was an optional last stage).

## Consequences, and the honest limits

- **Both surfaces in one, sorted out by role** works cleanly precisely because of the posture
  asymmetry between member (self-contained driving) and admin (fleet-wide, confirm required).
- **The biggest risk specific to this is prompt injection × a confused deputy on the mutating
  tools** (the admin side). A Claude that has been made to read audit logs or files, and also
  holds rotate/stop_all, can be steered into destructive operations by injection → killed by
  separate read/write tokens, a human `confirm` on dangerous tools, and dry-run. E (member) is
  self-contained and out of scope for this.
- **Blast radius**: an admin token is the key to that deployment's management surface. Leaking
  it means the management surface is compromised. Held down by short lifetimes, rotation, a read
  default, scope separation and audit. **It does not spread between companies, because
  deployments are separate** — the strength of this model.
- MCP only adds a mouth to the CP; it creates no new trust boundary. Conversely, **MCP
  authentication is itself the CP's attack surface**, so do not weaken the entrance.
- The member MCP's read tools are complementary, since the Console exists. E (drive) is primary;
  read expansion is secondary.
- Shipped off by default (`AF_MCP_ENABLED`), configured in the P3-10 runbook (`/mcp` on the
  ingress must pass Bearer through — one path exclusion in oauth2-proxy). No phone-home.
