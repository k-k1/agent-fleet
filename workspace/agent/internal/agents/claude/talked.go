package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// ErrTranscriptLost is BuildLaunch's refusal to resume a slot that held a conversation whose
// jsonl is gone (claude's own retention sweep, a hand delete). Launching anyway would run
// `claude --session-id <sid>` — a blank conversation under the old session, which reads as the
// old one losing its memory with no word said. The user starts fresh explicitly
// (StartFresh, then relaunch) instead.
var ErrTranscriptLost = errors.New("the conversation log (jsonl) was deleted, so it cannot be resumed; a new conversation can be started")

// ErrTranscriptRestore: the backup copy of a deleted log exists to be restored but restoring it
// failed on I/O. It is not evidence that there is no backup, so it is not ErrTranscriptLost.
var ErrTranscriptRestore = errors.New("could not restore the conversation log from the backup")

// talked remembers, per slot, the id of the log claude last wrote the slot's conversation
// under. Without it a missing jsonl cannot be told from "never talked" (the safe reading,
// start fresh, then silently discards a deleted conversation), and recording the id rather
// than a flag stops LiveSID's fall-back to the slot id from hiding a deleted live log behind
// an older slot log. Lives in the agent state dir, so it survives a stop.
var talked = agents.NewSidStore("claude-talked")

// NoteTalked records the log id of slot's conversation after a finished turn (Stop hook).
// Rewritten only when the id changes, so the steady-state cost is one small read per turn.
func NoteTalked(slot string) {
	if slot == "" {
		return
	}
	if id := LiveSID(slot); talked.Read(slot) != id {
		talked.Write(slot, id)
	}
}

// talkedID is the log id a slot's conversation should be resumable under. ok=false: the slot
// never talked as far as we can tell. A slot with a finished turn on record (the status
// file's TurnEnd or a completion key — both written long before the marker existed) counts
// as talked under its slot id; a slot with neither is indistinguishable from one that never
// talked and starts new.
func talkedID(slot string) (string, bool) {
	if id := talked.Read(slot); id != "" {
		return id, true
	}
	// The ledger's id is taken at face value, not through LiveSID: that falls back to the slot
	// when the live log is missing, which is exactly the deletion being looked for.
	recorded := func() string {
		if live := sids.Read(slot); live != "" {
			return live
		}
		return slot
	}
	if st, _ := status.Read(slot); st.TurnEnd {
		return recorded(), true
	}
	if _, ok := status.ReadCompletionKey(slot); ok {
		return recorded(), true
	}
	return "", false
}

// HadConversation reports whether slot is known to have held a conversation.
func HadConversation(slot string) bool { _, ok := talkedID(slot); return ok }

// StartFresh is the user's explicit choice to begin a new conversation in this slot. Every log
// still on disk under the slot id (the older half of a drifted conversation, in any project
// directory) would be resumed by the very next launch, so each is set aside as
// <name>.jsonl.superseded.<unixnano> — kept, never overwritten, outside claude's *.jsonl
// lookup. Only when all of them are out of the way are the marker and the pre-marker evidence
// dropped; on any error nothing is dropped, the conversation stays "lost", and the user can
// press the button again.
//
// The logs are enumerated with an exact-id sweep of every project directory, not
// rawJSONLPathsIn: that returns a single cwd-guessed hit or a memoized answer, and a second
// copy under another project would survive and be resumed.
//
// The whole fresh start holds the slot's backup lock, the one RestoreTranscript takes: a restore
// slipping in between the set-aside of the logs and of the backup would resume the old
// conversation on the next launch.
func StartFresh(slot string) error {
	defer lockBackup(slot)()
	return startFreshLocked(slot)
}

// StartFreshIfLost is StartFresh that re-checks, inside the lock, that the conversation is still
// lost: a restore that ran since the caller looked has made it resumable again, and a stale
// request must not discard it. It reports whether it started fresh.
func StartFreshIfLost(slot string) (bool, error) {
	defer lockBackup(slot)()
	if !lostTranscript(slot) {
		return false, nil
	}
	return true, startFreshLocked(slot)
}

func startFreshLocked(slot string) error {
	ids := map[string]bool{slot: true}
	if live := sids.Read(slot); live != "" {
		ids[live] = true
	}
	for id := range ids {
		paths, err := exactLogs(id)
		if err != nil {
			return err
		}
		for _, p := range paths {
			if err := setAside(p); err != nil {
				return err
			}
		}
		rest, err := exactLogs(id)
		if err != nil {
			return err
		}
		if len(rest) > 0 {
			return fmt.Errorf("conversation log still present: %s", rest[0])
		}
	}
	// The Agent's backup of the log (backup.go) would be restored by the next launch otherwise.
	if err := supersedeBackup(slot); err != nil {
		return err
	}
	talked.Remove(slot)
	status.Remove(slot)
	status.RemoveCompletionKey(slot)
	sids.Remove(slot)
	return nil
}

// setAside renames p to a name that does not exist yet.
func setAside(p string) error {
	for i := 0; i < 5; i++ {
		dst := fmt.Sprintf("%s.superseded.%d", p, time.Now().UnixNano())
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		return os.Rename(p, dst)
	}
	return fmt.Errorf("no free name to set aside %s", p)
}

// LostTranscript is lostTranscript for callers outside the package: a fresh start may only be
// honoured while the conversation is still lost, never to discard a log that is healthy.
func LostTranscript(slot string) bool { return lostTranscript(slot) }

// lostTranscript reports whether slot's conversation is known to have existed but its log
// (under the id it was last written under) is gone or holds nothing resumable.
func lostTranscript(slot string) bool {
	id, ok := talkedID(slot)
	return ok && !resumableAt(id, session.CWDForUUID(slot))
}

// exactLogs lists <id>.jsonl in every project directory. Unlike filepath.Glob it returns I/O
// errors: an unreadable projects tree must not read as "no logs", or the evidence gets dropped
// and a log that is merely hidden is resumed once it is readable again. A missing projects
// directory is the ordinary no-log case.
func exactLogs(id string) ([]string, error) {
	root := filepath.Join(ConfigDir(), "projects")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(root, e.Name(), id+".jsonl")
		_, err := os.Lstat(p)
		switch {
		case err == nil:
			out = append(out, p)
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, err
		}
	}
	return out, nil
}
