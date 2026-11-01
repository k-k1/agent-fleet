package main

// connectionRelays are the member's connections, all stored and used inside the Workspace: the
// MCP registry, git providers, agent CLI credentials, ops and chat-bridge credentials. The login
// legs whose state lives only in the Agent process take restLoginFlow and stay in
// registerConnectionRoutes.
var connectionRelays = []agentRelay{
	// Hand-registered in routes.go (wrapped). The git rows are around the Bitbucket capability
	// reset; the sign-in steps are restLoginFlow's, and are polled until done, so they audit
	// only the answer that says the credential was stored.
	{route: "PUT /api/connections/git/{host}", audit: auditConnection("git", "save", pathShaped("host", auditHostRe)), wrapped: true},
	{route: "DELETE /api/connections/git/{host}", audit: auditConnection("git", "delete", pathShaped("host", auditHostRe)), wrapped: true},
	{route: "POST /api/connections/agy/complete", audit: auditConnection("agy", "signin", nil), auditIf: storedSignin, wrapped: true},
	{route: "POST /api/connections/claude/complete", audit: auditConnection("claude", "signin", nil), auditIf: storedSignin, wrapped: true},
	{route: "POST /api/connections/codex/device/poll", audit: auditConnection("codex", "signin", nil), auditIf: storedSignin, wrapped: true},
	{route: "POST /api/connections/cursor/poll", audit: auditConnection("cursor", "signin", nil), auditIf: storedSignin, wrapped: true},
	{route: "POST /api/connections/kiro/poll", audit: auditConnection("kiro", "signin", nil), auditIf: storedSignin, wrapped: true},
	{route: "POST /api/connections/muse/poll", audit: auditConnection("muse", "signin", nil), auditIf: storedSignin, wrapped: true},
	{route: "POST /api/connections/opencode/oauth/poll", audit: auditConnection("opencode", "signin", nil), auditIf: storedSignin, wrapped: true},
	// MCP registry (docs/log/48 P0) — the implementation is Agent-side
	// (workspace/agent/mcp_servers.go).
	{route: "GET /api/mcp-servers"},
	{route: "POST /api/mcp-servers", audit: auditMCPServer("create")},
	{route: "POST /api/mcp-servers/test"},
	{route: "PUT /api/mcp-servers/{id}", audit: auditMCPServer("update")},
	{route: "POST /api/mcp-servers/{id}/enabled"},
	{route: "DELETE /api/mcp-servers/{id}", audit: auditMCPServer("delete")},
	// P4: the member's half of tenant distribution — pull the tenant set now (instead of
	// waiting for the poll), and fill in the values of a user_secret server's headers.
	{route: "POST /api/mcp-servers/tenant-refresh"},
	{route: "PUT /api/mcp-servers/{id}/secrets", audit: auditMCPServer("secrets")},
	{route: "GET /api/mcp-servers/{id}/oauth"},
	{route: "PUT /api/mcp-servers/{id}/oauth/client", audit: auditMCPServer("oauth_client")},
	{route: "GET /api/connections"},
	{route: "GET /api/connections/git/{host}/repos"},
	{route: "GET /api/connections/git/{host}/branches"},
	{route: "PUT /api/connections/git/{host}/identity"},
	{route: "DELETE /api/connections/claude", audit: auditConnection("claude", "delete", nil)},
	{route: "DELETE /api/connections/agy", audit: auditConnection("agy", "delete", nil)},
	{route: "GET /api/connections/agy/usage"},
	{route: "DELETE /api/connections/cursor", audit: auditConnection("cursor", "delete", nil)},
	{route: "DELETE /api/connections/kiro", audit: auditConnection("kiro", "delete", nil)},
	// kiro on-demand install (docs/log/43 Track B/C) — the ~855MB bundle is not baked on
	// the lean image, so the connection card triggers a background install (POST) and
	// polls its progress (GET). Proxied to the Agent (installs into the user's ~/.local).
	{route: "POST /api/connections/kiro/install"},
	{route: "GET /api/connections/kiro/install"},
	{route: "POST /api/connections/muse/api-key", audit: auditConnection("muse", "api_key", nil)},
	{route: "DELETE /api/connections/muse", audit: auditConnection("muse", "delete", nil)},
	// muse on-demand install (ADR 0095 decision 8) — the proprietary ~299MB binary is not in
	// the image, so the card triggers a background install (POST) and polls it (GET). Proxied
	// to the Agent, which installs into the member's ~/.local.
	{route: "POST /api/connections/muse/install"},
	{route: "GET /api/connections/muse/install"},
	{route: "PUT /api/connections/opencode", audit: auditConnection("opencode", "save", nil)},
	{route: "DELETE /api/connections/opencode/{env}", audit: auditConnection("opencode", "delete", pathShaped("env", auditEnvNameRe))},
	{route: "POST /api/connections/opencode/oauth/cancel"},
	{route: "DELETE /api/connections/opencode/oauth", audit: auditConnection("opencode", "oauth_delete", nil)},
	{route: "PUT /api/connections/opencode/workspace"},
	{route: "POST /api/connections/opencode/serve/restart"},
	// SVN saved basic-auth creds (docs/log/41) — add/correct, and forget, a stored server
	// credential. The CP is an explicit allowlist, so PUT needs its own line.
	{route: "PUT /api/connections/svn", audit: auditConnection("svn", "save", nil)},
	{route: "DELETE /api/connections/svn", audit: auditConnection("svn", "delete", nil)},
	// Codex auth — proxied to the Agent (codex owns auth.json; no public callback,
	// device-auth polls OpenAI from inside the container).
	{route: "POST /api/connections/codex/api-key", audit: auditConnection("codex", "api_key", nil)},
	{route: "DELETE /api/connections/codex", audit: auditConnection("codex", "delete", nil)},
	// Ops connections (docs/log/25 Phase 1): the credentials are stored in the
	// Workspace's encrypted secrets and injected into the ops MCP servers at
	// spawn; the CP only proxies here, never holds the secrets.
	// Jira (docs/log/80 P1) — where work items are fetched from. It rides along on GET
	// /api/connections, but save and delete each need their own line here: the CP is an
	// explicit allowlist, not a catch-all.
	{route: "PUT /api/connections/jira", audit: auditConnection("jira", "save", nil)},
	{route: "DELETE /api/connections/jira", audit: auditConnection("jira", "delete", nil)},
	{route: "PUT /api/connections/jira/site"},
	// llama.cpp member connection (docs/log/107): a member's own LAN llama-server URL + key,
	// stored in the Agent's encrypted secrets like Jira/Grafana above — the CP only proxies,
	// same as those, and never sees the key. Once saved it BYPASSES the CP entirely for the
	// lcpp kind's own chat turns (engines.go's harnessEngineToken); this route only carries
	// the settings card's save/delete/check.
	{route: "PUT /api/connections/lcpp", audit: auditConnection("lcpp", "save", nil)},
	{route: "DELETE /api/connections/lcpp", audit: auditConnection("lcpp", "delete", nil)},
	{route: "POST /api/connections/lcpp/check"},
	{route: "PUT /api/connections/pagerduty", audit: auditConnection("pagerduty", "save", nil)},
	{route: "DELETE /api/connections/pagerduty", audit: auditConnection("pagerduty", "delete", nil)},
	{route: "PUT /api/connections/grafana", audit: auditConnection("grafana", "save", nil)},
	{route: "DELETE /api/connections/grafana", audit: auditConnection("grafana", "delete", nil)},
	{route: "PUT /api/connections/cloudwatch", audit: auditConnection("cloudwatch", "save", nil)},
	{route: "DELETE /api/connections/cloudwatch", audit: auditConnection("cloudwatch", "delete", nil)},
	{route: "PUT /api/connections/aws", audit: auditConnection("aws", "save", nil)},
	{route: "DELETE /api/connections/aws", audit: auditConnection("aws", "delete", nil)},
	// Chat bridge (docs/log/37 P1): the Discord bot token lives in the Workspace's
	// encrypted secrets like the ops credentials above — proxied, never held here.
	{route: "PUT /api/connections/discord", audit: auditConnection("discord", "save", nil)},
	{route: "DELETE /api/connections/discord", audit: auditConnection("discord", "delete", nil)},
	{route: "POST /api/connections/discord/inspect"},
	{route: "POST /api/connections/discord/guilds"},
	// Chat bridge Slack (docs/log/37, Slack follow-up): bot + app-level tokens live in the
	// Workspace's encrypted secrets like Discord above — proxied, never held here.
	{route: "PUT /api/connections/slack", audit: auditConnection("slack", "save", nil)},
	{route: "DELETE /api/connections/slack", audit: auditConnection("slack", "delete", nil)},
	{route: "POST /api/connections/slack/inspect"},
	{route: "POST /api/connections/slack/channels"},
}
