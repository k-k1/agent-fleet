package main

// Cleanup operations that bundle-then-remove (docs/log/32): the destructive tidy-up that
// reclaims disk. Each writes a recoverable gz archive (cleanup_archive.go) BEFORE it
// removes anything, so a mistaken cleanup can be restored. Routes:
//   DELETE /sessions/{name}[?stop=1]    → delete_session (trash: archive, then forget meta + delete jsonl)
//   DELETE /repos/{name}/branches/{b}   → delete_branch  (merged only)
//   GET/POST/DELETE /cleanup/archives*  → list / restore / purge the safety net

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// nowUTC is the clock for archive ids; a var so tests can pin it.
var nowUTC = func() time.Time { return time.Now().UTC() }

// idSlug derives a short filesystem-safe slug from a name for the archive id.
func idSlug(name string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	if len(s) > 24 {
		s = s[:24]
	}
	if s == "" {
		s = "item"
	}
	return s
}

// handleDeleteSession (DELETE /sessions/{name}) moves a session to the trash (ADR 0101
// decision 1): its meta and transcript jsonl are bundled into a cleanup archive, then removed,
// so the delete is recoverable. A running session is refused unless ?stop=1 asks to stop it
// first. ?reclaim=1 used to choose between this and a delete that skipped the trash; there is
// no such delete any more, so the parameter is accepted and means nothing.
func handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	stop := r.URL.Query().Get("stop") == "1" || r.URL.Query().Get("stop") == "true"
	arch, code, err := trashSession(m, stop)
	if err != nil {
		sessionx.WriteTrashErr(w, code, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": name, "archive": arch})
}

// trashSession is the ONE way a session's meta is forgotten (ADR 0101 decision 1). Every
// route that deletes a session comes here: DELETE /sessions/{name}, the old /stop, and the
// shell / ssm sessions of a deleted working copy. The order is the safety:
//
//  1. refuse a locked session (docs/log/45), and a running one unless stop asks to halt it;
//  2. settle the usage ledger up to the final turn (fold-on-delete, docs/log/46 §3-b);
//  3. write the gz archive — if that fails, NOTHING is removed;
//  4. only then remove the transcript, the meta (with its lineage row, ADR 0096 decision 6),
//     the side files, the terminal history and the runtime records.
//
// It never touches the working copy (ADR 0101 decision 3). The managed kinds' ClientMessageID
// ledger stays: a session in the trash can be restored and resumed, so purging the archive is
// what drops it (purgeCleanupArchive). The terminal history does not go into the archive: it
// is short-lived on purpose and its retention is the tenant's setting, which a trash with no
// expiry would override.
func trashSession(m session.Meta, stop bool) (string, string, error) {
	// One delete of a name at a time: two in the same second used to race each other's archive
	// (the loser saw the meta gone and purged what it took for its own copy — the only one).
	unlock := lockTrashName(m.Name)
	defer unlock()
	// Re-read under that lock: the caller's meta may be from before a delete that just finished.
	cur, ok := session.ReadMeta(m.Name)
	if !ok {
		return "", "not_found", errors.New("no such session: " + m.Name)
	}
	m = cur
	if m.Locked {
		return "", errCodeLocked, errors.New("session is locked against deletion; unlock it first")
	}
	if sessionx.SessionAlive(m) {
		if !stop {
			return "", sessionx.TrashErrRunning, errors.New("session is running; stop it before deleting")
		}
		halted, err := sessionx.HaltSession(m)
		if err != nil {
			return "", sessionx.TrashErrStop, err
		}
		// halt re-merges the on-disk lock, so a lock flipped meanwhile is seen here.
		if halted.Locked {
			return "", errCodeLocked, errors.New("session is locked against deletion; unlock it first")
		}
		m = halted
	}
	// Called after the halt so the final events written on exit are in the transcript first.
	finalizeSessionUsage(m)
	// What the archive is about to hold. Taken BEFORE reading, so the archive holds at least
	// this much; anything beyond it at removal time was written after, and is not in the gz.
	before := transcriptSizes(m)
	arch, err := archiveSessionForDelete(m)
	if err != nil {
		return "", sessionx.TrashErrArchive, err
	}
	if trashAfterArchive != nil {
		trashAfterArchive(m)
	}
	// Checked again at the point of removal, under the meta lock: archiving a large transcript
	// takes a while, and in that time the session may have been locked, resumed (it is running
	// again, or its transcript grew), or deleted by something else. In each case nothing is
	// removed, and the archive just written goes — it would be a stale duplicate.
	abort, code, why := false, "", ""
	sessionx.WithSessionMetaLock(func() {
		cur, ok := session.ReadMeta(m.Name)
		switch {
		case !ok:
			abort, code, why = true, "not_found", "the session was deleted by another request meanwhile"
		case cur.Locked:
			abort, code, why = true, errCodeLocked, "session is locked against deletion; unlock it first"
		case sessionx.SessionAlive(cur) || transcriptGrew(m, before):
			abort, code, why = true, sessionx.TrashErrResumed, "the session was resumed while it was being moved to the trash; nothing was deleted"
		default:
			session.RemoveMetaAndLineage(m.Name) // a person's delete (ADR 0096 decision 6)
		}
	})
	if abort {
		sessionx.WithCleanupLock(func() { _, _ = purgeCleanupArchive(arch) })
		return "", code, errors.New(why)
	}
	// Now that the conversation is safely bundled and the meta is gone, delete the live jsonl(s).
	if m.Kind == session.KindClaude {
		if _, _, matched := claude.TranscriptRead(session.UUID(m.Dir, m.Name)); len(matched) > 0 {
			for _, p := range matched {
				_ = os.Remove(p)
			}
		}
	}
	sessionx.ForgetRuntime(m)
	removeSessionSideFiles(m.Name)
	removeTerminalHistory(m.Name)
	invalidateCleanupUsage() // the trash just grew
	return arch, "", nil
}

// trashAfterArchive is a test seam: it runs between writing the archive and the final check,
// where a resume or a lock can land. Nil in production.
var trashAfterArchive func(m session.Meta)

// trashNameLocks serialises trashSession per session name.
var trashNameLocks = struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

func lockTrashName(name string) func() {
	trashNameLocks.mu.Lock()
	l, ok := trashNameLocks.m[name]
	if !ok {
		l = &sync.Mutex{}
		trashNameLocks.m[name] = l
	}
	trashNameLocks.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// transcriptSizes is the size of each transcript file of a claude session (other kinds keep
// their conversation in their own store, which the trash does not take).
func transcriptSizes(m session.Meta) map[string]int64 {
	out := map[string]int64{}
	if m.Kind != session.KindClaude {
		return out
	}
	_, _, matched := claude.TranscriptRead(session.UUID(m.Dir, m.Name))
	for _, p := range matched {
		if fi, err := os.Stat(p); err == nil {
			out[p] = fi.Size()
		}
	}
	return out
}

// transcriptGrew reports whether a transcript file appeared or grew since before was taken —
// i.e. whether the conversation has lines the archive does not.
func transcriptGrew(m session.Meta, before map[string]int64) bool {
	for p, size := range transcriptSizes(m) {
		if was, ok := before[p]; !ok || size > was {
			return true
		}
	}
	return false
}

// trashStoppedSession is trashSession for a session that must already be stopped (gitx's
// TrashSession: the shell / ssm sessions of a deleted working copy). A named function rather
// than a closure so git_wiring_test can check the wiring by identity.
func trashStoppedSession(m session.Meta) error {
	_, _, err := trashSession(m, false)
	return err
}

// removeSessionSideFiles drops the per-session side files keyed by session NAME —
// handoff proposals (session-handoffs/), transcript marks (session-marks/) and the cached
// answer translations (session-translations/). They are annotations about a conversation
// that is being deleted, not part of it, so they do not go into the archive and a restore
// does not bring them back. (Names are never reused — they are random slugs — so the files
// would not resurface on another session; they would only sit on disk for ever.)
func removeSessionSideFiles(name string) {
	sessionx.RemoveHandoffProposals(name)
	sessionx.RemoveSessionMarks(name)
	removeSessionTranslations(name)
}

// archiveSessionForDelete bundles a session's meta + jsonl(s) into a cleanup archive
// and returns the archive id. Non-claude transcripts live in the agent's native store
// (codex rollout / opencode sqlite), which we do not reclaim here — the meta is still
// archived so restore brings the row back.
func archiveSessionForDelete(m session.Meta) (string, error) {
	metaJSON := marshalMeta(m)
	payloads := map[string][]byte{}
	as := cleanupArchivedSession{Name: m.Name, Display: session.Display(m), Kind: m.Kind, Meta: metaJSON}
	if m.Kind == session.KindClaude {
		if _, _, matched := claude.TranscriptRead(session.UUID(m.Dir, m.Name)); len(matched) > 0 {
			for i, p := range matched {
				b, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				entry := "sessions/" + idSlug(m.Name) + "/" + jsonlEntryName(i, p)
				payloads[entry] = b
				as.JSONLPaths = append(as.JSONLPaths, p)
				as.JSONLNames = append(as.JSONLNames, entry)
			}
		}
	}
	man := cleanupManifest{
		ID: newCleanupID(nowUTC(), idSlug(m.Name)), At: nowUTC().Format(time.RFC3339),
		Reason: "delete_session", Sessions: []cleanupArchivedSession{as},
	}
	if err := writeCleanupArchive(&man, payloads); err != nil {
		return "", err
	}
	return man.ID, nil
}

// handleDeleteBranch (DELETE /repos/{name}/branch?branch=<name>[&remote=1]) deletes a
// MERGED local branch, recording its name+SHA in a cleanup archive first. Unmerged
// branches are refused (git branch -d fails; we never -D) — the commits would be
// orphaned. The branch is a query param, not a path segment, because branch names
// contain "/".
//
// remote=1 additionally removes the branch from origin, and it needs a STRICTER merged
// test of its own, run before anything is deleted:
//
//	`git branch -d` accepts a branch that is merged into its UPSTREAM, not only one merged
//	into HEAD — measured: a pushed, never-merged temp branch deletes locally without a
//	murmur. For a local-only delete that is fine (the commits are still on origin, which is
//	why git allows it), but combined with a push --delete it is precisely how the commits
//	get orphaned — the safety net one step relies on is the thing the next step removes.
//
// So remote=1 additionally requires the branch to be an ancestor of this working copy's
// HEAD, and refuses the whole request otherwise: both sides survive a refusal. The push
// then runs last, because it is the one step nothing here can undo — the gz archive can
// re-create a local ref from its SHA, no archive can re-create a ref on someone else's
// server. A push that fails leaves the local delete standing and is reported in the body
// rather than as an error status.
func handleDeleteBranch(w http.ResponseWriter, r *http.Request) {
	dir, ok := gitx.RepoDirFromPath(w, r)
	if !ok {
		return
	}
	branch := r.URL.Query().Get("branch")
	if strings.TrimSpace(branch) == "" || strings.Contains(branch, "..") {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_branch", "invalid branch name")
		return
	}
	if !gitx.GitBranchExists(dir, branch) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such branch: "+branch)
		return
	}
	wantRemote := r.URL.Query().Get("remote") == "1" || r.URL.Query().Get("remote") == "true"
	if wantRemote && !gitx.OK(dir, "merge-base", "--is-ancestor", branch, "HEAD") {
		httpx.WriteErr(w, http.StatusConflict, "branch_not_in_head",
			"branch is not contained in this working copy's HEAD; deleting it on origin too would orphan its commits")
		return
	}
	sha := gitx.GitBranchSHA(dir, branch)
	man := cleanupManifest{
		ID: newCleanupID(nowUTC(), idSlug(branch)), At: nowUTC().Format(time.RFC3339),
		Reason:   "delete_branch",
		Branches: []cleanupArchivedBranch{{Repo: r.PathValue("name"), Name: branch, SHA: sha}},
	}
	if err := writeCleanupArchive(&man, nil); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "archive_failed", err.Error())
		return
	}
	// -d (not -D): git refuses if the branch isn't merged, so unmerged work is protected.
	if out, err := gitx.Combined(dir, "branch", "-d", branch); err != nil {
		_, _ = purgeCleanupArchive(man.ID) // nothing was deleted — don't leave a stale archive
		httpx.WriteErr(w, http.StatusConflict, "branch_unmerged",
			"branch is not fully merged; not deleted (push/merge it, or delete in the Console): "+strings.TrimSpace(out))
		return
	}
	// remote / remote_error are always on the wire ("" = the flag was not passed), and the
	// payload stays ONE map literal at the write site: wiremap_golden_test.go goldens the key
	// set by parsing this literal, and a map built up over several statements leaves the
	// response with no coverage at all.
	invalidateCleanupUsage() // the trash just grew
	remote, remoteErr := "", ""
	if wantRemote {
		state, err := deleteRemoteBranch(dir, branch)
		remote = state
		if err != nil {
			remote, remoteErr = "failed", err.Error()
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"deleted": branch, "archive": man.ID, "remote": remote, "remote_error": remoteErr,
	})
}

// remotePushTimeout bounds the one network round trip in this package. GIT_TERMINAL_PROMPT=0
// only stops git asking for a password — an unreachable or slow host still hangs, and this
// handler is called in a loop while a modal waits on it.
const remotePushTimeout = 60 * time.Second

// deleteRemoteBranch removes branch from origin and reports WHICH of the three ordinary
// outcomes happened, because the caller has to tell them apart in what it shows:
//
//	"deleted"  — the remote ref was there and is gone
//	"absent"   — nothing to delete: no origin, or the branch was never pushed. A worktree
//	             branch that stayed local is the common case, and calling that a failure
//	             would put an error in front of the user on the ordinary path.
//	error      — git's own message, verbatim; the credential helper's refusal reads here.
func deleteRemoteBranch(dir, branch string) (string, error) {
	if url, err := gitx.Run(dir, "remote", "get-url", "origin"); err != nil || url == "" {
		return "absent", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), remotePushTimeout)
	defer cancel()
	raw, err := gitx.CmdContext(ctx, dir, "push", "origin", "--delete", branch).CombinedOutput()
	msg := strings.TrimSpace(string(raw))
	if err == nil {
		return "deleted", nil
	}
	if strings.Contains(msg, "remote ref does not exist") {
		return "absent", nil
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("origin did not answer within %s", remotePushTimeout)
	}
	if msg == "" {
		return "", err
	}
	return "", fmt.Errorf("%s", msg)
}

func handleListCleanupArchives(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"archives": listCleanupArchives()})
}

func handleRestoreCleanupArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// restoreCleanupArchive takes the cleanup lock itself, for the meta hand-over only.
	restored, err := restoreCleanupArchive(id)
	if errors.Is(err, errRestoreStopped) {
		// Not "no such archive": it exists, part of it may be back, and restoring again is
		// what finishes it — the Console says so.
		invalidateCleanupUsage()
		httpx.WriteErr(w, http.StatusConflict, "restore_incomplete", err.Error())
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "restore_failed", err.Error())
		return
	}
	invalidateCleanupUsage() // restored sessions take their cache off the orphan count
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"restored": restored})
}

// handlePurgeOldCleanupArchives (DELETE /cleanup/archives?older_than_days=N) purges every
// archive written more than N days ago — the person-pressed "delete permanently: older ones"
// of the trash tab (ADR 0101 decision 6). There is no automatic expiry (ADR 0097 decisions 2
// and 3); this only saves picking hundreds of rows one by one. Each purge takes the cleanup
// lock on its own, so a long run does not stall the survey or a restore. An archive a restore
// left half done is kept, exactly as the single purge refuses it, and counted as kept.
func handlePurgeOldCleanupArchives(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.URL.Query().Get("older_than_days"))
	if err != nil || days < 1 {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "older_than_days must be a whole number of days, 1 or more")
		return
	}
	cutoff := nowUTC().Add(-time.Duration(days) * 24 * time.Hour)
	purged, kept := 0, 0
	var bytes int64
	var gone []cleanupManifest
	for _, m := range listCleanupArchives() {
		at, err := time.Parse(time.RFC3339, m.At)
		if err != nil {
			kept++ // no readable date: it cannot be said to be old, so it stays, and says so
			continue
		}
		if !at.Before(cutoff) {
			continue
		}
		var perr error
		var man cleanupManifest
		sessionx.WithCleanupLock(func() { man, perr = purgeCleanupArchive(m.ID) })
		if perr != nil {
			kept++
			continue
		}
		gone = append(gone, man)
		purged++
		bytes += m.Bytes
	}
	dropPurgedLedgers(gone)
	invalidateCleanupUsage()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"purged": purged, "bytes": bytes, "kept": kept})
}

func handlePurgeCleanupArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var err error
	var man cleanupManifest
	sessionx.WithCleanupLock(func() { man, err = purgeCleanupArchive(id) })
	if errors.Is(err, errRestoreIncomplete) {
		httpx.WriteErr(w, http.StatusConflict, "restore_incomplete", err.Error())
		return
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "purge_failed", err.Error())
		return
	}
	dropPurgedLedgers([]cleanupManifest{man})
	invalidateCleanupUsage()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"purged": id})
}

// jsonlEntryName builds a stable tar entry basename for the i-th jsonl of a session.
func jsonlEntryName(i int, path string) string {
	base := path
	if idx := strings.LastIndexByte(base, '/'); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSuffix(base, ".jsonl")
	if base == "" {
		base = "transcript"
	}
	return fmt.Sprintf("%02d-%s.jsonl", i, idSlug(base))
}

// marshalMeta serializes a session.Meta for the archive (restore replays it).
func marshalMeta(m session.Meta) string {
	b, _ := json.Marshal(m)
	return string(b)
}
