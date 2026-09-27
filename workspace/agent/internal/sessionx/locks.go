package sessionx

// The working-copy side of the deletion-lock registry (docs/log/45).
//
// Sessions (session.Meta.Locked) and conversations (chatConversation.Locked) have their own
// JSON to carry the flag, but a working copy (clone / worktree) has no AF-owned metadata
// file — the directory itself is the object, a marker inside it would dirty git status, and
// one under .git would go away with the worktree. So the lock lives in a small ledger next
// to them (~/.config/agent-fleet/locks.json), keyed by the working copy's absolute path.
//
// The key is the path because the automatic prune (maybePruneWorktree) knows only dir, not
// name. Entries whose working copy is gone are dropped on read: a stale one is harmless in
// itself, but a re-clone reviving the same path must not inherit a ghost lock.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// lockLedger is the on-disk shape of locks.json. Only working copies live here;
// sessions and conversations carry their own Locked flag.
type lockLedger struct {
	// Worktrees maps a working copy's absolute path → locked. Absent = unlocked.
	Worktrees map[string]bool `json:"worktrees,omitempty"`
}

// lockMu serializes the read-modify-write of the ledger (two concurrent toggles
// must not lose one another's entry).
var lockMu sync.Mutex

// sessionLockMu covers the handful of metadata updates that may race a lock
// toggle. In particular GET /sessions stamps StoppedAt as a side effect; without
// this mutex, a list request that read the old meta could write it back just
// after POST /sessions/{name}/lock and silently clear Locked again.
var sessionLockMu sync.Mutex

func lockLedgerPath() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "locks.json")
}

// readLockLedger loads the ledger, dropping entries whose directory is gone. A
// missing/corrupt file reads as an empty ledger — a lost ledger must never make
// the Agent fail, it just means nothing is locked.
func readLockLedger() lockLedger {
	var l lockLedger
	b, err := os.ReadFile(lockLedgerPath())
	if err != nil || json.Unmarshal(b, &l) != nil {
		return lockLedger{Worktrees: map[string]bool{}}
	}
	if l.Worktrees == nil {
		l.Worktrees = map[string]bool{}
	}
	for dir, locked := range l.Worktrees {
		if !locked || !session.DirExists(dir) {
			delete(l.Worktrees, dir)
		}
	}
	return l
}

func writeLockLedger(l lockLedger) error {
	if err := os.MkdirAll(filepath.Dir(lockLedgerPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(lockLedgerPath(), append(b, '\n'), 0o600)
}

// RepoLocked reports whether the working copy at dir is pinned against deletion.
func RepoLocked(dir string) bool {
	if dir == "" {
		return false
	}
	lockMu.Lock()
	defer lockMu.Unlock()
	return readLockLedger().Worktrees[AbsPath(dir)]
}

// SetRepoLock pins/unpins a working copy. Unlocking removes the entry rather than
// storing false, so the ledger stays a plain set of locked paths.
func SetRepoLock(dir string, locked bool) error {
	lockMu.Lock()
	defer lockMu.Unlock()
	l := readLockLedger()
	p := AbsPath(dir)
	if locked {
		l.Worktrees[p] = true
	} else {
		delete(l.Worktrees, p)
	}
	return writeLockLedger(l)
}

// LockedRepoDirs returns the locked working copies as a set, for callers that
// annotate a whole list (GET /repos) without one ledger read per row.
func LockedRepoDirs() map[string]bool {
	lockMu.Lock()
	defer lockMu.Unlock()
	out := map[string]bool{}
	for dir := range readLockLedger().Worktrees {
		out[dir] = true
	}
	return out
}

// --- handlers ---

// lockReq is the body every lock toggle takes: {"locked": true|false}.
type lockReq struct {
	Locked bool `json:"locked"`
}

// HandleSessionLock (POST /sessions/{name}/lock) pins/unpins a session against
// deletion. Locking a live session is fine — the lock only refuses removal, not
// stopping it (/halt) nor archiving it (reversible).
func HandleSessionLock(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req lockReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// Not inside a working-copy delete: see WithDeletionGate.
	deletionGate.Lock()
	defer deletionGate.Unlock()
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m.Locked = req.Locked
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": name, "locked": m.Locked})
}

// UpdateSessionMeta is a read-modify-write of one meta under the same lock: fn edits the meta
// as it is on disk NOW, and returns false to write nothing. Reports whether it wrote (false also
// when the meta is gone — deleted, so there is nothing to write back).
//
// Every write that starts from a meta read earlier goes through here, and fn sets only the
// fields its caller owns (issue #950). Writing the earlier snapshot back instead — even with a
// few fields re-merged from disk, which is what WriteSessionMetaKeepingLock used to do — rolls
// back whatever another writer changed in between: a deletion lock set meanwhile (and the next
// delete goes through), an archive, a rename, a keep-awake pin, a stop-after-turn arm. The
// handlers that hold a snapshot are the slow ones (a kill, a relaunch, a driver RPC), so the
// window is seconds, and the list polls every few. session.WriteMeta itself is left to the
// first write of a new name; meta_write_sites_test.go freezes that.
func UpdateSessionMeta(name string, fn func(m *session.Meta) bool) (session.Meta, bool) {
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	m, ok := session.ReadMeta(name)
	if !ok || !fn(&m) {
		return m, false
	}
	session.WriteMeta(m)
	return m, true
}

// deletionGate serialises a working-copy delete (gitx.HandleDeleteRepo, from its guards to
// settling its sessions) against the lock endpoints. Without it a lock set while `git worktree
// remove` ran was answered "locked" and the folder was deleted anyway (ADR 0101). Order: the
// gate is always taken first, never while holding sessionLockMu or lockMu.
var deletionGate sync.Mutex

// WithDeletionGate runs fn inside the deletion gate (gitx takes it through its deps).
func WithDeletionGate(fn func()) {
	deletionGate.Lock()
	defer deletionGate.Unlock()
	fn()
}

// WithSessionMetaLock runs fn holding the lock every meta read-modify-write above takes. The
// trash forgets a meta inside it, after re-reading the lock, so a lock set while the archive
// was being written is honoured and no snapshot write can land between the check and the
// removal.
func WithSessionMetaLock(fn func()) {
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	fn()
}

// keepAwakeMaxHours caps how far one pin can extend. Extending is just pressing again, and
// the cap makes "pinned forever, billed forever" structurally impossible (the same reason as
// principle 3 in docs/log/75 §75.5).
const keepAwakeMaxHours = 24

// HandleSessionKeepAwake (POST /sessions/{name}/keep-awake) pins a session against the
// idle-stop reaper for a bounded window. Body: {"hours": 4} — 0 or less clears the pin.
//
// This is the escape hatch for shell / ssm (docs/log/75): af has no way to tell a running
// job apart from an idle shell, so instead of guarding the container on a guess, the user
// declares it. It works the same for a claude session on a long unattended run.
func HandleSessionKeepAwake(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		Hours float64 `json:"hours"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if req.Hours <= 0 {
		m.KeepAwakeUntil = ""
	} else {
		h := req.Hours
		if h > keepAwakeMaxHours {
			h = keepAwakeMaxHours
		}
		m.KeepAwakeUntil = time.Now().Add(time.Duration(h * float64(time.Hour))).Format(time.RFC3339)
	}
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": name, "keepAwakeUntil": m.KeepAwakeUntil})
}

// HandleRepoLock (POST /repos/{name}/lock) pins/unpins a working copy (a clone or
// a linked worktree) against deletion — including the automatic prune that drops a
// clean worktree once its last session goes away.
func HandleRepoLock(w http.ResponseWriter, r *http.Request) {
	dir, ok := gitx.RepoAnyDirFromPath(w, r)
	if !ok {
		return
	}
	var req lockReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	deletionGate.Lock() // not inside a working-copy delete: see WithDeletionGate
	defer deletionGate.Unlock()
	if err := SetRepoLock(dir, req.Locked); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "lock_save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": r.PathValue("name"), "locked": req.Locked})
}

// HandleChatLock (POST /chat/conversations/{id}/lock) pins/unpins an assistant
// conversation against deletion.
func HandleChatLock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	var req lockReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	unlock := chatx.LockConv(id) // serialize with an in-flight turn's load-modify-save
	defer unlock()
	c, err := chatx.LoadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	c.Locked = req.Locked
	if err := chatx.SaveConv(c); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "chat_save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "locked": c.Locked})
}

// AbsPath normalizes a working-copy path so the ledger key matches regardless of
// how the caller spelled it (relative path, trailing slash, symlinked ~).
func AbsPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// settleInitialPrompt records how the create's initial prompt ended (ADR 0100 decision 2).
// It moves the state only out of pending: the same delivery code also serves prompts that are
// not a create's initial one (a carried interaction, a stopped session's first input), and
// those must not invent a state for a session that never had one.
func settleInitialPrompt(name, state string) {
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	m, ok := session.ReadMeta(name)
	if !ok || m.InitialPromptState != session.InitialPromptPending {
		return
	}
	m.InitialPromptState = state
	session.WriteMeta(m)
}

// RecoverPendingInitialPrompts turns every pending initial prompt into unknown. Called once at
// Agent start: the delivery goroutine died with the previous process, the meta did not, and a
// state nothing will ever settle would hold the studio pane on "sending" forever. A resend
// after this may deliver the persona twice; the persona is a statement of role, and reading
// it twice does no harm, where never being able to resend does.
func RecoverPendingInitialPrompts() int {
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	n := 0
	for _, m := range session.ListMetas() {
		if m.InitialPromptState != session.InitialPromptPending {
			continue
		}
		m.InitialPromptState = session.InitialPromptUnknown
		session.WriteMeta(m)
		n++
	}
	return n
}
