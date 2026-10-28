package main

// connectionRelays are the member's connections, all stored and used inside the Workspace: the
// MCP registry, git providers, agent CLI credentials, ops and chat-bridge credentials. The login
// legs whose state lives only in the Agent process take restLoginFlow and stay in
// registerConnectionRoutes.
var connectionRelays = []agentRelay{
	// MCP registry (docs/log/48 P0) — the implementation is Agent-side
	// (workspace/agent/mcp_servers.go).
	{route: "GET /api/mcp-servers"},
	{route: "POST /api/mcp-servers"},
	{route: "POST /api/mcp-servers/test"},
	{route: "PUT /api/mcp-servers/{id}"},
	{route: "POST /api/mcp-servers/{id}/enabled"},
	{route: "DELETE /api/mcp-servers/{id}"},
	// P4: the member's half of tenant distribution — pull the tenant set now (instead of
	// waiting for the poll), and fill in the values of a user_secret server's headers.
	{route: "POST /api/mcp-servers/tenant-refresh"},
	{route: "PUT /api/mcp-servers/{id}/secrets"},
	{route: "GET /api/mcp-servers/{id}/oauth"},
	{route: "PUT /api/mcp-servers/{id}/oauth/client"},
	{route: "GET /api/connections"},
	{route: "GET /api/connections/git/{host}/repos"},
	{route: "GET /api/connections/git/{host}/branches"},
	{route: "PUT /api/connections/git/{host}/identity"},
	{route: "DELETE /api/connections/claude"},
	{route: "DELETE /api/connections/agy"},
	{route: "GET /api/connections/agy/usage"},
	{route: "DELETE /api/connections/cursor"},
	{route: "DELETE /api/connections/kiro"},
	// kiro on-demand install (docs/log/43 Track B/C) — the ~855MB bundle is not baked on
	// the lean image, so the connection card triggers a background install (POST) and
	// polls its progress (GET). Proxied to the Agent (installs into the user's ~/.local).
	{route: "POST /api/connections/kiro/install"},
	{route: "GET /api/connections/kiro/install"},
	{route: "POST /api/connections/muse/api-key"},
	{route: "DELETE /api/connections/muse"},
	// muse on-demand install (ADR 0095 decision 8) — the proprietary ~299MB binary is not in
	// the image, so the card triggers a background install (POST) and polls it (GET). Proxied
	// to the Agent, which installs into the member's ~/.local.
	{route: "POST /api/connections/muse/install"},
	{route: "GET /api/connections/muse/install"},
	{route: "PUT /api/connections/opencode"},
	{route: "DELETE /api/connections/opencode/{env}"},
	{route: "POST /api/connections/opencode/oauth/cancel"},
	{route: "DELETE /api/connections/opencode/oauth"},
	{route: "PUT /api/connections/opencode/workspace"},
	{route: "POST /api/connections/opencode/serve/restart"},
	// SVN saved basic-auth creds (docs/log/41) — add/correct, and forget, a stored server
	// credential. The CP is an explicit allowlist, so PUT needs its own line.
	{route: "PUT /api/connections/svn"},
	{route: "DELETE /api/connections/svn"},
	// Codex auth — proxied to the Agent (codex owns auth.json; no public callback,
	// device-auth polls OpenAI from inside the container).
	{route: "POST /api/connections/codex/api-key"},
	{route: "DELETE /api/connections/codex"},
	// Ops connections (docs/log/25 Phase 1): the credentials are stored in the
	// Workspace's encrypted secrets and injected into the ops MCP servers at
	// spawn; the CP only proxies here, never holds the secrets.
	// Jira (docs/log/80 P1) — where work items are fetched from. It rides along on GET
	// /api/connections, but save and delete each need their own line here: the CP is an
	// explicit allowlist, not a catch-all.
	{route: "PUT /api/connections/jira"},
	{route: "DELETE /api/connections/jira"},
	{route: "PUT /api/connections/jira/site"},
	// llama.cpp member connection (docs/log/107): a member's own LAN llama-server URL + key,
	// stored in the Agent's encrypted secrets like Jira/Grafana above — the CP only proxies,
	// same as those, and never sees the key. Once saved it BYPASSES the CP entirely for the
	// lcpp kind's own chat turns (engines.go's harnessEngineToken); this route only carries
	// the settings card's save/delete/check.
	{route: "PUT /api/connections/lcpp"},
	{route: "DELETE /api/connections/lcpp"},
	{route: "POST /api/connections/lcpp/check"},
	{route: "PUT /api/connections/pagerduty"},
	{route: "DELETE /api/connections/pagerduty"},
	{route: "PUT /api/connections/grafana"},
	{route: "DELETE /api/connections/grafana"},
	{route: "PUT /api/connections/cloudwatch"},
	{route: "DELETE /api/connections/cloudwatch"},
	{route: "PUT /api/connections/aws"},
	{route: "DELETE /api/connections/aws"},
	// Chat bridge (docs/log/37 P1): the Discord bot token lives in the Workspace's
	// encrypted secrets like the ops credentials above — proxied, never held here.
	{route: "PUT /api/connections/discord"},
	{route: "DELETE /api/connections/discord"},
	{route: "POST /api/connections/discord/inspect"},
	{route: "POST /api/connections/discord/guilds"},
	// Chat bridge Slack (docs/log/37, Slack follow-up): bot + app-level tokens live in the
	// Workspace's encrypted secrets like Discord above — proxied, never held here.
	{route: "PUT /api/connections/slack"},
	{route: "DELETE /api/connections/slack"},
	{route: "POST /api/connections/slack/inspect"},
	{route: "POST /api/connections/slack/channels"},
}
