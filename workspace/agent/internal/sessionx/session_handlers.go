package sessionx

// HTTP handlers for the session API: list / create / fork / stop / halt / archive /
// restore / recreate.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// ManagedAlive reports a managed session's liveness — the runtime-handle
// counterpart of tmuxx.HasSession (docs/log/27 P2/P3; each kind implements it in its own package).
func ManagedAlive(m session.Meta) bool {
	switch m.Kind {
	case session.KindOpencode:
		return opencode.ManagedAlive(m.Name)
	case session.KindCodex:
		return codex.ManagedAlive(m.Name)
	case session.KindCopilot:
		return copilot.ManagedAlive(m.Name)
	case session.KindCursor:
		return cursor.ManagedAlive(m.Name)
	case session.KindKiro:
		return kiro.ManagedAlive(m.Name)
	}
	return false
}

// managedBusy reports a managed session has a turn running or queued — the refusal
// condition for an exclusive driver switch (/driver, docs/log/27 §2: a switch always goes
// stop -> drain -> resume, and never happens while busy, which leaves the drain to the
// user waiting for idle).
func managedBusy(m session.Meta) bool {
	switch m.Kind {
	case session.KindOpencode:
		return opencode.ManagedBusy(m.Name)
	case session.KindCodex:
		return codex.ManagedBusy(m.Name)
	case session.KindCopilot:
		return copilot.ManagedBusy(m.Name)
	case session.KindCursor:
		return cursor.ManagedBusy(m.Name)
	case session.KindKiro:
		return kiro.ManagedBusy(m.Name)
	}
	return false
}

// dropManagedRuntime detaches a managed session from its runtime — the equivalent of the
// tmux kill-session that stop / halt / archive / recreate do: abort the running turn and
// forget the handle. The conversation's source of truth (SQLite / rollout) stays intact,
// so a resume reconnects to it.
func dropManagedRuntime(m session.Meta) {
	switch m.Kind {
	case session.KindOpencode:
		opencode.DropHandle(m.Name)
	case session.KindCodex:
		codex.DropHandle(m.Name)
	case session.KindCopilot:
		copilot.DropHandle(m.Name)
	case session.KindCursor:
		cursor.DropHandle(m.Name)
	case session.KindKiro:
		kiro.DropHandle(m.Name)
	}
}

// removeManagedLedger drops the ClientMessageID ledger on /stop, discarding the slot's
// identity with it. halt/archive do not call it: those can be resumed.
func removeManagedLedger(m session.Meta) {
	switch m.Kind {
	case session.KindOpencode:
		opencode.RemoveLedger(m.Name)
	case session.KindCodex:
		codex.RemoveLedger(m.Name)
	case session.KindCopilot:
		copilot.RemoveLedger(m.Name)
	case session.KindCursor:
		cursor.RemoveLedger(m.Name)
	case session.KindKiro:
		kiro.RemoveLedger(m.Name)
	}
}

// HandleListSessions returns the live claude_* tmux sessions.
// We query names and each session's cwd separately rather than packing both
// into one -F line: a tab/control-char delimiter is mangled by some tmux
// builds (e.g. Debian bookworm 3.3a), so a single delimited format is fragile.
func HandleListSessions(w http.ResponseWriter, r *http.Request) {
	metas := map[string]session.Meta{}
	for _, m := range session.ListMetas() {
		metas[m.Name] = m
	}

	live := tmuxx.LiveSessionNames()
	// A managed session (docs/log/27 P2) has no tmux — the runtime handle decides liveness.
	// A dead daemon or an Agent restart drops the handle, so the session shows as stopped
	// and comes back on reconcile or a resume click; the same rule tui states as "a tmux
	// session exists => it is alive".
	for name, m := range metas {
		if m.DriverKind() == session.DriverManaged && ManagedAlive(m) {
			live[name] = true
		}
	}

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
				m = WriteSessionMetaKeepingLock(m)
			}
			sessions = append(sessions, wireSession(m, true))
			continue
		}
		// Stopped (exited): stamp when first noticed, prune once older than the TTL,
		// otherwise keep it listed as resumable.
		if m.StoppedAt == "" {
			// This is the ONLY point where af first notices the pane is gone, and it covers
			// a workspace stop, a container SIGKILL, a claude crash and a user /exit alike.
			// A session that vanished with a modal open is promoted to a carry-over here
			// (docs/log/75 §75.6.3, trigger 2). It runs before the resume boot hook clears
			// the payload: listing a stopped session always reaches this first.
			PromoteCarriedFor(m)
			m.StoppedAt = now.Format(time.RFC3339)
			m = WriteSessionMetaKeepingLock(m)
		} else if t, e := time.Parse(time.RFC3339, m.StoppedAt); e == nil && now.Sub(t) > ttl && !m.Locked {
			// The deletion lock (docs/log/45) applies to automatic deletion too: a locked
			// row is never pruned past the TTL and stays listed as stopped.
			finalizeSessionUsage(m) // fold into the usage ledger before forgetting it (docs/log/46 §3-b)
			status.RemoveCarried(session.UUID(m.Dir, name))
			session.RemoveMeta(name)
			gitx.MaybePruneWorktree(m.Dir) // last reference expired → clean up its worktree if clean
			continue
		}
		sessions = append(sessions, wireSession(m, false))
	}
	// Surface ORPHAN sessions: a live claude_* tmux session with no meta. These are
	// invisible to the meta-driven list above, so the auto-namer would reuse their
	// name and HandleCreateSession then fails with "session already running" — a
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
		b, wt := gitx.GitDirInfo(dir)
		return dirInfo{branch: b, worktree: wt}
	})
	// Stable order: newest first by creation time.
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	// repoJobs is the only channel telling the CP "no session, but the workspace IS busy"
	// (docs/log/78). An import takes minutes to hours while GET polling deliberately does
	// not count as activity, so without this idle-stop kills a running clone / checkout.
	// The reaper reads this list on every sweep, so it costs no extra request.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "repoJobs": repoJobsRunning()})
}

// HandleSessionCatalog is the sharing inventory. Unlike the ordinary list it
// includes archived rows, but only kinds with a structured transcript.
func HandleSessionCatalog(w http.ResponseWriter, r *http.Request) {
	live := tmuxx.LiveSessionNames()
	items := []session.Session{}
	for _, m := range session.ListMetas() {
		if !AgentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		alive := live[m.Name]
		if m.DriverKind() == session.DriverManaged {
			alive = ManagedAlive(m)
		}
		items = append(items, wireSession(m, alive))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

type CreateReq struct {
	// Name is IGNORED: the server auto-allocates a unique slug as the session's
	// identity. Kept in the wire struct only so older clients that still send it
	// don't error. Title is the optional user-facing display name (→ claude --name).
	Name  string `json:"name"`
	Title string `json:"title"`
	Color string `json:"color"` // terminal background hue (hex); SSM host color, else empty
	Dir   string `json:"dir"`
	// Subdir starts the agent in a folder BENEATH the resolved working copy
	// (slash-relative, e.g. "console/src"). Applied last, so it composes with every
	// way Dir gets resolved — plain dir, clone-then-start, worktree-then-start (where
	// it points inside the FRESH worktree). The session's Dir keeps recording the
	// working copy itself; only the launched process starts deeper (Meta.CWD).
	Subdir string `json:"subdir"`
	// IdempotencyKey dedupes a retried/concurrent create so a client that times out
	// (but whose request the backend actually completed) can't spawn a duplicate on
	// retry. The stdio MCP create_session tool derives it deterministically from the
	// conversation id + launch args, so an LLM re-issuing the same call reproduces the
	// same key. Empty => the server falls back to a report_to-scoped intent fingerprint
	// (createIdempotencyKey); interactive Console launches send neither and aren't deduped.
	IdempotencyKey string `json:"idempotency_key"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	Mode           string `json:"mode"` // "plan" | "normal"
	// SkipPermissions asks whether to skip tool approvals (docs/log/76). It is three-valued:
	// unset (null) means "follow the per-kind default in Settings > Agents", and the Console
	// only sends it when the user touched the toggle. Folding unset into false would launch
	// even the kinds enabled in settings with approvals on.
	SkipPermissions *bool  `json:"skip_permissions"`
	Kind            string `json:"kind"` // "claude" (default) | "opencode" | "codex" | "shell"
	// Driver selects the control route (docs/log/27 §9.2): "" | "tui" (the default, a TUI
	// inside tmux) | "managed" (shared runtime + structured RPC, no pane). Managed launches
	// were opened up in P2 (opencode serve) and P3 (codex app-server); an unsupported kind
	// is rejected explicitly.
	Driver string `json:"driver"`
	// InitialPrompt, when set, is typed into the session once its agent CLI has booted
	// and then submitted (deliverInitialPrompt) — the server-side launch-task delivery an
	// orchestrator (the fleet operator / create_session MCP tool) uses to spawn a session
	// AND hand it the first task in one call. The Console delivers its own launch prompt
	// client-side (open.ts) and leaves this empty.
	InitialPrompt string `json:"initial_prompt"`
	// ReportTo (docs/log/30) links the new session to an assistant conversation and arms a
	// one-shot completion report: the first awaiting-input / abnormal-exit event after
	// launch posts a report message into that conversation. Set by the af_write MCP's
	// create_session (which knows its own conversation id via --conv); Console creates
	// leave it empty.
	ReportTo string `json:"report_to"`
	// Source attributes the initial_prompt injection's origin for the mirror badge
	// (docs/log/38): "schedule" / "schedule-manual" from the CP scheduler; anything else
	// (incl. empty — the operator MCP) records as "operator". Whitelisted server-side.
	Source string `json:"source"`
	// Origin / OriginConv record who STARTED this session (docs/log/46 §2-c, ADR 0029 §6) —
	// a different axis from Source (which attributes one injected prompt). The MCP
	// create_session sends "operator" plus its own conversation slug; the Console sends
	// nothing and defaults to "user". Schedule creates are resolved from Source, so the
	// CP scheduler needs no new wire field. Whitelisted server-side (session.ValidOrigin).
	Origin     string `json:"origin"`
	OriginConv string `json:"origin_conv"`
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
	// off an EXISTING working copy (Dir = the parent, e.g. the main/develop brainstorming clone)
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
	// SSMAlias: the host bookmark's alias (CP disambiguates it with the profile when it
	// collides with another host). Used to default a kind=ssm session's Title to
	// "{alias} @MMDD-HHMM" when the client sent no title.
	SSMAlias     string `json:"ssm_alias"`
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

// resolveLiveModel turns a picker label or a short, unambiguous family name into
// the exact live catalog slug.  Orchestrators often receive a human request such
// as "terra" rather than the CLI spelling "gpt-5.6-terra"; passing the former to
// Codex defers a simple validation error until after the session has been created.
// An unavailable/ambiguous requested model is rejected before clone/worktree side
// effects.  If the live catalog cannot be read (offline/CLI not installed), retain
// the requested value: the normal launch path remains usable in that degraded mode.
func resolveLiveModel(requested string, choices []agents.ModelChoice) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || len(choices) == 0 {
		return requested, nil
	}
	norm := func(s string) string {
		return strings.ToLower(strings.TrimSpace(s))
	}
	want := norm(requested)
	// An exact id/label match wins outright, even when it also happens to be a
	// prefix of another choice (e.g. "sakana/fugu" vs "sakana/fugu-ultra"): the
	// fuzzy family-name matching below is only for when nothing matched exactly.
	for _, choice := range choices {
		if want == norm(choice.ID) || want == norm(choice.Label) {
			return choice.ID, nil
		}
	}
	var matches []string
	for _, choice := range choices {
		id, label := norm(choice.ID), norm(choice.Label)
		if strings.HasSuffix(id, "-"+want) || strings.HasSuffix(label, "-"+want) ||
			strings.HasPrefix(id, want+"-") || strings.HasPrefix(label, want+"-") {
			matches = append(matches, choice.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("モデル %q は曖昧です。利用可能なモデルから完全名を指定してください: %s",
			requested, joinModelIDs(matches, modelAmbiguousLimit))
	}
	return "", fmt.Errorf("モデル %q は利用できません。近い候補: %s。"+
		"一覧は起動ダイアログのモデル選択（アシスタントは list_models）で確認してください。",
		requested, joinModelIDs(nearestModels(want, choices), modelSuggestLimit))
}

// retiredModelError is the wording for "the id is right, but the model is no longer
// offered". Only opencode can be in that state (the one kind whose catalog carries a
// status), and without saying so the user keeps re-entering the same id.
func retiredModelError(requested string, choices []agents.ModelChoice) string {
	return fmt.Sprintf("モデル %q は提供終了しています（opencode.ai 側で退役）。近い候補: %s。",
		requested, joinModelIDs(nearestModels(strings.ToLower(requested), choices), modelSuggestLimit))
}

// Rejection messages are read in a Console toast and in a phone notification, so the
// id list has to fit there: the live opencode catalog alone runs to ~60 entries, and
// spelling all of them out pushed the actual reason off the top of the notification
// (measured: an opencode launch-failure notification was filled entirely with model
// names). So name a few ids and say how many were left out — the full list already has a
// home (the launch dialog / list_models).
const (
	modelSuggestLimit   = 5  // unknown model: only the nearest candidates
	modelAmbiguousLimit = 10 // ambiguous: the user picks from these, so show more
)

// joinModelIDs renders at most limit ids, appending how many were dropped.
func joinModelIDs(ids []string, limit int) string {
	if len(ids) <= limit {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s（ほか %d 件）", strings.Join(ids[:limit], ", "), len(ids)-limit)
}

// nearestModels reorders the catalog by rough similarity to what was requested, so a
// truncated message names plausible ids rather than whichever ones sort first. The
// scoring is deliberately crude — shared dot/dash/slash tokens, then a longest-common-
// prefix tie-break — because it only has to ORDER the list; resolveLiveModel has
// already decided that nothing here matches. Returns every id (the caller truncates)
// so the "N more" count ("ほか %d 件") stays the size of the real catalog.
func nearestModels(want string, choices []agents.ModelChoice) []string {
	wanted := make(map[string]bool)
	for _, tok := range modelTokens(want) {
		wanted[tok] = true
	}
	type scored struct {
		id             string
		shared, prefix int
	}
	rows := make([]scored, 0, len(choices))
	for _, choice := range choices {
		row := scored{id: choice.ID, prefix: commonPrefixLen(strings.ToLower(choice.ID), want)}
		for _, tok := range modelTokens(choice.ID) {
			if wanted[tok] {
				row.shared++
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].shared != rows[j].shared {
			return rows[i].shared > rows[j].shared
		}
		return rows[i].prefix > rows[j].prefix
	})
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.id)
	}
	return out
}

// modelTokens splits an id into its comparable parts: "opencode-go/glm-5.2" =>
// opencode, go, glm, 5, 2. Everything non-alphanumeric is a separator: each kind spells
// ids with its own mix of / - . _, so splitting them all at the same granularity makes
// them comparable whichever one an id uses.
func modelTokens(id string) []string {
	return strings.FieldsFunc(strings.ToLower(id), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
	})
}

// commonPrefixLen is the byte-wise tie-break for nearestModels: among ids sharing the same
// number of tokens, the one matching the requested string for longer is nearer, which puts
// ids on the same billing route first.
func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// CreateOrigin resolves a new session's origin (ADR 0029 §6) from the create request.
// Precedence — most specific caller wins:
//  1. an explicit origin field (the af_write MCP's create_session sends "operator" plus
//     the conversation slug it belongs to);
//  2. Source="schedule"/"schedule-manual", which the CP scheduler ALREADY sends for the
//     mirror badge (docs/log/38) — deriving from it keeps the scheduler free of a new field;
//  3. "user" — the Console launch flow, the only path a human drives, and the only one
//     that legitimately arrives unlabeled.
//
// The conversation slug is only meaningful for operator-started sessions ("which operator
// conversation made this expensive purchase"), so it is dropped otherwise.
func CreateOrigin(req *CreateReq) (origin, conv string) {
	if req.Origin != "" {
		origin = session.ValidOrigin(req.Origin)
	} else if s := injectionSource(req.Source); s == TurnSourceSchedule || s == TurnSourceScheduleManual {
		origin = session.OriginSchedule
	} else {
		origin = session.OriginUser
	}
	if origin == session.OriginOperator {
		conv = req.OriginConv
	}
	return origin, conv
}

// HandleCreateSession launches a claude session inside a detached tmux session.
func HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	// Idempotency guard (session_idempotency.go): collapse a retried / concurrent create
	// onto the first one so a client that timed out mid-launch can't spawn a duplicate.
	idemKey := createIdempotencyKey(&req)
	committed := false
	if idemKey != "" {
		if prev, dup := createLedger.begin(idemKey); dup {
			if prev.state == createDone {
				// The identical create already succeeded — replay that session verbatim.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(prev.body)
			} else {
				// The first create is still launching (the timed-out client's retry, or a
				// genuine double-fire). Refuse with a distinct code the MCP tool reconciles
				// into the eventual session rather than surfacing as a retryable failure.
				httpx.WriteErr(w, http.StatusConflict, "create_in_progress",
					"同じ内容のセッションを作成中です。完了までお待ちください")
			}
			return
		}
		// We own the inflight entry: clear it on any non-committed exit so a real failure
		// doesn't wedge the key (a completed entry is kept for replay by ledger.fail).
		defer func() {
			if !committed {
				createLedger.fail(idemKey)
			}
		}()
	}
	// writeCreated finalizes a successful launch: cache the session for idempotent replay,
	// mark the key committed (so the defer above won't clear it), then write the response.
	writeCreated := func(m session.Meta) {
		body := wireSession(m, true)
		if idemKey != "" {
			if b, err := json.Marshal(body); err == nil {
				createLedger.complete(idemKey, b)
				committed = true
			}
		}
		httpx.WriteJSON(w, http.StatusCreated, body)
	}
	title, ok := CleanTitle(req.Title)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_title", "title is too long (max 80) or contains control characters")
		return
	}
	if req.Mode != "" && req.Mode != "normal" && req.Mode != "plan" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_mode", `mode must be "plan" or "normal"`)
		return
	}
	// "Ask for tool approval" is only accepted for kinds whose pending approvals can be
	// answered from the Console (docs/log/76). Ignoring it silently would leave the caller
	// believing the session runs with approvals on, so refusing is the honest answer.
	// Skipping (true) is the historical default and stays allowed for every kind.
	if req.SkipPermissions != nil && !*req.SkipPermissions && !AgentOf(NormalizeKind(req.Kind)).Caps().PermissionChoice {
		httpx.WriteErr(w, http.StatusBadRequest, "permission_choice_unsupported",
			"this agent kind cannot surface tool approvals to the Console; skip_permissions must stay true")
		return
	}
	// Driver validation up-front, before any side effect (clone / worktree). The default
	// tui is normalized to "" when persisted, keeping metas byte-identical to existing ones.
	driver := strings.TrimSpace(req.Driver)
	switch driver {
	case "", session.DriverTUI:
		driver = ""
	case session.DriverManaged:
		// docs/log/27 P2/P3: only kinds registered in driverOf (opencode / codex); claude
		// is out of scope (ADR 0015). A raw req.Kind can still turn into claude through
		// NormalizeKind further down, so never decide on it — normalize here and keep
		// using the normalized value.
		if _, ok := managedDrivers[NormalizeKind(req.Kind)]; !ok {
			httpx.WriteErr(w, http.StatusBadRequest, "driver_unsupported",
				"managed ドライバはこの kind では利用できません")
			return
		}
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "bad_driver", "unknown driver: "+req.Driver)
		return
	}
	// Models the user disabled (ui-prefs hiddenModels — model_deny.go) are refused for every
	// kind, before any side effect (clone / worktree). This create is reached not only from
	// the Console launch flow but from the schedule (CP scheduler) and MCP create_session,
	// and narrowing the catalog alone lets an explicit id through, so this guard is the
	// real one.
	if kind := NormalizeKind(req.Kind); ModelHidden(kind, req.Model) {
		httpx.WriteErr(w, http.StatusBadRequest, "model_hidden", hiddenModelError(strings.TrimSpace(req.Model)))
		return
	}
	// For kinds with a live catalog, drop the hidden models from the candidate set too:
	// resolveLiveModel expands an unambiguous short name into the full id, so without that
	// a short name like "fab" would resolve to an excluded model.
	if NormalizeKind(req.Kind) == session.KindCodex && strings.TrimSpace(req.Model) != "" {
		model, err := resolveLiveModel(req.Model, FilterVisibleModels(session.KindCodex, codex.Models()))
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_model", err.Error())
			return
		}
		req.Model = model
	} else if NormalizeKind(req.Kind) == session.KindCopilot && strings.TrimSpace(req.Model) != "" {
		model, err := resolveLiveModel(req.Model, FilterVisibleModels(session.KindCopilot, copilot.Models()))
		if err != nil {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_model", err.Error())
			return
		}
		req.Model = model
	} else if NormalizeKind(req.Kind) == session.KindOpencode && strings.TrimSpace(req.Model) != "" {
		ids := VisibleModelIDs(session.KindOpencode, opencode.Models())
		choices := make([]agents.ModelChoice, 0, len(ids))
		for _, id := range ids {
			choices = append(choices, agents.ModelChoice{ID: id, Label: id})
		}
		model, err := resolveLiveModel(req.Model, choices)
		if err != nil {
			// A retired model needs its own wording: with the typo message the user never
			// reaches the cause, since the id and the billing route are both right and
			// only opencode.ai stopped offering it (models.go Retired).
			if requested := strings.TrimSpace(req.Model); opencode.Retired(requested) {
				httpx.WriteErr(w, http.StatusBadRequest, "bad_model", retiredModelError(requested, choices))
				return
			}
			httpx.WriteErr(w, http.StatusBadRequest, "bad_model", err.Error())
			return
		}
		req.Model = model
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
		// SVN has no worktree analog (docs/log/41): isolate parallel work by checking out a
		// different path into its own folder instead. Refuse a worktree launch here with a
		// clear message rather than letting ensureWorktree fail on the missing .git.
		if isSvnRepo(parent) {
			httpx.WriteErr(w, http.StatusBadRequest, "svn_no_worktree",
				"svn working copies have no worktree; check out a different path into its own folder instead")
			return
		}
		var dir string
		var err error
		folderSeg := strings.TrimSpace(req.Folder) // "" => folder derives from the branch
		if req.UseExisting {
			// "Work on the existing branch": check out req.Branch (local or DWIM-tracked
			// remote) into the worktree. Reached from the launch dialog's existing-branch mode
			// and from the SCM branch actions, as well as from a name collision.
			base := strings.TrimSpace(req.Branch)
			// git holds a branch in one worktree at a time. Refuse BEFORE any directory or
			// fetch side effect, naming the copy that has it — worktree add would fail
			// halfway through otherwise, with a message that doesn't say what to do next.
			if occ := gitx.WorktreeBranches(parent)[base]; occ != "" {
				gitx.WriteBranchInUse(w, base, occ)
				return
			}
			gitx.EnsureBranchRef(parent, base) // branch pushed since our last fetch => fetch once
			dir, err = gitx.EnsureWorktree(parent, base, "", "")
			if err == nil {
				gitx.FastForwardWorktree(dir) // start at the branch's current tip, not a stale one
			}
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
			} else if local, remote := gitx.BranchNameStatus(parent, nb); local {
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
			dir, err = gitx.EnsureWorktree(parent, req.Branch, nb, folderSeg)
			if err == nil {
				// Start at origin's tip, the same intent as fastForwardWorktree on the
				// existing-branch path. An empty base means the parent's HEAD is the start
				// point, so pull using the parent's current branch name.
				base := strings.TrimSpace(req.Branch)
				if base == "" {
					// GitCurrentBranch answers "(detached)" when detached: there is
					// nothing to pull from, so do nothing (passing that as a branch name
					// only produces a confusing log line).
					if b := gitx.GitCurrentBranch(parent); b != "(detached)" {
						base = b
					}
				}
				gitx.FastForwardNewWorktreeToOrigin(dir, base)
			}
		}
		if err != nil {
			httpx.WriteErr(w, http.StatusBadGateway, "worktree_failed", err.Error())
			return
		}
		req.Dir = dir
	} else if strings.TrimSpace(req.RemoteURL) != "" {
		// Clone-then-start: ensure the repo exists and use it as the working dir.
		dir, err := gitx.EnsureRepo(req.RemoteURL, req.Branch, req.NewBranch, req.RepoName)
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
	// Subdir (optional): the CWD narrows to a folder beneath the resolved working copy.
	// Validated AFTER Dir so a worktree launch checks the path inside the fresh worktree
	// — the parent may well have a folder the new branch doesn't (or vice versa).
	subdir, subdirOK := session.CleanSubdir(req.Subdir)
	if !subdirOK {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_subdir",
			"subdir must be a relative path inside the working copy: "+req.Subdir)
		return
	}
	if subdir != "" && !session.DirExists(filepath.Join(req.Dir, filepath.FromSlash(subdir))) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_subdir",
			"subdir does not exist: "+filepath.Join(req.Dir, filepath.FromSlash(subdir)))
		return
	}

	// Identity is a freshly allocated random slug — NOT the client's name. It (and the
	// sid it derives) can't collide with an archived/pruned session's jsonl, so a new
	// session never accidentally --resumes a past conversation.
	name := allocSessionName(req.Dir)

	kind := NormalizeKind(req.Kind)
	// An SSM session with no client title defaults to "{host alias} @MMDD-HHMM" — a
	// human-meaningful "target + timestamp" name (vs the generic {home-basename} @… fallback).
	if kind == session.KindSSM && title == "" {
		title = ssmDefaultTitle(req.SSMAlias, req.SSMTarget, time.Now())
	}
	label := ""
	if AgentOf(kind).Caps().UsesLabel {
		label = sessionLabelFor(req.Dir, title, name)
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
	origin, originConv := CreateOrigin(&req)
	meta := session.Meta{
		Name: name, Dir: req.Dir, Subdir: subdir, Model: req.Model, Effort: req.Effort, Mode: req.Mode, Kind: kind, Driver: driver, Title: title, Color: req.Color, Label: label,
		SkipPermissions: req.SkipPermissions,
		Repo:            filepath.Base(req.Dir), Branch: gitx.GitCurrentBranch(req.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: ssm,
		Origin: origin, OriginConv: originConv,
	}
	// docs/log/51 Phase 3, the self-report fast path: add one line to the launch task saying
	// "call af_report when you are done" — only for an instruction that owes a report, i.e.
	// one with report_to. Placed before the managed / tui split so both launch paths carry
	// the same line.
	if req.ReportTo != "" {
		req.InitialPrompt = withSelfReportHint(req.InitialPrompt, meta)
	}
	if meta.DriverKind() == session.DriverManaged {
		// managed (docs/log/27 P2): no tmux pane — the driver opens a thread on the shared
		// runtime. The first prompt needs no boot-screen scraping and can just be sent
		// (§10.2-9 — idempotent through ClientMessageID).
		d, _ := driverOf(meta)
		h, err := mcpx.StartManagedSession(d, meta)
		if err != nil {
			writeRuntimeErr(w, err)
			return
		}
		session.WriteMeta(meta)
		if p := strings.TrimSpace(req.InitialPrompt); p != "" {
			if err := h.Send(agents.TurnInput{Prompt: p}); err != nil {
				log.Printf("managed initial prompt %s: %v", name, err)
			} else {
				markSessionWorking(name)
			}
		}
		// docs/log/51 Phase 2: add one row to the instruction ledger (the old one-bit arm).
		// A managed session has no session-status hook, but completion is picked up through
		// the notify seam and the reconciler.
		if req.ReportTo != "" {
			chatx.AddInstruction(name, req.ReportTo, injectionSource(req.Source))
			recordInjection(name, req.InitialPrompt, injectionSource(req.Source)) // orchestrated start (docs/log/30 ② / docs/log/38)
		} else if s := scheduleInjectionSource(req.Source); s != "" {
			// A session created by a schedule with reporting off: no ledger row (there is
			// nowhere to report to), but remember where the first prompt came from —
			// otherwise the initial_prompt turn alone loses its badge (docs/log/38).
			recordInjection(name, req.InitialPrompt, s)
		}
		writeCreated(meta)
		return
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
	// docs/log/51 Phase 2: raise one instruction row addressed to the launching conversation.
	// Raised even without an initial_prompt: the operator may steer it by hand with
	// send_to_session afterwards.
	// The initial_prompt, when present, is an orchestrated injection (docs/log/30 ② /
	// docs/log/38) — remember it with its origin so the mirror badges its user turn.
	if req.ReportTo != "" {
		chatx.AddInstruction(name, req.ReportTo, injectionSource(req.Source))
		recordInjection(name, req.InitialPrompt, injectionSource(req.Source))
	} else if s := scheduleInjectionSource(req.Source); s != "" {
		recordInjection(name, req.InitialPrompt, s) // schedule with reporting off (same as the managed path)
	}

	writeCreated(meta)
}

// HandleIdempotencyLookup lets a client that lost the create response (a mid-launch
// timeout) find out what became of its request without risking a duplicate: it returns
// 200 + the created session once done, 202 while still launching, or 404 if no such
// create is (or is still) on record. The stdio MCP create_session tool polls this to
// reconcile a timed-out POST instead of retrying it. GET /sessions-idempotency/{key}.
func HandleIdempotencyLookup(w http.ResponseWriter, r *http.Request) {
	e, ok := createLedger.lookup(r.PathValue("key"))
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no create on record for this key")
		return
	}
	if e.state == createDone {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(e.body)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "creating"})
}

// HandleForkSession forks a session's conversation into a NEW session of the same
// kind (POST /sessions/{name}/fork). The fork shares the source's history up to now
// but then diverges independently — each CLI's native fork copies the conversation,
// leaving the source running/intact (claude --fork-session / opencode --session
// --fork / codex fork). The per-kind source id comes from agents.Forker; the fork's
// first launch (via ForkFrom) materializes the copy, and later launches resume it.
//
// An optional body `{"at": "<anchorId>"}` narrows it to a POINT fork (docs/log/55): the new
// session gets the history up to — but not including — that turn, so the user can retake
// it. The anchor is a transcript.Turn.AnchorID the mirror handed out; the kind's
// ForkAtResolver validates it and translates it into that engine's inclusivity. No body
// (or no `at`) keeps the original whole-conversation behaviour, so old clients are
// unaffected.
func HandleForkSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	// The body is optional: an empty one (the pre-docs/log/55 clients, and the "fork the
	// whole thing" path) decodes to EOF, which is not an error here. Anything else that
	// fails to parse IS one — silently forking the whole conversation because a typo'd
	// body was ignored is exactly the outcome §55 refuses to produce.
	var req struct {
		At string `json:"at"`
		// include=true carries over this message and the answer it got (continue from there).
		// The default (false) stops just before this message, so it can be retyped.
		Include bool `json:"include"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "invalid fork request body")
		return
	}
	req.At = strings.TrimSpace(req.At)
	src, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "no_session", "session not found: "+name)
		return
	}
	ag := AgentOf(src.Kind)
	forker, canFork := ag.(agents.Forker)
	if !canFork || !ag.Caps().CanFork {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeForkUnsupportedKind, "this session type does not support forking")
		return
	}
	// "Does point-forking exist at all" depends only on the request, so answer it before
	// looking at the conversation's state. Later on, an `at` sent to an unsupported kind
	// would come back with an unrelated reason such as "no conversation to fork yet",
	// making a design mistake in the flow look like a state problem.
	// Whether the launch route (managed or CLI) fits is the kind's business: the condition
	// differs per kind (opencode/codex need the runtime API, claude only has a TUI), so
	// demanding managed here would reject claude forever. The resolver answers that with
	// ErrForkAtRoute.
	resolver, hasResolver := ag.(agents.ForkAtResolver)
	if req.At != "" && (!hasResolver || !ag.Caps().CanForkAt) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeForkAtUnsupported,
			"this session type cannot fork at a past message")
		return
	}
	if !session.DirExists(src.Dir) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeForkMissingDir, "cannot fork: the working folder does not exist")
		return
	}
	// Resolve the fork point before ForkSource. No resolver depends on ForkSource's result
	// (each reads the conversation itself), so the order is free, and answering here first
	// gives a more concrete reason: telling a TUI session on the wrong launch route "there
	// is no conversation to fork yet" only makes the user add more conversation, and it
	// never starts working.
	// Stop when this fails: "asked for a point, got the whole conversation forked" arrives
	// with plausible-looking history, so the user cannot notice the breakage.
	var forkAt string
	if req.At != "" {
		at, err := resolver.ResolveForkAt(src, agents.ForkPoint{Anchor: req.At, Include: req.Include})
		if err != nil {
			code := errCodeForkBadAnchor
			if errors.Is(err, agents.ErrForkAtRoute) {
				code = errCodeForkAtUnsupported // the launch route, not the fork point, is the problem
			}
			httpx.WriteErr(w, http.StatusBadRequest, code, err.Error())
			return
		}
		forkAt = at
	}
	forkFrom, err := forker.ForkSource(src)
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "not_resumable", err.Error())
		return
	}
	forkName := allocSessionName(src.Dir)
	title, _ := CleanTitle(forkTitle(src))
	// The driver is inherited: forking a managed session stays managed (copied through the
	// runtime's fork API, docs/log/27 P2), while tui keeps the CLI fork launch.
	meta := session.Meta{
		Name: forkName, Dir: src.Dir, Subdir: src.Subdir, Model: src.Model, Effort: src.Effort, Mode: src.Mode,
		Kind: src.Kind, Driver: src.Driver, Title: title, SkipPermissions: src.SkipPermissions,
		Repo:      filepath.Base(src.Dir),
		Branch:    gitx.GitCurrentBranch(src.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), ForkFrom: forkFrom, ForkAt: forkAt,
		// A session grown from a handoff has origin=handoff (ADR 0029 §6). Inheriting the
		// source's origin would blend it into "sessions a human opened" and hide the spend
		// handoffs add. The originating conversation IS inherited from the parent, so a
		// handoff from an operator-started session stays traceable in the same chain.
		Origin: session.OriginHandoff, OriginConv: src.OriginConv,
	}
	if ag.Caps().UsesLabel {
		meta.Label = sessionLabelFor(src.Dir, title, meta.Name)
	}
	if meta.DriverKind() == session.DriverManaged {
		d, ok := driverOf(meta)
		if !ok {
			httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
				"managed driver はこの kind ではまだ利用できません")
			return
		}
		if _, err := mcpx.StartManagedSession(d, meta); err != nil {
			writeRuntimeErr(w, err)
			return
		}
		session.WriteMeta(meta)
		httpx.WriteJSON(w, http.StatusCreated, wireSession(meta, true))
		return
	}
	if err := startSessionTmux(meta, false); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(meta)
	httpx.WriteJSON(w, http.StatusCreated, wireSession(meta, true))
}

// HandleStopSession kills the tmux session and forgets its meta so it stops
// appearing in the list. Tolerates an already-exited session (meta only).
func HandleStopSession(w http.ResponseWriter, r *http.Request) {
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
	// /stop FORGETS the meta — it is the Console's delete. A locked session (docs/log/45)
	// refuses it; stopping without losing the row is /halt, which stays open.
	if hadMeta && meta.Locked {
		httpx.WriteErr(w, http.StatusForbidden, errCodeLocked,
			"session is locked against deletion; unlock it first (or use /halt to stop it and keep the row)")
		return
	}
	if hadMeta {
		status.Remove(session.UUID(meta.Dir, name))
		status.RemoveExit(name)
		dropManagedRuntime(meta) // managed: abort the running turn and forget the handle
		removeManagedLedger(meta)
	}
	if live {
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
			return
		}
	}
	if hadMeta {
		// fold-on-delete (docs/log/46 §3-b): /stop is the Console's delete and forgets the
		// meta right after, so the session leaves ListMetas and is never folded again (even
		// with its transcript still on disk). The ordinary fold leaves the open trailing turn
		// alone, so without finalizing here that last turn never reaches the ledger. Called
		// after killing tmux so the final events written on exit are in the transcript first.
		finalizeSessionUsage(meta)
	}
	status.RemoveCarried(session.UUID(meta.Dir, name))
	session.RemoveMeta(name)
	removeTerminalHistory(name)
	// Stopping forgets the session; if it was the last one in a worktree and that
	// worktree is clean, auto-remove it so worktrees don't pile up (no-op otherwise).
	if hadMeta {
		gitx.MaybePruneWorktree(meta.Dir)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stopped": name})
}

// HandleHaltSession stops a RUNNING session into the stopped (resumable) state: it
// kills the live tmux but KEEPS the meta visible (Archived stays false), so the row
// stays listed and the user can resume it later (claude --resume). This is the
// button counterpart of quitting in the terminal — distinct from /stop (which also
// forgets the meta = removes it from the list) and /archive (which hides it).
// An optional JSON body {"disarm_report":true} additionally cancels a pending
// one-shot operator report (docs/log/30) — sent by the MCP stop_session tool, whose stop
// means "instruction cancelled"; the Console halt sends no body and keeps the arm.
func HandleHaltSession(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		DisarmReport bool `json:"disarm_report"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional (Console sends none)
	if body.DisarmReport {
		// Disarm even when the session turns out to be already stopped below: the
		// operator's intent (cancel the instruction) does not depend on liveness.
		chatx.DisarmSessionReport(name)
	}
	if m.DriverKind() == session.DriverManaged {
		// Promote the carry-over BEFORE DropHandle (docs/log/75 P5): a pending Interaction
		// lives only inside the runtime handle and is gone the moment it is dropped —
		// calling later finds ManagedAlive false and gets nothing.
		PromoteCarriedFor(m)
		// halt on managed means dropping the runtime handle; the daemon is shared, so it
		// keeps running. The meta stays, so the row reads as stopped (resumable) — the same
		// semantics as tui's kill-session. DropHandle aborts the running turn.
		dropManagedRuntime(m)
		status.Remove(session.UUID(m.Dir, name))
		m.StoppedAt = time.Now().Format(time.RFC3339)
		// Re-merge the on-disk lock: the meta snapshot above is seconds old by now and a
		// blind WriteMeta would roll back a lock the user flipped meanwhile.
		m = WriteSessionMetaKeepingLock(m)
		httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
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
	// Promote the carry-over BEFORE killing the pane (docs/log/75 P5). claude's pending
	// state is the on-disk pending-* files and can still be read later, but kiro's approval
	// panel exists only as text in the pane and is gone after kill-session. Promoting is
	// idempotent for claude, and the status.Remove below does not erase the carry-over.
	PromoteCarriedFor(m)
	// Kinds that only flush their resume state on a graceful exit (agy) get a
	// chance to quit on their own; true = the pane already ended, skip the kill.
	stopped := false
	if gs, ok := AgentOf(m.Kind).(agents.GracefulStopper); ok {
		stopped = gs.GracefulStop(m)
	}
	if !stopped {
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
			return
		}
	}
	status.Remove(session.UUID(m.Dir, name))
	// Stamp StoppedAt now so the prune TTL starts here (HandleListSessions would
	// otherwise stamp it on the next poll; doing it here keeps the wire consistent).
	// GracefulStop/kill above can take seconds, so re-merge the on-disk lock instead
	// of writing back the stale snapshot (lost-update guard, same as list).
	m.StoppedAt = time.Now().Format(time.RFC3339)
	m = WriteSessionMetaKeepingLock(m)
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
}

// HandleArchiveSession hides a session from the active list but KEEPS its meta (and
// jsonl), so it can be restored later. Kills the live tmux session if any. This is
// the non-destructive counterpart to stop (which forgets the meta).
func HandleArchiveSession(w http.ResponseWriter, r *http.Request) {
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
	// Promote the carry-over BEFORE killing the pane (docs/log/75 P5, ADR 0055 decision 12).
	// Archiving, like halt, folds a session away while it may still hold a pending modal,
	// and cursor's ACP request, kiro's approval panel and a managed Interaction all live
	// only inside the process: called after kill-session / DropHandle it gets nothing, and
	// the question is lost silently.
	PromoteCarriedFor(m)
	if tn := session.TmuxName(name); tmuxx.HasSession(tn) {
		_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).Run()
	}
	dropManagedRuntime(m) // managed: drop the runtime handle instead of a pane
	status.Remove(session.UUID(m.Dir, name))
	status.RemoveExit(name)
	m.Archived = true
	session.WriteMeta(m)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"archived": name})
}

// HandleRestoreSession brings an archived session back into the active list as a
// stopped session (the user clicks it to resume). The conversation (jsonl) is intact.
func HandleRestoreSession(w http.ResponseWriter, r *http.Request) {
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

// HandleListArchived returns archived sessions (for the restore modal).
func HandleListArchived(w http.ResponseWriter, r *http.Request) {
	sessions := []session.Session{}
	for _, m := range session.ListMetas() {
		if m.Archived {
			sessions = append(sessions, wireSession(m, false))
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// HandleRecreateSession starts a fresh session in the slot while PRESERVING the old
// one: the old session is archived (hidden from the active list but kept + restorable,
// its jsonl intact), NOT discarded, and a new session (fresh slug/sid, same
// title/dir/model/kind) is minted and pre-launched live. Allocating a new slug (hence
// a new sid) rather than reusing the old id lets the fresh session survive detached
// until the browser attaches (a reused id would exit first), so we pre-launch here
// like create — which lets the Console open it straight into chat. Non-destructive:
// the past conversation stays recoverable from the archive. Returns the new (alive)
// session.
func HandleRecreateSession(w http.ResponseWriter, r *http.Request) {
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
	// Promote the carry-over BEFORE killing the pane (docs/log/75 P5, ADR 0055 decision 12).
	// Recreating also folds the old session away, and a pending question / approval request
	// does not survive kill-session / DropHandle — calling later gets nothing.
	PromoteCarriedFor(m)
	if tn := session.TmuxName(name); tmuxx.HasSession(tn) {
		_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).Run()
	}
	dropManagedRuntime(m) // managed: drop the runtime handle instead of a pane
	status.Remove(session.UUID(m.Dir, m.Name))
	status.RemoveExit(m.Name)
	m.Archived = true
	session.WriteMeta(m)

	// Fresh identity, same slot. No ForkFrom — recreate means "start empty", not
	// "re-copy the fork source". The driver is inherited: a slot created managed is
	// recreated managed (docs/log/27 P2).
	newMeta := session.Meta{
		Name: allocSessionName(m.Dir), Dir: m.Dir, Subdir: m.Subdir, Model: m.Model, Effort: m.Effort, Mode: m.Mode,
		Kind: m.Kind, Driver: m.Driver, SkipPermissions: m.SkipPermissions,
		Title: m.Title, Color: m.Color, Repo: m.Repo, Branch: gitx.GitCurrentBranch(m.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: m.SSM,
		// recreate means "make the same slot again, empty", so the origin is inherited (ADR 0029 §6).
		Origin: session.OriginOf(m), OriginConv: m.OriginConv,
	}
	if AgentOf(newMeta.Kind).Caps().UsesLabel {
		newMeta.Label = sessionLabelFor(newMeta.Dir, newMeta.Title, newMeta.Name)
	}
	if newMeta.DriverKind() == session.DriverManaged {
		d, ok := driverOf(newMeta)
		if !ok {
			// Same as fork: silently skipping the launch when the driver is missing would
			// fake alive=true.
			m.Archived = false
			session.WriteMeta(m)
			httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
				"managed driver はこの kind ではまだ利用できません")
			return
		}
		if _, err := mcpx.StartManagedSession(d, newMeta); err != nil {
			m.Archived = false
			session.WriteMeta(m)
			writeRuntimeErr(w, err)
			return
		}
		session.WriteMeta(newMeta)
		httpx.WriteJSON(w, http.StatusOK, wireSession(newMeta, true))
		return
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
