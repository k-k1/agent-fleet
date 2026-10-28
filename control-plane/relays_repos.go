package main

// repoRelays are repository ops, the source-control view and the file browser (/api stripped ->
// /repos*, /git/identity, /fs/*). File, git and repository changes are audited (docs/log/20 M1).
var repoRelays = []agentRelay{
	{route: "GET /api/repos"},
	{route: "POST /api/repos", audit: auditAs("repo.clone", nil)},
	{route: "POST /api/repos/init"}, // new working copy with no import source (mkdir + git init)
	{route: "DELETE /api/repos/{name}", audit: auditByName("repo.delete")},
	{route: "GET /api/repos/{name}/recreate"}, // deleted worktree back at its path (#1040)
	{route: "POST /api/repos/{name}/recreate"},
	{route: "POST /api/repos/{name}/lock"}, // deletion lock (docs/log/45)
	{route: "GET /api/repos/{name}/status"},
	{route: "GET /api/repos/{name}/branches"},
	{route: "DELETE /api/repos/{name}/branch"}, // ?branch=<name>; cleanup (docs/log/32)
	{route: "POST /api/repos/{name}/checkout", audit: auditByName("git.checkout")},
	{route: "POST /api/repos/{name}/fetch", audit: auditByName("git.fetch")},
	{route: "POST /api/repos/{name}/ff", audit: auditByName("git.ff")},
	{route: "POST /api/repos/{name}/parent-ff", audit: auditByName("git.parent_ff")},
	// Repository import jobs (docs/log/78) — progress and outcome of a clone / svn checkout.
	{route: "GET /api/repo-jobs"},
	// Cancel or dismiss an import job, audited under the job id from the URL.
	{route: "DELETE /api/repo-jobs/{id}", audit: auditRepoJobCancel},
	// Subversion (docs/log/41) — checkout / update / cleanup, proxied to the Agent.
	{route: "POST /api/repos/svn", audit: auditAs("repo.svn.checkout", nil)},
	{route: "POST /api/repos/{name}/svn-update", audit: auditByName("repo.svn.update")},
	{route: "POST /api/repos/{name}/svn-cleanup", audit: auditByName("repo.svn.cleanup")},
	// Re-authentication of an existing SVN working copy (docs/log/41 amendment).
	{route: "GET /api/repos/{name}/svn-auth"},
	// Storing a credential for this working copy's server: the target is the working copy, and
	// the credential in the body is never read.
	{route: "POST /api/repos/{name}/svn-auth", audit: auditByName("repo.svn.auth")},
	// Read-only SVN history and local changes (#1705) — proxied to the Agent.
	{route: "GET /api/repos/{name}/svn-log"},
	{route: "GET /api/repos/{name}/svn-show"},
	{route: "GET /api/repos/{name}/svn-changes"},
	{route: "GET /api/repos/{name}/svn-diff"},
	// Launch prompt templates (repo launch modal) — proxied to the Agent.
	{route: "GET /api/repos/{name}/prompt-templates"},
	// Launch modal skill picker — the mirror's list before the session exists.
	{route: "GET /api/repos/{name}/skills"},
	// Branch naming resolver (ADR 0103 decision 7) — proxied to the Agent.
	{route: "GET /api/repos/{name}/branch-rule"},
	{route: "POST /api/repos/{name}/branch-name"},
	{route: "POST /api/repos/{name}/branch-name/check"},
	{route: "GET /api/repos/{name}/gitflow"},
	{route: "POST /api/repos/{name}/gitflow/init"},
	{route: "GET /api/branch-rules/user"},
	{route: "PUT /api/branch-rules/user"},
	{route: "POST /api/branch-rules/preview"},
	// Project-scope MCP servers (docs/log/56 P0/P1) — proxied to the Agent.
	{route: "GET /api/repos/{name}/mcp"},
	{route: "POST /api/repos/{name}/mcp/plan"},
	{route: "POST /api/repos/{name}/mcp/apply"},
	// Source-control view + light edits (docs/17 P3-5) — proxied to the Agent.
	{route: "GET /api/repos/{name}/changes"},
	{route: "GET /api/repos/{name}/diff"},
	{route: "GET /api/repos/{name}/log"},
	{route: "GET /api/repos/{name}/graph"},
	{route: "GET /api/repos/{name}/submodules"},
	{route: "GET /api/repos/{name}/show"},
	{route: "POST /api/repos/{name}/stage"},
	{route: "POST /api/repos/{name}/unstage"},
	{route: "POST /api/repos/{name}/discard", audit: auditByName("git.discard")},
	{route: "POST /api/repos/{name}/commit", audit: auditByName("git.commit")},
	{route: "GET /api/repos/{name}/identity"},
	{route: "PUT /api/repos/{name}/identity"},
	{route: "GET /api/git/identity"},
	{route: "PUT /api/git/identity"},
	// File browser (docs/17 P3-5 stage 2) — proxied to the Agent.
	{route: "GET /api/fs/tree"},
	{route: "GET /api/fs/search"},
	{route: "GET /api/fs/file"},
	// Resolves path references in mirror text — read-only, stat only
	// (workspace/agent/fs_resolve.go).
	{route: "POST /api/fs/resolve"},
	{route: "GET /api/fs/download"},
	// A folder as one zip (workspace/agent/fs_zip.go, ADR 0111). The Agent builds the archive
	// into a temp file first, so a refusal is a JSON error and a success has a Content-Length;
	// the relay below passes both through. Not audited (ordinary read, ADR 0111 open point).
	{route: "GET /api/fs/download-zip"},
	// A picture's width and height from its header — read-only, a bounded header read per
	// path (workspace/agent/fs_imagesize.go).
	{route: "POST /api/fs/imagesize"},
	// Every picture under a folder as one flat list — read-only, a walk bounded by depth,
	// folders, files and time (workspace/agent/fs_images.go).
	{route: "GET /api/fs/images"},
	{route: "POST /api/fs/upload", audit: auditAs("fs.upload", query("path"))},
	{route: "GET /api/fs/changes"},
	{route: "GET /api/fs/linemarks"},
	{route: "POST /api/fs/mkdir", audit: auditAs("fs.mkdir", query("path"))},
	{route: "POST /api/fs/newfile", audit: auditAs("fs.newfile", query("path"))},
	{route: "POST /api/fs/rename", audit: auditFSRename},
	{route: "DELETE /api/fs/delete", audit: auditAs("fs.delete", query("path"))},
	// Wrapped in routes.go: fsFilePut checks the body first and hands the audited path over.
	{route: "PUT /api/fs/file", audit: auditFSFilePut, wrapped: true},
}
