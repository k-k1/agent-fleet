package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// A scripted pane: frames[i] is what the i-th read returns (the last one repeats), and no
// test waits for real time.
func scriptCursorPane(t *testing.T, frames ...string) *int {
	t.Helper()
	reads := 0
	prevCap, prevSleep := cursorCapture, cursorSleep
	cursorCapture = func(string) string {
		f := frames[min(reads, len(frames)-1)]
		reads++
		return f
	}
	cursorSleep = func(d time.Duration) { time.Sleep(d / 50) }
	t.Cleanup(func() { cursorCapture, cursorSleep = prevCap, prevSleep })
	return &reads
}

const cursorSent = "say hello in one word"

// The prompt the TUI hands back with an error row is held, with the error as the reason, and
// is not retyped. The first frames are the second cursor took it before refusing.
func TestConfirmCursorSubmitHoldsARejectedPrompt(t *testing.T) {
	m := session.Meta{Name: "cur_rej", Kind: session.KindCursor}
	reads := scriptCursorPane(t,
		cursorPane(t, "pane-submit-composing.txt"), cursorPane(t, "pane-submit-composing.txt"),
		cursorPane(t, "pane-submit-rejected.txt"))
	held, reason := confirmCursorSubmit(m, cursorSent)
	if !held || !strings.Contains(reason, "Named models unavailable") ||
		!strings.Contains(reason, "Free plans can only use Auto") {
		t.Errorf("held=%v reason=%q, want held with the TUI's error text", held, reason)
	}
	if strings.Contains(reason, "hideIcon") {
		t.Errorf("reason carries the toast's option rows: %q", reason)
	}
	if *reads > 6 {
		t.Errorf("read the pane %d times; a rejection is reported as soon as two polls agree", *reads)
	}
}

// A turn that started (composer back on its placeholder) is delivered, and so is every frame
// the check does not recognise.
func TestConfirmCursorSubmitDeliversWhenNothingWasHandedBack(t *testing.T) {
	m := session.Meta{Name: "cur_ok", Kind: session.KindCursor}
	for name, frame := range map[string]string{
		"working":  cursorPane(t, "pane-submit-working.txt"),
		"answered": cursorPane(t, "pane-submit-answered.txt"),
		"unread":   "",
		"unknown":  "a screen with nothing recognisable\n",
	} {
		scriptCursorPane(t, frame)
		if held, reason := confirmCursorSubmit(m, cursorSent); held || reason != "" {
			t.Errorf("%s: held=%v reason=%q, want delivered", name, held, reason)
		}
	}
	// A draft that is only there for one poll (a send still in flight) is not a rejection.
	scriptCursorPane(t, cursorPane(t, "pane-submit-typed.txt"), cursorPane(t, "pane-submit-working.txt"))
	if held, _ := confirmCursorSubmit(m, cursorSent); held {
		t.Error("a draft seen once, then a started turn, read as held")
	}
}

// A draft that stays the whole window with no error row is held too, without a reason.
func TestConfirmCursorSubmitHoldsAStuckDraft(t *testing.T) {
	m := session.Meta{Name: "cur_stuck", Kind: session.KindCursor}
	scriptCursorPane(t, cursorPane(t, "pane-submit-typed.txt"))
	held, reason := confirmCursorSubmit(m, cursorSent)
	if !held || reason != "" {
		t.Errorf("held=%v reason=%q, want held with no reason", held, reason)
	}
}

// fakeCursorTmux is a tmux whose pane shows an idle composer until text is pasted, then the
// frame given.
func fakeCursorTmux(t *testing.T, afterPaste string) (bin string) {
	t.Helper()
	bin = t.TempDir()
	idle := "  → Add a follow-up\n\n\n  Auto\n  ~/proj\n"
	for f, c := range map[string]string{"idle.txt": idle, "after.txt": afterPaste} {
		if err := os.WriteFile(filepath.Join(bin, f), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%3\n' ;;
  load-buffer) /bin/cat > /dev/null; : > "` + filepath.Join(bin, "pasted") + `" ;;
  capture-pane)
    if [ -e "` + filepath.Join(bin, "pasted") + `" ]; then /bin/cat "` + filepath.Join(bin, "after.txt") + `"
    else /bin/cat "` + filepath.Join(bin, "idle.txt") + `"; fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("AF_TMUX_SOCKET", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	return bin
}

// End to end through /input on a cursor Terminal session: a confirmed send whose prompt the
// TUI hands back answers held with the reason (the peer tool maps that to queued / not
// delivered), and the same send to a TUI that took it answers plain.
func TestInputToCursorTerminalReportsARejectedSubmit(t *testing.T) {
	withTempHome(t)
	prevSleep := cursorSleep
	cursorSleep = func(d time.Duration) { time.Sleep(d / 50) }
	t.Cleanup(func() { cursorSleep = prevSleep })

	send := func(t *testing.T, name, frame string) (rec map[string]any) {
		t.Helper()
		fakeCursorTmux(t, frame)
		session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCursor})
		r := postInput(t, name, `{"prompt":"`+cursorSent+`","confirm":true}`)
		if r.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", r.Code, r.Body.String())
		}
		if err := json.Unmarshal(r.Body.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		return rec
	}

	got := send(t, "cur_http_rej", cursorPane(t, "pane-submit-rejected.txt"))
	if got["held"] != true || !strings.Contains(got["held_reason"].(string), "Named models unavailable") {
		t.Errorf("rejected submit answered %v, want held with the error as held_reason", got)
	}
	got = send(t, "cur_http_ok", cursorPane(t, "pane-submit-answered.txt"))
	if _, held := got["held"]; held {
		t.Errorf("accepted submit answered %v, want no held", got)
	}
	// Without confirm (the Console's own composer) nothing is watched.
	fakeCursorTmux(t, cursorPane(t, "pane-submit-rejected.txt"))
	session.WriteMeta(session.Meta{Name: "cur_http_plain", Dir: t.TempDir(), Kind: session.KindCursor})
	r := postInput(t, "cur_http_plain", `{"prompt":"`+cursorSent+`"}`)
	if r.Code != http.StatusOK || strings.Contains(r.Body.String(), "held") {
		t.Errorf("unconfirmed send = %d %s, want a plain 200", r.Code, r.Body.String())
	}
}

// A prompt cursor handed back stays in the composer. The next confirmed send, whatever it
// says, must not be pasted after it: the two would run as one prompt. The pane is left alone.
func TestInputToCursorTerminalRefusesAStaleDraft(t *testing.T) {
	withTempHome(t)
	bin := fakeCursorTmux(t, "")
	rejected := cursorPane(t, "pane-submit-rejected.txt")
	if err := os.WriteFile(filepath.Join(bin, "idle.txt"), []byte(rejected), 0o600); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "cur_stale", Dir: t.TempDir(), Kind: session.KindCursor})
	r := postInput(t, "cur_stale", `{"prompt":"a different request","confirm":true}`)
	if r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), "composer_not_empty") {
		t.Fatalf("send over a stale draft = %d %s, want 409 composer_not_empty", r.Code, r.Body.String())
	}
	if _, err := os.Stat(filepath.Join(bin, "pasted")); err == nil {
		t.Error("the prompt was pasted over the stale draft")
	}
}
