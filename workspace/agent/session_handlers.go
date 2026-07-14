package main

// セッション API の HTTP ハンドラ（一覧/作成/フォーク/停止/中断/アーカイブ/復元/作り直し）。
// session.go からの機械的分割（docs/23 P1-W4）。

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// handleListSessions returns the live claude_* tmux sessions.
// We query names and each session's cwd separately rather than packing both
// into one -F line: a tab/control-char delimiter is mangled by some tmux
// builds (e.g. Debian bookworm 3.3a), so a single delimited format is fragile.
func handleListSessions(w http.ResponseWriter, r *http.Request) {
	metas := map[string]session.Meta{}
	for _, m := range session.ListMetas() {
		metas[m.Name] = m
	}

	live := tmuxx.LiveSessionNames()

	now := time.Now()
	ttl := session.StoppedTTL()
	sessions := []session.Session{}
	for name, m := range metas {
		if m.Archived {
			continue // hidden from the active list; restorable via the archive modal
		}
		if live[name] {
			// Running: clear any prior stopped mark so resume resets the clock.
			if m.StoppedAt != "" {
				m.StoppedAt = ""
				session.WriteMeta(m)
			}
			sessions = append(sessions, wireSession(m, true))
			continue
		}
		// Stopped (exited): stamp when first noticed, prune once older than the TTL,
		// otherwise keep it listed as resumable.
		if m.StoppedAt == "" {
			m.StoppedAt = now.Format(time.RFC3339)
			session.WriteMeta(m)
		} else if t, e := time.Parse(time.RFC3339, m.StoppedAt); e == nil && now.Sub(t) > ttl {
			session.RemoveMeta(name)
			maybePruneWorktree(m.Dir) // last reference expired → clean up its worktree if clean
			continue
		}
		sessions = append(sessions, wireSession(m, false))
	}
	// Surface ORPHAN sessions: a live claude_* tmux session with no meta. These are
	// invisible to the meta-driven list above, so the auto-namer would reuse their
	// name and handleCreateSession then fails with "session already running" — a
	// confusing dead end (the session can't be seen or archived). List them so they
	// show up, count toward name uniqueness, and can be attached/archived. We can't
	// recover dir/model without a meta; kind is sniffed from the pane command.
	for name := range live {
		if _, ok := metas[name]; ok {
			continue
		}
		sessions = append(sessions, session.Session{
			Name: name, Tmux: session.TmuxName(name), Kind: tmuxx.PaneKind(name), Repo: name,
			Alive: true, Resumable: true,
		})
	}
	// Enrich rows from their working copy (worktree flag + branch-drift). One git call
	// per unique dir.
	annotateSessions(sessions, func(dir string) dirInfo {
		b, wt := gitDirInfo(dir)
		return dirInfo{branch: b, worktree: wt}
	})
	// Stable order: newest first by creation time.
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

type createReq struct {
	// Name is IGNORED: the server auto-allocates a unique slug as the session's
	// identity. Kept in the wire struct only so older clients that still send it
	// don't error. Title is the optional user-facing display name (→ claude --name).
	Name  string `json:"name"`
	Title string `json:"title"`
	Color string `json:"color"` // terminal background hue (hex); SSM host color, else empty
	Dir   string `json:"dir"`
	Model string `json:"model"`
	Kind  string `json:"kind"` // "claude" (default) | "opencode" | "codex" | "shell"
	// InitialPrompt, when set, is typed into the session once its agent CLI has booted
	// and then submitted (deliverInitialPrompt) — the server-side launch-task delivery an
	// orchestrator (フリート・オペレーター / create_session MCP tool) uses to spawn a session
	// AND hand it the first task in one call. The Console delivers its own launch prompt
	// client-side (open.ts) and leaves this empty.
	InitialPrompt string `json:"initial_prompt"`
	// Optional clone-then-start: when remote_url is set, the repo is cloned
	// (or reused) under ~/repos and its path becomes the session CWD, ignoring dir.
	// RepoName overrides the target folder so two branches of the same repo can
	// be cloned side by side (empty => derived from remote_url, the legacy name).
	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch"`
	RepoName  string `json:"repo_name"`
	// NewBranch, when set, is created off Branch (the base) right after the clone and
	// switched to, so the session starts on a fresh branch. Empty => no new branch.
	NewBranch string `json:"new_branch"`
	// Worktree switches to worktree-then-start: instead of cloning, spin a git worktree
	// off an EXISTING working copy (Dir = the parent, e.g. the main/develop 壁打ち clone)
	// at ~/repos/<repo>@<branch> and use it as CWD. Branch is the base, NewBranch (opt)
	// the fresh branch to create off it. Lets a decided task branch off into its own
	// directory + session without touching the parent. RemoteURL is ignored when set.
	Worktree bool `json:"worktree"`
	// UseExisting (worktree only) checks out an EXISTING branch (Branch) into the
	// worktree instead of creating a new one — the "work on the existing branch" answer
	// to a name collision. NewBranch is ignored; a remote-only branch is DWIM-tracked.
	UseExisting bool `json:"use_existing"`
	// Folder (worktree only) overrides the ~/repos/<repo>@<seg> folder segment so it can
	// diverge from the branch — e.g. an auto branch temp/<x> in a wip-<x> folder. Empty
	// => the folder is derived from the branch name.
	Folder string `json:"folder"`
	// SSM (kind=ssm) coordinates, resolved and forwarded by the Control Plane from a
	// host bookmark (control-plane/ssm.go). No secrets — SSO login happens in-pane.
	SSMProfile   string `json:"ssm_profile"`
	SSMTarget    string `json:"ssm_target"`
	SSMDocument  string `json:"ssm_document"`
	SSMRegion    string `json:"ssm_region"`
	SSOStartURL  string `json:"sso_start_url"`
	SSORegion    string `json:"sso_region"`
	SSOAccountID string `json:"sso_account_id"`
	SSORoleName  string `json:"sso_role_name"`
	// SSMForceLogin: run `aws sso logout` + `aws sso login` unconditionally at launch
	// (skip the cached-token short-circuit) so the user re-authenticates. One-shot.
	SSMForceLogin bool `json:"ssm_force_login"`
}

// handleCreateSession launches a claude session inside a detached tmux session.
func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	title, ok := cleanTitle(req.Title)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_title", "title is too long (max 80) or contains control characters")
		return
	}
	// Worktree-then-start: spin a git worktree off an existing working copy (req.Dir =
	// the parent) and use it as the CWD, so a decided task branches into its own dir +
	// session without touching the parent's running sessions.
	if req.Worktree {
		parent := strings.TrimSpace(req.Dir)
		if parent == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_dir", "worktree requires dir (the parent working copy)")
			return
		}
		if !filepath.IsAbs(parent) {
			parent = filepath.Join(homeDir(), parent)
		}
		var dir string
		var err error
		folderSeg := strings.TrimSpace(req.Folder) // "" => folder derives from the branch
		if req.UseExisting {
			// "Work on the existing branch": check out req.Branch (local or DWIM-tracked
			// remote) into the worktree — the chosen resolution of a name collision.
			dir, err = ensureWorktree(parent, req.Branch, "", "")
		} else {
			// Branch naming is deferred: unless the client sends an explicit name we
			// start on a throwaway branch temp/<slug> in a wip-<slug> folder (same
			// slug). The user (or the LLM suggestion) renames the branch later — the
			// folder stays put, so the session id holds.
			nb := strings.TrimSpace(req.NewBranch)
			if nb == "" {
				slug := randSlug() // random → skip the collision check
				nb = "temp/" + slug
				folderSeg = "wip-" + slug
			} else if local, remote := branchNameStatus(parent, nb); local {
				// A same-named local branch: -b would fail anyway, but stop with a clear
				// message rather than git's raw error, and let the user pick another name.
				httpx.WriteErr(w, http.StatusConflict, "branch_exists",
					fmt.Sprintf("branch %q already exists locally; choose another name", nb))
				return
			} else if remote {
				// A same-named PAST remote branch: -b would silently create a divergent
				// local branch that collides at push. Refuse and let the user rename or
				// work on the existing one (use_existing).
				httpx.WriteErr(w, http.StatusConflict, "branch_exists_remote",
					fmt.Sprintf("a remote branch %q already exists; rename it or work on the existing branch", nb))
				return
			}
			dir, err = ensureWorktree(parent, req.Branch, nb, folderSeg)
		}
		if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "worktree_failed", err.Error())
			return
		}
		req.Dir = dir
	} else if strings.TrimSpace(req.RemoteURL) != "" {
		// Clone-then-start: ensure the repo exists and use it as the working dir.
		dir, err := ensureRepo(req.RemoteURL, req.Branch, req.NewBranch, req.RepoName)
		if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "clone_failed", err.Error())
			return
		}
		req.Dir = dir
	}
	if req.Dir == "" {
		req.Dir = homeDir()
	} else if !filepath.IsAbs(req.Dir) {
		// A relative dir (e.g. "projects/x" from the New Session directory picker) is
		// resolved against home, mirroring the fs/tree browser's home-relative paths.
		req.Dir = filepath.Join(homeDir(), req.Dir)
	}
	if fi, err := os.Stat(req.Dir); err != nil || !fi.IsDir() {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_dir", "dir does not exist: "+req.Dir)
		return
	}

	// Identity is a freshly allocated random slug — NOT the client's name. It (and the
	// sid it derives) can't collide with an archived/pruned session's jsonl, so a new
	// session never accidentally --resumes a past conversation.
	name := allocSessionName(req.Dir)

	kind := normalizeKind(req.Kind)
	label := ""
	if agentOf(kind).Caps().UsesLabel {
		label = sessionLabelFor(req.Dir, title)
	}
	var ssm *session.SSMMeta
	if kind == session.KindSSM {
		if strings.TrimSpace(req.SSMTarget) == "" {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_ssm", "ssm_target (instance id) is required")
			return
		}
		ssm = &session.SSMMeta{
			Profile: req.SSMProfile, Target: req.SSMTarget, Document: req.SSMDocument,
			Region: req.SSMRegion, StartURL: req.SSOStartURL, SSORegion: req.SSORegion,
			AccountID: req.SSOAccountID, RoleName: req.SSORoleName,
		}
		if ssm.Region == "" {
			ssm.Region = ssm.SSORegion
		}
	}
	meta := session.Meta{
		Name: name, Dir: req.Dir, Model: req.Model, Kind: kind, Title: title, Color: req.Color, Label: label,
		Repo: filepath.Base(req.Dir), Branch: gitCurrentBranch(req.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: ssm,
	}
	if err := startSessionTmux(meta, req.SSMForceLogin); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(meta)

	// Optional launch task: deliver it once the CLI has booted (async — the create
	// response returns the session immediately so the caller can start polling).
	if strings.TrimSpace(req.InitialPrompt) != "" {
		go deliverInitialPrompt(name, req.InitialPrompt)
	}

	httpx.WriteJSON(w, http.StatusCreated, wireSession(meta, true))
}

// handleForkSession forks a session's conversation into a NEW session of the same
// kind (POST /sessions/{name}/fork). The fork shares the source's history up to now
// but then diverges independently — each CLI's native fork copies the conversation,
// leaving the source running/intact (claude --fork-session / opencode --session
// --fork / codex fork). The per-kind source id comes from agents.Forker; the fork's
// first launch (via ForkFrom) materializes the copy, and later launches resume it.
func handleForkSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	src, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_session", "session not found: "+name)
		return
	}
	ag := agentOf(src.Kind)
	forker, canFork := ag.(agents.Forker)
	if !canFork || !ag.Caps().CanFork {
		httpx.WriteErr(w, http.StatusBadRequest, "unsupported_kind", "このセッション種別は分岐に対応していません")
		return
	}
	if !session.DirExists(src.Dir) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_dir", "作業フォルダが存在しないため分岐できません")
		return
	}
	forkFrom, err := forker.ForkSource(src)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "not_resumable", err.Error())
		return
	}
	forkName := allocSessionName(src.Dir)
	title, _ := cleanTitle(forkTitle(src))
	meta := session.Meta{
		Name: forkName, Dir: src.Dir, Model: src.Model, Kind: src.Kind, Title: title,
		Repo:      filepath.Base(src.Dir),
		Branch:    gitCurrentBranch(src.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), ForkFrom: forkFrom,
	}
	if ag.Caps().UsesLabel {
		meta.Label = sessionLabelFor(src.Dir, title)
	}
	if err := startSessionTmux(meta, false); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(meta)
	httpx.WriteJSON(w, http.StatusCreated, wireSession(meta, true))
}

// handleStopSession kills the tmux session and forgets its meta so it stops
// appearing in the list. Tolerates an already-exited session (meta only).
func handleStopSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	tn := session.TmuxName(name)
	meta, hadMeta := session.ReadMeta(name)
	live := tmuxx.HasSession(tn)
	if !live && !hadMeta {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if hadMeta {
		status.Remove(session.UUID(meta.Dir, name))
		status.RemoveExit(name)
	}
	if live {
		if out, err := exec.Command("tmux", "kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
			return
		}
	}
	session.RemoveMeta(name)
	removeTerminalHistory(name)
	// Stopping forgets the session; if it was the last one in a worktree and that
	// worktree is clean, auto-remove it so worktrees don't pile up (no-op otherwise).
	if hadMeta {
		maybePruneWorktree(meta.Dir)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stopped": name})
}

// handleHaltSession stops a RUNNING session into the 停止中 (resumable) state: it
// kills the live tmux but KEEPS the meta visible (Archived stays false), so the row
// stays listed and the user can resume it later (claude --resume). This is the
// button counterpart of quitting in the terminal — distinct from /stop (which also
// forgets the meta = removes it from the list) and /archive (which hides it).
func handleHaltSession(w http.ResponseWriter, r *http.Request) {
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
	tn := session.TmuxName(name)
	if !tmuxx.HasSession(tn) {
		// Already stopped — nothing to do; report the current (stopped) wire.
		httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
		return
	}
	// Best-effort: disconnect any active Remote Control bridge before killing the
	// pane, so a later resume's autoconnect registers fresh under the current
	// title instead of resuming the stale one (see disconnectRemoteControl).
	disconnectRemoteControl(name, m)
	if out, err := exec.Command("tmux", "kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
		return
	}
	status.Remove(session.UUID(m.Dir, name))
	// Stamp StoppedAt now so the prune TTL starts here (handleListSessions would
	// otherwise stamp it on the next poll; doing it here keeps the wire consistent).
	m.StoppedAt = time.Now().Format(time.RFC3339)
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
}

// handleArchiveSession hides a session from the active list but KEEPS its meta (and
// jsonl), so it can be restored later. Kills the live tmux session if any. This is
// the non-destructive counterpart to stop (which forgets the meta).
func handleArchiveSession(w http.ResponseWriter, r *http.Request) {
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
	if tn := session.TmuxName(name); tmuxx.HasSession(tn) {
		_ = exec.Command("tmux", "kill-session", "-t", session.ExactTarget(tn)).Run()
	}
	status.Remove(session.UUID(m.Dir, name))
	status.RemoveExit(name)
	m.Archived = true
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"archived": name})
}

// handleRestoreSession brings an archived session back into the active list as a
// stopped session (the user clicks it to resume). The conversation (jsonl) is intact.
func handleRestoreSession(w http.ResponseWriter, r *http.Request) {
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
	m.Archived = false
	m.StoppedAt = "" // re-stamped on next list, resetting the prune clock
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
}

// handleListArchived returns archived sessions (for the restore modal).
func handleListArchived(w http.ResponseWriter, r *http.Request) {
	sessions := []session.Session{}
	for _, m := range session.ListMetas() {
		if m.Archived {
			sessions = append(sessions, wireSession(m, false))
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// handleRecreateSession starts a fresh session in the slot while PRESERVING the old
// one: the old session is archived (hidden from the active list but kept + restorable,
// its jsonl intact), NOT discarded, and a new session (fresh slug/sid, same
// title/dir/model/kind) is minted and pre-launched live. Allocating a new slug (hence
// a new sid) rather than reusing the old id lets the fresh session survive detached
// until the browser attaches (a reused id would exit first), so we pre-launch here
// like create — which lets the Console open it straight into chat. Non-destructive:
// the past conversation stays recoverable from the archive. Returns the new (alive)
// session.
func handleRecreateSession(w http.ResponseWriter, r *http.Request) {
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
	// Archive the old identity: kill its tmux, clear the live status cache, hide it from
	// the active list. Keep the meta + jsonl (and any captured resume id) so it restores.
	if tn := session.TmuxName(name); tmuxx.HasSession(tn) {
		_ = exec.Command("tmux", "kill-session", "-t", session.ExactTarget(tn)).Run()
	}
	status.Remove(session.UUID(m.Dir, m.Name))
	status.RemoveExit(m.Name)
	m.Archived = true
	session.WriteMeta(m)

	// Fresh identity, same slot. No ForkFrom — recreate means "start empty", not
	// "re-copy the fork source".
	newMeta := session.Meta{
		Name: allocSessionName(m.Dir), Dir: m.Dir, Model: m.Model, Kind: m.Kind,
		Title: m.Title, Color: m.Color, Repo: m.Repo, Branch: gitCurrentBranch(m.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: m.SSM,
	}
	if agentOf(newMeta.Kind).Caps().UsesLabel {
		newMeta.Label = sessionLabelFor(newMeta.Dir, newMeta.Title)
	}
	if err := startSessionTmux(newMeta, false); err != nil {
		// Un-archive the old session so a launch failure doesn't silently drop it from
		// the active list.
		m.Archived = false
		session.WriteMeta(m)
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(newMeta)
	httpx.WriteJSON(w, http.StatusOK, wireSession(newMeta, true))
}
