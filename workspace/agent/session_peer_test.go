package main

// docs/log/58 / ADR 0041 — セッション同士のメッセージの「守るべき不変条件」を固定する。
// ここで落ちるということは、迂回できる穴が開いたということ。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestPeerTargetAllowedExcludesShellAndSSM(t *testing.T) {
	// shell / ssm への送信は任意コマンド実行そのもの（ADR 0041 決定5）。空 Kind も
	// 弾く: normalizeKind は未知/空を claude へ倒すので、そこを通すと穴になる。
	for _, kind := range []string{session.KindShell, session.KindSSM, "", "nonsense"} {
		if peerTargetAllowed(kind) {
			t.Errorf("peerTargetAllowed(%q) = true, want false", kind)
		}
	}
	for _, kind := range []string{
		session.KindClaude, session.KindCodex, session.KindOpencode,
		session.KindCursor, session.KindKiro, session.KindAgy, session.KindCopilot,
	} {
		if !peerTargetAllowed(kind) {
			t.Errorf("peerTargetAllowed(%q) = false, want true", kind)
		}
	}
}

func TestPeerPolicyRejections(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindCodex})
	session.WriteMeta(session.Meta{Name: "peershell", Dir: t.TempDir(), Kind: session.KindShell})
	session.WriteMeta(session.Meta{Name: "peergone", Dir: t.TempDir(), Kind: session.KindClaude, Archived: true})

	if _, err := peerPolicy("peersrc", "peerdst"); err != nil {
		t.Fatalf("claude → codex should be allowed, got %v", err)
	}
	for _, tc := range []struct{ from, to, wantCode string }{
		{"peersrc", "peersrc", "peer_self"},
		{"peersrc", "peershell", "peer_target_forbidden"},
		{"peershell", "peerdst", "peer_from_forbidden"},
		{"peersrc", "peergone", "peer_target_unknown"},
		{"peersrc", "nosuch", "peer_target_unknown"},
		{"nosuch", "peerdst", "peer_from_unknown"},
		{"peersrc", "bad name!", "bad_name"},
	} {
		_, err := peerPolicy(tc.from, tc.to)
		rej, ok := err.(*peerRejection)
		if !ok {
			t.Errorf("peerPolicy(%q,%q) err = %v, want *peerRejection", tc.from, tc.to, err)
			continue
		}
		if rej.Code != tc.wantCode {
			t.Errorf("peerPolicy(%q,%q) code = %q, want %q", tc.from, tc.to, rej.Code, tc.wantCode)
		}
	}
}

func TestPeerEnvelopeNamesTheSenderAndTheReplyPolicy(t *testing.T) {
	// 封筒はサーバが必ず付ける。受け取った側が「誰から来たのか」を本文だけで判断できる
	// 唯一の手掛かりで、workspace-notes の常設ルールがこの目印に紐づく。intent / reply が
	// 同じ行に乗るのは、返信規律が効くのが着信の瞬間だから（docs/log/58 §58.14）。
	got := peerEnvelope("s7abc12", "notice", "none", "  develop を rebase した  ")
	if got != "[agent-fleet:peer from=s7abc12 intent=notice reply=none] develop を rebase した" {
		t.Fatalf("peerEnvelope = %q", got)
	}
	// ミラーは封筒を正規表現で読み戻す（console/.../transcript/model.ts）。名前の直後に
	// 語が増えても壊れない形にしてあるが、from= が先頭であることは契約として守る。
	if !strings.HasPrefix(got, "[agent-fleet:peer from=s7abc12 ") {
		t.Fatalf("封筒の先頭が from= でない: %q", got)
	}
}

func TestPeerResolveIntentDerivesReplyPolicy(t *testing.T) {
	// 返信方針は送信側に選ばせない（notice なのに「返信を要求する」封筒を作れてしまう）。
	for intent, want := range map[string]string{
		"request": "only-if-blocked", "question": "required", "answer": "none", "notice": "none",
	} {
		got, err := peerResolveIntent(intent)
		if err != nil || got != want {
			t.Errorf("peerResolveIntent(%q) = %q, %v; want %q", intent, got, err, want)
		}
	}
	// 空も未知も既定値へ倒さない。どちらへ倒しても必ず誤る（依頼が黙殺されるか、
	// 単なる共有に返信が返ってくるか）。
	for _, bad := range []string{"", "  ", "fyi", "REQUEST"} {
		if _, err := peerResolveIntent(bad); err == nil {
			t.Errorf("peerResolveIntent(%q) がエラーにならない", bad)
		}
	}
}

func TestSessionInputRequiresPeerIntent(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindClaude})

	req := httptest.NewRequest(http.MethodPost, "/sessions/peerdst/input",
		strings.NewReader(`{"prompt":"hi","peer_from":"peersrc"}`))
	req.SetPathValue("name", "peerdst")
	rec := httptest.NewRecorder()
	handleSessionInput(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_peer_intent") {
		t.Fatalf("status = %d, body = %s, want 400 bad_peer_intent", rec.Code, rec.Body.String())
	}
}

func TestSessionInputRejectsPeerIntentWithoutPeerFrom(t *testing.T) {
	// 素の投入に種別だけ載せても封筒は付かない。黙って無視すると、呼び出し元は
	// 「返信規律を伝えた」つもりのまま普通の割り込みを打つことになる。
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindClaude})

	req := httptest.NewRequest(http.MethodPost, "/sessions/peerdst/input",
		strings.NewReader(`{"prompt":"hi","peer_intent":"notice"}`))
	req.SetPathValue("name", "peerdst")
	rec := httptest.NewRecorder()
	handleSessionInput(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "peer_intent_without_from") {
		t.Fatalf("status = %d, body = %s, want 400 peer_intent_without_from", rec.Code, rec.Body.String())
	}
}

func TestPeerLimiterDropsDuplicatesAndThrottles(t *testing.T) {
	l := &peerLimiter{sends: map[string][]time.Time{}, recent: map[string]time.Time{}}
	base := time.Unix(1_800_000_000, 0)

	if err := l.allow("a", "b", "same", base); err != nil {
		t.Fatalf("first send rejected: %v", err)
	}
	// 同一 (宛先, 本文) の連投 = 往復ループの形。
	err := l.allow("a", "b", "same", base.Add(time.Second))
	if rej, ok := err.(*peerRejection); !ok || rej.Code != "peer_duplicate" {
		t.Fatalf("duplicate err = %v, want peer_duplicate", err)
	}
	// 窓を越えれば同じ文面も通る。
	if err := l.allow("a", "b", "same", base.Add(peerDuplicateWindow+time.Second)); err != nil {
		t.Fatalf("after duplicate window: %v", err)
	}

	l2 := &peerLimiter{sends: map[string][]time.Time{}, recent: map[string]time.Time{}}
	for i := 0; i < peerRatePerWindow; i++ {
		if err := l2.allow("a", "b", string(rune('A'+i)), base); err != nil {
			t.Fatalf("send %d rejected: %v", i, err)
		}
	}
	err = l2.allow("a", "b", "one too many", base)
	if rej, ok := err.(*peerRejection); !ok || rej.Code != "peer_rate_limited" {
		t.Fatalf("over-limit err = %v, want peer_rate_limited", err)
	}
	// 窓が流れれば回復する（永久 ban ではない）。
	if err := l2.allow("a", "b", "later", base.Add(peerRateWindow+time.Second)); err != nil {
		t.Fatalf("after rate window: %v", err)
	}
}

// **最重要**: peer メッセージが指示台帳（arm）に載る経路を作らせない。載ると docs/log/51 の
// リコンサイラが「利用者の新指示」と誤認して早期 settle を起こす。AF の投入は TUI 打鍵で、
// 受信側 transcript ではネイティブ経路と違い通常入力と区別が付かない（docs/log/58 §58.12）ので、
// 入口で拒むことが唯一の防御になる。
func TestSessionInputRefusesPeerFromWithReportTo(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindClaude})

	req := httptest.NewRequest(http.MethodPost, "/sessions/peerdst/input",
		strings.NewReader(`{"prompt":"hi","peer_from":"peersrc","report_to":"11111111-1111-1111-1111-111111111111"}`))
	req.SetPathValue("name", "peerdst")
	rec := httptest.NewRecorder()
	handleSessionInput(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s (want 400)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "peer_with_report_to") {
		t.Fatalf("body = %s, want peer_with_report_to", rec.Body.String())
	}
}

func TestSessionInputPeerPolicyIsEnforcedServerSide(t *testing.T) {
	// MCP 層を差し替えても迂回できないことの確認 — 拒否は /input が行う。
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peershell", Dir: t.TempDir(), Kind: session.KindShell})

	req := httptest.NewRequest(http.MethodPost, "/sessions/peershell/input",
		strings.NewReader(`{"prompt":"rm -rf /","peer_from":"peersrc"}`))
	req.SetPathValue("name", "peershell")
	rec := httptest.NewRecorder()
	handleSessionInput(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s (want 403)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "peer_target_forbidden") {
		t.Fatalf("body = %s, want peer_target_forbidden", rec.Body.String())
	}
}

func TestSessionInputRejectsOversizePeerMessage(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	session.WriteMeta(session.Meta{Name: "peersrc", Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "peerdst", Dir: t.TempDir(), Kind: session.KindClaude})

	body, err := json.Marshal(map[string]string{
		"prompt":    strings.Repeat("x", peerMaxMessageBytes+1),
		"peer_from": "peersrc",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/sessions/peerdst/input", strings.NewReader(string(body)))
	req.SetPathValue("name", "peerdst")
	rec := httptest.NewRecorder()
	handleSessionInput(rec, req)

	// 無言で切り詰めない（送ったのに後半が消えている、が最悪の失敗）。
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "message_too_long") {
		t.Fatalf("status = %d, body = %s, want 400 message_too_long", rec.Code, rec.Body.String())
	}
}

func TestPeerValidateMessageAccepts16KiBBoundary(t *testing.T) {
	if err := peerValidateMessage(strings.Repeat("x", peerMaxMessageBytes)); err != nil {
		t.Fatalf("message at %d byte limit rejected: %v", peerMaxMessageBytes, err)
	}
}
