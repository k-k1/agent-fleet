package main

// memoRelays are the memo image attachments (docs/log/21 image attachments): upload, serve and
// GC proxied verbatim to the workspace Agent (/api stripped -> /memos/*), which stores the bytes
// per-container. These are more specific than POST /api/memos, so the mux routes them here rather
// than to registerMemoRoutes' CP handlers.
var memoRelays = []agentRelay{
	{route: "POST /api/memos/paste-image"},
	{route: "GET /api/memos/images/{file}"},
	{route: "POST /api/memos/images/gc"},
}
