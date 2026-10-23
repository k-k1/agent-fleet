# 0113. A member-origin mark the Agent can verify: the CP signs a member's relayed write, the Agent holds only the public key

English | [日本語](0113-member-origin-mark.ja.md)

- Status: **accepted** (2026-10-12). Built with #1997 and extended by #2014 and #2076, first released in v0.31.0:
  the CP signs 17 relayed routes (`memberOriginRoutes` in `control-plane/member_origin.go`, with the bundle
  import signed by body hash) and the handoff push it makes itself; the Agent verifies
  (`workspace/agent/internal/memberorigin`) and requires the mark on the member-only writes. `restore`,
  `claude-import`, `muse-import` and `claude-export` stay open by decision (2026-10-14 note). The mark is
  exercised by a real CP and Agent only through the handoff push in `e2e/handoff_test.go`; **no real-fleet run of
  the memory writes is recorded**, and both halves are tested against a shared wire vector.
- Tracking: #1997. Follow-ups: none open (#2014 and #2076 are closed). Related: [0108](0108-af-owned-agent-memory.md) decisions 4 and 8 (the switch, and why an
  approval step is not a boundary) / [0102](0102-aws-login-through-the-console.md) (binding a result to a
  person's press when a route cannot tell who called it) / [07-security §7.2, §7.5](../build/07-security.md#75-cp--agent-authentication).

## Context

Every Agent route accepts the workspace's `AGENT_TOKEN`, and every session's shell holds it: the af MCP
server needs it. The CP adds `X-AF-Relay: cp` to what it relays, but that header is a log hint any caller can
set. So a route meant only for the member's Console cannot tell the member from a session.

For AF memory that matters in three places. `entries/pin` and `entries/revert` (revert also forgets) record the
member as the author by route and are not gated by the `agentMemory` switch, because the switch governs what
sessions may do. A member create/edit route would do the same with arbitrary new text, so a session could write
memory as the member while the switch is off; it was pulled out of #1985 for that reason.

The constraint that shapes the answer: a session runs as the **same uid** as the Agent and shares its
filesystem, process table and network namespace. Anything the Agent can read — its environment,
`/proc/<pid>/environ`, a file, a key handed to it — a session can read too. A shared HMAC key delivered to the
Agent is therefore a key the session holds as well.

## Decisions

1. **The mark is an Ed25519 signature made by the CP; the Agent holds only public keys.** Verifying needs
   nothing secret, so it does not matter that a session can read what the Agent reads. The private key never
   leaves the CP process.
2. **The key is derived, not stored.** Version *N*'s seed is `HMAC-SHA256(token-signing master,
   "af-member-origin-sign/vN")`, the same master the per-membership bridge tokens derive from
   (`tokenSignMaster`: `AF_MASTER_KEY`'s digest, or the deployment's random dev master). Nothing new to
   provision or back up, and a leaked seed of one version says nothing about the next.
3. **Delivery reuses the start env.** `workspaceExtraEnv` injects, at every container start,
   `AF_MEMBER_ORIGIN_KEYS=v<N>:<public key>,v<N+1>:<public key>`, `AF_WORKSPACE_ID` and `AF_MEMBERSHIP_ID`.
   None of it is secret. The Agent reads them once; a session cannot change a running process's environment.
4. **What is signed.** The header `X-AF-Member-Origin: <kid>.<base64url(claims)>.<base64url(signature)>`,
   signature over `"af-member-origin/v1\n" + claims`, where the claims are the JSON bytes as sent (no
   canonicalisation): method, the Agent-side path, the raw query, SHA-256 of the body, workspace id,
   membership id, the CP identity id, `iat`, `exp` (60 s after `iat`) and a 128-bit nonce. The Agent rejects
   any mismatch with the request it received, a lifetime over 2 minutes, an `iat` more than 30 s ahead of its
   clock, an expired mark, and a nonce it has seen.
5. **Replay cache, and a fence for restarts.** The Agent keeps each nonce until its `exp`, at most 4,096.
   When full and nothing has expired it refuses (503) rather than accept a mark it cannot remember: a full
   cache must never become a replay window. The cache lives in the process, so a restart empties it. Any
   mark the previous process accepted had an `iat` at most 30 s (the accepted skew) past that process's
   clock, so at most 30 s past this process's start; the Agent therefore refuses every mark with
   `iat` < start + 31 s (the second covers rounding the start down). That holds whatever the CP's clock
   says, as long as the Agent's own clock does not run backwards across its restart. The price is that
   member writes answer 503 ("try again in a minute") for the first ~31 s after the Agent starts, longer
   by as much as the CP's clock is behind. Persisting the nonces was not chosen: the file would be
   writable by the sessions it is meant to fence.
6. **Which routes, and what never reaches the Agent.** The CP signs exactly
   `POST /api/agents/memory/entries/{pin,revert,member-save}` (`memberOriginRoutes`) and only when the
   workspace is the caller's own membership's. Every relay to the Agent — REST, SSE and the preview relay
   (HTTP, SSE and WebSocket, path and host form) — drops any `X-AF-Member-Origin` the browser sent. REST and
   SSE also drop every cookie (no Agent route reads one; the Console's `af_session` among them) and the
   gateway identity and access-token headers, as the preview relay already did: the member's login cookie
   in the workspace would let a session have the CP sign anything. Before it signs, the CP also refuses
   what another page could make the browser send with the member's cookie: a preview app on its own host
   is same-site, so `SameSite=Lax` still sends the cookie, and a `text/plain` no-cors POST needs no
   preflight. The body must be `application/json` (415 otherwise; a cross-origin page cannot send that
   without a preflight the CP never answers), `Sec-Fetch-Site`, when present, must be `same-origin`, and
   `Origin`, when present, must be the Console's own host or `PUBLIC_BASE_URL`'s (the WebSocket origin
   rule). A caller sending neither header is not a browser and needs the login cookie itself. The Agent
   also refuses a non-JSON body behind a valid mark. The Agent wraps the same three routes in
   `memberorigin.Require`, and their handlers refuse again when no verified claims are in the request
   context, so a route registered without the middleware stays shut.
7. **Unsigned pin and revert are refused.** No compatibility window: the Console reaches these routes only
   through the CP, which signs them, so nothing legitimate sends them unsigned. An Agent older than the CP
   ignores the header (pin and revert behave as before, and `member-save` is a 404); a CP older than the Agent
   sends no mark and the Agent refuses — the feature is off, not open.
8. **Author only when verified — on these three routes.** pin, revert and `member-save` write the author
   `member` / `console` only behind a valid mark. That is not yet true of every route that records
   `member`: the claude and muse imports (`agent_memory_claude_import.go`, `agent_memory_muse_import.go`)
   also commit as `member` and are still unsigned, so an import's author is the route it came by, not a
   verified person, until #2014 brings them under the mark. `member-save` runs the tools' own save
   (revision check, secret scan, limits), is not gated by the switch (like pin and revert), cannot start a
   project, and leaves the project's `project.json` as the sessions wrote it. The CP audits it as
   `memory.entry.save` with an id-shaped project hint only.
9. **Rotation.** `AF_MEMBER_ORIGIN_KEY_VERSION` (CP env, default 1) selects the signing key. A workspace
   holds the public keys of the version current at its last start and of the one after it. Raising the
   version by one therefore keeps every workspace started under the previous version verifying; a second
   raise before it restarts does not (one started with v1/v2 cannot verify v3), nor does raising it by more
   than one or rotating `AF_MASTER_KEY`: such a workspace refuses member writes until it restarts. The old
   private key is never used again. `0` turns signing off (nothing injected, every member write refused).

## Threat model

- **In scope**: a session, or anything running in it, that holds `AGENT_TOKEN`, can set any header, can read
  the Agent's environment and files, and can capture a mark it sees in transit inside the workspace. It cannot
  produce a mark (no private key), reuse one for another route, query, body, workspace or member (all
  signed), or replay one (nonce and expiry; across a restart, the fence of decision 5).
- **What the mark does not do.** It decides what the Agent's own routes accept; it does not make the store
  tamper-proof. A session with a shell can write the store's files and history directly, as the uid that owns
  them, and can replace binaries in `~/.local/bin` that the next start runs. That was already true of every
  author field (decision 8 of 0108 records the same limit), and no in-workspace mechanism closes it.
  `ptrace_scope=1` (measured in this workspace) keeps a session from reading the Agent's memory, but there is
  no private key there to read anyway.
- **As strong as the CP's authentication.** The CP signs for whoever it resolves as the member. Under
  `AUTH=dev` it trusts every caller, so the mark adds nothing there; under `oauth` it needs the member's
  session cookie, which the relays keep out of the workspace (decision 6).
- **Two Agents of one workspace at once.** The restart fence covers a restarted process, not a second
  live one with its own empty cache. On `ecs-ec2` there is none: the service runs with `MaximumPercent`
  100 / `MinimumHealthyPercent` 0, so the old task stops before the new one starts. On Fargate (`ecs`) the
  service keeps ECS's default rolling deployment (200 / 100), so a forced redeploy of a running workspace
  can overlap two tasks for a while. A mark one accepted could then be accepted by the other, but only if
  it reaches it: the workspace security group admits the Agent port from the CP's security group alone
  (07-security §7.2), so a session in one task cannot send to the other's Agent, and the CP relay strips
  any mark it is handed. The bound there is network isolation, not the cache; not verified on real
  Fargate.
- **The path-form preview is the Console's origin.** An app served under `/preview/{port}/` runs on the
  Console's own origin, so neither `Origin` nor `Sec-Fetch-Site` sets it apart, and it can call every
  Console API as the member, not only these routes. That is the reason [0062](0062-preview-subdomain.md)
  decision 7 moved previews to their own hosts; the host form is covered above, the path form is not.
- **Out of scope**: a compromised CP (it holds the master), and a stolen master key.

## Failure modes

| Situation | Result |
|---|---|
| Workspace started before this change, or by a CP with signing off | 503 `member_origin_unavailable` on the three routes until it restarts |
| No mark, or only `AGENT_TOKEN` / `X-AF-Relay` | 403 `member_origin_required` |
| Mark issued within the restart fence (first ~31 s after the Agent starts) | 503 `member_origin_unavailable`; the Console's retry a minute later succeeds |
| Mark for another request, workspace, member, key; expired; replayed | 403 `member_origin_invalid` |
| Body over the CP's 2 MiB buffer | 413 at the CP; nothing reaches the Agent |
| Request for another member's workspace | 403 at the CP; nothing is signed |
| Body not `application/json`; `Sec-Fetch-Site` not `same-origin`; `Origin` not the Console's | 415 / 403 at the CP; nothing is signed |

## Rejected

- **A shared HMAC key in the Agent's env or a file.** Readable by every session (same uid); forgeable.
- **Keeping the Agent's key in another process or uid.** No such process exists in a workspace, and adding a
  root helper contradicts the no-root image.
- **A per-request token the Agent asks the CP to confirm.** Needs a CP round trip on every member write and a
  CP endpoint a session can also reach with its own credentials; the signature needs neither.
- **An unsigned grace period for pin and revert.** Every legitimate caller is the CP, which signs.

## Note 2026-10-13: the other Console memory writes, route by route (#2014)

Decision 6 left the remaining Console-only memory writes unsigned. Each was traced to every caller
(Console, the assistant's MCP tools in `mcpx`, the `af-memory` CLI, scripts, the Agent's own
paths). The Agent's automatic import and write-back on the memory switch (#1972) is not built; when it
is, it calls the importers in process, not through these routes.

| Route | Callers | Decision |
|---|---|---|
| `POST /agents/memory/snapshots` | Console only; the automatic snapshot loop calls `memorySnapshot` in process | **Gated** (`memberOriginRoutes` + `memberorigin.Require`) |
| `POST /agents/memory/import/apply` | Console only (`memoryTransfer.tsx`) | **Gated** |
| `POST /agents/memory/restore` | Console and the assistant's `restore_memory_snapshot` MCP tool (`mcp_stdio.go`) | Open: a legitimate agent caller. Needs a separate Console-only path if it is to be gated |
| `POST /agents/memory/import` | Console, a multipart upload | Open: the CP signs only `application/json` bodies up to 2 MiB, and changing the mark's coverage is out of scope here |
| `POST /agents/memory/claude-import`, `/muse-import` | Console and `af-memory import` / `muse-import` (`afmemory.go`), documented for terminal use | Open: `af-memory` is a session caller. Whether it should lose the write is a product decision |
| `POST /agents/memory/claude-export` | Console and `af-memory export` (`export.go`) | Open, same reason |

Decision 8 therefore still holds for the imports: their `member` author is the route they came by.
Gated routes are listed identically on both sides; `TestMemberOriginRoutesAgreeWithTheControlPlane`
(Agent module) reads the CP's table and probes the real mux, so a one-sided change fails.

`import/apply` buffers up to 2 MiB (the CP's own cap on a body it signs), not the save limit: its body lists
every selected slug, and a large bundle can exceed the save limit.

Follow-ups: #2076

## Note 2026-10-14: import is marked by body hash; restore and the CLI routes stay open (#2076)

- **`POST /agents/memory/import` is gated.** The upload is multipart and up to 64 MiB
  (`AF_MEMORY_IMPORT_MAX`), far over the 2 MiB the CP buffers for a JSON mark, so the mark signs the
  body's SHA-256 as before but the hash is computed while streaming. The CP spools the upload to a
  file, hashes it, signs and relays the file (cap 130 MiB, 413 above it); the Agent spools what it
  receives, hashes it, verifies the mark against that hash, then hands the handler the file
  (`memberorigin.RequireUpload`). Because a cross-origin page can send a multipart POST without a
  preflight, the JSON content-type defence of decision 6 does not apply; the CP instead requires the
  custom header `X-AF-Console` (forcing a preflight it never answers), plus the same `Sec-Fetch-Site`
  and `Origin` checks. The mark's crypto and the claims are unchanged.
- **`POST /agents/memory/restore` stays open, and cannot be protected by the mark.** Its legitimate
  agent caller is the assistant's `restore_memory_snapshot` tool, which runs in the separate af-mcp
  process and reaches the Agent over loopback with `AGENT_TOKEN`. The Agent cannot tell that process
  from a session shell, so any route the tool can reach is reachable by a session; a second
  token-only route would only move the hole. The user-approved restore flow is kept over a nominal gate.
- **`claude-import`, `muse-import` and `claude-export` stay open** (owner decision, #2076): the
  `af-memory` CLI calls them from sessions and that is a supported use. Decision 8 therefore still
  holds for those routes: their `member` author is the route they came by.

The table above is updated by this note: `import` is **gated**; `restore` and the three CLI routes are open for the
reasons given here.

Review notes on the above (same day):

- The mark's lifetime for an upload starts when the CP has read the whole body, not when the request
  began, so a slow upload is not signed already half-expired.
- CP spooling is bounded (4 uploads at once, one per membership, 429 beyond) because the spool lives
  on the CP's size-limited temp volume.
- **A limit of the hashed form.** The Agent's spool, and the import handler's own temporary copy, are
  writable by any process of the Agent's uid. The mark decides who may start an import; it does not
  protect the bytes taken in from a same-uid rewrite between verification and reading. That is the
  same limit as writing the store directly (Threat model, "What the mark does not do"), not a new
  capability, and it is not closed in code: an open or unlinked descriptor is reachable through
  `/proc/<pid>/fd` just the same.
