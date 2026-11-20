package mcpx

import (
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func advertises(tools []map[string]any, name string) bool {
	for _, tool := range tools {
		if tool["name"] == name {
			return true
		}
	}
	return false
}

// defer_queue_item is offered to a session a queue run started, and to no other session or the
// assistant: everything else has no run to hand back.
func TestQueueDeferOfferedOnlyToAQueueRunsSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	session.WriteMeta(session.Meta{Name: "run01", Kind: session.KindClaude, QueueDelivery: "dlv1"})
	session.WriteMeta(session.Meta{Name: "plain01", Kind: session.KindClaude})

	withSessionSurface(t, "run01")
	if !advertises(mcpStdioToolList(), queueDeferTool) {
		t.Fatal("a queue run's session is not offered defer_queue_item")
	}
	if spec, _ := toolNamed(queueDeferTool); spec.refuse() != "" {
		t.Fatalf("refused: %s", spec.refuse())
	}
	mcpSourceSession = "plain01"
	if advertises(mcpStdioToolList(), queueDeferTool) {
		t.Fatal("a session no queue run started is offered defer_queue_item")
	}
	if spec, _ := toolNamed(queueDeferTool); spec.refuse() == "" {
		t.Fatal("a session no queue run started may call defer_queue_item")
	}
	setFlags(true, false, false)
	if advertises(mcpStdioToolList(), queueDeferTool) {
		t.Fatal("the assistant is offered defer_queue_item")
	}
}
