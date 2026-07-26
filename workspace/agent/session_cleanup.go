package main

// GET /sessions/cleanup — an on-demand survey of what can be tidied up: finished
// sessions still listed, and worktrees whose work is done. It only READS and
// classifies; acting is the caller's job (archive_session / delete_worktree, guarded).
// The passive 7-day auto-prune (handleListSessions) is unchanged — this is the
// "what's cleanable now" view it can't give, surfaced to both the Console and the
// assistant so a fleet that has drifted into chaos can be swept deliberately.

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// cleanupCandidate is one tidy-up target. safety grades how safe it is to act on:
//   - "safe":   reversible or provably done (a merged, clean worktree with no live
//     session; the assistant may act after a one-line confirm).
//   - "review": act only after checking (a stopped session might still be wanted; a
//     clean-but-unmerged worktree holds commits not yet in the parent).
//   - "keep":   do NOT auto-clean (live sessions, uncommitted/unpushed work) — needs
//     the user to stop/push/force in the Console.
//
// action names the tool that acts on it ("archive_session" / "delete_worktree"), or
// "" when there is no assistant action (informational — orphan panes, archived rows).
type cleanupCandidate struct {
	Type     string `json:"type"` // "session" | "worktree"
	Action   string `json:"action,omitempty"`
	ID       string `json:"id"` // session name, or repo base name (delete_worktree arg)
	Display  string `json:"display,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Path     string `json:"path,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Relation string `json:"relation,omitempty"` // worktree vs parent: same|contained|unmerged|diverged
	Dirty    bool   `json:"dirty,omitempty"`
	Ahead    int    `json:"ahead,omitempty"`
	Safety   string `json:"safety"` // safe | review | keep
	Reason   string `json:"reason"`
}

// classifySessionCleanup grades a session meta. Returns ok=false to skip it entirely
// (it is live — working, not clutter). Pure: no git/tmux, so it is unit-tested.
func classifySessionCleanup(locked, archived, live bool) (action, safety, reason string, ok bool) {
	switch {
	case live:
		return "", "", "", false // running — not a cleanup target
	case locked:
		// 削除ロック（docs/45）: 掃除の対象外。黙って隠すのではなく keep として見せる —
		// 「なぜ片付かないのか」が利用者にもオペレーターにも分かるように。
		return "", "keep", "ロック中（削除保護。解除するまで掃除対象外）", true
	case archived:
		// Archived rows are TTL-exempt (handleListSessions skips them before the prune),
		// so they accumulate. delete_session reclaims them (meta + jsonl), bundled to a
		// recoverable archive first — review before acting (archived ≠ throwaway).
		return "delete_session", "review", "アーカイブ済み（自動prune対象外。delete_session で回収可・復元可）", true
	default:
		// Stopped but resumable: finished work sitting in the active list. archive is
		// reversible (restore), but "stopped" ≠ "finished", so review before acting.
		return "archive_session", "review", "停止中（再開可能）。完了していれば archive で一覧から整理", true
	}
}

// classifyWorktreeCleanup grades a linked worktree from its live-session count and git
// state. Pure — the handler supplies the git facts. Mirrors maybePruneWorktree's
// conservatism (never touch dirty/ahead) but also proposes merged worktrees the passive
// prune leaves for 7 days.
func classifyWorktreeCleanup(locked bool, liveCount, ahead int, dirty bool, relation string) (action, safety, reason string) {
	switch {
	case locked:
		return "", "keep", "ロック中（削除保護。解除するまで掃除対象外）" // docs/45
	case liveCount > 0:
		return "", "keep", "稼働中のセッションがある（先に停止が必要）"
	case dirty || ahead > 0:
		return "", "keep", "未コミット/未pushの変更あり（push か Console で強制削除）"
	case relation == "contained" || relation == "same":
		return "delete_worktree", "safe", "マージ済み・クリーン（親に取り込み済み）"
	default:
		// clean but the branch has commits not in the parent (unmerged/diverged/unknown).
		return "delete_worktree", "review", "クリーンだが未マージ（固有コミットあり。削除でブランチは残るが要確認）"
	}
}

func handleSessionsCleanup(w http.ResponseWriter, r *http.Request) {
	out := []cleanupCandidate{}

	// Sessions: finished/stale rows in (or hidden from) the active list.
	live := tmuxx.LiveSessionNames()
	for _, m := range session.ListMetas() {
		isLive := live[m.Name] || (m.DriverKind() == session.DriverManaged && managedAlive(m))
		action, safety, reason, ok := classifySessionCleanup(m.Locked, m.Archived, isLive)
		if !ok {
			continue
		}
		out = append(out, cleanupCandidate{
			Type: "session", Action: action, ID: m.Name, Display: session.Display(m),
			Kind: m.Kind, Safety: safety, Reason: reason,
		})
	}
	// Orphan tmux panes (live session, no meta): can't be archived by name until a meta
	// exists — informational so the operator points the user at the Console.
	for name := range live {
		if _, ok := session.ReadMeta(name); ok {
			continue
		}
		out = append(out, cleanupCandidate{
			Type: "session", ID: name, Display: name, Kind: tmuxx.PaneKind(name),
			Safety: "review", Reason: "orphan（メタ無しの実行中ペイン）。Console でアタッチ/整理",
		})
	}

	// Worktrees under ~/repos: linked worktrees whose work may be done.
	entries, _ := os.ReadDir(reposRoot())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(reposRoot(), e.Name())
		if !isGitRepo(dir) || !isLinkedWorktree(dir) {
			continue
		}
		liveCount := len(liveSessionsInDir(dir))
		st, _ := gitStatus(dir)
		relation := ""
		if parent := worktreeParent(dir); parent != "" {
			relation = gitWorktreeIntegration(parent, dir, gitCurrentBranch(parent)).Relation
		}
		action, safety, reason := classifyWorktreeCleanup(repoLocked(dir), liveCount, st.Ahead, st.Dirty, relation)
		out = append(out, cleanupCandidate{
			Type: "worktree", Action: action, ID: e.Name(), Path: dir, Branch: st.Branch,
			Relation: relation, Dirty: st.Dirty, Ahead: st.Ahead, Safety: safety, Reason: reason,
		})
	}

	// Merged local branches left behind by removed worktrees (temp/*, etc.). Only NON-
	// worktree repos are scanned as the branch home; a worktree's branch is checked out
	// there and excluded by mergedLocalBranches anyway. Deleting a merged branch is
	// recoverable (its commits are in the parent line; delete_branch records the SHA).
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(reposRoot(), e.Name())
		if !isGitRepo(dir) || isLinkedWorktree(dir) {
			continue
		}
		for _, b := range mergedLocalBranches(dir) {
			out = append(out, cleanupCandidate{
				Type: "branch", Action: "delete_branch", ID: e.Name(), Branch: b,
				Safety: "safe", Reason: "マージ済みローカルブランチ（親に取り込み済み。削除しても復元可）",
			})
		}
	}

	counts := map[string]int{"safe": 0, "review": 0, "keep": 0}
	for _, c := range out {
		counts[c.Safety]++
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"candidates": out, "counts": counts})
}
