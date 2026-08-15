package opencode

// 失敗したターンの可視化（errors.go）。実測ボディ（opencode 1.18.5、opencode Zen の
// 残高切れ）をそのまま食わせて、driver が 200 のまま失敗を取り落とさないこと・read 層が
// 「parts が空だから」と丸ごと捨てないことを固定する。

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// 実測ボディ（driver が受ける形）。HTTP は 200 で、失敗は info.error にだけ載り
// parts は空 — status だけ見ていた頃はこれが「正常完了」だった。
const zenCreditsBody = `{"info":{"role":"assistant","modelID":"deepseek-v4-pro","providerID":"opencode",` +
	`"tokens":{"input":0,"output":0},` +
	`"error":{"name":"APIError","data":{"statusCode":401,"isRetryable":false,` +
	`"message":"Insufficient balance. Manage your billing here: https://opencode.ai/workspace/wrk_x/billing",` +
	`"metadata":{"url":"https://opencode.ai/zen/v1/chat/completions"}}}},"parts":[]}`

func TestDecodeTurnErrorReadsProviderFailureFrom200Body(t *testing.T) {
	e, ok := decodeTurnError(strings.NewReader(zenCreditsBody))
	if !ok {
		t.Fatal("provider failure in a 200 body must be detected")
	}
	if e.label() != "APIError (HTTP 401)" {
		t.Errorf("label = %q", e.label())
	}
	if !strings.HasPrefix(e.detail(), "Insufficient balance.") {
		t.Errorf("detail = %q", e.detail())
	}
	if !strings.HasPrefix(e.summary(), "[error] APIError (HTTP 401): Insufficient balance.") {
		t.Errorf("summary = %q", e.summary())
	}
	if p := e.part(); p.Kind != "error" || p.Info != e.label() || p.Text != e.detail() {
		t.Errorf("part = %+v", p)
	}
}

// 転写ストアは info オブジェクトそのものを行に持つ（ラップが無い）— 同じ decoder が
// 両方の形を受けること。
func TestDecodeMessageErrorAcceptsStoreRowShape(t *testing.T) {
	row := `{"role":"assistant","modelID":"glm-5.2",` +
		`"error":{"name":"ProviderAuthError","data":{"providerID":"opencode"}}}`
	e, ok := decodeMessageError([]byte(row))
	if !ok {
		t.Fatal("store-row error must be detected")
	}
	// message が無い名前もある（実測）— providerID へ落ちること。
	if e.label() != "ProviderAuthError" || e.detail() != "provider: opencode" {
		t.Errorf("label=%q detail=%q", e.label(), e.detail())
	}
}

// 中断は失敗ではない: Interrupt が既に cancelled を刻んでおり、部分回答も parts に
// 残っている。ここでエラー表示を出すと、ユーザー自身の中断が毎回エラーに見える。
func TestDecodeMessageErrorIgnoresDeliberateAbort(t *testing.T) {
	if _, ok := decodeMessageError([]byte(`{"error":{"name":"MessageAbortedError","data":{}}}`)); ok {
		t.Error("a deliberate abort must not be reported as a failure")
	}
	if _, ok := decodeMessageError([]byte(`{"role":"assistant"}`)); ok {
		t.Error("a clean turn must not be reported as a failure")
	}
	if _, ok := decodeMessageError([]byte(`not json`)); ok {
		t.Error("an undecodable row must not be reported as a failure")
	}
}

// 本丸の回帰: 200＋info.error のターンは failed で終わり、失敗の理由が
// MarkTurnEndErr（＝オペレーター報告とチャットブリッジ）まで届くこと。
func TestTurnWith200ErrorBodyLandsFailedAndReportsReason(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnBody = zenCreditsBody
	h := newTestHandle(t, srv)

	got := make(chan string, 4)
	agents.SetStateNotifier(func(sid, previous, state, excerpt string) {
		if state == agents.StateFailed {
			got <- excerpt
		}
	})
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_fail"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnFailed)
	select {
	case excerpt := <-got:
		if !strings.Contains(excerpt, "Insufficient balance") {
			t.Errorf("excerpt = %q, want the provider's message", excerpt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a failed turn must notify the failure, not a plain completion")
	}
}

func TestRetryableProviderFailureIsRetriedOnce(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnBodies = []string{
		`{"info":{"error":{"name":"APIError","data":{"statusCode":500,"isRetryable":true,"message":"Internal server error"}}},"parts":[]}`,
		`{"info":{"role":"assistant"},"parts":[]}`,
	}
	h := newTestHandle(t, srv)
	origWait := waitBeforeProviderRetry
	waitBeforeProviderRetry = func(time.Duration) {}
	t.Cleanup(func() { waitBeforeProviderRetry = origWait })

	if err := h.Send(agents.TurnInput{Prompt: "retry me", ClientMessageID: "msg_retry"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(m.turns))
	}
}

// read 層の回帰: 失敗したターンは parts が空なので、「表示できる part が無い」判定で
// 丸ごと捨てられ、ミラーにも get_session_output にも何も出なかった。エラーを 1 part
// として持ち上げ、Text（= /output・コピー・チャットブリッジ）にも載ること。
func TestReadSessionKeepsFailedTurnWithErrorPart(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_e"
	insMsg(t, db, "m1", ses, 1000, `{"role":"user","time":{"created":1000}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"やって"}`)
	// 失敗した assistant ターン: part は 1 件も無く、理由は message 行にだけある。
	insMsg(t, db, "m2", ses, 1100, `{"role":"assistant","modelID":"deepseek-v4-pro","time":{"created":1100},`+
		`"error":{"name":"APIError","data":{"statusCode":401,"message":"Insufficient balance."}}}`)

	turns := readSession(db, ses)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2 (the failed assistant turn must not be dropped)", len(turns))
	}
	got := turns[1]
	if got.Role != "assistant" || got.Model != "deepseek-v4-pro" {
		t.Fatalf("turn = %+v", got)
	}
	if len(got.Parts) != 1 || got.Parts[0].Kind != "error" {
		t.Fatalf("parts = %+v, want a single error part", got.Parts)
	}
	if got.Parts[0].Info != "APIError (HTTP 401)" || got.Parts[0].Text != "Insufficient balance." {
		t.Errorf("error part = %+v", got.Parts[0])
	}
	if !strings.Contains(got.Text, "Insufficient balance.") {
		t.Errorf("text = %q, want the failure to reach the flattened form", got.Text)
	}
}

// エラーと部分出力が両方あるケース（ツールまで動いてから落ちた）: 本文は残しつつ
// エラーを末尾に足す。
func TestReadSessionAppendsErrorAfterPartialOutput(t *testing.T) {
	db := newOpencodeTestDB(t)
	ses := "ses_p"
	insMsg(t, db, "m1", ses, 1000, `{"role":"assistant","time":{"created":1000},`+
		`"error":{"name":"APIError","data":{"statusCode":429,"message":"Rate limited."}}}`)
	insPart(t, db, "p1", "m1", ses, 1, `{"type":"text","text":"調べます"}`)

	turns := readSession(db, ses)
	if len(turns) != 1 || len(turns[0].Parts) != 2 {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].Parts[0].Kind != "text" || turns[0].Parts[1].Kind != "error" {
		t.Fatalf("parts = %+v, want text then error", turns[0].Parts)
	}
	if turns[0].Text != "調べます\n\n[error] APIError (HTTP 429): Rate limited." {
		t.Errorf("text = %q", turns[0].Text)
	}
}
