package agents

// Pending peer messages (#1031): a peer message sent while its target waits on its user's
// decision (a question, a plan approval, a permission prompt) waits here and is delivered
// through /input once the user has answered and the turn has ended (sessionx/peer_pending.go).
//
// The files use the held-peer format and helpers (heldpeers.go) under a sibling directory, not
// held-peer/ itself. A held message was already accepted — its injection and fleet-graph rows
// are written and only the runtime has not taken it — and every Managed Resume feeds held-peer/
// straight to the runtime (DeliverHeld, TurnQueue.adoptHeld). A pending message has been
// accepted by nobody: it must reach the session through the whole /input path, and only once
// the blocker is gone, so Resume must never see it. It also serves Terminal (CLI) sessions,
// whose held-peer directories SweepHeld removes.

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pendingSubdir = "pending-peer"

// PendingPeer is one queued peer message. Message is the raw body; the envelope is built when
// it is delivered, like any peer send.
type PendingPeer struct {
	ID        string
	From      string
	Intent    string
	Message   string
	BlockedOn string
	QueuedAt  time.Time
	seq       uint64
}

func (p PendingPeer) file() heldPeer {
	seq := p.seq
	if seq == 0 {
		seq = heldSeq.Add(1)
	}
	return heldPeer{ID: p.ID, Prompt: p.Message, From: p.From, Intent: p.Intent,
		BlockedOn: p.BlockedOn, QueuedAt: p.QueuedAt, Seq: seq}
}

// PutPendingPeer writes p. Writing it back after a failed delivery keeps its place: the order
// is QueuedAt, then the sequence it was first written with.
func PutPendingPeer(name string, p PendingPeer) error {
	if !heldName(name) || p.ID == "" {
		return errors.New("bad pending peer message")
	}
	b, err := json.Marshal(p.file())
	if err != nil {
		return err
	}
	return writeFileAtomic(spoolFile(pendingSubdir, name, p.ID), b)
}

// PendingPeers returns session name's pending messages, oldest first.
func PendingPeers(name string) []PendingPeer {
	var out []PendingPeer
	for _, hp := range loadSpool(pendingSubdir, name) {
		out = append(out, PendingPeer{ID: hp.ID, From: hp.From, Intent: hp.Intent, Message: hp.Prompt,
			BlockedOn: hp.BlockedOn, QueuedAt: hp.QueuedAt, seq: hp.Seq})
	}
	return out
}

// TakePendingPeer removes one message and reports whether this call removed it. The removal is
// the claim: of a delivery and a member's drop racing for the same message, exactly one sees
// true.
func TakePendingPeer(name, id string) bool {
	if !heldName(name) || id == "" {
		return false
	}
	return os.Remove(spoolFile(pendingSubdir, name, id)) == nil
}

// DropPendingPeers discards session name's pending messages (archive, trash, recreate), logging
// each: the sender is not told.
func DropPendingPeers(name, reason string) {
	if !heldName(name) {
		return
	}
	for _, p := range PendingPeers(name) {
		log.Printf("pending peer message: %s: dropped %s from %s queued %s (%s)",
			name, p.ID, p.From, p.QueuedAt.Format(time.RFC3339), reason)
	}
	if err := os.RemoveAll(spoolDir(pendingSubdir, name)); err != nil {
		log.Printf("pending peer message: %s: remove: %v", name, err)
	}
}

// PendingPeerSessions lists the sessions that have a pending-peer directory, for the boot
// sweep and for restarting their deliveries.
func PendingPeerSessions() []string {
	ents, err := os.ReadDir(filepath.Join(heldBase(), pendingSubdir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && heldName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// SweepPendingTemp removes temp files a crash left in name's pending directory.
func SweepPendingTemp(name string) {
	dir := spoolDir(pendingSubdir, name)
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if !strings.HasPrefix(f.Name(), ".tmp-") {
			continue
		}
		if fi, err := f.Info(); err == nil && time.Since(fi.ModTime()) > heldTmpGrace {
			os.Remove(filepath.Join(dir, f.Name()))
		}
	}
}
