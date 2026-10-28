package main

// memoryRelays are agent memory: the 0022 version control, AF-owned memory (ADR 0108), the
// background review (ADR 0117), and the imports and exports between stores and environments.
//
// The member-only writes are signed (ADR 0113): a session holds the same AGENT_TOKEN the relay
// uses, so without the mark it could act as the member. A route that is not signed here never
// carries a mark, so a member route added to the Agent alone stays refused rather than open.
//
// Where an audited write names its subject, the Console repeats it in the query as a hint,
// because the body is never read (docs/log/20 §A.6). Only a hint of the expected shape is copied
// into the ledger; anything else is recorded as an empty target, and the event is still kept.
var memoryRelays = []agentRelay{
	// Agent memory version control (docs/log/39 / ADR 0022 P1-P3) — a relay for work that
	// completes inside the Agent. The last segment differs from {kind}/models, so the
	// patterns do not collide.
	{route: "GET /api/agents/memory/roots"},
	{route: "GET /api/agents/memory/snapshots"},
	// A manual snapshot (docs/log/39). Signed, like the import's apply step: both are 0022
	// history routes only the Console calls (ADR 0113, 2026-10-13 note).
	{route: "POST /api/agents/memory/snapshots", audit: auditAs("memory.snapshot", nil), sign: signBody},
	{route: "GET /api/agents/memory/diff"},
	{route: "GET /api/agents/memory/tree"},
	// Rollback (docs/log/39 stage 4). The source rev in the query is the audit hint; the body's
	// rev/at/scope is what actually governs, and what happened is recorded in the repo's restore
	// commit (AF-Restore-Rev / -Scope).
	{route: "POST /api/agents/memory/restore", audit: auditAs("memory.restore", query("rev"))},
	{route: "PUT /api/agents/memory/settings"},
	// AF-owned agent memory (ADR 0108): the change list and the way back from a change. The
	// tools' own routes (/agents/memory/entries, …/search, …/read, …/forget) stay unrelayed:
	// agents reach them on loopback.
	{route: "GET /api/agents/memory/entries/changes"},
	{route: "GET /api/agents/memory/entries/diff"},
	// Undoing or forgetting one change; only a commit id is copied into the ledger.
	{route: "POST /api/agents/memory/entries/revert", audit: auditAs("memory.entry.revert", queryShaped("commit", auditCommitHint)), sign: signBody},
	{route: "GET /api/agents/memory/entries/list"},
	// Pinning or unpinning (#1703): the project id is the hint, never the free-text memory name.
	{route: "POST /api/agents/memory/entries/pin", audit: auditAs("memory.entry.pin", queryShaped("project", auditProjectHint)), sign: signBody},
	// The background memory review (ADR 0117): the member's "Review now" for one session, and the
	// review's status. "Review now" spends tokens and sends an excerpt to a model provider, so a
	// session's own AGENT_TOKEN must not be able to start it; the session name is the hint.
	{route: "GET /api/agents/memory-review"},
	{route: "POST /api/agents/memory-review/run", audit: auditAs("memory.review.run", queryShaped("session", auditSessionNameHint)), sign: signBody},
	// The member's decisions on the queue the review fills (ADR 0117 decision 6): approving
	// publishes into every kind's memory, and the list returns text nobody has published, so a
	// session's own AGENT_TOKEN must not be able to do either. The decisions are audited with a
	// proposal or run id (the run is empty for "reject all"); the list changes nothing.
	{route: "POST /api/agents/memory-review/proposals/list", sign: signBody},
	{route: "POST /api/agents/memory-review/proposals/approve", audit: auditAs("memory.review.approve", queryShaped("id", auditReviewIDHint)), sign: signBody},
	{route: "POST /api/agents/memory-review/proposals/reject", audit: auditAs("memory.review.reject", queryShaped("id", auditReviewIDHint)), sign: signBody},
	{route: "POST /api/agents/memory-review/proposals/reject-all", audit: auditAs("memory.review.reject_all", queryShaped("run", auditReviewIDHint)), sign: signBody},
	{route: "POST /api/agents/memory-review/runs/revert", audit: auditAs("memory.review.revert_run", queryShaped("run", auditReviewIDHint)), sign: signBody},
	// The member's create/edit (#1997), audited under the project id.
	{route: "POST /api/agents/memory/entries/member-save", audit: auditAs("memory.entry.save", queryShaped("project", auditProjectHint)), sign: signBody},
	{route: "GET /api/agents/memory/entries/member-read"},
	{route: "GET /api/agents/memory/entries/member-search"},
	{route: "GET /api/agents/memory/entries/member-latest"},
	{route: "GET /api/agents/memory/entries/member-forgotten"},
	// The member's purge of one memory's past revisions from the history (#1982); the preview
	// only reads.
	{route: "GET /api/agents/memory/entries/purge-preview"},
	{route: "POST /api/agents/memory/entries/purge", audit: auditAs("memory.entry.purge", queryShaped("project", auditProjectHint)), sign: signBody},
	// Moving the memories of a project whose working copy is gone (#1984), audited under the
	// source project id.
	{route: "GET /api/agents/memory/entries/projects"},
	{route: "GET /api/agents/memory/entries/project-move-preview"},
	{route: "POST /api/agents/memory/entries/project-move", audit: auditAs("memory.project.move", queryShaped("project", auditProjectHint)), sign: signBody},
	// The one-time import of claude's own memory (ADR 0108 decision 6): sources, a read-only
	// preview, and the apply, which writes the store.
	{route: "GET /api/agents/memory/claude-import"},
	{route: "GET /api/agents/memory/claude-import/preview"},
	{route: "POST /api/agents/memory/claude-import", audit: auditAs("memory.claude_import", queryShaped("project", auditProjectHint))},
	// AF memory out of an imported memory bundle (#1983): a read-only preview, and the signed
	// import, audited under the import id.
	{route: "GET /api/agents/memory/af-import/preview"},
	{route: "POST /api/agents/memory/af-import", audit: auditAs("memory.af_import", queryShaped("importId", auditImportHint)), sign: signBody},
	// The one-time import of muse's project memory (#1991), audited as the claude import is.
	{route: "GET /api/agents/memory/muse-import"},
	{route: "GET /api/agents/memory/muse-import/preview"},
	{route: "POST /api/agents/memory/muse-import", audit: auditAs("memory.muse_import", queryShaped("project", auditProjectHint))},
	// The reverse: AF memory written back into claude's own, previewed first (#1914). The
	// write-back changes claude's store.
	{route: "GET /api/agents/memory/claude-export"},
	{route: "GET /api/agents/memory/claude-export/preview"},
	{route: "POST /api/agents/memory/claude-export", audit: auditAs("memory.claude_export", queryShaped("project", auditProjectHint))},
	// What the Agent memory switch does by itself with claude's native memory (#1972): the
	// reconciler's state, the counts the confirmation shows, and the member's wake / retry /
	// give-up. The Agent's own runs never pass through here (it has no channel to this ledger;
	// each memory change is a commit by the member in the 0022 history), so the member's part is
	// what is signed and audited.
	{route: "GET /api/agents/memory/auto-sync"},
	{route: "GET /api/agents/memory/auto-sync/preview"},
	{route: "POST /api/agents/memory/auto-sync", audit: auditAutoSync, sign: signBody},
	// Transfer between environments (P3). export streams the body with its
	// Content-Disposition untouched and import hands the multipart straight to the
	// Agent; rest passes body and headers through.
	//
	// Reads are not audited, with one exception: export is the only path that carries someone's
	// personal memory out of the environment, and without a record of who exported what format
	// when there is nothing to trace afterwards (docs/log/39 stage 4). The target is the format
	// only. The import records its receipt (into refs/imports; live is not touched yet) and its
	// apply as separate rows; the upload is signed by its hash, not buffered.
	{route: "GET /api/agents/memory/export", audit: auditAs("memory.export", query("format"))},
	{route: "POST /api/agents/memory/import", audit: auditAs("memory.import", nil), sign: signHash},
	{route: "POST /api/agents/memory/import/apply", audit: auditAs("memory.import.apply", query("importId")), sign: signBody},
}
