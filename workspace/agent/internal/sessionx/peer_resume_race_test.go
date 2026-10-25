package sessionx

// #2741: a peer message to a claude session that send_to_peer_session has just resumed. The
// fake pane below plays a CLI that has drawn its composer (sessionInputReady passes) but still
// drops what is typed, optionally while it replays the notification of a background task that
// ended while it was stopped. Those are the two shapes the reports took: 502
// delivery_unconfirmed with the status left at working and the retry refused as a duplicate,
// and delivered:true for a message that never became a turn.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// bootingClaude is the fake pane's state on disk, shared with its tmux script.
type bootingClaude struct {
	name, jsonl, boot, log string
}

// fakeBootingClaudeTmux stands up the fake pane for a claude session named name. While the
// boot file exists, a submitted prompt is dropped, and a replay line (when given) lands in the
// transcript with the spinner up. Once the boot file is gone, a submitted prompt is written as
// the user line claude would write for it.
func fakeBootingClaudeTmux(t *testing.T, name, replay string) bootingClaude {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	proj := filepath.Join(cfg, "projects", "-race")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	b := bootingClaude{
		name:  name,
		jsonl: filepath.Join(proj, session.UUID(dir, name)+".jsonl"),
		boot:  filepath.Join(bin, "booting"),
		log:   filepath.Join(bin, "tmux.log"),
	}
	if err := os.WriteFile(b.jsonl, []byte(`{"type":"assistant","message":{"content":[]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.boot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%7\n' ;;
  send-keys)
    last=""; for a in "$@"; do last="$a"; done
    case " $* " in *" -l "*) printf '%s' "$last" > "$TMUX_TEST_LOG.draft"; exit 0 ;; esac
    [ "$last" = Enter ] || exit 0
    if [ -e "$TMUX_TEST_BOOT" ]; then
      if [ -n "$TMUX_TEST_REPLAY" ] && [ ! -e "$TMUX_TEST_LOG.busy" ]; then
        printf '%s\n' "$TMUX_TEST_REPLAY" >> "$TMUX_TEST_JSONL"
        : > "$TMUX_TEST_LOG.busy"
      fi
    elif [ -e "$TMUX_TEST_LOG.draft" ]; then
      printf '{"type":"user","message":{"role":"user","content":"%s"}}\n' "$(/bin/cat "$TMUX_TEST_LOG.draft")" >> "$TMUX_TEST_JSONL"
    fi
    /bin/rm -f "$TMUX_TEST_LOG.draft" ;;
  capture-pane)
    if [ -e "$TMUX_TEST_LOG.busy" ]; then
      printf '\xe2\x9c\xbb Working\xe2\x80\xa6 (esc to interrupt)\n\n  \xe2\x8f\xb5\xe2\x8f\xb5 bypass permissions on\n'
    else
      printf '\xe2\x9d\xaf \n\n  \xe2\x8f\xb5\xe2\x8f\xb5 bypass permissions on\n'
    fi ;;
  load-buffer) /bin/cat > /dev/null ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", b.log)
	t.Setenv("TMUX_TEST_BOOT", b.boot)
	t.Setenv("TMUX_TEST_JSONL", b.jsonl)
	t.Setenv("TMUX_TEST_REPLAY", replay)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")

	pc, pr, pp := deliveryConfirmWindow, deliveryRetryWindow, deliveryPoll
	deliveryConfirmWindow, deliveryRetryWindow, deliveryPoll = 300*time.Millisecond, 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { deliveryConfirmWindow, deliveryRetryWindow, deliveryPoll = pc, pr, pp })
	return b
}

// booted ends the fake boot: from here a submitted prompt becomes a turn.
func (b bootingClaude) booted(t *testing.T) {
	t.Helper()
	if err := os.Remove(b.boot); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(b.log + ".busy")
}

// turns counts the user lines in the transcript that carry text.
func (b bootingClaude) turns(t *testing.T, text string) int {
	t.Helper()
	raw, _ := os.ReadFile(b.jsonl)
	n := 0
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, `"type":"user"`) && strings.Contains(l, text) {
			n++
		}
	}
	return n
}

const raceFrom, raceMsg = "race_sender", "push the branch now"

func racePeerSend(t *testing.T, to string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"prompt": raceMsg, "peer_from": raceFrom, "peer_intent": "request"})
	rec := postInput(t, to, string(b))
	return rec.Code, rec.Body.String()
}

// Case B of the report: the resume replays a stale task notification, whose user line and
// spinner used to pass for the typed message, and the send answered delivered for a message
// the session never saw.
func TestPeerSendDuringResumeReplayIsNotReportedDelivered(t *testing.T) {
	pendingTestEnv(t)
	replay, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": "<task-notification>\n<task-id>m1</task-id>\n<status>killed</status>\n</task-notification>"}})
	b := fakeBootingClaudeTmux(t, "race_replay", string(replay))
	session.WriteMeta(session.Meta{Name: raceFrom, Dir: t.TempDir(), Kind: session.KindClaude})

	code, body := racePeerSend(t, b.name)
	if code != http.StatusBadGateway || !strings.Contains(body, "delivery_unconfirmed") {
		t.Fatalf("send into the booting pane: %d %s, want 502 delivery_unconfirmed", code, body)
	}
	if n := b.turns(t, raceMsg); n != 0 {
		t.Fatalf("the fake pane dropped the prompt, yet the transcript holds %d turns of it", n)
	}

	b.booted(t)
	if code, body := racePeerSend(t, b.name); code != http.StatusOK {
		t.Fatalf("the retry once the CLI is up: %d %s, want 200 (not peer_duplicate)", code, body)
	}
	if n := b.turns(t, raceMsg); n != 1 {
		t.Fatalf("after the retry the transcript holds %d turns of the message, want 1", n)
	}
}

// Cases A, C and D: the typed prompt vanished, the send answered 502, and the status read
// working for a session that sat idle; an identical retry was refused as peer_duplicate.
func TestPeerSendDroppedDuringBootLeavesNoWorkingAndCanBeRetried(t *testing.T) {
	pendingTestEnv(t)
	b := fakeBootingClaudeTmux(t, "race_drop", "")
	session.WriteMeta(session.Meta{Name: raceFrom, Dir: t.TempDir(), Kind: session.KindClaude})
	m, _ := session.ReadMeta(b.name)

	code, body := racePeerSend(t, b.name)
	if code != http.StatusBadGateway || !strings.Contains(body, "delivery_unconfirmed") {
		t.Fatalf("send into the booting pane: %d %s, want 502 delivery_unconfirmed", code, body)
	}
	if got := status.LiveState(session.UUID(m.Dir, m.Name)); got != "idle" {
		t.Fatalf("status after delivery_unconfirmed = %q, want idle (no turn started)", got)
	}

	b.booted(t)
	if code, body := racePeerSend(t, b.name); code != http.StatusOK {
		t.Fatalf("the identical retry: %d %s, want 200 (not peer_duplicate)", code, body)
	}
	if n := b.turns(t, raceMsg); n != 1 {
		t.Fatalf("after the retry the transcript holds %d turns of the message, want 1", n)
	}
}

// The fix for the race itself: a send to a session the Agent launched moments ago is held in
// the pending spool (blocked_on "starting"), nothing is typed while the CLI boots, and the
// message is typed once the pane has settled, becoming exactly one turn.
func TestPeerSendToJustLaunchedClaudeIsHeldUntilItSettles(t *testing.T) {
	pendingTestEnv(t)
	b := fakeBootingClaudeTmux(t, "race_hold", "")
	session.WriteMeta(session.Meta{Name: raceFrom, Dir: t.TempDir(), Kind: session.KindClaude})
	var settled atomic.Bool
	prevSettled := peerBootSettled
	peerBootSettled = func(string) bool { return settled.Load() }
	t.Cleanup(func() {
		drainPendingLoops()
		peerBootSettled = prevSettled
	})
	noteSessionLaunched(b.name)

	b2, _ := json.Marshal(map[string]string{"prompt": raceMsg, "peer_from": raceFrom, "peer_intent": "request"})
	q, blocked := decodeQueued(t, postInput(t, b.name, string(b2)))
	if q != b.name || blocked != peerBlockedStarting {
		t.Fatalf("queued=%q blocked_on=%q, want %q %q", q, blocked, b.name, peerBlockedStarting)
	}
	settle()
	if got := typedPeerLines(t, b.log); len(got) != 0 {
		t.Fatalf("typed into a booting pane: %v", got)
	}

	b.booted(t)
	settled.Store(true)
	waitFor(t, "the held message to become a turn", func() bool { return b.turns(t, raceMsg) == 1 })
	waitFor(t, "the delivery loop to end", func() bool { return !loopRunning(b.name) })
	if n := b.turns(t, raceMsg); n != 1 {
		t.Fatalf("the held message became %d turns, want 1", n)
	}
}

// A hold that never sees the pane settle ends at peerBootHoldMax.
func TestPeerBootHoldEndsAtItsCap(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	prevNow, prevSettled := peerBootNow, peerBootSettled
	peerBootNow = func() time.Time { return now }
	peerBootSettled = func(string) bool { return false }
	t.Cleanup(func() { peerBootNow, peerBootSettled = prevNow, prevSettled })
	m := session.Meta{Name: "hold_cap", Kind: session.KindClaude}
	noteSessionLaunched(m.Name)
	if got := peerBootBlocker(m); got != peerBlockedStarting {
		t.Fatalf("just launched: %q, want %q", got, peerBlockedStarting)
	}
	if got := peerBootBlocker(session.Meta{Name: "hold_cap", Kind: session.KindCodex}); got != "" {
		t.Fatalf("another kind is not held: %q", got)
	}
	now = now.Add(peerBootHoldMax)
	if got := peerBootBlocker(m); got != "" {
		t.Fatalf("past the cap: %q, want no hold", got)
	}
}
