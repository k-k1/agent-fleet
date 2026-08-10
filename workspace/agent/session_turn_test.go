package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// fakeTmux installs a tmux stub that logs every invocation and answers the
// primitives the /turn tui route touches (has-session / list-panes / load-buffer /
// capture-pane). Returns the log path.
func fakeTmux(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%7\n' ;;
  capture-pane) printf '\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	// HOME alone leaves CLAUDE_CONFIG_DIR / CODEX_HOME / COPILOT_HOME pointing at the
	// real trees, and a session launch materializes the MCP registry into them
	// (session_tmux.go / startManagedSession). See isolateAgentConfigDirs.
	isolateAgentConfigDirs(t)
	return logPath
}

func postTurn(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/turn", strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handleSessionTurn(rec, req)
	return rec
}

// start / steer は tui では /input {prompt} と同じ type+submit 経路（codex は
// bracketed paste）に落ちる — Console が意味論エンドポイントへ移っても入力の
// 信頼性が変わらないことの回帰ガード。
func TestHandleSessionTurnStartTypesPrompt(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "turn_codex"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})

	for _, op := range []string{"start", "steer"} {
		t.Run(op, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			rec := postTurn(t, name, `{"op":"`+op+`","prompt":"do the thing"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			got, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "paste-buffer -p -d -b af-prompt-"+name+"-") ||
				!strings.Contains(string(got), "send-keys -t %7 Enter") {
				t.Fatalf("tmux commands = %q, want bracketed paste + Enter", got)
			}
		})
	}
}

// interrupt は tui では Escape に落ちる（チャットの停止ボタンの意味論化）。working は
// 付けない — Stop hook が発火しないので付けると 進行中 に張り付く。
func TestHandleSessionTurnInterruptSendsEscape(t *testing.T) {
	logPath := fakeTmux(t)
	const name = "turn_stop"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindCodex})

	rec := postTurn(t, name, `{"op":"interrupt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "send-keys -t %7 Escape") {
		t.Fatalf("tmux commands = %q, want send-keys Escape", got)
	}
	if st, ok := status.Read(session.UUID(dir, name)); ok && st.State == "working" {
		t.Fatal("interrupt must not mark the session working")
	}
}

// 質問待ち中の start/steer は /input と同じ理由（TUI モーダルの誤答）で 409。
func TestHandleSessionTurnQuestionPendingGate(t *testing.T) {
	fakeTmux(t)
	const name = "turn_auq"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	status.Persist(session.UUID(dir, name), "question")

	rec := postTurn(t, name, `{"op":"start","prompt":"free text"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "question_pending") {
		t.Fatalf("status = %d, body = %s, want 409 question_pending", rec.Code, rec.Body.String())
	}
}

// driver 未登録の kind（claude）の managed セッションへの /turn・/respond は
// 正直に 501。opencode/codex は登録済みなので runtime が
// 使えない環境では 502 runtime_failed に落ちる — 無効化フラグで決定的に再現する。
func TestHandleSessionTurnManagedUnavailable(t *testing.T) {
	fakeTmux(t)
	const name = "turn_managed"
	// claude に managed driver は無い（ADR 0015 — 対象外）→ 501。
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Driver: session.DriverManaged})

	rec := postTurn(t, name, `{"op":"start","prompt":"x"}`)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "driver_unavailable") {
		t.Fatalf("status = %d, body = %s, want 501 driver_unavailable", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/respond",
		strings.NewReader(`{"id":"i1","decision":"answer","answers":[{"options":[0]}]}`))
	req.SetPathValue("name", name)
	rec = httptest.NewRecorder()
	handleSessionRespond(rec, req)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "driver_unavailable") {
		t.Fatalf("respond status = %d, body = %s, want 501 driver_unavailable", rec.Code, rec.Body.String())
	}
}

// opencode の managed は P2 で解禁済み — /turn は ThreadHandle へ向かい、runtime が
// 起こせない環境では 502 runtime_failed で正直に落ちる（501 ではない）。
func TestHandleSessionTurnManagedOpencodeNeedsRuntime(t *testing.T) {
	fakeTmux(t)
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")
	const name = "turn_mng_oc"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverManaged})

	rec := postTurn(t, name, `{"op":"start","prompt":"x"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime_failed") {
		t.Fatalf("status = %d, body = %s, want 502 runtime_failed", rec.Code, rec.Body.String())
	}
}

// tui セッションへの /respond は明示的に unsupported（Console は keys/seq を使う）。
func TestHandleSessionRespondUnsupportedForTUI(t *testing.T) {
	fakeTmux(t)
	const name = "respond_tui"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/respond",
		strings.NewReader(`{"id":"i1","decision":"answer"}`))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handleSessionRespond(rec, req)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "respond_unsupported") {
		t.Fatalf("status = %d, body = %s, want 501 respond_unsupported", rec.Code, rec.Body.String())
	}
}

// op / decision / id のバリデーション。
func TestHandleSessionTurnValidation(t *testing.T) {
	fakeTmux(t)
	const name = "turn_valid"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	if rec := postTurn(t, name, `{"op":"dance"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad op: status = %d, want 400", rec.Code)
	}
	if rec := postTurn(t, name, `{"op":"start","prompt":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt: status = %d, want 400", rec.Code)
	}
	if rec := postTurn(t, "no_such", `{"op":"start","prompt":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: status = %d, want 404", rec.Code)
	}
}

// 作成 API の driver バリデーション（docs/27 P2/P3）: managed は driver 登録済みの
// kind（opencode/codex）だけ受理し、未登録 kind（claude 等）は副作用より前に明示拒否。
// 未知値は bad_driver。受理された managed 作成は runtime 不可の環境では 502 で
// 落ちる（tmux セッションは一切作らない）。
func TestCreateSessionManagedDriverGate(t *testing.T) {
	logPath := fakeTmux(t)
	// allocSessionName は tmux の has-session で名前占有を見る — 既定スタブは常に
	// exit 0（=どの名前も使用中）で無限ループするので、無し（exit 1）に差し替える。
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 1 ;;
  list-panes) printf '1 %%7\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(filepath.Dir(logPath), "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")

	// claude は managed 対象外（ADR 0015）→ 400 driver_unsupported。
	req := httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"kind":"claude","driver":"managed"}`))
	rec := httptest.NewRecorder()
	handleCreateSession(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "driver_unsupported") {
		t.Fatalf("claude managed: status = %d, body = %s, want 400 driver_unsupported", rec.Code, rec.Body.String())
	}

	// opencode は解禁済み — バリデーションは通り、runtime が無効なので 502 になる。
	req = httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"kind":"opencode","driver":"managed"}`))
	rec = httptest.NewRecorder()
	handleCreateSession(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime_failed") {
		t.Fatalf("opencode managed: status = %d, body = %s, want 502 runtime_failed", rec.Code, rec.Body.String())
	}
	// tmux セッションを作っていない（managed は pane を持たない）。
	if got, _ := os.ReadFile(logPath); strings.Contains(string(got), "new-session") {
		t.Fatalf("managed create must not spawn tmux, log = %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"kind":"opencode","driver":"warp"}`))
	rec = httptest.NewRecorder()
	handleCreateSession(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_driver") {
		t.Fatalf("status = %d, body = %s, want 400 bad_driver", rec.Code, rec.Body.String())
	}
}

// wireSession はワイヤへ driver を素通しし（"" は omitempty で落ちる）、Console が
// paneless（managed）描画を session リストだけで判定できるようにする。
func TestWireSessionCarriesDriver(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	m := session.Meta{Name: "s1", Dir: "/tmp/x", Kind: session.KindOpencode, Driver: session.DriverManaged}
	if got := wireSession(m, false).Driver; got != session.DriverManaged {
		t.Fatalf("wire driver = %q, want managed", got)
	}
	m.Driver = ""
	if got := wireSession(m, false).Driver; got != "" {
		t.Fatalf("wire driver = %q, want empty (tui default)", got)
	}
	if m.DriverKind() != session.DriverTUI {
		t.Fatalf("DriverKind() = %q, want tui", m.DriverKind())
	}
}
