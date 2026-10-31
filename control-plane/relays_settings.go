package main

// settingsRelays are the per-CLI settings and usage, toolchains, instructions and UI preferences
// the Agent answers inside the container.
var settingsRelays = []agentRelay{
	// Claude settings (Remote Control / notifications / RTK) — proxied to the Agent.
	{route: "GET /api/claude/settings"},
	{route: "PUT /api/claude/settings"},
	{route: "GET /api/claude/usage"},
	{route: "GET /api/codex/usage"},
	{route: "GET /api/codex/settings"},
	{route: "PUT /api/codex/settings"},
	// Copilot account credit usage (WsBar chip) — proxied to the Agent.
	{route: "GET /api/copilot/usage"},
	// muse subscription quota (WsBar chip) — proxied to the Agent (ADR 0095 decision 10).
	{route: "GET /api/muse/usage"},
	// Every usage chip's payload in one response (#3127): the WS bar polls this once.
	{route: "GET /api/usage/all"},
	// codex / opencode rtk toggle — proxied to the Agent.
	{route: "GET /api/agents/rtk"},
	{route: "PUT /api/agents/rtk"},
	// rtk token-savings history (WsBar "rtk 効果" chip) — proxied to the Agent.
	{route: "GET /api/agents/rtk/gain"},
	// User instructions (docs/log/60 / ADR 0042) — the source of truth and its
	// destinations are both inside the container, so this is only an Agent relay.
	{route: "GET /api/user-notes"},
	{route: "PUT /api/user-notes"},
	{route: "GET /api/user-notes/preview"},
	// Personal skills catalog (#2792) — read-only, answered inside the container.
	{route: "GET /api/skills/catalog"},
	{route: "GET /api/skills/file"},
	// codex / opencode model catalogs (launch model picker) — proxied to the Agent.
	{route: "GET /api/agents/{kind}/models"},
	// The Agent's kind catalog (#1077) — proxied to the Agent.
	{route: "GET /api/agents/kinds"},
	// Toolchain selection (node / java) — proxied to the Agent.
	{route: "GET /api/env/toolchains"},
	{route: "PUT /api/env/toolchains"},
	// Database card (ADR 0086 P1 decision 9) — proxied to the Agent.
	// GET returns the per-engine state list; POST starts/stops/resets an engine.
	{route: "GET /api/env/databases"},
	{route: "POST /api/env/databases/{engine}/{action}"},
	// One-button Temurin install for the Java row (agent jdk_install_http.go): POST
	// starts the download in the workspace, GET polls its state.
	{route: "POST /api/env/jdk-install"},
	{route: "GET /api/env/jdk-install"},
	{route: "GET /api/env/jdk-latest"},
	{route: "GET /api/env/node-latest"},
	{route: "GET /api/env/toolchain-installs"},
	{route: "DELETE /api/env/jdk/{major}"},
	{route: "DELETE /api/env/node/{major}"},
	{route: "POST /api/env/node-install"},
	{route: "GET /api/env/node-install"},
	{route: "GET /api/env/tool-versions"},
	// Per-user UI preferences (Console display settings) — proxied to the Agent. Every write is
	// signed, because some settings are the member's alone: turning the memory review on (ADR
	// 0117) sends other sessions' conversations to a model provider, and a session holds the same
	// AGENT_TOKEN the relay uses. The Agent asks for the mark only on a write that arms such a
	// setting (workspace/agent/ui_prefs.go); the rest ignore it.
	{route: "GET /api/env/ui-prefs"},
	{route: "PUT /api/env/ui-prefs", sign: signBody},
	// AI assist per-feature resolution (docs/log/103 §103.6) — proxied to the Agent. Owner-only
	// on purpose, no twin in registerSessionShareRoutes: it answers with THIS workspace's own
	// backend/pin state, which a share recipient has no business reading (the same reasoning as
	// /api/sessions/{name}/translate above).
	{route: "GET /api/ai-assist/resolution"},
}
