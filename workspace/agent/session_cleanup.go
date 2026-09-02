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
	Type    string `json:"type"` // "session" | "worktree"
	Action  string `json:"action,omitempty"`
	ID      string `json:"id"` // session name, or repo base name (delete_worktree arg)
	Display string `json:"display,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Path    string `json:"path,omitempty"`
	// Repo is the working-copy FOLDER basename under ~/repos ("agent-fleet@wip-sw32vcm"
	// for a worktree, "agent-fleet" for the clone). Every type carries it — it is what
	// the Console groups the list by (repo → working copy), so a session candidate has to
	// name its working copy just like the worktree and branch candidates do. "" for an
	// orphan pane (no meta ⇒ no known dir).
	Repo      string `json:"repo,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Relation  string `json:"relation,omitempty"` // worktree vs parent: same|contained|unmerged|diverged
	Dirty     bool   `json:"dirty,omitempty"`
	Ahead     int    `json:"ahead,omitempty"`
	Safety    string `json:"safety"` // safe | review | keep
	ReasonKey string `json:"reason_key,omitempty"`
	Reason    string `json:"reason"`
}

// The "reason" of a candidate is text WE generate for the user to read, so per ADR 0033
// it is carried as a catalog key and rendered by the Console (`clean.reason.*` in
// console/src/lib/i18n/locales/{ja,en}/admin.ts) — it follows settings.locale instead of
// freezing to Japanese as it did before.
//
// Reason still ships the source-language (ja, ADR 0016 §4) sentence: it is the fallback
// for a Console that does not know a key (version skew), and it is what the assistant /
// operator actually reads — list_cleanup_candidates relays this JSON to the model, which
// has no catalog. Two readers, one field each: key for the Console, prose for the model.
const (
	cleanReasonLocked       = "clean.reason.locked"
	cleanReasonArchived     = "clean.reason.archived"
	cleanReasonStopped      = "clean.reason.stopped"
	cleanReasonEphemeral    = "clean.reason.ephemeral"
	cleanReasonOrphanPane   = "clean.reason.orphan_pane"
	cleanReasonWtLive       = "clean.reason.wt_live"
	cleanReasonWtLockedSess = "clean.reason.wt_locked_session"
	cleanReasonWtDirty      = "clean.reason.wt_dirty"
	cleanReasonWtMerged     = "clean.reason.wt_merged"
	cleanReasonWtUnmerged   = "clean.reason.wt_unmerged"
	cleanReasonBranchMerged = "clean.reason.branch_merged"
)

var cleanupReasonJA = map[string]string{
	cleanReasonLocked:       "ロック中（削除保護。解除するまで掃除対象外）",
	cleanReasonArchived:     "アーカイブ済み（自動prune対象外。delete_session で回収可・復元可）",
	cleanReasonStopped:      "停止中（再開可能）。完了していれば archive で一覧から整理",
	cleanReasonEphemeral:    "停止中の shell/ssm（残す会話なし）。削除で片付く",
	cleanReasonOrphanPane:   "orphan（メタ無しの実行中ペイン）。Console でアタッチ/整理",
	cleanReasonWtLive:       "稼働中のセッションがある（先に停止が必要）",
	cleanReasonWtLockedSess: "削除ロックされたセッションが残っている（解除するまで削除不可）",
	cleanReasonWtDirty:      "未コミット/未pushの変更あり（push か Console で強制削除）",
	cleanReasonWtMerged:     "マージ済み・クリーン（親に取り込み済み）",
	cleanReasonWtUnmerged:   "クリーンだが未マージ（固有コミットあり。削除でブランチは残るが要確認）",
	cleanReasonBranchMerged: "マージ済みローカルブランチ（親に取り込み済み。削除しても復元可）",
}

// cleanupReasonText resolves a reason key to its source-language sentence. An unknown key
// degrades to the key itself rather than to an empty reason.
func cleanupReasonText(key string) string {
	if s, ok := cleanupReasonJA[key]; ok {
		return s
	}
	return key
}

// classifySessionCleanup grades a session meta. Returns ok=false to skip it entirely
// (it is live — working, not clutter). ephemeral = shell/ssm: no conversation worth
// keeping, so archiving is meaningless — deletion is the whole tidy-up. Pure: no
// git/tmux, so it is unit-tested.
func classifySessionCleanup(locked, archived, live, ephemeral bool) (action, safety, reasonKey string, ok bool) {
	switch {
	case live:
		return "", "", "", false // running — not a cleanup target
	case locked:
		// 削除ロック（docs/log/45）: 掃除の対象外。黙って隠すのではなく keep として見せる —
		// 「なぜ片付かないのか」が利用者にもオペレーターにも分かるように。
		return "", "keep", cleanReasonLocked, true
	case archived:
		// Archived rows are TTL-exempt (handleListSessions skips them before the prune),
		// so they accumulate. delete_session reclaims them (meta + jsonl), bundled to a
		// recoverable archive first — review before acting (archived ≠ throwaway).
		return "delete_session", "review", cleanReasonArchived, true
	case ephemeral:
		// A stopped shell/ssm holds nothing restorable — delete_session still bundles
		// its meta to the recoverable archive, so this is safe to act on in bulk.
		return "delete_session", "safe", cleanReasonEphemeral, true
	default:
		// Stopped but resumable: finished work sitting in the active list. archive is
		// reversible (restore), but "stopped" ≠ "finished", so review before acting.
		return "archive_session", "review", cleanReasonStopped, true
	}
}

// classifyWorktreeCleanup grades a linked worktree from its live-session count and git
// state. Pure — the handler supplies the git facts. Mirrors maybePruneWorktree's
// conservatism (never touch dirty/ahead) but also proposes merged worktrees the passive
// prune leaves for 7 days.
func classifyWorktreeCleanup(locked bool, lockedSessions, liveCount, ahead int, dirty bool, relation string) (action, safety, reasonKey string) {
	switch {
	case locked:
		return "", "keep", cleanReasonLocked // docs/log/45
	case lockedSessions > 0:
		// 削除ロック済みセッション（docs/log/45）が住む WT は handleDeleteRepo が 403 で拒む。
		// safe と提案しても実行時に失敗するだけ — keep として理由ごと見せる。
		return "", "keep", cleanReasonWtLockedSess
	case liveCount > 0:
		return "", "keep", cleanReasonWtLive
	case dirty || ahead > 0:
		return "", "keep", cleanReasonWtDirty
	case relation == "contained" || relation == "same":
		return "delete_worktree", "safe", cleanReasonWtMerged
	default:
		// clean but the branch has commits not in the parent (unmerged/diverged/unknown).
		return "delete_worktree", "review", cleanReasonWtUnmerged
	}
}

func handleSessionsCleanup(w http.ResponseWriter, r *http.Request) {
	out := []cleanupCandidate{}

	// Sessions: finished/stale rows in (or hidden from) the active list.
	live := tmuxx.LiveSessionNames()
	metas := session.ListMetas()
	for _, m := range metas {
		isLive := live[m.Name] || (m.DriverKind() == session.DriverManaged && managedAlive(m))
		ephemeral := m.Kind == session.KindShell || m.Kind == session.KindSSM
		action, safety, reasonKey, ok := classifySessionCleanup(m.Locked, m.Archived, isLive, ephemeral)
		if !ok {
			continue
		}
		out = append(out, cleanupCandidate{
			Type: "session", Action: action, ID: m.Name, Display: session.Display(m),
			Kind: m.Kind, Path: m.Dir, Repo: m.Repo, Safety: safety,
			ReasonKey: reasonKey, Reason: cleanupReasonText(reasonKey),
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
			Safety: "review", ReasonKey: cleanReasonOrphanPane, Reason: cleanupReasonText(cleanReasonOrphanPane),
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
		// 削除ロック済みセッション（stopped/archived 含む）が居る WT は削除も 403 で
		// 拒まれる — handleDeleteRepo と同じ判定（lockedSessionsInDir）で先に keep にする。
		lockedSess := len(lockedSessionsInDir(metas, dir))
		action, safety, reasonKey := classifyWorktreeCleanup(repoLocked(dir), lockedSess, liveCount, st.Ahead, st.Dirty, relation)
		out = append(out, cleanupCandidate{
			Type: "worktree", Action: action, ID: e.Name(), Path: dir, Repo: e.Name(), Branch: st.Branch,
			Relation: relation, Dirty: st.Dirty, Ahead: st.Ahead, Safety: safety,
			ReasonKey: reasonKey, Reason: cleanupReasonText(reasonKey),
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
				Type: "branch", Action: "delete_branch", ID: e.Name(), Repo: e.Name(), Branch: b,
				Safety: "safe", ReasonKey: cleanReasonBranchMerged, Reason: cleanupReasonText(cleanReasonBranchMerged),
			})
		}
	}

	counts := map[string]int{"safe": 0, "review": 0, "keep": 0}
	for _, c := range out {
		counts[c.Safety]++
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"candidates": out, "counts": counts})
}
