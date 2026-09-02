package chatx

// 中断ターン（docs/log/47）がマーカーに依存せず報告されること。
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
	// 自動再開（docs/log/47 §4-6）は OFF。この試験が押さえているのは**報告経路そのもの**
	// で、ON のときの「まず Agent が再開させ、打ち切ってから報告する」挙動は
	// TestSessionReportHeldWhileAutoResuming が別に固定する。
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false,"claudeAbortAutoResume":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withTestReconciler(t, 20*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", HandleChatReport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
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

	AddInstruction(m.Name, conv.ID, "operator")

	reports := func() []ChatMessage {
		unlock := LockConv(conv.ID)
		defer unlock()
		c, err := LoadConv(conv.ID)
		if err != nil {
			return nil
		}
		var out []ChatMessage
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

// TestSessionReportHeldWhileAutoResuming: 自動再開（docs/log/47 §4-6）が引き受けている間、
// 中断報告は出ない。ここがトークンの節約そのもの — 出せば「再開させろ」と伝えるだけの
// アシスタントのターンが1つ走り、しかもその再開は Agent が既にやっている。
//
// 抑止が片道切符でないことも同時に固定する: 打ち切り（GaveUp）を書いた瞬間に、同じ
// 転写・同じ指示行のまま報告が配られなければならない。ここが壊れると、再送で直らな
// かった中断が誰にも届かなくなる（v1 の「黙って止まる」に逆戻り）。
func TestSessionReportHeldWhileAutoResuming(t *testing.T) {
	home := withTempHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil { // 自動再開は既定 ON
		t.Fatal(err)
	}
	withTestReconciler(t, 20*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", HandleChatReport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot56", Dir: t.TempDir(), Kind: session.KindClaude, Title: "自動再開中"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	proj := filepath.Join(cfg, "projects", "p1")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	body := `{"type":"user","timestamp":"` + at + `","message":{"content":"go"}}` + "\n" +
		`{"type":"assistant","timestamp":"` + at + `","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: Stream idle timeout - no chunks received"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// 再開の途中（1回送って次を待っている）— この間は報告しない。
	// 移送前は main の abortResumeStates に状態を直接書いて、**本物の** abortResumeHolds に
	// true を返させていた。chatx からは main の var に届かないので、**同じ入力（＝この
	// セッションは自動再開の最中）を継ぎ目に注入する**形へ置き換える。判定そのものは
	// main の abort_resume_test.go が持つ（責務の切れ目はここ）。
	stubAbortResumeHolds(t, m.Name, true)

	AddInstruction(m.Name, conv.ID, "operator")

	reports := func() int {
		unlock := LockConv(conv.ID)
		defer unlock()
		c, err := LoadConv(conv.ID)
		if err != nil {
			return 0
		}
		n := 0
		for i := range c.Messages {
			if c.Messages[i].Role == "report" {
				n++
			}
		}
		return n
	}

	time.Sleep(300 * time.Millisecond) // デバウンス 2 tick を十分に跨ぐ
	if n := reports(); n != 0 {
		t.Fatalf("report count = %d, want 0（自動再開の途中で報告している）", n)
	}

	// 打ち切り → 同じ転写のまま報告が出る。
	// 打ち切り済み（GaveUp=capped）＝もう保持しない、を継ぎ目へ注入する。
	stubAbortResumeHolds(t, m.Name, false)
	deadline := time.Now().Add(3 * time.Second)
	for reports() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := reports(); n != 1 {
		t.Fatalf("report count = %d after giving up, want 1（打ち切っても誰にも届かない）", n)
	}
}
