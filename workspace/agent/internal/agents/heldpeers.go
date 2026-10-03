package agents

// Held peer messages (#1255): another session's message waiting in a Managed driver's queue
// survives what drops the queue — halt, the execution-method switch's stop, Agent shutdown, a
// daemon drain, and a crash — and becomes the session's first turn(s) on its next start.
//
// The queue writes a peer entry through to disk when it accepts it, not at teardown: a crash
// or an OOM kill runs no teardown, and those are the restarts nobody is told about. The file
// goes when the entry is handed to the runtime (Commit), when a stop discards it and when it is
// removed by id; teardown (DropAll) leaves it. Archive and trash drop the session's files
// (DropHeld): the issue's decision, since a message to a folded-away session has no reader.
//
// One file per message, written by temp file + rename and removed by name, so no writer ever
// reads, modifies and writes back a shared file (fstore has no lock and no rename, and a
// read-modify-write there measurably loses writes). Order is the queue time, then a
// process-wide sequence for entries queued in the same instant.
//
// Only peer messages are held. Operator and scheduled prompts are lost with the queue as
// before (#1257), and the member's own queued input is the member's to resend.

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
type heldPeer struct {
	ID          string    `json:"id"`
	Prompt      string    `json:"prompt"`
	Attachments []string  `json:"attachments,omitempty"`
	From        string    `json:"from,omitempty"`
	QueuedAt    time.Time `json:"queuedAt"`
	Seq         uint64    `json:"seq"`
}

func heldDir(name string) string { return filepath.Join(heldBase(), heldSubdir, name) }

// heldName reports whether name may be a held directory: a valid session name is one path
// segment. ReadMeta does not check the name inside a meta file, and DropHeld removes a whole
// directory, so a name such as ".." would otherwise reach the state root.
func heldName(name string) bool { return session.ValidName(name) }

// heldFile names the file after a hash of the id: the id comes off the wire and is not a safe
// file name, and the same id always maps to the same file.
func heldFile(name, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(heldDir(name), hex.EncodeToString(sum[:16])+".json")
}

func isHeldPeer(in TurnInput) bool { return in.Origin.Kind == OriginPeer }

// putHeld writes in's file. Caller holds the driver's handle lock.
func putHeld(name string, in TurnInput) {
	if !heldName(name) || in.ClientMessageID == "" {
		return
	}
	b, err := json.Marshal(heldPeer{
		ID: in.ClientMessageID, Prompt: in.Prompt, Attachments: in.Attachments,
		From: in.Origin.From, QueuedAt: in.queuedAt, Seq: heldSeq.Add(1),
	})
	if err == nil {
		err = writeFileAtomic(heldFile(name, in.ClientMessageID), b)
	}
	if err != nil {
		// The message still runs from memory; only a restart before it starts would lose it.
		log.Printf("held peer message: %s: write %s: %v", name, in.ClientMessageID, err)
	}
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

func heldExists(name, id string) bool {
	if !heldName(name) {
		return false
	}
	_, err := os.Stat(heldFile(name, id))
	return err == nil
}

// loadHeld returns name's held messages, oldest first. A file that does not decode is removed:
// it would otherwise be retried on every start and never deliver.
func loadHeld(name string) []heldPeer {
	if !heldName(name) {
		return nil
	}
	dir := heldDir(name)
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
			log.Printf("held peer message: %s: dropping unreadable %s", name, n)
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

// HeldCount is the number of peer messages held for session name.
func HeldCount(name string) int { return len(loadHeld(name)) }

// DropHeld discards session name's held peer messages (archive, trash, a switch to Terminal),
// logging what went and why: nobody else is told.
func DropHeld(name, reason string) {
	if !heldName(name) {
		return
	}
	for _, hp := range loadHeld(name) {
		log.Printf("held peer message: %s: dropped %s from %s queued %s (%s)",
			name, hp.ID, hp.From, hp.QueuedAt.Format(time.RFC3339), reason)
	}
	if err := os.RemoveAll(heldDir(name)); err != nil {
		log.Printf("held peer message: %s: remove: %v", name, err)
	}
}

// DeliverHeld sends session name's held peer messages to h, oldest first. Every Managed
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

// restoredInput is the TurnInput a held message is sent again as.
func restoredInput(hp heldPeer) TurnInput {
	return TurnInput{
		Prompt:          MarkHeldEnvelope(hp.Prompt, hp.QueuedAt),
		Attachments:     hp.Attachments,
		ClientMessageID: hp.ID,
		Origin:          Origin{Kind: OriginPeer, From: hp.From},
		queuedAt:        hp.QueuedAt,
		restored:        true,
	}
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

// SweepHeld removes held messages nobody will deliver, at Agent boot: a crash can land between
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
			DropHeld(name, "the session no longer exists")
			continue
		case m.Archived:
			DropHeld(name, "the session is archived")
			continue
		case m.DriverKind() != session.DriverManaged:
			DropHeld(name, "the session runs on Terminal (CLI)")
			continue
		}
		files, _ := os.ReadDir(filepath.Join(root, name))
		for _, f := range files {
			if !strings.HasPrefix(f.Name(), ".tmp-") {
				continue
			}
			if fi, err := f.Info(); err == nil && time.Since(fi.ModTime()) > heldTmpGrace {
				os.Remove(filepath.Join(root, name, f.Name()))
			}
		}
	}
}
