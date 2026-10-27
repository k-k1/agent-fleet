package status

import "time"

// RestoreNotes records, per session, each time the Agent put a deleted conversation log
// back from its own backup (claude.RestoreTranscript). The mirror shows one note per entry
// at the moment it happened, so it must outlive polls and Agent restarts: a restore leaves a
// log on disk, and the next resume finds nothing lost and has nothing to say.
type RestoreNotes struct {
	// At holds one RFC3339 stamp per restore, oldest first, capped at restoreNotesKept.
	At []string `json:"at"`
}

// restoreNotesKept bounds the file: a session that loses its log over and over should not
// grow an unbounded list, and the oldest notes are the ones scrolled out of sight anyway.
const restoreNotesKept = 20

// AddRestoreNote appends one restore, stamped now. Call it once per restore that succeeded.
func AddRestoreNote(sid string) error {
	n, _ := restoreNoteFiles.Read(sid)
	n.At = append(n.At, time.Now().Format(time.RFC3339))
	if len(n.At) > restoreNotesKept {
		n.At = n.At[len(n.At)-restoreNotesKept:]
	}
	return restoreNoteFiles.Write(sid, n)
}

// ReadRestoreNotes returns the stamps recorded for sid, oldest first; nil when none.
func ReadRestoreNotes(sid string) []string {
	n, _ := restoreNoteFiles.Read(sid)
	return n.At
}

func RemoveRestoreNotes(sid string) { restoreNoteFiles.Remove(sid) }
