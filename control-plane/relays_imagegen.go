package main

// imagegenRelays are image generation (ADR 0081): the pane that makes pictures without an LLM in
// the loop.
//
// Relayed verbatim to the Agent, which is where the ComfyUI graph, the engine credential, the
// disk and the ledger already are (decision 1): a browser-side route would need an engine
// credential that does not exist and a second copy of the family dispatch. The gateway is not
// touched — cancel reaches ComfyUI over the pass-through the Agent already uses.
//
// All of them are plain REST. The queue is what makes that possible (decision 2): enqueueing
// answers at once and the browser polls, so nothing here waits out a cold start behind the
// ALB's 60-second idle timeout the way the blocking `POST /imagegen/generate` would. That
// route stays off this list on purpose — it is the MCP tool's door, not the pane's.
var imagegenRelays = []agentRelay{
	{route: "GET /api/imagegen/status"},       // the member-facing catalogue (decision 5)
	{route: "POST /api/imagegen/jobs"},        // enqueue N jobs as one group
	{route: "GET /api/imagegen/jobs"},         // the queue, polled at 2 s while anything runs
	{route: "DELETE /api/imagegen/jobs/{id}"}, // cancel one picture
	{route: "GET /api/imagegen/props"},        // a picture's resolved request (sidecar, else PNG chunk)
	{route: "POST /api/imagegen/groups/{id}"}, // pause / resume / skip / cancel a batch (decision 12)
	{route: "POST /api/imagegen/queue"},       // the same, over every group at once
	// The image studio (ADR 0100): relayed the same way. The body of a studio PUT carries
	// If-Match, which the relay's header clone passes through untouched.
	{route: "GET /api/imagegen/studios"},                // list the studios
	{route: "POST /api/imagegen/studios"},               // create one from the pane's draft
	{route: "GET /api/imagegen/studios/{id}"},           // one studio, polled by its pane
	{route: "PUT /api/imagegen/studios/{id}"},           // merge-patch the draft
	{route: "DELETE /api/imagegen/studios/{id}"},        // delete draft and versions; pictures stay
	{route: "POST /api/imagegen/studios/{id}/bind"},     // attach or detach a session
	{route: "POST /api/imagegen/studios/{id}/press"},    // trial or enqueue, recorded as a version
	{route: "POST /api/imagegen/studios/{id}/rewind"},   // restore the draft of an earlier entry
	{route: "GET /api/imagegen/studios/{id}/draft-log"}, // the edit log, paged
	{route: "GET /api/imagegen/studios/{id}/persona"},   // the first turn for a new session
	{route: "GET /api/imagegen/history"},                // pictures, newest first
	{route: "GET /api/imagegen/knowledge"},              // one knowledge document
	{route: "POST /api/imagegen/knowledge"},             // append to its records
	{route: "PUT /api/imagegen/knowledge"},              // the notes editor, all four sections
}
