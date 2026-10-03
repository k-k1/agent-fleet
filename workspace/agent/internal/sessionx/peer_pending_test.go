package sessionx

// #1031: a peer message to a session waiting on its user's decision is queued and delivered
// after the answer, once the turn the answer unblocked has ended — exactly once, through /input.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// pendingTestEnv isolates HOME and the sessions dir, gives the delivery loop a fast tick and a
// fresh rate limiter, and waits for every loop before HOME is restored.
func pendingTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	withTempHome(t)
	prevPoll, prevIdle, prevRate := pendingPeerPoll, pendingPeerIdlePoll, peerRate
	pendingPeerPoll, pendingPeerIdlePoll = 5*time.Millisecond, 5*time.Millisecond
	peerRate = &peerLimiter{sends: map[string][]time.Time{}, recent: map[string]time.Time{}}
	t.Cleanup(func() {
		drainPendingLoops()
		pendingPeerPoll, pendingPeerIdlePoll, peerRate = prevPoll, prevIdle, prevRate
	})
}

// drainPendingLoops empties every spool and waits for the delivery loops to end. A cleanup that
// restores a stub the loops read calls it first: it runs before pendingTestEnv's (LIFO).
func drainPendingLoops() {
	for _, n := range agents.PendingPeerSessions() {
		agents.DropPendingPeers(n, "test end")
	}
	pendingLoops.Wait()
}

// fakeClaudeTmux is fakeTmux whose pane shows claude's idle footer until a line is submitted
// and its spinner after, so the TUI path's delivery confirmation sees the turn start.
func fakeClaudeTmux(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
case "$1" in
  has-session) exit 0 ;;
  list-panes) printf '1 %%7\n' ;;
  send-keys)
    for a in "$@"; do if [ "$a" = Enter ]; then : > "$TMUX_TEST_LOG.typed"; fi; done ;;
  capture-pane)
    if [ -e "$TMUX_TEST_LOG.typed" ]; then
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
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	isolateAgentConfigDirs(t)
	return logPath
}

// typedPeerLines returns the peer envelopes typed into the fake pane.
func typedPeerLines(t *testing.T, logPath string) []string {
	t.Helper()
	b, _ := os.ReadFile(logPath)
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "send-keys") && strings.Contains(l, "[agent-fleet:peer ") {
			out = append(out, l)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settle lets the delivery loop run several ticks, for asserting something did NOT happen.
func settle() { time.Sleep(60 * time.Millisecond) }

func peerBody(from, msg string) string {
	b, _ := json.Marshal(map[string]string{"prompt": msg, "peer_from": from, "peer_intent": "request"})
	return string(b)
}

func decodeQueued(t *testing.T, rec *httptest.ResponseRecorder) (queued, blockedOn string) {
	t.Helper()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s, want 202 queued", rec.Code, rec.Body.String())
	}
	var r struct {
		Queued    string `json:"queued"`
		BlockedOn string `json:"blocked_on"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r.Queued, r.BlockedOn
}

func statusPending(t *testing.T, name string) float64 {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/status", nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	HandleSessionStatus(rec, req)
	var r map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	n, _ := r["pendingPeerMessages"].(float64)
	return n
}

// claude (Terminal): the blocker is the real one, read from the status store the hooks write.
func TestPeerToClaudeWaitingOnUserIsDeliveredAfterTheAnswer(t *testing.T) {
	for _, st := range []string{"question", "plan", "permission"} {
		t.Run(st, func(t *testing.T) {
			pendingTestEnv(t)
			logPath := fakeClaudeTmux(t)
			const name, from = "pp_claude", "pp_sender"
			m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
			session.WriteMeta(m)
			session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
			sid := session.UUID(m.Dir, name)
			status.Persist(sid, st)

			q, blocked := decodeQueued(t, postInput(t, name, peerBody(from, "PR #7 is ready")))
			if q != name || blocked != st {
				t.Fatalf("queued=%q blocked_on=%q, want %q %q", q, blocked, name, st)
			}
			if n := statusPending(t, name); n != 1 {
				t.Errorf("/status pendingPeerMessages = %v, want 1", n)
			}
			settle()
			if got := typedPeerLines(t, logPath); len(got) != 0 {
				t.Fatalf("typed into the %s dialog: %v", st, got)
			}

			// The user answered: the turn runs on. Nothing may interleave with it.
			status.Persist(sid, "working")
			settle()
			if got := typedPeerLines(t, logPath); len(got) != 0 {
				t.Fatalf("delivered into the turn the answer unblocked: %v", got)
			}

			status.Persist(sid, "idle")
			waitFor(t, "delivery", func() bool { return len(typedPeerLines(t, logPath)) > 0 })
			settle()
			got := typedPeerLines(t, logPath)
			if len(got) != 1 {
				t.Fatalf("delivered %d times, want exactly once: %v", len(got), got)
			}
			if !strings.Contains(got[0], "[agent-fleet:peer from="+from+" intent=request reply=only-if-blocked queued=") ||
				!strings.Contains(got[0], "PR #7 is ready") {
				t.Errorf("delivered line = %q, want the peer envelope with queued=", got[0])
			}
			if n := len(agents.PendingPeers(name)); n != 0 {
				t.Errorf("spool after delivery = %d, want 0", n)
			}
			if n := statusPending(t, name); n != 0 {
				t.Errorf("/status pendingPeerMessages after delivery = %v, want 0", n)
			}
		})
	}
}

// blockingFakeHandle is a Managed handle that refuses free text while its interaction is up,
// as codex's does, and records what it was sent.
type blockingFakeHandle struct {
	agents.ThreadHandle
	mu    sync.Mutex
	inter *agents.Interaction
	got   []agents.TurnInput
}

func (h *blockingFakeHandle) SendQueued(in agents.TurnInput) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inter != nil {
		return false, agents.ErrQuestionPending
	}
	h.got = append(h.got, in)
	return false, nil
}

func (h *blockingFakeHandle) Send(in agents.TurnInput) error { _, err := h.SendQueued(in); return err }

func (h *blockingFakeHandle) Snapshot() (agents.ThreadSnapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agents.ThreadSnapshot{Interaction: h.inter}, nil
}

func (h *blockingFakeHandle) set(inter *agents.Interaction) {
	h.mu.Lock()
	h.inter = inter
	h.mu.Unlock()
}

func (h *blockingFakeHandle) sent() []agents.TurnInput {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]agents.TurnInput(nil), h.got...)
}

type blockingFakeDriver struct {
	agents.Driver
	h *blockingFakeHandle
}

func (d *blockingFakeDriver) Resume(session.Meta) (agents.ThreadHandle, error) { return d.h, nil }
func (d *blockingFakeDriver) LiveHandle(session.Meta) (agents.ThreadHandle, bool) {
	return d.h, true
}

func installManagedFake(t *testing.T, h *blockingFakeHandle) {
	t.Helper()
	prev, had := managedDrivers[session.KindCodex]
	managedDrivers[session.KindCodex] = &blockingFakeDriver{h: h}
	t.Cleanup(func() {
		drainPendingLoops()
		if had {
			managedDrivers[session.KindCodex] = prev
			return
		}
		delete(managedDrivers, session.KindCodex)
	})
}

// stubTurnEnded replaces the liveness half of peerDeliveryReady (a Managed runtime the test
// cannot start) and keeps the real blocker check.
func stubTurnEnded(t *testing.T, ended *bool, mu *sync.Mutex) {
	t.Helper()
	prev := peerDeliveryReady
	peerDeliveryReady = func(m session.Meta) (bool, bool) {
		if peerQueueBlocker(m) != "" {
			return false, true
		}
		mu.Lock()
		defer mu.Unlock()
		return *ended, true
	}
	t.Cleanup(func() {
		drainPendingLoops()
		peerDeliveryReady = prev
	})
}

// codex (Managed): question and permission come from the handle's pending interaction, the
// blocker promptBlocker does not see; plan is stubbed, codex having no Managed plan dialog.
func TestPeerToManagedCodexWaitingOnUserIsDeliveredAfterTheAnswer(t *testing.T) {
	cases := []struct {
		state string
		inter *agents.Interaction
	}{
		{"question", &agents.Interaction{ID: "q1", Kind: agents.InteractionQuestion}},
		{"permission", &agents.Interaction{ID: "a1", Kind: agents.InteractionApproval}},
		{"plan", nil},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			pendingTestEnv(t)
			h := &blockingFakeHandle{inter: c.inter}
			installManagedFake(t, h)
			var mu sync.Mutex
			ended := false
			stubTurnEnded(t, &ended, &mu)
			planUp := c.state == "plan"
			if planUp {
				prev := peerQueueBlocker
				peerQueueBlocker = func(m session.Meta) string {
					mu.Lock()
					defer mu.Unlock()
					if planUp {
						return "plan"
					}
					return prev(m)
				}
				t.Cleanup(func() {
					drainPendingLoops()
					peerQueueBlocker = prev
				})
			}
			const name, from = "pp_codex", "pp_sender"
			session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
			session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})

			if _, blocked := decodeQueued(t, postInput(t, name, peerBody(from, "rebase onto develop"))); blocked != c.state {
				t.Fatalf("blocked_on = %q, want %q", blocked, c.state)
			}
			settle()
			if got := h.sent(); len(got) != 0 {
				t.Fatalf("sent while %s is pending: %+v", c.state, got)
			}

			h.set(nil) // the user answered; the turn runs on
			mu.Lock()
			planUp = false
			mu.Unlock()
			settle()
			if got := h.sent(); len(got) != 0 {
				t.Fatalf("sent into the turn the answer unblocked: %+v", got)
			}

			mu.Lock()
			ended = true
			mu.Unlock()
			waitFor(t, "delivery", func() bool { return len(h.sent()) > 0 })
			settle()
			got := h.sent()
			if len(got) != 1 {
				t.Fatalf("sent %d times, want exactly once", len(got))
			}
			if got[0].Origin != (agents.Origin{Kind: agents.OriginPeer, From: from}) ||
				!strings.HasPrefix(got[0].Prompt, "[agent-fleet:peer from="+from+" intent=request reply=only-if-blocked queued=") {
				t.Errorf("delivered %+v, want a peer turn with the queued envelope", got[0])
			}
		})
	}
}

// The user answers between the blocker check and the write: the loop started by the enqueue
// re-reads the target and delivers without waiting for another block.
func TestPendingPeerDeliveredWhenAnswerRacesTheEnqueue(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{}
	installManagedFake(t, h)
	var mu sync.Mutex
	ended := true
	stubTurnEnded(t, &ended, &mu)
	prev := peerQueueBlocker
	calls := 0
	peerQueueBlocker = func(m session.Meta) string {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return "question" // seen by the send; gone by the time the loop looks
		}
		return ""
	}
	t.Cleanup(func() {
		drainPendingLoops()
		peerQueueBlocker = prev
	})
	const name, from = "pp_race", "pp_sender"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	decodeQueued(t, postInput(t, name, peerBody(from, "hello")))
	waitFor(t, "delivery", func() bool { return len(h.sent()) == 1 })
}

// Messages leave in the order they came, and one sent while others still wait joins the queue
// rather than overtaking them.
func TestPendingPeersAreFIFOAndLaterSendsDoNotOvertake(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{inter: &agents.Interaction{ID: "q", Kind: agents.InteractionQuestion}}
	installManagedFake(t, h)
	var mu sync.Mutex
	ended := false
	stubTurnEnded(t, &ended, &mu)
	const name, from = "pp_fifo", "pp_sender"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	decodeQueued(t, postInput(t, name, peerBody(from, "first")))
	decodeQueued(t, postInput(t, name, peerBody(from, "second")))
	h.set(nil) // answered, turn still running: a third send must not jump the queue
	if _, blocked := decodeQueued(t, postInput(t, name, peerBody(from, "third"))); blocked != "question" {
		t.Errorf("a send behind waiting messages reports blocked_on=%q, want the head's question", blocked)
	}
	mu.Lock()
	ended = true
	mu.Unlock()
	waitFor(t, "three deliveries", func() bool { return len(h.sent()) == 3 })
	for i, want := range []string{"first", "second", "third"} {
		if p := h.sent()[i].Prompt; !strings.HasSuffix(p, want) {
			t.Errorf("delivery %d = %q, want %q", i, p, want)
		}
	}
}

// An Agent restart between the enqueue and the answer: the file is all that is left, and the
// boot hook delivers it once the user has answered.
func TestPendingPeerSurvivesAgentRestart(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{inter: &agents.Interaction{ID: "q", Kind: agents.InteractionQuestion}}
	installManagedFake(t, h)
	var mu sync.Mutex
	ended := true
	stubTurnEnded(t, &ended, &mu)
	const name, from = "pp_restart", "pp_sender"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	// What the previous process left: the spool file, no loop.
	if err := agents.PutPendingPeer(name, agents.PendingPeer{ID: "af_prev", From: from, Intent: "notice",
		Message: "left over", BlockedOn: "question", QueuedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	ResumePendingPeers()
	settle()
	if len(h.sent()) != 0 {
		t.Fatal("delivered while the question is still up")
	}
	h.set(nil)
	waitFor(t, "delivery after restart", func() bool { return len(h.sent()) == 1 })
	if p := h.sent()[0].Prompt; !strings.Contains(p, "intent=notice reply=none queued=") || !strings.HasSuffix(p, "left over") {
		t.Errorf("delivered %q", p)
	}
}

func TestPendingPeerCapTTLAndRecheck(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{inter: &agents.Interaction{ID: "q", Kind: agents.InteractionQuestion}}
	installManagedFake(t, h)
	var mu sync.Mutex
	ended := true
	stubTurnEnded(t, &ended, &mu)
	const name = "pp_cap"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	senders := []string{"pp_s1", "pp_s2", "pp_s3", "pp_s4", "pp_s5"}
	for _, s := range senders {
		session.WriteMeta(session.Meta{Name: s, Dir: t.TempDir(), Kind: session.KindClaude})
	}
	// 20 queue (the rate limit is per sender: 4 messages each from 5 senders).
	for i := 0; i < pendingPeerCap; i++ {
		decodeQueued(t, postInput(t, name, peerBody(senders[i%5], "msg "+string(rune('a'+i)))))
	}
	rec := postInput(t, name, peerBody(senders[0], "one too many"))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "peer_queue_full") {
		t.Fatalf("21st send: status = %d, body = %s, want 429 peer_queue_full", rec.Code, rec.Body.String())
	}
	agents.DropPendingPeers(name, "test")

	// Expired: dropped, never delivered.
	agents.PutPendingPeer(name, agents.PendingPeer{ID: "af_old", From: senders[0], Intent: "notice",
		Message: "stale", QueuedAt: time.Now().Add(-pendingPeerTTL - time.Minute)})
	// The sender is archived by delivery time: the policy no longer passes, so it is dropped.
	agents.PutPendingPeer(name, agents.PendingPeer{ID: "af_gone", From: senders[1], Intent: "notice",
		Message: "from an archived sender", QueuedAt: time.Now()})
	src, _ := session.ReadMeta(senders[1])
	src.Archived = true
	session.WriteMeta(src)
	h.set(nil)
	kickPendingPeers(name)
	waitFor(t, "spool drained", func() bool { return len(agents.PendingPeers(name)) == 0 })
	settle()
	if got := h.sent(); len(got) != 0 {
		t.Errorf("delivered %+v, want the expired and the refused message dropped", got)
	}
}

// The member drops one before it is delivered; the other still arrives.
func TestPendingPeerDroppedByMember(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{inter: &agents.Interaction{ID: "q", Kind: agents.InteractionQuestion}}
	installManagedFake(t, h)
	var mu sync.Mutex
	ended := true
	stubTurnEnded(t, &ended, &mu)
	const name, from = "pp_drop", "pp_sender"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	decodeQueued(t, postInput(t, name, peerBody(from, "drop me")))
	decodeQueued(t, postInput(t, name, peerBody(from, "keep me")))
	wire := pendingPeersWire(name)
	if len(wire) != 2 || wire[0].From != from || wire[0].BlockedOn != "question" {
		t.Fatalf("wire = %+v", wire)
	}
	req := httptest.NewRequest(http.MethodDelete, "/sessions/"+name+"/pending-peer/"+wire[0].ID, nil)
	req.SetPathValue("name", name)
	req.SetPathValue("id", wire[0].ID)
	rec := httptest.NewRecorder()
	HandleDropPendingPeer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("drop: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	HandleDropPendingPeer(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("second drop: %d, want 404", rec.Code)
	}
	h.set(nil)
	waitFor(t, "delivery", func() bool { return len(h.sent()) == 1 })
	settle()
	if got := h.sent(); len(got) != 1 || !strings.HasSuffix(got[0].Prompt, "keep me") {
		t.Errorf("delivered %+v, want only the kept message", got)
	}
}

// Halt keeps the spool (a later start delivers it); archive and the trash drop it.
func TestPendingPeerKeptByHaltDroppedByArchiveAndTrash(t *testing.T) {
	pendingTestEnv(t)
	fakeTmux(t)
	prev := peerDeliveryReady
	peerDeliveryReady = func(session.Meta) (bool, bool) { return false, false }
	t.Cleanup(func() {
		drainPendingLoops()
		peerDeliveryReady = prev
	})
	const name = "pp_halt"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	put := func() {
		if err := agents.PutPendingPeer(name, agents.PendingPeer{ID: agents.NormalizeMsgID(""), From: "x",
			Intent: "notice", Message: "m", QueuedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	put()
	if _, err := haltSession(m, true); err != nil {
		t.Fatal(err)
	}
	if n := len(agents.PendingPeers(name)); n != 1 {
		t.Fatalf("after halt = %d, want 1", n)
	}
	ArchiveSession(m)
	if n := len(agents.PendingPeers(name)); n != 0 {
		t.Fatalf("after archive = %d, want 0", n)
	}
	put()
	ForgetRuntime(m)
	if n := len(agents.PendingPeers(name)); n != 0 {
		t.Fatalf("after the trash = %d, want 0", n)
	}
}

// Only peer messages queue: the Console's own send, send_to_session (report_to) and a
// schedule keep their 409, and auth_expired / the usage-limit menu keep refusing peers too.
func TestNonPeerSendsAndLongBlockersKeepRefusing(t *testing.T) {
	pendingTestEnv(t)
	fakeTmux(t)
	const name, from = "pp_keep409", "pp_sender"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude}
	session.WriteMeta(m)
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	status.Persist(session.UUID(m.Dir, name), "question")
	for _, body := range []string{
		`{"prompt":"from the composer"}`,
		`{"prompt":"from the operator","report_to":"conv1"}`,
		`{"prompt":"scheduled","source":"schedule"}`,
	} {
		if rec := postInput(t, name, body); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "question_pending") {
			t.Errorf("%s: status = %d, body = %s, want 409 question_pending", body, rec.Code, rec.Body.String())
		}
	}
	prev := peerQueueBlocker
	t.Cleanup(func() {
		drainPendingLoops()
		peerQueueBlocker = prev
	})
	for i, st := range []string{agents.StateAuth, agents.StateBlocked} {
		peerQueueBlocker = func(session.Meta) string { return st }
		rec := postInput(t, name, peerBody(from, "msg "+st+string(rune('0'+i))))
		if rec.Code != http.StatusConflict {
			t.Errorf("peer send while %s: status = %d, body = %s, want 409", st, rec.Code, rec.Body.String())
		}
	}
	if n := len(agents.PendingPeers(name)); n != 0 {
		t.Errorf("non-peer sends were queued: %d", n)
	}
}

// The real peerQueueBlocker reads a Managed handle's interaction: a question is question, an
// approval is permission.
func TestPeerQueueBlockerReadsManagedInteraction(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{}
	installManagedFake(t, h)
	m := session.Meta{Name: "pp_inter", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	for _, c := range []struct {
		inter *agents.Interaction
		want  string
	}{
		{nil, ""},
		{&agents.Interaction{Kind: agents.InteractionQuestion}, "question"},
		{&agents.Interaction{Kind: agents.InteractionApproval}, "permission"},
	} {
		h.set(c.inter)
		if got := peerQueueBlocker(m); got != c.want {
			t.Errorf("interaction %+v: blocker = %q, want %q", c.inter, got, c.want)
		}
	}
}

// The target blocks again between the readiness check and the send: /input refuses (409), and
// the message goes back in its place rather than being lost, then arrives once.
func TestPendingPeerRefusedAtDeliveryIsKept(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{inter: &agents.Interaction{ID: "q", Kind: agents.InteractionQuestion}}
	installManagedFake(t, h)
	prev := peerDeliveryReady
	peerDeliveryReady = func(session.Meta) (bool, bool) { return true, true } // misses the question
	t.Cleanup(func() {
		drainPendingLoops()
		peerDeliveryReady = prev
	})
	const name, from = "pp_reblock", "pp_sender"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	if err := agents.PutPendingPeer(name, agents.PendingPeer{ID: "af_x", From: from, Intent: "notice",
		Message: "still here", QueuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	kickPendingPeers(name)
	settle()
	// Sampled between the claim and the write-back the spool reads empty, so wait for it.
	waitFor(t, "the refused message back in the spool", func() bool { return len(agents.PendingPeers(name)) == 1 })
	if len(h.sent()) != 0 {
		t.Fatalf("sent %+v while the question is up", h.sent())
	}
	h.set(nil)
	waitFor(t, "delivery", func() bool { return len(h.sent()) == 1 })
	settle()
	if len(h.sent()) != 1 {
		t.Fatalf("sent %d times, want once", len(h.sent()))
	}
}

// resumeHookDriver runs hook inside Resume, which is where a delivery sits between its claim
// and its send.
type resumeHookDriver struct {
	*blockingFakeDriver
	hook func()
}

func (d *resumeHookDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	if d.hook != nil {
		d.hook()
	}
	return d.h, nil
}

// Review 1: while the last waiting message is claimed and on its way the spool reads empty; a
// new send must still queue behind it, not overtake it.
func TestPendingPeerInFlightIsNotOvertaken(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{}
	installManagedFake(t, h)
	var mu sync.Mutex
	ended := true
	stubTurnEnded(t, &ended, &mu)
	const name, from = "pp_inflight", "pp_sender"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	p := agents.PendingPeer{ID: "af_first", From: from, Intent: "request", Message: "first", BlockedOn: "question", QueuedAt: time.Now()}
	if err := agents.PutPendingPeer(name, p); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	managedDrivers[session.KindCodex] = &resumeHookDriver{blockingFakeDriver: &blockingFakeDriver{h: h}, hook: func() {
		if calls.Add(1) == 1 { // only the first delivery is held; a send that overtakes passes
			close(entered)
			<-release
		}
	}}
	done := make(chan bool)
	go func() { done <- deliverPendingPeer(name, p) }()
	<-entered
	if _, blocked := decodeQueued(t, postInput(t, name, peerBody(from, "second"))); blocked != "question" {
		t.Errorf("a send behind the message in flight reports blocked_on=%q, want question", blocked)
	}
	close(release)
	if !<-done {
		t.Fatal("the first delivery failed")
	}
	waitFor(t, "both delivered", func() bool { return len(h.sent()) == 2 })
	if got := h.sent(); !strings.HasSuffix(got[0].Prompt, "first") || !strings.HasSuffix(got[1].Prompt, "second") {
		t.Errorf("order = %q, %q; want first, second", got[0].Prompt, got[1].Prompt)
	}
}

// Review 2: an archive that completes while a delivery is in flight is final: the refused
// delivery does not write its message back.
func TestPendingPeerArchiveDuringDeliveryIsFinal(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{inter: &agents.Interaction{ID: "new_q", Kind: agents.InteractionQuestion}}
	installManagedFake(t, h)
	const name, from = "pp_archive_race", "pp_sender"
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	session.WriteMeta(m)
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	p := agents.PendingPeer{ID: "af_old", From: from, Intent: "request", Message: "old", BlockedOn: "question", QueuedAt: time.Now()}
	if err := agents.PutPendingPeer(name, p); err != nil {
		t.Fatal(err)
	}
	managedDrivers[session.KindCodex] = &resumeHookDriver{blockingFakeDriver: &blockingFakeDriver{h: h}, hook: func() {
		dropPendingPeers(name, "archived")
		m.Archived = true
		session.WriteMeta(m)
	}}
	if deliverPendingPeer(name, p) {
		t.Fatal("delivered into a question")
	}
	if n := len(agents.PendingPeers(name)); n != 0 {
		t.Fatalf("the archive completed but the delivery wrote %d message(s) back", n)
	}
}

// Review 3: a blocker that does not queue wins over a waiting queue — the send goes to /input,
// whose own guards refuse it (stubbed here, so only "not queued" is asserted), instead of being
// queued behind the old question.
func TestLongBlockerRefusesEvenWithAQueue(t *testing.T) {
	pendingTestEnv(t)
	fakeTmux(t)
	const name, from = "pp_long_queue", "pp_sender"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	prevReady, prevBlock := peerDeliveryReady, peerQueueBlocker
	peerDeliveryReady = func(session.Meta) (bool, bool) { return false, false }
	t.Cleanup(func() {
		drainPendingLoops()
		peerDeliveryReady, peerQueueBlocker = prevReady, prevBlock
	})
	for i, st := range []string{agents.StateAuth, agents.StateBlocked} {
		dropPendingPeers(name, "reset")
		if err := agents.PutPendingPeer(name, agents.PendingPeer{ID: "af_old", From: from, Intent: "request",
			Message: "old", BlockedOn: "question", QueuedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		peerQueueBlocker = func(session.Meta) string { return st }
		rec := postInput(t, name, peerBody(from, "new "+string(rune('0'+i))))
		if rec.Code == http.StatusAccepted {
			t.Errorf("%s: queued behind the old question: %s", st, rec.Body.String())
		}
		if n := len(agents.PendingPeers(name)); n != 1 {
			t.Errorf("%s: spool = %d, want the old message only", st, n)
		}
	}
}

// Review 4: the cap holds under concurrent sends.
func TestPendingPeerCapHoldsUnderConcurrentSends(t *testing.T) {
	pendingTestEnv(t)
	const name = "pp_cap_race"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
	prevReady, prevBlock := peerDeliveryReady, peerQueueBlocker
	peerDeliveryReady = func(session.Meta) (bool, bool) { return false, false }
	peerQueueBlocker = func(session.Meta) string { return "question" }
	t.Cleanup(func() {
		drainPendingLoops()
		peerDeliveryReady, peerQueueBlocker = prevReady, prevBlock
	})
	for i := 0; i < pendingPeerCap-1; i++ {
		agents.PutPendingPeer(name, agents.PendingPeer{ID: "af_" + string(rune('a'+i)), From: "pp_sender",
			Intent: "request", Message: "old", BlockedOn: "question", QueuedAt: time.Now()})
	}
	senders := make([]string, 30)
	for i := range senders {
		senders[i] = "pp_sender_" + strconv.Itoa(i)
		session.WriteMeta(session.Meta{Name: senders[i], Dir: t.TempDir(), Kind: session.KindClaude})
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var accepted, full int
	var cmu sync.Mutex
	for _, from := range senders {
		wg.Add(1)
		go func(from string) {
			defer wg.Done()
			<-start
			rec := postInput(t, name, peerBody(from, "new"))
			cmu.Lock()
			defer cmu.Unlock()
			switch {
			case rec.Code == http.StatusAccepted:
				accepted++
			case rec.Code == http.StatusTooManyRequests && strings.Contains(rec.Body.String(), "peer_queue_full"):
				full++
			}
		}(from)
	}
	close(start)
	wg.Wait()
	if n := len(agents.PendingPeers(name)); n != pendingPeerCap || accepted != 1 || full != len(senders)-1 {
		t.Fatalf("spool=%d accepted=%d full=%d; want %d, 1, %d", n, accepted, full, pendingPeerCap, len(senders)-1)
	}
}

// Review 5: a delivery refused and retried leaves no fleet-graph arrow; the one that lands
// leaves exactly one.
func TestPendingPeerGraphRecordedOncePerDelivery(t *testing.T) {
	pendingTestEnv(t)
	h := &blockingFakeHandle{inter: &agents.Interaction{ID: "q", Kind: agents.InteractionQuestion}}
	installManagedFake(t, h)
	const name, from = "pp_graph", "pp_sender"
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged})
	session.WriteMeta(session.Meta{Name: from, Dir: t.TempDir(), Kind: session.KindClaude})
	p := agents.PendingPeer{ID: "af_g", From: from, Intent: "request", Message: "undelivered", BlockedOn: "question", QueuedAt: time.Now()}
	if err := agents.PutPendingPeer(name, p); err != nil {
		t.Fatal(err)
	}
	peerRows := func() int {
		files, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".local", "state", "agent-fleet", "fleet-graph", "activity-*.jsonl"))
		n := 0
		for _, f := range files {
			b, _ := os.ReadFile(f)
			n += strings.Count(string(b), `"ev":"peer"`)
		}
		return n
	}
	deliverPendingPeer(name, p)
	deliverPendingPeer(name, p)
	if n := peerRows(); n != 0 {
		t.Fatalf("two refused deliveries left %d peer rows, want 0", n)
	}
	h.set(nil)
	if !deliverPendingPeer(name, p) {
		t.Fatal("the delivery after the answer failed")
	}
	if n := peerRows(); n != 1 {
		t.Fatalf("a delivered message left %d peer rows, want 1", n)
	}
}
