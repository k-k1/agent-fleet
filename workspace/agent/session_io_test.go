package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Every sender that types a prompt into a codex pane — send_to_session, the scheduler's
// reuse send, /turn — goes through this handler. Keep it on the same bracketed-paste
// primitive deliverInitialPrompt uses, so a regression cannot fix launches while leaving
// the mid-session sends broken (codex eats an Enter bundled into a fast literal stream).
func TestHandleSessionInputUsesBracketedPasteForCodex(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	stdinPath := filepath.Join(bin, "tmux.stdin")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%7\n' ;;
  load-buffer) /bin/cat > "$TMUX_TEST_STDIN" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("TMUX_TEST_STDIN", stdinPath)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))

	const name = "launch_codex"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/input",
		strings.NewReader(`{"prompt":"a long launch prompt"}`))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handleSessionInput(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(commands)
	if !strings.Contains(got, "load-buffer -b af-prompt-"+name+"-") ||
		!strings.Contains(got, "paste-buffer -p -d -b af-prompt-"+name+"-") ||
		!strings.Contains(got, "send-keys -t %7 Enter") {
		t.Fatalf("tmux commands = %q, want load + bracketed paste + Enter", got)
	}
}

func TestRememberCodexTUIModePersistsMirrorToggle(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	const name = "codex_mode_memory"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})

	rememberCodexTUIMode(name, "/plan", nil)
	m, _ := session.ReadMeta(name)
	if m.Mode != "plan" {
		t.Fatalf("/plan mode = %q, want plan", m.Mode)
	}

	rememberCodexTUIMode(name, "", []string{"BTab"})
	m, _ = session.ReadMeta(name)
	if m.Mode != "normal" {
		t.Fatalf("BTab from plan mode = %q, want normal", m.Mode)
	}

	rememberCodexTUIMode(name, "", []string{"BTab"})
	m, _ = session.ReadMeta(name)
	if m.Mode != "plan" {
		t.Fatalf("BTab from normal mode = %q, want plan", m.Mode)
	}

	rememberCodexTUIMode(name, "/model", nil)
	m, _ = session.ReadMeta(name)
	if m.Mode != "plan" {
		t.Fatalf("unrelated slash command changed mode to %q", m.Mode)
	}
}

// Codex and OpenCode need an explicit bracketed-paste boundary before the submit
// Enter. A raw send-keys -l burst leaves their paste detector active and can swallow
// Enter, which is especially visible in create_session's first prompt delivery.
func TestTypeLineAndSubmitUsesBracketedPasteForTUIs(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	stdinPath := filepath.Join(bin, "tmux.stdin")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
if [ "$1" = "load-buffer" ]; then
  /bin/cat > "$TMUX_TEST_STDIN"
fi
`
	tmux := filepath.Join(bin, "tmux")
	if err := os.WriteFile(tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("TMUX_TEST_STDIN", stdinPath)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))

	for _, kind := range []string{session.KindCodex, session.KindOpencode} {
		t.Run(kind, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			name := "paste_" + kind
			session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: kind})
			const prompt = "first line\nsecond line"
			if err := typeLineAndSubmit(name, "%7", prompt); err != nil {
				t.Fatalf("typeLineAndSubmit: %v", err)
			}
			got, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			commands := string(got)
			if !strings.Contains(commands, "load-buffer -b af-prompt-"+name+"-") ||
				!strings.Contains(commands, "paste-buffer -p -d -b af-prompt-"+name+"-") ||
				!strings.Contains(commands, "send-keys -t %7 Enter") {
				t.Fatalf("tmux commands = %q, want load + bracketed paste + Enter", commands)
			}
			pasted, err := os.ReadFile(stdinPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(pasted) != prompt {
				t.Fatalf("pasted text = %q, want %q", pasted, prompt)
			}
		})
	}
}

// A {prompt} sent while an interaction is pending would be typed into that modal,
// which swallows the text and lets the Enter confirm its highlighted row: a wrong
// AUQ answer (docs/dev/92), a SILENT PLAN APPROVAL, or a silent 許可. The gate is a
// whitelist — only idle (new turn) and working (steering) pass — so a state added
// upstream blocks by default instead of quietly joining the hole.
func TestPromptBlockerGatesPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "auq_guard"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, name)

	if got := promptBlocker(name); got != "" {
		t.Fatalf("no status recorded yet — must read as free, got %q", got)
	}
	for _, blocked := range []string{"question", "plan", "permission", "some-new-state"} {
		status.Persist(sid, blocked)
		if got := promptBlocker(name); got != blocked {
			t.Fatalf("status=%s must gate the {prompt} path, got %q", blocked, got)
		}
	}
	// Free states: a new turn (idle) and steering the running one (working) must pass —
	// blocking "working" would break every steer / queued injection.
	for _, free := range []string{"idle", "working"} {
		status.Persist(sid, free)
		if got := promptBlocker(name); got != "" {
			t.Fatalf("status=%s must not gate prompts, got %q", free, got)
		}
	}
	if got := promptBlocker("no_such_session"); got != "" {
		t.Fatalf("unknown session must read as free, got %q", got)
	}
	// AskUserQuestion / ExitPlanMode fire their OWN permission_prompt between the tool's
	// PreToolUse and PostToolUse, so the stored state reads "permission" while the
	// terminal still shows the question menu / the plan dialog. The refusal must name the
	// modal that is really up — telling the operator 「許可カードから」 for a plan sends
	// them to a card the Console isn't even showing (it suppresses the permission there).
	status.Persist(sid, "permission")
	status.WritePendingPlan(sid, "# 作業計画")
	if got := promptBlocker(name); got != "plan" {
		t.Fatalf("permission state with a captured plan = %q, want plan", got)
	}
	status.WritePendingQuestion(sid, []byte(`[{"question":"q"}]`))
	if got := promptBlocker(name); got != "question" {
		t.Fatalf("a captured question outranks the plan, got %q", got)
	}
	status.RemovePendingQuestion(sid)
	status.RemovePendingPlan(sid)
	if got := promptBlocker(name); got != "permission" {
		t.Fatalf("a genuine tool permission must stay permission, got %q", got)
	}
}

// Each blocking state gets its own wire code so the Console can explain it; the
// question code keeps its exact historical spelling (the CP scheduler, the MCP drive
// tools and the err.<code> i18n catalogs all classify on it).
func TestBlockedErrCode(t *testing.T) {
	for state, want := range map[string]string{
		"question":   "question_pending",
		"plan":       "plan_pending",
		"permission": "permission_pending",
		"whatever":   "interaction_pending",
	} {
		if got := blockedErrCode(state); got != want {
			t.Errorf("blockedErrCode(%q) = %q, want %q", state, got, want)
		}
		if blockedErrMessage(state) == "" {
			t.Errorf("blockedErrMessage(%q) is empty", state)
		}
	}
}

// Regression: a {prompt} sent while an ExitPlanMode approval is pending used to be
// TYPED INTO THE DIALOG, where the text is swallowed and the trailing Enter confirms
// the first row — always an approval. Every non-Console sender could reach it
// (SendSelectionModal, memo / scheduler injections, send_to_session, the bridge). The
// handler must refuse with 409 plan_pending and send NOTHING to the pane.
func TestHandleSessionInputRefusesPendingPlan(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%7\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))

	const name = "plan_guard"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	status.Persist(session.UUID(dir, name), "plan")

	rec := postInput(t, name, `{"prompt":"別件のメモです"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "plan_pending") {
		t.Fatalf("status = %d, body = %s, want 409 plan_pending", rec.Code, rec.Body.String())
	}
	if b, err := os.ReadFile(logPath); err == nil && strings.Contains(string(b), "send-keys") {
		t.Fatalf("nothing may be typed into the plan dialog, tmux commands = %q", b)
	}
}

func postInput(t *testing.T, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/input", strings.NewReader(body))
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	handleSessionInput(rec, req)
	return rec
}

// A scheduled fire with 完了報告 OFF carries no report_to, and the badge origin used to be
// recorded INSIDE the report_to branch — so every default-settings schedule (the Console
// checkbox is off by default) and the usage-limit auto-resume (report:false by
// construction) landed in the mirror as if the user had typed it. The origin must be
// remembered from `source` alone; the instruction ledger must still stay empty, since
// there is no conversation to report back to.
func TestScheduledInputWithoutReportToStillBadges(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%3\n' ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))

	const name = "sched_no_report"
	const prompt = "利用上限がリセットされました。続けてください。"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})

	if rec := postInput(t, name, `{"prompt":"`+prompt+`","source":"schedule"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	turns := []transcript.Turn{{Role: "user", Text: prompt}}
	tagInjectedTurns(name, turns)
	if turns[0].Source != turnSourceSchedule {
		t.Errorf("Source = %q, want %q (mirror badge)", turns[0].Source, turnSourceSchedule)
	}
	if rows := readInstrRows(name); len(rows) != 0 {
		t.Errorf("instruction ledger = %d rows, want 0 (report_to が空＝報告先が無い)", len(rows))
	}
}

// Regression for the sx37vu7 引継ぎ bug (assistant chat's send_to_session): a managed
// session's {prompt} must route to its ThreadHandle, not the tmux not_running check —
// a live managed session has no tmux pane, so the old code 409'd on every send. claude
// has no managed driver (ADR 0015) so this exercises the same "no driver registered"
// path as /turn's equivalent test, confirming /input now shares its branch.
func TestHandleSessionInputManagedUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	const name = "input_managed"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude, Driver: session.DriverManaged})

	rec := postInput(t, name, `{"prompt":"x"}`)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "driver_unavailable") {
		t.Fatalf("status = %d, body = %s, want 501 driver_unavailable (not 409 not_running)", rec.Code, rec.Body.String())
	}
}

// opencode's managed driver is registered — /input must reach the ThreadHandle (502
// runtime_failed when no runtime can start) rather than tmux's not_running.
func TestClipOutputTail(t *testing.T) {
	// max<=0 は無効（従来挙動 — クリップしない）。
	if out, clipped := clipOutputTail("abc", 0); clipped || out != "abc" {
		t.Fatalf("max=0: (%q, %v)", out, clipped)
	}
	// 上限以内はそのまま。
	if out, clipped := clipOutputTail("abc", 3); clipped || out != "abc" {
		t.Fatalf("fits: (%q, %v)", out, clipped)
	}
	// 超過は末尾 max バイト＋省略マーカー前置。
	out, clipped := clipOutputTail("0123456789", 4)
	if !clipped || out != sessionOutputClipNote+"6789" {
		t.Fatalf("clip: (%q, %v)", out, clipped)
	}
	// マルチバイト境界: 「あ」(3バイト) の途中で切れる max はルーン境界まで前進する。
	out, clipped = clipOutputTail("あいう", 4) // 末尾4バイト = 「う」+「い」の残り1バイト
	if !clipped || out != sessionOutputClipNote+"う" {
		t.Fatalf("rune boundary: (%q, %v)", out, clipped)
	}
	// 前進の結果すべて落ちても壊れない（マーカーのみ）。
	out, clipped = clipOutputTail("ああ", 2)
	if !clipped || out != sessionOutputClipNote {
		t.Fatalf("all-dropped: (%q, %v)", out, clipped)
	}
}

func TestHandleSessionInputManagedOpencodeNeedsRuntime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")
	const name = "input_managed_oc"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindOpencode, Driver: session.DriverManaged})

	rec := postInput(t, name, `{"prompt":"x"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "runtime_failed") {
		t.Fatalf("status = %d, body = %s, want 502 runtime_failed (not 409 not_running)", rec.Code, rec.Body.String())
	}
}

// 作業を始める（起動モーダル）の最初の指示は when_ready で渡される: ここから同期的に
// 打つのではなく、CLI が composer を描くのを待ってから打つ配達ループ
// （deliverInitialPrompt）へ委ねる。Console のミラーが載っていなくても走るのが要点で、
// tmux がまだ無い瞬間に呼ばれても not_running で落としてはいけない。
func TestHandleSessionInputWhenReadyDefersDelivery(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	stdinPath := filepath.Join(bin, "tmux.stdin")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%9\n' ;;
  capture-pane) printf 'ready\nmedium · ~/repos\n' ;;
  load-buffer) /bin/cat > "$TMUX_TEST_STDIN" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("TMUX_TEST_STDIN", stdinPath)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))

	// 知らないセッション名は待たせず落とす（配達ループは黙って 30 秒待つだけなので）。
	if rec := postInput(t, "no_such_session", `{"prompt":"x","when_ready":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: status = %d, body = %s, want 404", rec.Code, rec.Body.String())
	}

	// 配達の意味論を持つ他の指定とは併用できない（それぞれ自分の配達規約を持つ）。
	const name = "launch_when_ready"
	// codex を使うのは配達確認（claude 専用の転写スナップショット）を回さないため。
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex})
	if rec := postInput(t, name, `{"prompt":"x","when_ready":true,"report_to":"conv1"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("report_to: status = %d, body = %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := postInput(t, name, `{"keys":["Enter"],"when_ready":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("keys: status = %d, body = %s, want 400", rec.Code, rec.Body.String())
	}

	rec := postInput(t, name, `{"prompt":"最初の指示","when_ready":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s, want 202", rec.Code, rec.Body.String())
	}
	// 202 は「受け付けた」であって「打った」ではない — 実際の打鍵は配達ループの中で起きる。
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(logPath)
		if strings.Contains(string(b), "send-keys -t %9 Enter") {
			if got, _ := os.ReadFile(stdinPath); string(got) != "最初の指示" {
				t.Fatalf("pasted text = %q, want the prompt", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("prompt was never delivered; tmux commands = %s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
