package main

// sessionRelays are the session operations the Agent answers: lifecycle, cleanup, drive I/O,
// transcripts, and the cloud CLI logins af-aws-exec and af-gcloud-exec start from a session.
// Deleting a session (to the trash, ADR 0101) is audited under both of its routes: /stop is its
// old name, and moving the Console to DELETE must not drop it from the log.
var sessionRelays = []agentRelay{
	{route: "GET /api/sessions/{name}/move/preview"},
	// The move's follow-ups (ADR 0115 decisions 1, 11.4 and 11.6) are audited as
	// session.move.<step>; the preview is a read.
	{route: "POST /api/sessions/{name}/move/cancel", audit: auditByName("session.move.cancel")},
	{route: "POST /api/sessions/{name}/move/reopen", audit: auditByName("session.move.reopen")},
	{route: "POST /api/sessions/{name}/move/deliver", audit: auditByName("session.move.deliver")},
	{route: "POST /api/sessions/{name}/stop", audit: auditByName("session.delete")},
	{route: "POST /api/sessions/{name}/halt"},
	{route: "POST /api/sessions/{name}/recreate"},
	{route: "GET /api/sessions/archived"},
	{route: "POST /api/sessions/{name}/archive"},
	{route: "POST /api/sessions/{name}/restore"},
	// Cleanup (docs/log/32): survey, per-session usage, delete-with-reclaim, and the gz
	// safety-net archive (list/restore/purge). Proxied verbatim; also used by the MCP
	// cleanup tools, which reach the Agent directly via the resolved runtime.
	{route: "GET /api/sessions/usage"},
	// Per-feature usage time series (docs/log/46 P3 / ADR0029) — relayed verbatim; the
	// Agent has already aggregated it.
	{route: "GET /api/usage/series"},
	// Past-session search (ADR 0110): the full-text index lives in the workspace, so the
	// Console's search box reaches it through the relay like every other workspace read.
	{route: "GET /api/session-search"},
	{route: "GET /api/session-search/turns"},
	{route: "GET /api/sessions/cleanup"},
	// Fleet session graph (ADR 0096): lanes = sessions, x = time. Proxied verbatim like
	// the other GETs above; the Agent has already assembled the page.
	{route: "GET /api/fleet-graph"},
	{route: "DELETE /api/sessions/{name}", audit: auditByName("session.delete")},
	{route: "POST /api/sessions/{name}/lock"}, // deletion lock (docs/log/45)
	{route: "GET /api/cleanup/archives"},
	{route: "POST /api/cleanup/archives/{id}/restore"},
	{route: "DELETE /api/cleanup/archives/{id}"},
	{route: "DELETE /api/cleanup/archives"}, // ?older_than_days=N (ADR 0101 decision 6)
	{route: "GET /api/cleanup/usage"},
	{route: "DELETE /api/cleanup/cache/{feature}"},
	{route: "GET /api/cleanup/tool-caches"},
	{route: "DELETE /api/cleanup/tool-caches/{name}"},
	{route: "GET /api/cleanup/home"},
	{route: "POST /api/cleanup/home/measure"}, // starts a read-only walk (#3083)
	{route: "GET /api/cleanup/leftovers"},
	{route: "DELETE /api/cleanup/leftovers/{kind}"},
	// Programmatic drive I/O (docs/0006 P3-6 E) — proxied to the Agent. Also used
	// by the MCP tools, which call the Agent directly via the resolved runtime.
	{route: "POST /api/sessions/{name}/input"},
	// Drop a peer message queued behind the user's answer (#1031).
	{route: "DELETE /api/sessions/{name}/pending-peer/{id}"},
	// Semantic turn ops + Interaction reply (docs/log/27 P1.5) — proxied verbatim.
	{route: "POST /api/sessions/{name}/turn"},
	{route: "POST /api/sessions/{name}/respond"},
	// Plan approval response (docs/log/30 / ExitPlanMode) — lets the Console's plan card
	// send a rejection plus feedback in one action. The Agent takes care of Escape,
	// waiting for the composer to come back, and the submission. Posting to /input while
	// the approval dialog is open feeds the text to the modal and turns Enter into an
	// approval, so this is the only route that delivers it safely.
	{route: "POST /api/sessions/{name}/plan-respond"},
	// Where that pending plan lives on disk — read by the plan card's "review in another
	// session" launch, which puts the path (never the 12-36 KB body) in the reviewer's
	// first prompt.
	{route: "GET /api/sessions/{name}/plan-file"},
	// Keep-awake pin (docs/log/75) — shields the session and the Workspace from idle
	// auto-stop for a bounded time.
	{route: "POST /api/sessions/{name}/keep-awake"},
	// Stop-after-turn arm (docs/log/85) — fold this session away once the turn it is
	// running ends. The pin's mirror image, and proxied the same way.
	{route: "POST /api/sessions/{name}/stop-after-turn"},
	// Spend budget (#1054) — set the cap, and read the spend against it (with the
	// session's create_session children beside it). The Agent enforces it; proxied verbatim.
	{route: "POST /api/sessions/{name}/spend-cap"},
	{route: "GET /api/sessions/{name}/spend"},
	// Read and live-update a managed session's ThreadSettings (docs/log/27 P2 §9.4-3) —
	// proxied verbatim.
	{route: "GET /api/sessions/{name}/settings"},
	{route: "POST /api/sessions/{name}/settings"},
	// Exclusive driver switch (docs/log/27 P3 §2: tui ⇄ managed) — proxied verbatim.
	{route: "POST /api/sessions/{name}/driver"},
	{route: "POST /api/sessions/{name}/paste-image"},
	{route: "GET /api/sessions/{name}/pasted/{file}"},
	{route: "GET /api/sessions/{name}/status"},
	{route: "GET /api/sessions/{name}/output"},
	// SSM login status polled by the New Session modal (docs/log/p3-ssm-session.md)
	// — surfaces the device-auth URL and the "ready" transition without attaching yet.
	{route: "GET /api/sessions/{name}/ssm-login"},
	// af-aws-exec's Console login (ADR 0102) and af-gcloud-exec's (ADR 0107 decision 3): the
	// presses, the cancels and the log-outs are audited (auditAWSLogin, gcpLoginAudit), the
	// polls and lists are reads. The presses are wrapped, at the end of this table.
	// Settings' "Recent runs" (ADR 0126 decision 2): af-aws-exec's run rows, a read. The
	// session mirror's rows come with /messages and need no route of their own.
	{route: "GET /api/aws-runs"},
	// "Confirm before use" (ADR 0126 decision 5): the pending requests are a read, the member's
	// answer is audited with the request id and the Console's profile and session hints.
	{route: "GET /api/aws-confirm"},
	{route: "POST /api/aws-confirm/{id}/approve", audit: auditAWSConfirm},
	{route: "POST /api/aws-confirm/{id}/deny", audit: auditAWSConfirm},
	{route: "GET /api/aws-login"},
	{route: "POST /api/aws-login/{id}/cancel", audit: auditAWSLogin},
	// The Settings row's "Log in" (#1028): an attempt without a request, and each row's state.
	{route: "GET /api/aws-login/profiles"},
	// "Log out" of one profile: a plain request, nothing lives on in the Agent's memory.
	{route: "POST /api/aws-login/profiles/{name}/logout", audit: auditAWSLogin},
	{route: "GET /api/gcp-login"},
	{route: "POST /api/gcp-login/{id}/cancel", audit: auditGCPLogin},
	{route: "GET /api/gcp-login/profiles"},
	// "Log out" of one profile: a plain request, as for AWS.
	{route: "POST /api/gcp-login/profiles/{name}/logout", audit: auditGCPLogin},
	// Structured transcript for the Console chat view (case-A).
	{route: "GET /api/sessions/{name}/messages"},
	{route: "GET /api/sessions/{name}/export"},
	{route: "GET /api/sessions/{name}/handoff-proposal"},
	{route: "POST /api/sessions/{name}/handoff-proposal"},
	{route: "DELETE /api/sessions/{name}/handoff-proposal"},
	// Transcript marks (docs/log/69 / ADR 0050) — the owner's Console route. The
	// recipient's route is in registerSessionShareRoutes, where the CP evaluates the ACL
	// and stamps the author.
	{route: "GET /api/sessions/{name}/marks"},
	{route: "POST /api/sessions/{name}/marks"},
	{route: "DELETE /api/sessions/{name}/marks"},
	// Auto session-title suggestion accept/dismiss (session_title.go, Agent-side).
	{route: "POST /api/sessions/{name}/title/accept"},
	{route: "POST /api/sessions/{name}/title/dismiss"},
	{route: "POST /api/sessions/{name}/title/suggest"},
	{route: "POST /api/sessions/{name}/title/set"},
	{route: "POST /api/sessions/{name}/suggest-branch"},  // LLM branch-name suggestion (this session's convo)
	{route: "POST /api/sessions/{name}/suggest-replies"}, // LLM reply suggestion v2 (this session's convo)
	{route: "GET /api/sessions/{name}/skills"},           // mirror skill picker (docs/log/50 / ADR0034)
	{route: "GET /api/sessions/{name}/committed"},        // changed-files bar's "committed" verdict (docs/log/68 P2)
	// Per-answer translation (docs/log/97) — the OWNER's route only, deliberately with no twin
	// in registerSessionShareRoutes: pressing it runs a model in the owner's Workspace and
	// spends the owner's tokens, so a recipient reading a shared session must not be able to
	// fire it. The shared view therefore shows the answer as it was written, and renders no
	// translate button at all (the absent-capability rule in transcript/capabilities.ts).
	{route: "POST /api/sessions/{name}/translate"},
	{route: "GET /api/sessions/{name}/translations"},
	{route: "POST /api/sessions/{name}/rename-branch"}, // worktree deferred-naming: git branch -m
	// Wrapped in routes.go: create, fork and move go through the CP's quota check, and the
	// login presses through restLoginFlow because the attempt lives only in the Agent's memory.
	{route: "POST /api/sessions", audit: auditAs("session.create", nil), wrapped: true},
	{route: "POST /api/sessions/{name}/fork", audit: auditByName("session.fork"), wrapped: true},
	{route: "POST /api/sessions/{name}/move", audit: auditByName("session.move"), wrapped: true},
	{route: "POST /api/sessions/{name}/move/retry", audit: auditByName("session.move.retry"), wrapped: true},
	{route: "POST /api/aws-login/{id}/start", audit: auditAWSLogin, wrapped: true},
	{route: "POST /api/aws-login/profiles/{name}/start", audit: auditAWSLogin, wrapped: true},
	{route: "POST /api/gcp-login/{id}/start", audit: auditGCPLogin, wrapped: true},
	{route: "POST /api/gcp-login/profiles/{name}/start", audit: auditGCPLogin, wrapped: true},
	{route: "POST /api/gcp-login/profiles/{name}/attempts/{attempt}/code", audit: auditGCPLogin, wrapped: true},
}
