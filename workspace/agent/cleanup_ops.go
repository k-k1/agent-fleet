package main

// Cleanup operations that bundle-then-remove (docs/log/32): the destructive tidy-up that
// reclaims disk. Each writes a recoverable gz archive (cleanup_archive.go) BEFORE it
// removes anything, so a mistaken cleanup can be restored. Routes:
//   DELETE /sessions/{name}?reclaim=1   → delete_session (forget meta + delete jsonl)
//   DELETE /repos/{name}/branches/{b}   → delete_branch  (merged only)
//   GET/POST/DELETE /cleanup/archives*  → list / restore / purge the safety net

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"net/http"
	"os"
	"strings"
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

// handleDeleteSession (DELETE /sessions/{name}?reclaim=1) removes a session for good:
// its meta is forgotten AND its transcript jsonl is deleted to reclaim space — bundled
// into a cleanup archive first so it is recoverable. Refuses a LIVE session (stop it
// first). Without ?reclaim=1 it behaves like stop (forget meta, keep jsonl) for
// backward compatibility with any caller hitting this path.
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
	if m.Locked {
		httpx.WriteErr(w, http.StatusForbidden, errCodeLocked,
			"session is locked against deletion; unlock it first")
		return
	}
	if sessionx.SessionAlive(m) {
		httpx.WriteErr(w, http.StatusConflict, "session_running",
			"session is running; stop it before deleting")
		return
	}
	// fold-on-delete (docs/log/46 §3-b): commit the ledger up to the final turn before the
	// transcript disappears. Ordinary folding leaves the open turn behind, so without this the
	// last turn would never make it in.
	finalizeSessionUsage(m)
	reclaim := r.URL.Query().Get("reclaim") == "1" || r.URL.Query().Get("reclaim") == "true"
	if !reclaim {
		session.RemoveMeta(name)
		removeSessionSideFiles(name)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": name})
		return
	}
	arch, err := archiveSessionForDelete(m)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "archive_failed", err.Error())
		return
	}
	// Now that the conversation is safely bundled, delete the live jsonl(s) + meta.
	if m.Kind == session.KindClaude {
		if _, _, matched := claude.TranscriptRead(session.UUID(m.Dir, m.Name)); len(matched) > 0 {
			for _, p := range matched {
				_ = os.Remove(p)
			}
		}
	}
	session.RemoveMeta(name)
	removeSessionSideFiles(name)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": name, "archive": arch})
}

// removeSessionSideFiles drops the per-session side files keyed by session NAME —
// handoff proposals (session-handoffs/), transcript marks (session-marks/) and the cached
// answer translations (session-translations/).
//
// Session names are slot names and get REUSED. Left behind, they resurface on
// whatever session lands in that slot next: someone else's handoff card in the middle
// of an unrelated conversation, someone else's highlight on an unrelated sentence.
// Neither is part of the cleanup archive — they are annotations about a conversation
// that is being deleted, so a restore does not want them back either.
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
	if err := writeCleanupArchive(man, payloads); err != nil {
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
	if err := writeCleanupArchive(man, nil); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "archive_failed", err.Error())
		return
	}
	// -d (not -D): git refuses if the branch isn't merged, so unmerged work is protected.
	if out, err := gitx.Combined(dir, "branch", "-d", branch); err != nil {
		_ = purgeCleanupArchive(man.ID) // nothing was deleted — don't leave a stale archive
		httpx.WriteErr(w, http.StatusConflict, "branch_unmerged",
			"branch is not fully merged; not deleted (push/merge it, or delete in the Console): "+strings.TrimSpace(out))
		return
	}
	// remote / remote_error are always on the wire ("" = the flag was not passed), and the
	// payload stays ONE map literal at the write site: wiremap_golden_test.go goldens the key
	// set by parsing this literal, and a map built up over several statements leaves the
	// response with no coverage at all.
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
	restored, err := restoreCleanupArchive(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "restore_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"restored": restored})
}

func handlePurgeCleanupArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := purgeCleanupArchive(id); err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "purge_failed", err.Error())
		return
	}
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
