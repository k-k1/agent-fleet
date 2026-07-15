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
	t.Setenv("HOME", t.TempDir())
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

// managed セッションへの /turn・/respond は driver 登録（P2）まで正直に 501。
func TestHandleSessionTurnManagedUnavailable(t *testing.T) {
	fakeTmux(t)
	const name = "turn_managed"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverManaged})

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

// 作成 API は managed を受けたら（P2 で解禁するまで）副作用より前に明示拒否。
// 既定（"" / "tui"）は Driver を空で永続化する — 既存メタとバイト同一。
func TestCreateSessionRejectsManagedDriver(t *testing.T) {
	fakeTmux(t)
	req := httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"kind":"opencode","driver":"managed"}`))
	rec := httptest.NewRecorder()
	handleCreateSession(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "driver_unsupported") {
		t.Fatalf("status = %d, body = %s, want 400 driver_unsupported", rec.Code, rec.Body.String())
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
