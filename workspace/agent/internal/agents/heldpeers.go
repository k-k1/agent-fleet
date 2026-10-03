package agents

// Held input (#1255, #1257): a peer message, an operator prompt (send_to_session with
// report_to) or a scheduled prompt waiting in a Managed driver's queue survives what drops the
// queue — halt, the execution-method switch's stop, Agent shutdown, a daemon drain, and a crash —
// and becomes the session's first turn(s) on its next start. These are the origins nobody at the
// session's keyboard can resend: the member's own queued input is the member's to resend, and is
// not held.
//
// The queue writes a held entry through to disk when it accepts it, not at teardown: a crash
// or an OOM kill runs no teardown, and those are the restarts nobody is told about. The file
// goes when the entry is handed to the runtime (Commit), when a stop discards it and when it is
// removed by id; teardown (DropAll) leaves it. Archive and trash drop the session's files
// (DropHeld): a message to a folded-away session has no reader.
//
// An operator or scheduled prompt that goes without running is reported through
// OnHeldDropped: the operator's instruction ledger still owes a report for it, and the
// scheduler recorded the run as fired. A peer message's drop is only logged (ADR 0041 keeps
// peers off the ledger).
//
// One file per message, written by temp file + rename and removed by name, so no writer ever
// reads, modifies and writes back a shared file (fstore has no lock and no rename, and a
// read-modify-write there measurably loses writes). Order is the queue time, then a
// process-wide sequence for entries queued in the same instant, one FIFO across origins.
//
// The directory keeps its held-peer name: files an earlier Agent wrote there carry no origin
// and are peer messages.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// heldBase is the state root the held files live under; a variable so tests keep them out of
// the real HOME.
var heldBase = paths.AgentStateDir

const heldSubdir = "held-peer"

var heldSeq atomic.Uint64

// heldPeer is one held message on disk. The prompt is stored as JSON, so any text survives.
// The pending-peer spool (pendingpeers.go) shares the format and adds Intent and BlockedOn.
type heldPeer struct {
	ID          string    `json:"id"`
	Prompt      string    `json:"prompt"`
	Attachments []string  `json:"attachments,omitempty"`
	From        string    `json:"from,omitempty"`
	Intent      string    `json:"intent,omitempty"`
	BlockedOn   string    `json:"blockedOn,omitempty"`
	QueuedAt    time.Time `json:"queuedAt"`
	Seq         uint64    `json:"seq"`
	// Origin is the input's Origin.Kind; empty is a peer message.
	Origin   string      `json:"origin,omitempty"`
	Schedule ScheduleRef `json:"schedule,omitempty"`
	Instr    string      `json:"instr,omitempty"`
}

func (hp heldPeer) origin() string {
	if hp.Origin == "" {
		return OriginPeer
	}
	return hp.Origin
}

func heldDir(name string) string { return spoolDir(heldSubdir, name) }

func spoolDir(sub, name string) string { return filepath.Join(heldBase(), sub, name) }

// heldName reports whether name may be a held directory: a valid session name is one path
// segment. ReadMeta does not check the name inside a meta file, and DropHeld removes a whole
// directory, so a name such as ".." would otherwise reach the state root.
func heldName(name string) bool { return session.ValidName(name) }

// heldFile names the file after a hash of the id: the id comes off the wire and is not a safe
// file name, and the same id always maps to the same file.
func heldFile(name, id string) string { return spoolFile(heldSubdir, name, id) }

func spoolFile(sub, name, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(spoolDir(sub, name), hex.EncodeToString(sum[:16])+".json")
}

// isHeldOrigin reports whether input of origin kind is held: the origins nobody at the
// session's keyboard can resend.
func isHeldOrigin(kind string) bool {
	switch kind {
	case OriginPeer, OriginOperator, OriginSchedule, OriginScheduleManual:
		return true
	}
	return false
}

func isHeld(in TurnInput) bool { return isHeldOrigin(in.Origin.Kind) }

// Reasons a held input goes without running (HeldDrop.Reason).
const (
	DropArchived  = "archived"
	DropTrashed   = "trashed"
	DropRecreated = "recreated"
	DropTerminal  = "switched-to-terminal"
	DropGone      = "session-gone"
	DropDiscarded = "discarded" // a second stop or the discard-queue stop
	DropStopped   = "stopped"   // a first stop caught it before it started
	DropRemoved   = "removed"   // the member removed it from the queue
	DropWithdrawn = "withdrawn" // the operator's stop_session withdrew its instructions
)

// HeldDrop is one held operator or scheduled input that went without running.
type HeldDrop struct {
	Session  string
	ID       string // the input's ClientMessageID
	Instr    string // TurnInput.Instr
	Origin   string
	Schedule ScheduleRef
	QueuedAt time.Time
	Reason   string
}

// OnHeldDropped is told about every held operator or scheduled input that is dropped before
// it ran, so its instruction is reported as not run and its scheduled run recorded as not
// executed. Set once at boot, before anything is queued; nil drops silently (this package's
// tests). It may be called under a driver's handle lock: it must not block on the network or
// call back into a driver.
var OnHeldDropped func(HeldDrop)

func reportDropped(d HeldDrop) {
	if d.Origin == OriginPeer || d.Origin == "" {
		return
	}
	log.Printf("held input: %s: dropped %s %s queued %s (%s)",
		d.Session, d.Origin, d.ID, d.QueuedAt.Format(time.RFC3339), d.Reason)
	if f := OnHeldDropped; f != nil {
		f(d)
	}
}

// dropQueued reports in, a held input a stop or a removal took out of the queue.
func dropQueued(name string, in TurnInput, reason string) {
	if !isHeld(in) {
		return
	}
	reportDropped(HeldDrop{Session: name, ID: in.ClientMessageID, Instr: in.Instr, Origin: in.Origin.Kind,
		Schedule: in.Schedule, QueuedAt: in.queuedAt, Reason: reason})
}

// putHeld writes in's file and reports whether it did. Caller holds the driver's handle lock.
func putHeld(name string, in TurnInput) bool {
	if !heldName(name) || in.ClientMessageID == "" {
		return false
	}
	b, err := json.Marshal(heldPeer{
		ID: in.ClientMessageID, Prompt: in.Prompt, Attachments: in.Attachments,
		From: in.Origin.From, QueuedAt: in.queuedAt, Seq: heldSeq.Add(1),
		Origin: heldOriginField(in.Origin.Kind), Schedule: in.Schedule, Instr: in.Instr,
	})
	if err == nil {
		err = writeFileAtomic(heldFile(name, in.ClientMessageID), b)
	}
	if err != nil {
		// The message still runs from memory; only a restart before it starts would lose it.
		log.Printf("held peer message: %s: write %s: %v", name, in.ClientMessageID, err)
		return false
	}
	return true
}

// claimHeld removes id's file and reports whether this caller removed it. The file is the
// token for a held input: Commit and the drops each claim it before acting, so an input that
// a drop has reported as not run cannot also be handed to the runtime by a queue that adopted
// it meanwhile (a Resume racing an archive), and one already handed over is not reported.
//
// Only a removal that succeeded is a claim. One that fails for another reason (EACCES, EIO)
// leaves the token in place, and treating it as a claim would let both sides act on it: the
// input then neither runs nor is reported, and stays on disk for the next start to deliver.
func claimHeld(name, id string) bool {
	if !heldName(name) || id == "" {
		return true
	}
	err := os.Remove(heldFile(name, id))
	if err != nil && !os.IsNotExist(err) {
		log.Printf("held peer message: %s: remove %s: %v (left for the next start)", name, id, err)
	}
	return err == nil
}

// heldOriginField is the origin as stored: empty for a peer message, the spelling every
// earlier Agent's files have.
func heldOriginField(kind string) string {
	if kind == OriginPeer {
		return ""
	}
	return kind
}

func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func releaseHeld(name, id string) {
	if !heldName(name) || id == "" {
		return
	}
	if err := os.Remove(heldFile(name, id)); err != nil && !os.IsNotExist(err) {
		log.Printf("held peer message: %s: remove %s: %v", name, id, err)
	}
}

// HeldWaiting reports whether input id of session name is still held: queued and not yet
// handed to the runtime, in this process's queue or on disk for the next start. The report
// reconciler reads it so an instruction that has not started is not reported as done.
func HeldWaiting(name, id string) bool { return id != "" && heldExists(name, id) }

// HeldInstrs is the set of instruction rows (TurnInput.Instr) whose input session name still
// holds. The report reconciler keeps those rows pending.
func HeldInstrs(name string) map[string]bool {
	out := map[string]bool{}
	for _, hp := range loadHeld(name) {
		if hp.Instr != "" {
			out[hp.Instr] = true
		}
	}
	return out
}

func heldExists(name, id string) bool {
	if !heldName(name) {
		return false
	}
	_, err := os.Stat(heldFile(name, id))
	return err == nil
}

// loadHeld returns name's held messages, oldest first. A file that does not decode is removed:
// it would otherwise be retried on every start and never deliver.
func loadHeld(name string) []heldPeer { return loadSpool(heldSubdir, name) }

func loadSpool(sub, name string) []heldPeer {
	if !heldName(name) {
		return nil
	}
	dir := spoolDir(sub, name)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []heldPeer
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var hp heldPeer
		if json.Unmarshal(b, &hp) != nil || hp.ID == "" {
			log.Printf("%s message: %s: dropping unreadable %s", sub, name, n)
			os.Remove(filepath.Join(dir, n))
			continue
		}
		out = append(out, hp)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].QueuedAt.Equal(out[j].QueuedAt) {
			return out[i].QueuedAt.Before(out[j].QueuedAt)
		}
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// HeldCount is the number of inputs held for session name.
func HeldCount(name string) int { return len(loadHeld(name)) }

// DropHeld discards session name's held inputs (archive, trash, a switch to Terminal), one of
// the Drop* reasons. A peer message's drop is logged; an operator or scheduled prompt's is
// reported (OnHeldDropped).
func DropHeld(name, reason string) {
	if !heldName(name) {
		return
	}
	for _, hp := range loadHeld(name) {
		if !claimHeld(name, hp.ID) {
			continue // handed to the runtime meanwhile: it runs, and is not reported
		}
		if hp.origin() == OriginPeer {
			log.Printf("held peer message: %s: dropped %s from %s queued %s (%s)",
				name, hp.ID, hp.From, hp.QueuedAt.Format(time.RFC3339), reason)
		}
		reportDropped(heldDropOf(name, hp, reason))
	}
	// Not RemoveAll: a file written after the listing above is an input a live queue accepted
	// meanwhile, which nobody has claimed or reported. Removing it would make its Commit read
	// the missing token as "dropped and reported" and lose it silently. It stays, and runs or
	// is dropped by whoever claims it. Stale temp files go; the directory goes once empty.
	removeStaleTmp(heldDir(name))
	if err := os.Remove(heldDir(name)); err != nil && !os.IsNotExist(err) {
		log.Printf("held peer message: %s: directory kept: %v", name, err)
	}
}

// removeStaleTmp removes the temp files a crashed write left in dir, sparing young ones that may
// be a write in flight.
func removeStaleTmp(dir string) {
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

// DropHeldOrigin discards session name's held inputs of origin kind only, reporting each.
// The operator's stop_session uses it for its own prompts: it withdrew those instructions, so
// a later start must not run them, while peer messages and scheduled prompts stay.
func DropHeldOrigin(name, kind, reason string) {
	if !heldName(name) {
		return
	}
	for _, hp := range loadHeld(name) {
		if hp.origin() != kind || !claimHeld(name, hp.ID) {
			continue
		}
		reportDropped(heldDropOf(name, hp, reason))
	}
}

func heldDropOf(name string, hp heldPeer, reason string) HeldDrop {
	return HeldDrop{Session: name, ID: hp.ID, Instr: hp.Instr, Origin: hp.origin(), Schedule: hp.Schedule,
		QueuedAt: hp.QueuedAt, Reason: reason}
}

// DeliverHeld sends session name's held inputs to h, oldest first. Every Managed
// driver's Resume calls it once the handle is live, so they become the first turns of the next
// start and run ahead of whatever the caller of Resume sends next.
//
// Calling it on a handle that already holds them is harmless: the queue drops an entry it
// holds, and one whose file is gone (it was handed to the runtime meanwhile).
//
// A failed Send never writes a file back: only the queue, under the handle lock, knows whether
// the entry was refused before it was queued or was committed (it may have reached the runtime,
// or another Resume may have delivered it). So the file decides what follows. Still there: the
// send was refused before the queue took it (a question pending, the runtime gone), and the
// rest stay on disk; the next input the queue accepts adopts them ahead of itself
// (TurnQueue.adoptHeld), so they are not overtaken even if the guard lifts in between. Gone: the start failed after its commit, and is reported as a failed
// turn like any failed start; the rest carry on, ahead of the caller's input.
func DeliverHeld(name string, h ThreadHandle) {
	for _, hp := range loadHeld(name) {
		if err := h.Send(restoredInput(hp)); err != nil {
			if heldExists(name, hp.ID) {
				log.Printf("held peer message: %s: deliver %s: %v (kept for the next start)", name, hp.ID, err)
				return
			}
			log.Printf("held peer message: %s: deliver %s: %v (its start failed)", name, hp.ID, err)
		}
	}
}

// restoredInput is the TurnInput a held message is sent again as. The prompt is the one
// accepted, so an operator prompt keeps its self-report line and a peer message its envelope;
// only the queue time is added.
func restoredInput(hp heldPeer) TurnInput {
	origin := hp.origin()
	prompt := MarkHeldEnvelope(hp.Prompt, hp.QueuedAt)
	if origin != OriginPeer {
		prompt = MarkHeldInstruction(hp.Prompt, hp.QueuedAt)
	}
	return TurnInput{
		Prompt:          prompt,
		Attachments:     hp.Attachments,
		ClientMessageID: hp.ID,
		Origin:          Origin{Kind: origin, From: hp.From},
		Schedule:        hp.Schedule,
		Instr:           hp.Instr,
		queuedAt:        hp.QueuedAt,
		restored:        true,
		onDisk:          true,
	}
}

// heldMarkHead opens the line MarkHeldInstruction appends.
const heldMarkHead = "\n\n[agent-fleet:held queued="

// MarkHeldInstruction appends `[agent-fleet:held queued=<time>]` to an operator or scheduled
// prompt delivered after a restart, so the agent can judge an instruction that waited across a
// stop. It goes at the end: the prompt has no envelope to carry it, and the mirror matches the
// turn to its recorded origin by the text before the mark (StripHeldMark).
func MarkHeldInstruction(prompt string, at time.Time) string {
	if at.IsZero() || strings.Contains(prompt, heldMarkHead) {
		return prompt
	}
	return prompt + heldMarkHead + at.Local().Format(time.RFC3339) + "]"
}

// StripHeldMark returns text without the mark MarkHeldInstruction appended, and whether there
// was one.
func StripHeldMark(text string) (string, bool) {
	i := strings.LastIndex(text, strings.TrimLeft(heldMarkHead, "\n"))
	if i < 0 || !strings.HasSuffix(text, "]") || strings.Contains(text[i:], "\n") {
		return text, false
	}
	return strings.TrimRight(text[:i], "\n"), true
}

// MarkHeldEnvelope adds queued=<time> to a peer envelope (`[agent-fleet:peer from=… reply=…]`)
// so the receiving agent can judge a message delivered long after it was sent. A prompt that
// already carries the mark, or has no envelope, is returned as it is. The Console reads the
// envelope with a pattern that allows further attributes.
func MarkHeldEnvelope(prompt string, at time.Time) string {
	const head = "[agent-fleet:peer "
	if !strings.HasPrefix(prompt, head) || at.IsZero() {
		return prompt
	}
	end := strings.IndexByte(prompt, ']')
	if end < 0 || strings.Contains(prompt[:end], " queued=") {
		return prompt
	}
	return prompt[:end] + " queued=" + at.Local().Format(time.RFC3339) + prompt[end:]
}

// heldTmpGrace is how old a temp file must be before SweepHeld removes it: a younger one may be
// a write still in flight.
const heldTmpGrace = time.Minute

// SweepHeld removes held inputs nobody will deliver, at Agent boot: a crash can land between
// the trash removing a session's meta and DropHeld, and a send racing an archive can write after
// it. Boot reconciliation walks metas only, so without this such a directory stays forever. It
// drops the directories of sessions with no meta, archived ones and Terminal ones (only a Managed
// start delivers), and temp files a crash left behind. A directory whose name is not a session
// name is left alone: nothing here wrote it.
func SweepHeld() {
	root := filepath.Join(heldBase(), heldSubdir)
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !heldName(name) {
			continue
		}
		m, ok := session.ReadMeta(name)
		switch {
		case !ok:
			DropHeld(name, DropGone)
			continue
		case m.Archived:
			DropHeld(name, DropArchived)
			continue
		case m.DriverKind() != session.DriverManaged:
			DropHeld(name, DropTerminal)
			continue
		}
		removeStaleTmp(filepath.Join(root, name))
	}
}
