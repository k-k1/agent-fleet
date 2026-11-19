package mcpx

import (
	"encoding/json"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorelay"
)

// defer_queue_item (ADR 0137, addendum 2026-11-19, part B): a session that a queue run started
// hands its message back to the queue, to be run again later. It is offered only to such a session
// (mcpQueueDeferOffered), the Agent relays it with the session's name for the run its meta names
// (memorelay.HandleDefer), and the Control Plane only records it: the run's clean close applies it,
// unless a later turn of the session tree started or the session holds an open or merged PR. The
// reason is shown to the person and never put into a later prompt.

// queueDeferTool is the name the session surface advertises.
const queueDeferTool = "defer_queue_item"

// queueDeferArgs is defer_queue_item's arguments.
type queueDeferArgs struct {
	Reason    string `json:"reason"`
	NotBefore string `json:"not_before"`
	Cancel    bool   `json:"cancel"`
}

// mcpQueueDeferOffered reports whether the session this server serves was started by a queue
// run: only then does it have a message to hand back.
func mcpQueueDeferOffered() bool {
	if !selfReportOnly() {
		return false
	}
	self, err := mcpListOwningSession()
	return err == nil && memorelay.QueueDeliveryOf(self) != ""
}

// mcpStdioQueueDeferTools is the tool when it is offered, nothing otherwise.
func mcpStdioQueueDeferTools() []map[string]any {
	if !mcpQueueDeferOffered() {
		return nil
	}
	return sessionToolsNamed(queueDeferTool)
}

func queueDeferTools() []toolSpec {
	return []toolSpec{{
		name: queueDeferTool,
		session: func() map[string]any {
			return map[string]any{
				"name": queueDeferTool,
				"description": "Agent Fleet: hand the queue message that started this session back to its queue, to be run " +
					"again later by a new session. Call it only when the work cannot be done now and nothing was " +
					"changed for it - e.g. a prerequisite is not merged yet, or a service it needs is down - and " +
					"then end your turn without doing the work. It takes effect only when this run ends: if you " +
					"(or a session you started) take another turn afterwards, or this session holds an open or " +
					"merged pull request, the request is dropped and the message is treated as done. " +
					"reason (at most 1 KiB) is shown to your user, never to the next session. not_before is when " +
					"the message may run again (RFC 3339, 10 minutes to 7 days ahead, default 1 hour). A later " +
					"call replaces the request; cancel=true withdraws it. The third hand-back of one message " +
					"sends it back to your user instead.",
				"inputSchema": map[string]any{
					"type": "object", "additionalProperties": false,
					"properties": map[string]any{
						"reason":     map[string]any{"type": "string", "maxLength": 1024, "description": "Why the work cannot be done now, for your user"},
						"not_before": map[string]any{"type": "string", "description": "RFC 3339 time the message may run again (optional; default 1 hour from now)"},
						"cancel":     map[string]any{"type": "boolean", "description": "true withdraws an earlier request"},
					},
				},
			}
		},
		refuse: func() string {
			if !mcpQueueDeferOffered() {
				return "this session was not started by a queue run"
			}
			return ""
		},
		handle: handler(func(req mcpReq, p toolCallParams, a *queueDeferArgs) []byte {
			// The caller is this server's own session, never an argument: the relay hands back only
			// the run its meta names.
			self, err := mcpOwningSession()
			if err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
			if !a.Cancel && a.Reason == "" {
				return mcpToolErr(req.ID, "reason is required (or cancel=true to withdraw a request)")
			}
			body, err := json.Marshal(memorelay.DeferRequest{From: self, Reason: a.Reason, NotBefore: a.NotBefore, Cancel: a.Cancel})
			if err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
			out, err := agentDoTimeout(http.MethodPost, "/memos/defer", body, memoAgentTimeout)
			if err != nil {
				return mcpToolErr(req.ID, memoRelayErr(err))
			}
			return mcpTextResult(req.ID, out)
		}),
	}}
}
