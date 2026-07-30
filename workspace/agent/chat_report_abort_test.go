package main

// 中断ターン（docs/47）がマーカーに依存せず報告されること。
//
// 実測 sp2qemx (2026-07-30): API エラーでターンが落ちると claude は Stop hook を
// 鳴らさない。従来この中断を見ていたのはペース（ペイン）由来のヒール経路だけで、
// その入口が「マーカーが非 idle であること」に依存していたため、誤ヒールでマーカーが
// 消えた後は二度と評価されず、指示が pending のまま宙に浮いた。ここでは**マーカーを
// 一切書かず**に転写だけを植え、リコンサイラがレベルで拾って1通だけ報告することを固定する。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestSessionReportDetectsAbortWithoutMarker(t *testing.T) {
	home := withTempHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withTestReconciler(t, 20*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", handleChatReport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot55", Dir: t.TempDir(), Kind: session.KindClaude, Title: "中断検知"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	proj := filepath.Join(cfg, "projects", "p1")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	// 指示より後の時刻で、末尾が API エラーの転写。マーカーは書かない（＝誤ヒールで
	// 消えた後の実際の状態）。記帳レコードを後ろに足してあるのは、中断判定が許可リストで
	// 実レコードだけを見ていることも同時に押さえるため。
	at := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	body := `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"content":"go"}}` + "\n" +
		`{"type":"assistant","timestamp":"` + at + `","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: Server error mid-response. The response above may be incomplete."}]}}` + "\n" +
		`{"type":"system","subtype":"turn_duration","timestamp":"` + at + `"}` + "\n" +
		`{"type":"custom-title","customTitle":"[AF] 中断検知"}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	addInstruction(m.Name, conv.ID, turnSourceOperator)

	reports := func() []chatMessage {
		unlock := lockConv(conv.ID)
		defer unlock()
		c, err := loadConv(conv.ID)
		if err != nil {
			return nil
		}
		var out []chatMessage
		for i := range c.Messages {
			if c.Messages[i].Role == "report" {
				out = append(out, c.Messages[i])
			}
		}
		return out
	}

	deadline := time.Now().Add(3 * time.Second)
	for len(reports()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := reports()
	if len(got) != 1 {
		t.Fatalf("report count = %d, want 1 (マーカー不在でも中断は報告される)", len(got))
	}
	// 分類は「再送で直る中断」— 報告文が自動再開を促す側であること。
	if !strings.Contains(got[0].Content, "中断") {
		t.Fatalf("report card = %q, want the aborted-turn wording", got[0].Content)
	}
	awaitReported(t, m.Name)

	// 転写が変わらない限り再報告しない（毎 tick 同じレベルを読むので、行が閉じている
	// ことだけが二重報告を止めている）。
	time.Sleep(150 * time.Millisecond)
	if n := len(reports()); n != 1 {
		t.Fatalf("report count = %d after further ticks, want 1", n)
	}
}
