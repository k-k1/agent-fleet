package sessionx

// Peer messages to a session waiting on its user (#1031). A peer send whose target shows a
// question, a plan approval or a permission prompt is not refused with 409: typed text would
// land in that dialog, but only the user can clear it, so the sender could do nothing but poll
// and resend. The message is validated as any peer send (policy, intent, rate limit), written
// to the target's pending spool (agents/pendingpeers.go) and answered 202 queued. A per-target
// delivery loop sends the spool, oldest first, once the blocker is gone AND the turn the answer
// unblocked has ended, so a message never interleaves with the work the answer started.
//
// Delivery goes through HandleSessionInput itself, so injection records, the fleet graph,
// delivery confirmation and the peer policy are the ordinary ones. The request carries a
// context mark the wire cannot set: it skips the duplicate check (the message was counted when
// it was queued) and never queues again.
//
// auth_expired and the usage-limit menu keep refusing: they can last hours, and the sender
// should know rather than wait. Console, send_to_session and scheduled sends keep every 409:
// only peer messages queue.
//
// A message is claimed by removing its file before it is sent, and written back with its place
// kept when the send fails in a way that leaves it undelivered. A crash between those two
// steps loses that one message; the alternative, removing after the send, would deliver it
// twice after a crash, and a peer acting twice on one request is the worse failure.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

const (
	// pendingPeerTTL is how long a queued message waits for the user before it is dropped.
	pendingPeerTTL = 24 * time.Hour
	// pendingPeerCap bounds one target's spool; past it a send is refused like the rate limit.
	pendingPeerCap = 20
)

var (
	// pendingPeerPoll is the delivery loop's tick while the target runs, pendingPeerIdlePoll
	// while it is stopped (a halt keeps the spool; only a start can let it go). Variables so
	// tests run in milliseconds.
	pendingPeerPoll     = 2 * time.Second
	pendingPeerIdlePoll = 10 * time.Second
	pendingPeerNow      = time.Now
)

// queueableBlocker reports whether a peer send to a target in state st waits instead of being
// refused.
func queueableBlocker(st string) bool {
	return st == "question" || st == "plan" || st == "permission"
}

// peerQueueBlocker is promptBlocker plus a Managed handle's pending interaction: Managed
// codex keeps its question on the handle, where promptBlocker does not look (its Send refuses
// with ErrQuestionPending instead). A variable so tests stub the blocker.
var peerQueueBlocker = func(m session.Meta) string {
	// claude's usage-limit menu is guarded by /input itself, not by promptBlocker; it has to be
	// seen here too, or a target with a waiting queue would take the message instead of refusing.
	if m.DriverKind() != session.DriverManaged && NormalizeKind(m.Kind) == session.KindClaude &&
		tmuxx.AtRateLimitModal(m.Name) {
		return agents.StateBlocked
	}
	if st := promptBlocker(m.Name); st != "" {
		return st
	}
	if m.DriverKind() != session.DriverManaged {
		return ""
	}
	d, ok := driverOf(m)
	if !ok {
		return ""
	}
	lh, ok := d.(agents.LiveHandles)
	if !ok {
		return ""
	}
	h, ok := lh.LiveHandle(m)
	if !ok || h == nil {
		return ""
	}
	snap, err := h.Snapshot()
	if err != nil || snap.Interaction == nil {
		return ""
	}
	if snap.Interaction.Kind == agents.InteractionApproval {
		return "permission"
	}
	return "question"
}

// peerDeliveryReady reports whether the target can take a queued message now (alive, nothing
// to answer, the turn ended) and whether it runs at all. A variable so tests stub it.
var peerDeliveryReady = func(m session.Meta) (ready, alive bool) {
	if !SessionAlive(m) {
		return false, false
	}
	if peerQueueBlocker(m) != "" {
		return false, true
	}
	if m.DriverKind() == session.DriverManaged && managedBusy(m) {
		return false, true
	}
	// heal=true: the pane, not only the hook cache, decides. A cached idle with claude's spinner
	// on screen (no working hook yet, or a prompt frame mid-answer) reads working there, and a
	// message typed then would land in the turn the answer started.
	return DriveState(m, true, true) == "idle" && sessionInputReady(m, true), true
}

// pendingTarget serialises one target's queue decisions. mu is held for the enqueue decision
// (is anything waiting or in flight, the cap, the write), the claim, the write-back and the
// drops, never across the delivery's /input call. inflight is the claimed message on its way:
// while it is out the spool can read empty, and a new send must still queue behind it. gen
// moves on every drop (archive, trash, recreate, a gone session), so a delivery that was in
// flight across one does not write its message back.
type pendingTarget struct {
	mu       sync.Mutex
	inflight *agents.PendingPeer
	gen      uint64
}

var (
	pendingTargetsMu sync.Mutex
	pendingTargets   = map[string]*pendingTarget{}
)

func pendingTargetOf(name string) *pendingTarget {
	pendingTargetsMu.Lock()
	defer pendingTargetsMu.Unlock()
	t := pendingTargets[name]
	if t == nil {
		t = &pendingTarget{}
		pendingTargets[name] = t
	}
	return t
}

// dropPendingPeers is every drop of a target's spool: it invalidates a delivery in flight.
func dropPendingPeers(name, reason string) {
	t := pendingTargetOf(name)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gen++
	agents.DropPendingPeers(name, reason)
	if t.inflight != nil {
		log.Printf("pending peer message: %s: %s in flight will not be written back (%s)", name, t.inflight.ID, reason)
	}
}

// pendingDelivery is the context mark on a delivery's /input request.
type pendingDeliveryKey struct{}

type pendingDelivery struct{ queuedAt time.Time }

func pendingDeliveryOf(r *http.Request) (pendingDelivery, bool) {
	d, ok := r.Context().Value(pendingDeliveryKey{}).(pendingDelivery)
	return d, ok
}

// pendingQueueFull is the refusal past pendingPeerCap.
func pendingQueueFull() error {
	return peerReject("peer_queue_full",
		"宛先は利用者の回答待ちで、届けられていないメッセージが上限（%d 通）に達しています", pendingPeerCap)
}

// enqueueDecision is what a peer send to name does about the queue, decided under the target's
// lock so a claim, a write-back or another send cannot interleave: blockedOn == "" sends it now;
// otherwise it was queued (pending is the queue's length) or refused (err). allow is the rate
// limit, consulted only for a message that would be queued, so a refusal by the cap costs the
// sender nothing.
//
// A blocker that does not queue (an expired login, the usage-limit menu) wins over a waiting
// queue: such a send goes ahead and /input refuses it as before. Without one, a message waiting
// or in flight makes the send queue behind it.
func enqueueDecision(name string, dst session.Meta, from, intent, message string,
	allow func() error) (blockedOn string, pending int, err error) {
	t := pendingTargetOf(name)
	t.mu.Lock()
	defer t.mu.Unlock()
	st := peerQueueBlocker(dst)
	switch {
	case queueableBlocker(st):
		blockedOn = st
	case st != "":
		return "", 0, nil
	case t.inflight != nil:
		blockedOn = t.inflight.BlockedOn
	default:
		if q := agents.PendingPeers(name); len(q) > 0 {
			blockedOn = q[0].BlockedOn
		}
	}
	if blockedOn == "" {
		return "", 0, nil
	}
	n := len(agents.PendingPeers(name))
	if t.inflight != nil {
		n++
	}
	if n >= pendingPeerCap {
		return blockedOn, n, pendingQueueFull()
	}
	if err := allow(); err != nil {
		return blockedOn, n, err
	}
	p := agents.PendingPeer{ID: agents.NormalizeMsgID(""), From: from, Intent: intent,
		Message: strings.TrimSpace(message), BlockedOn: blockedOn, QueuedAt: pendingPeerNow()}
	if err := agents.PutPendingPeer(name, p); err != nil {
		return blockedOn, n, err
	}
	log.Printf("pending peer message: %s: queued %s from %s (%s)", name, p.ID, from, blockedOn)
	kickPendingPeers(name)
	return blockedOn, n + 1, nil
}

func takePending(name, id string) bool {
	t := pendingTargetOf(name)
	t.mu.Lock()
	defer t.mu.Unlock()
	return agents.TakePendingPeer(name, id)
}

// pendingPeerQueue answers a peer send that waits: 202 with the state it waits on.
func writePendingQueued(w http.ResponseWriter, name, blockedOn string, pending int) {
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
		"queued": name, "blocked_on": blockedOn, "pending": pending,
	})
}

var (
	pendingRunMu sync.Mutex
	pendingRun   = map[string]bool{}
	// pendingLoops lets tests wait for every delivery loop to finish.
	pendingLoops sync.WaitGroup
)

// kickPendingPeers starts name's delivery loop unless one runs.
func kickPendingPeers(name string) {
	pendingRunMu.Lock()
	defer pendingRunMu.Unlock()
	if pendingRun[name] {
		return
	}
	pendingRun[name] = true
	pendingLoops.Add(1)
	go runPendingPeers(name)
}

// runPendingPeers delivers name's spool, oldest first, one message per ended turn. It ends when
// the spool is empty; the emptiness is re-read under pendingRunMu, so a message queued while it
// was deciding to end is never left without a loop.
func runPendingPeers(name string) {
	defer pendingLoops.Done()
	for {
		list := agents.PendingPeers(name)
		list = dropExpiredPending(name, list)
		if len(list) == 0 {
			pendingRunMu.Lock()
			if len(agents.PendingPeers(name)) == 0 {
				delete(pendingRun, name)
				pendingRunMu.Unlock()
				return
			}
			pendingRunMu.Unlock()
			continue
		}
		meta, ok := session.ReadMeta(name)
		if !ok || meta.Archived {
			dropPendingPeers(name, "the session no longer exists or is archived")
			continue
		}
		ready, alive := peerDeliveryReady(meta)
		if ready {
			if deliverPendingPeer(name, list[0]) {
				// The delivery started a turn; the next message waits for it to end.
				time.Sleep(pendingPeerPoll)
				continue
			}
		}
		if alive {
			time.Sleep(pendingPeerPoll)
		} else {
			time.Sleep(pendingPeerIdlePoll)
		}
	}
}

func dropExpiredPending(name string, list []agents.PendingPeer) []agents.PendingPeer {
	now := pendingPeerNow()
	out := list[:0:0]
	for _, p := range list {
		if now.Sub(p.QueuedAt) > pendingPeerTTL {
			if takePending(name, p.ID) {
				log.Printf("pending peer message: %s: dropped %s from %s queued %s (expired after %s)",
					name, p.ID, p.From, p.QueuedAt.Format(time.RFC3339), pendingPeerTTL)
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// deliverPendingPeer sends p through /input and reports whether it reached the session. A
// failure that leaves it undelivered and may clear (blocked again, stopped, rate limited)
// writes it back in place; one that will not clear (the policy now refuses, the target is gone)
// or that may already have reached the session drops it.
func deliverPendingPeer(name string, p agents.PendingPeer) bool {
	t := pendingTargetOf(name)
	t.mu.Lock()
	if t.inflight != nil || !agents.TakePendingPeer(name, p.ID) {
		t.mu.Unlock()
		return false // the member dropped it meanwhile
	}
	t.inflight = &p
	gen := t.gen
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.inflight = nil
		t.mu.Unlock()
	}()
	body, _ := json.Marshal(map[string]string{"prompt": p.Message, "peer_from": p.From, "peer_intent": p.Intent})
	ctx := context.WithValue(context.Background(), pendingDeliveryKey{}, pendingDelivery{queuedAt: p.QueuedAt})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/sessions/"+name+"/input", bytes.NewReader(body))
	req.SetPathValue("name", name)
	rec := &captureWriter{header: http.Header{}}
	HandleSessionInput(rec, req)
	if rec.status >= 200 && rec.status < 300 {
		// Recorded here, once, rather than by /input: a delivery refused and retried would
		// otherwise add a peer arrow per attempt for a message that never arrived.
		fleetgraph.RecordPeer(p.From, name, p.Intent, p.Message)
		log.Printf("pending peer message: %s: delivered %s from %s", name, p.ID, p.From)
		return true
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.body.Bytes(), &resp)
	e := resp.Error
	// 502 (delivery unconfirmed, a runtime that failed after taking the turn) and a tmux error
	// (the Enter may be what failed, the text typed) may already have reached the session; a
	// retry could deliver twice, so they drop.
	keep := rec.status == http.StatusConflict ||
		rec.status == http.StatusTooManyRequests && e.Code == "peer_rate_limited" ||
		rec.status == http.StatusInternalServerError && e.Code != "tmux_failed"
	if keep {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.gen != gen {
			log.Printf("pending peer message: %s: dropped %s from %s (the spool was dropped during its delivery)", name, p.ID, p.From)
			return false
		}
		if err := agents.PutPendingPeer(name, p); err != nil {
			log.Printf("pending peer message: %s: lost %s from %s: write back: %v", name, p.ID, p.From, err)
		}
		return false
	}
	log.Printf("pending peer message: %s: dropped %s from %s (delivery answered %d %s)",
		name, p.ID, p.From, rec.status, strings.TrimSpace(rec.body.String()))
	return false
}

// captureWriter is the ResponseWriter of a delivery's in-process /input call.
type captureWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *captureWriter) Header() http.Header { return c.header }
func (c *captureWriter) WriteHeader(s int) {
	if c.status == 0 {
		c.status = s
	}
}
func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}

// pendingPeerWire is one queued message as the mirror shows it.
type pendingPeerWire struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Intent    string `json:"intent,omitempty"`
	BlockedOn string `json:"blockedOn,omitempty"`
	QueuedAt  string `json:"queuedAt"`
	// Excerpt is the message's head, enough for the member to tell which one to drop.
	Excerpt string `json:"excerpt"`
}

const pendingExcerptRunes = 120

func pendingExcerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > pendingExcerptRunes {
		return string(r[:pendingExcerptRunes-1]) + "…"
	}
	return s
}

func pendingPeersWire(name string) []pendingPeerWire {
	var out []pendingPeerWire
	for _, p := range agents.PendingPeers(name) {
		out = append(out, pendingPeerWire{ID: p.ID, From: p.From, Intent: p.Intent, BlockedOn: p.BlockedOn,
			QueuedAt: p.QueuedAt.UTC().Format(time.RFC3339), Excerpt: pendingExcerpt(p.Message)})
	}
	return out
}

// HandleDropPendingPeer (DELETE /sessions/{name}/pending-peer/{id}) lets the member drop one
// queued message before it is delivered.
func HandleDropPendingPeer(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var from string
	for _, p := range agents.PendingPeers(name) {
		if p.ID == id {
			from = p.From
		}
	}
	t := pendingTargetOf(name)
	t.mu.Lock()
	took := from != "" && agents.TakePendingPeer(name, id)
	t.mu.Unlock()
	if !took {
		httpx.WriteErr(w, http.StatusNotFound, "not_pending", "no such pending message (it may have been delivered)")
		return
	}
	log.Printf("pending peer message: %s: dropped %s from %s (by the member)", name, id, from)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"dropped": id})
}

// ResumePendingPeers runs at Agent boot: it drops the spools of sessions that are gone or
// archived and restarts the delivery loop of every other one, so a restart between queueing
// and the answer loses nothing.
func ResumePendingPeers() {
	for _, name := range agents.PendingPeerSessions() {
		m, ok := session.ReadMeta(name)
		switch {
		case !ok:
			dropPendingPeers(name, "the session no longer exists")
			continue
		case m.Archived:
			dropPendingPeers(name, "the session is archived")
			continue
		}
		agents.SweepPendingTemp(name)
		if len(agents.PendingPeers(name)) > 0 {
			kickPendingPeers(name)
		}
	}
}
