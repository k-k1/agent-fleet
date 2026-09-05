package chatx

// Contract test against the real binary (opt-in): only with AF_CURSOR_CHAT_LIVE=1 does this
// launch a real cursor-agent and check that cursorChat.send's headless chat route holds
// against the actual CLI. Auth is the environment's own Cursor login (the ambient
// ~/.config/cursor/auth.json). Run it with:
//
//	AF_CURSOR_CHAT_LIVE=1 go test -run TestCursorChatLive -v .
//
// The CLI ships weekly, so this is the line that catches drift in the -p contract: the
// result shape, --resume keeping context, and --mode ask enforcing read-only.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cursorChatLiveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("AF_CURSOR_CHAT_LIVE") != "1" {
		t.Skip("set AF_CURSOR_CHAT_LIVE=1 to run the live cursor chat contract test")
	}
}

func TestCursorChatLive(t *testing.T) {
	cursorChatLiveGate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 1) First turn: a plain-text answer comes back, and the resume handle and usage are
	//    captured.
	c := &ChatConversation{ID: "cursor-live", Agent: "cursor"}
	reply, err := cursorChat{}.Send(ctx, c, "Reply with exactly the single word: PONG")
	if err != nil {
		t.Fatalf("send #1: %v", err)
	}
	if !strings.Contains(reply, "PONG") {
		t.Fatalf("reply #1 missing PONG: %q", reply)
	}
	if c.CursorSessionID == "" {
		t.Fatalf("CursorSessionID not captured after send #1")
	}
	if c.Context == nil || c.Context.Tokens <= 0 {
		t.Fatalf("usage/context not recorded after send #1: %+v", c.Context)
	}
	firstSID := c.CursorSessionID

	// 2) Second turn: --resume continues the same conversation and the context (the word
	//    from the previous turn) survives.
	reply2, err := cursorChat{}.Send(ctx, c, "What exact single word did I ask you to reply with a moment ago? Answer with just that word.")
	if err != nil {
		t.Fatalf("send #2 (resume): %v", err)
	}
	if !strings.Contains(reply2, "PONG") {
		t.Fatalf("resume lost context; reply #2: %q", reply2)
	}
	if c.CursorSessionID != firstSID {
		t.Fatalf("resume changed session id: %q → %q", firstSID, c.CursorSessionID)
	}

	// 3) read-only is enforced: asked to write, --mode ask creates no file and the process
	//    returns a clean answer instead of hanging (the chat contract is: leave the host
	//    untouched).
	probe := filepath.Join(chatWorkdir(), "livetest_probe.txt")
	_ = os.Remove(probe)
	c3 := &ChatConversation{ID: "cursor-live-ro", Agent: "cursor"}
	_, err = cursorChat{}.Send(ctx, c3, "Create a file named livetest_probe.txt containing the word hello in the current directory using your tools, then tell me DONE.")
	if err != nil {
		t.Fatalf("send #3 (read-only): %v", err)
	}
	if _, statErr := os.Stat(probe); statErr == nil {
		_ = os.Remove(probe)
		t.Fatalf("read-only violated: %s was created despite --mode ask", probe)
	}
}
