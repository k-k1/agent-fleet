package chatx

// 実バイナリ契約テスト（opt-in）: AF_CURSOR_CHAT_LIVE=1 のときだけ実 cursor-agent を
// 起動し、cursorChat.send のヘッドレスチャット経路が実 CLI で成立することを検証する。
// 認証は環境の Cursor ログイン（~/.config/cursor/auth.json のアンビエント認証）前提。
// 実行例: AF_CURSOR_CHAT_LIVE=1 go test -run TestCursorChatLive -v .
//
// 週次リリースの CLI なので、これが -p 契約（result 形状・--resume 文脈保持・
// --mode ask の read-only 強制）のドリフト検知線になる。

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
		t.Skip("AF_CURSOR_CHAT_LIVE=1 で実 cursor チャット契約テストを有効化")
	}
}

func TestCursorChatLive(t *testing.T) {
	cursorChatLiveGate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 1) 第1ターン: 純テキスト回答が返り、resume ハンドルと usage が捕捉される。
	c := &chatConversation{ID: "cursor-live", Agent: "cursor"}
	reply, err := cursorChat{}.send(ctx, c, "Reply with exactly the single word: PONG")
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

	// 2) 第2ターン: 同じ会話を --resume で継続し、文脈（前ターンの語）が残る。
	reply2, err := cursorChat{}.send(ctx, c, "What exact single word did I ask you to reply with a moment ago? Answer with just that word.")
	if err != nil {
		t.Fatalf("send #2 (resume): %v", err)
	}
	if !strings.Contains(reply2, "PONG") {
		t.Fatalf("resume lost context; reply #2: %q", reply2)
	}
	if c.CursorSessionID != firstSID {
		t.Fatalf("resume changed session id: %q → %q", firstSID, c.CursorSessionID)
	}

	// 3) read-only 強制: 書込を頼んでも --mode ask なのでファイルは作られず、
	//    プロセスは hang せずクリーンな応答で返る（チャット契約＝ホスト非改変）。
	probe := filepath.Join(chatWorkdir(), "livetest_probe.txt")
	_ = os.Remove(probe)
	c3 := &chatConversation{ID: "cursor-live-ro", Agent: "cursor"}
	_, err = cursorChat{}.send(ctx, c3, "Create a file named livetest_probe.txt containing the word hello in the current directory using your tools, then tell me DONE.")
	if err != nil {
		t.Fatalf("send #3 (read-only): %v", err)
	}
	if _, statErr := os.Stat(probe); statErr == nil {
		_ = os.Remove(probe)
		t.Fatalf("read-only violated: %s was created despite --mode ask", probe)
	}
}
