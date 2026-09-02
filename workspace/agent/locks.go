package main

// 削除ロック（docs/log/45）の作業コピー側レジストリ。
//
// セッション（session.Meta.Locked）と会話（chatConversation.Locked）は自分の JSON を
// 持っているのでそこにフラグを置けるが、作業コピー（clone / worktree）は AF が所有する
// メタファイルを持たない — ディレクトリそのものが実体で、中に印を書けば git status を
// 汚し、.git に置けば worktree 削除で一緒に消える。そこでロックは外の小さな台帳
// （~/.config/agent-fleet/locks.json）に、作業コピーの絶対パスをキーとして持つ。
//
// パスをキーにするのは、自動 prune（maybePruneWorktree）が name ではなく dir しか
// 知らないため。削除された作業コピーのエントリは読み出し時に掃除する（stale が
// 残っても実害はないが、再 clone で同名パスが復活したときに幽霊ロックが効かないように）。

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

// repoLocked reports whether the working copy at dir is pinned against deletion.
func repoLocked(dir string) bool {
	if dir == "" {
		return false
	}
	lockMu.Lock()
	defer lockMu.Unlock()
	return readLockLedger().Worktrees[absPath(dir)]
}

// setRepoLock pins/unpins a working copy. Unlocking removes the entry rather than
// storing false, so the ledger stays a plain set of locked paths.
func setRepoLock(dir string, locked bool) error {
	lockMu.Lock()
	defer lockMu.Unlock()
	l := readLockLedger()
	p := absPath(dir)
	if locked {
		l.Worktrees[p] = true
	} else {
		delete(l.Worktrees, p)
	}
	return writeLockLedger(l)
}

// lockedRepoDirs returns the locked working copies as a set, for callers that
// annotate a whole list (GET /repos) without one ledger read per row.
func lockedRepoDirs() map[string]bool {
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

// handleSessionLock (POST /sessions/{name}/lock) pins/unpins a session against
// deletion. Locking a live session is fine — the lock only refuses removal, not
// stopping it into 停止中 (/halt) nor archiving it (reversible).
func handleSessionLock(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req lockReq
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
	m.Locked = req.Locked
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": name, "locked": m.Locked})
}

// writeSessionMetaKeepingLock writes a lifecycle-only update (currently the
// StoppedAt bookkeeping from GET /sessions) without allowing an older snapshot
// to overwrite the user's newer lock choice. Callers must use this rather than
// session.WriteMeta when they started from a listed meta.
//
// 停止しないピン（KeepAwakeUntil）も同じ扱い: 一覧のポーリングは秒単位で走るので、
// その最中に押されたピンを古いスナップショットが巻き戻すと、利用者から見て
// 「押したのに効いていない」になる（ロックで一度踏んだのと同じ穴）。
func writeSessionMetaKeepingLock(m session.Meta) session.Meta {
	sessionLockMu.Lock()
	defer sessionLockMu.Unlock()
	if current, ok := session.ReadMeta(m.Name); ok {
		m.Locked = current.Locked
		m.KeepAwakeUntil = current.KeepAwakeUntil
	}
	session.WriteMeta(m)
	return m
}

// keepAwakeMaxHours は 1 回のピンで延ばせる上限。延長は押し直せばよく、上限があることで
// 「押しっぱなしで永久に課金」が構造的に起きない（docs/log/75 §75.5 の原則 3 と同じ理由）。
const keepAwakeMaxHours = 24

// handleSessionKeepAwake (POST /sessions/{name}/keep-awake) pins a session against the
// idle-stop reaper for a bounded window. Body: {"hours": 4} — 0 以下で解除。
//
// これは shell / ssm のための逃げ道である（docs/log/75）: 走行中のジョブを af 側から
// 見分ける手段が無いので、推測でコンテナを守る代わりに、利用者に宣言してもらう。
// claude セッションにも同じように効く（長い自動走行を止めたくないとき）。
func handleSessionKeepAwake(w http.ResponseWriter, r *http.Request) {
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

// handleRepoLock (POST /repos/{name}/lock) pins/unpins a working copy (a clone or
// a linked worktree) against deletion — including the automatic prune that drops a
// clean worktree once its last session goes away.
func handleRepoLock(w http.ResponseWriter, r *http.Request) {
	dir, ok := gitx.RepoAnyDirFromPath(w, r)
	if !ok {
		return
	}
	var req lockReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := setRepoLock(dir, req.Locked); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "lock_save", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": r.PathValue("name"), "locked": req.Locked})
}

// handleChatLock (POST /chat/conversations/{id}/lock) pins/unpins an assistant
// conversation against deletion.
func handleChatLock(w http.ResponseWriter, r *http.Request) {
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

// absPath normalizes a working-copy path so the ledger key matches regardless of
// how the caller spelled it (relative path, trailing slash, symlinked ~).
func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		p = a
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
