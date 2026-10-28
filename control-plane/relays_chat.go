package main

// chatRelays are the assistant chat routes (docs/log/19) that the plain relay serves. The routes
// that wait on a model go through the held stream proxy and stay in registerChatRoutes.
var chatRelays = []agentRelay{
	{route: "GET /api/chat/conversations"},
	{route: "POST /api/chat/conversations"},
	{route: "GET /api/chat/conversations/{id}"},
	{route: "PATCH /api/chat/conversations/{id}"},
	{route: "POST /api/chat/conversations/{id}/title/suggest"},   // preview-only AI title suggestion (chat_title.go, Agent-side)
	{route: "POST /api/chat/conversations/{id}/suggest-replies"}, // LLM reply suggestion v2 (chat_suggest_reply.go, Agent-side)
	{route: "DELETE /api/chat/conversations/{id}"},
	{route: "POST /api/chat/conversations/{id}/lock"}, // deletion lock (docs/log/45)
	{route: "POST /api/chat/conversations/{id}/messages"},
	{route: "POST /api/chat/conversations/{id}/stop"}, // cancel a detached in-flight turn
	{route: "GET /api/chat/conversations/{id}/plan"},  // read the work plan (docs/log/33 stage 5)
	{route: "PUT /api/chat/conversations/{id}/plan"},  // hand-edit the work plan (docs/log/33 stage 5)
	{route: "POST /api/chat/conversations/{id}/paste-image"},
	{route: "GET /api/chat/conversations/{id}/pasted/{file}"},
}

// assistantRelays are the assistant templates (docs/log/19 Q2): configurable chat personas,
// proxied verbatim.
var assistantRelays = []agentRelay{
	{route: "GET /api/assistants"},
	{route: "POST /api/assistants"},
	{route: "GET /api/assistants/{id}"},
	{route: "PUT /api/assistants/{id}"},
	{route: "DELETE /api/assistants/{id}"},
}
