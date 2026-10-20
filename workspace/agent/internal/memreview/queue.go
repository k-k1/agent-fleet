package memreview

// The pending queue (ADR 0117 decisions 6 and 8): proposals are files in a directory beside the
// store, outside the memory repo, so unapproved text is never in the published history. The review
// writes it; the member decides (approve.go). The retention rule lives here with the store it
// belongs to:
//
//   - pending text is deleted when a decision is made;
//   - a rejected proposal keeps only its content hash, until the memory it concerned is forgotten;
//   - a proposal whose hash was rejected is never queued again, and an identical pending one is not
//     queued twice.
//
// Every file is a scanned artefact: the proposal's strings pass memoryx.ReviewTextProblem again on
// the way in, so the queue is clean even if a caller skipped ParseReply.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
)

// Proposal is one queued operation with what applying it later needs.
type Proposal struct {
	ID    string `json:"id"`
	RunID string `json:"runId"`
	// Index is the operation's position in its run's reply (the id derives from it).
	Index   int    `json:"index"`
	Session string `json:"session"`
	// ProjectID is the project of the reviewed session when the operation is project-scoped.
	ProjectID string    `json:"projectId,omitempty"`
	Op        Op        `json:"op"`
	Hash      string    `json:"hash"`
	Created   time.Time `json:"created"`
	// Stale is why the last approval attempt left the proposal unpublished (a memoryx.Review*
	// reason); "" while none has. It is a mark for the screen, not a state a decision needs:
	// approving always asks the store again.
	Stale string `json:"stale,omitempty"`
}

// rejection is what survives a rejected proposal: no text, only the hash and which memory it was
// about, so forgetting that memory can release it.
type rejection struct {
	Hash      string    `json:"hash"`
	Scope     string    `json:"scope"`
	Name      string    `json:"name"`
	ProjectID string    `json:"projectId,omitempty"`
	At        time.Time `json:"at"`
}

var (
	pendingStore  = store[Proposal]{"pending"}
	rejectedStore = store[rejection]{"rejected"}
	// decidedStore remembers, by proposal id and without any text, that a proposal got a decision.
	// A run that stopped after queueing some proposals replays its queueing on restart; without
	// this marker an approved or rejected proposal would come back as a new pending one. The
	// value is the run id, so the marker can go when that run has ended (gcDecidedLocked).
	decidedStore = store[string]{"decided"}
)

// OpHash identifies the content of an operation: what it would do to which memory, with which
// text. Reason and evidence are the model's justification and do not change the idea.
func OpHash(projectID string, op Op) string {
	h := sha256.New()
	for _, p := range []string{projectID, op.Op, op.Scope, op.Name, op.Type, op.Description, op.Body} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func proposalID(runID string, i int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", runID, i)))
	return hex.EncodeToString(h[:])[:20]
}

// Outcome of Enqueue for one proposal.
const (
	EnqueuedNew      = "queued"
	EnqueuedExisting = "existing" // the same proposal is already there (a resumed run)
	SkippedDecided   = "decided"  // this very proposal already got a decision (a replay)
	SkippedRejected  = "rejected" // the member already rejected this exact idea
	SkippedDuplicate = "duplicate"
)

// enqueue stores one proposal. It is idempotent: the id is derived from the run and the position,
// so a run resumed after a crash that had queued some of its operations queues the rest and
// nothing twice.
func enqueue(p Proposal) (string, error) {
	for _, s := range []string{p.Session, p.ProjectID, p.Op.Op, p.Op.Scope, p.Op.Name, p.Op.Type, p.Op.Description, p.Op.Body, p.Op.Reason} {
		if memoryx.ReviewTextProblem(s) != "" {
			return "", errors.New("a proposal failed the secret scan and was not queued")
		}
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if _, ok := pendingStore.Read(p.ID); ok {
		return EnqueuedExisting, nil
	}
	if _, ok := decidedStore.Read(p.ID); ok {
		return SkippedDecided, nil
	}
	if _, ok := rejectedStore.Read(p.Hash); ok {
		return SkippedRejected, nil
	}
	keys := pendingStore.keys()
	for _, id := range keys {
		if q, ok := pendingStore.Read(id); ok && q.Hash == p.Hash {
			return SkippedDuplicate, nil
		}
	}
	// Only a proposal that would be NEW needs room; the skips above do not. The bound is checked
	// here, inside the lock that inserts, so no reply can overshoot it.
	if len(keys) >= MaxPending {
		return "", ErrQueueFull
	}
	if err := pendingStore.Write(p.ID, p); err != nil {
		return "", err
	}
	return EnqueuedNew, nil
}

// Pending lists the queue, oldest first.
func Pending() []Proposal {
	stateMu.Lock()
	defer stateMu.Unlock()
	return listPendingLocked()
}

func listPendingLocked() []Proposal {
	var out []Proposal
	for _, id := range pendingStore.keys() {
		if p, ok := pendingStore.Read(id); ok {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// runHasPendingLocked reports whether the run still has a proposal waiting, with stateMu held.
func runHasPendingLocked(runID string) bool {
	for _, id := range pendingStore.keys() {
		if p, ok := pendingStore.Read(id); ok && p.RunID == runID {
			return true
		}
	}
	return false
}

// pendingCount is the queue length.
func pendingCount() int {
	stateMu.Lock()
	defer stateMu.Unlock()
	return len(pendingStore.keys())
}

// ErrNoProposal is the answer for an id that is not in the queue (never was, or already decided).
var ErrNoProposal = errors.New("no such proposal")

// markStale records why an approval left the proposal unpublished. The text stays: the member still
// has to decide, and a rejection is what removes it.
func markStale(id, reason string) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	p, ok := pendingStore.Read(id)
	if !ok || p.Stale == reason {
		return nil
	}
	p.Stale = reason
	return pendingStore.Write(id, p)
}

// Decision values for Decide.
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

// Decide removes a proposal from the queue. Its text is gone either way (and the error says when
// it is not: the caller retries); a rejection leaves the hash behind (see the file comment).
// Approving here does not publish anything: Approve (approve.go) calls this after it has published
// the proposal.
//
// The order is what makes a stop harmless: the rejection hash, then the decided marker, then the
// text's removal. A stop after the marker leaves a pending file that a retry of Decide removes,
// and a run replaying its queueing skips the proposal either way.
func Decide(id, decision string, now time.Time) error {
	if decision != DecisionApprove && decision != DecisionReject {
		return errors.New("decision must be approve or reject")
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	p, ok := pendingStore.Read(id)
	if !ok {
		return ErrNoProposal
	}
	if decision == DecisionReject {
		r := rejection{Hash: p.Hash, Scope: p.Op.Scope, Name: p.Op.Name, ProjectID: p.ProjectID, At: now}
		if err := rejectedStore.Write(p.Hash, r); err != nil {
			return err // the text stays until the hash is safe
		}
	}
	if err := decidedStore.Write(id, p.RunID); err != nil {
		return err
	}
	// The run that queued the proposal may still hold its text (it stopped, or is waiting for room
	// for the rest of its reply): that copy goes before the proposal's own file does, so a decision
	// that reports success has left no copy anywhere.
	if err := scrubRunOp(p); err != nil {
		return err
	}
	return pendingStore.Remove(id)
}

// scrubRunOp blanks the text of one operation in a run that has not ended, keeping what the replay
// needs (which operation it was, to skip it by its decided marker), and removes the run's staged
// copies.
func scrubRunOp(p Proposal) error {
	if r, ok := runStore.Read(p.RunID); ok && !r.Terminal() && p.Index >= 0 && p.Index < len(r.Ops) {
		op := r.Ops[p.Index]
		r.Ops[p.Index] = Op{Op: op.Op, Scope: op.Scope, Name: op.Name}
		if err := runStore.Write(r.ID, r); err != nil {
			return err
		}
	}
	// A run record being rewritten when the process stopped leaves a staged copy of the older
	// whole record, text included; it is removed whatever state the run is in. Done under the
	// state lock, so no live writer of the record is racing it.
	return runStore.removeStaged(p.RunID)
}

// gcDecidedLocked drops the decided markers whose run has ended (or is gone): only an open run can
// replay its queueing.
func gcDecidedLocked() {
	for _, id := range decidedStore.keys() {
		runID, _ := decidedStore.Read(id)
		if r, ok := runStore.Read(runID); !ok || r.Terminal() {
			_ = decidedStore.Remove(id)
		}
	}
}

// ForgetMemory releases the rejected hashes of a memory that has been forgotten, so a later review
// may propose it again, and returns how many it released. projectID is "" for the user scope. A
// hash it could not remove is not counted and the first such error is returned.
func ForgetMemory(scope, name, projectID string) (int, error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	return releaseLocked(scope, name, projectID, time.Time{})
}

// releaseLocked removes the rejections of one memory made at or before cutoff (all of them when it
// is zero), with stateMu held.
func releaseLocked(scope, name, projectID string, cutoff time.Time) (int, error) {
	n := 0
	var first error
	for _, h := range rejectedStore.keys() {
		r, ok := rejectedStore.Read(h)
		if !ok || r.Scope != scope || r.Name != name || r.ProjectID != projectID {
			continue
		}
		if !cutoff.IsZero() && r.At.After(cutoff) {
			continue
		}
		if err := rejectedStore.Remove(h); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		n++
	}
	return n, first
}

// Rejected reports whether the member rejected exactly this idea.
func Rejected(hash string) bool {
	stateMu.Lock()
	defer stateMu.Unlock()
	_, ok := rejectedStore.Read(hash)
	return ok
}
