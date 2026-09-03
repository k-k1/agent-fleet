package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
)

// managed runtime の起動失敗のうち、**待っても直らない**もの（共有 daemon の認証ゲート）は
// 一時的な失敗と別のコード・別のステータスで返す。
//
// これを混ぜていた頃、未ログインは 502 runtime_failed になり、Console は
// 「エージェントを起動できませんでした。しばらく待ってから再試行してください。」と訳した
// ——待っても直らない原因に「待て」と言い、原因（未ログイン）はどこにも出なかった。
// 5xx をやめるのは文言のためだけではない: Console の isTransientErr が 5xx を
// 「再試行してよい失敗」と読むので、5xx のままだと文言を直しても再試行対象に残る。
func TestWriteRuntimeErrSplitsPermanentFromTransient(t *testing.T) {
	call := func(err error) (int, string, string) {
		rec := httptest.NewRecorder()
		writeRuntimeErr(rec, err)
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
			t.Fatalf("body is not the error envelope: %s", rec.Body.String())
		}
		return rec.Code, body.Error.Code, body.Error.Message
	}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"codex 未ログイン", codex.ErrNotLoggedIn},
		{"opencode 未接続", opencode.ErrNotConnected},
		// 途中で包まれても分類は変わらない（driver は %w で包んで返すことがある）。
		{"包まれた codex 未ログイン", fmt.Errorf("codex thread の作成に失敗しました: %w", codex.ErrNotLoggedIn)},
	} {
		status, code, msg := call(tc.err)
		if status != http.StatusConflict || code != errCodeAgentNotConnected {
			t.Errorf("%s: status/code = %d/%q, want %d/%q", tc.name, status, code, http.StatusConflict, errCodeAgentNotConnected)
		}
		// 原因は汎用コードでは表せない。message に残っていること（Console は errDetail で併記する）。
		if msg == "" {
			t.Errorf("%s: message が空 — 原因が画面へ届かない", tc.name)
		}
	}

	// それ以外は従来どおり「一時的」。ここを恒久側へ倒すと、起動待ちが再試行不能に見える。
	status, code, msg := call(fmt.Errorf("opencode serve が時間内に起動しませんでした"))
	if status != http.StatusBadGateway || code != "runtime_failed" {
		t.Errorf("transient: status/code = %d/%q, want %d/%q", status, code, http.StatusBadGateway, "runtime_failed")
	}
	if msg != "opencode serve が時間内に起動しませんでした" {
		t.Errorf("transient: message = %q, want the server's own reason", msg)
	}
}
