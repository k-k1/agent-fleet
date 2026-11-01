package sessionx

// HTTP handlers for the session API: list / create / fork / stop / halt / archive /
// restore / recreate.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudruns"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/limitsnap"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// LcppLiveModels is lcpp's live-catalog seam: workspace/agent's main package (agent_models.go's
// lcppModels) is the only thing that knows how to reach the chat-engine catalog or a member's
// own llama.cpp connection, and internal/sessionx must not learn the Control Plane exists to
// ask for one — the same reason internal/harness's EngineToken/EngineWindow are func-vars
// rather than direct calls (docs/log/93's own seam). nil until main's init wires it; nil is a
// valid "nothing known yet" answer, matching resolveLiveModel's own handling of an empty
// catalog (docs/log/109).
var LcppLiveModels func(ctx context.Context) []agents.ModelChoice

// The other kinds' live catalogs, read by the create's model check. Behind seams only so a test
// can pin that check's answers: each one shells out to its CLI (copilot starts a whole TUI), which
// a test binary can neither assume nor afford.
var (
	codexModelsFn     = codex.Models
	copilotModelsFn   = copilot.Models
	opencodeModelsFn  = opencode.Models
	opencodeRetiredFn = opencode.Retired
)

// managedRuntimeOf resolves a kind's Managed runtime (agents.ManagedRuntime). The match is
// exact: no NormalizeKind and no AgentOf claude fallback, so an unknown or alternate spelling has
// no runtime and every question below answers false / does nothing.
func managedRuntimeOf(kind string) (agents.ManagedRuntimeOps, bool) {
	a, _ := RegisteredAgent(kind)
	r, ok := a.(agents.ManagedRuntime)
	if !ok {
		return agents.ManagedRuntimeOps{}, false
	}
	return r.ManagedRuntime(), true
}

// ManagedAlive reports a managed session's liveness — the runtime-handle
// counterpart of tmuxx.HasSession (docs/log/27 P2/P3; each kind implements it in its own package).
func ManagedAlive(m session.Meta) bool {
	rt, ok := managedRuntimeOf(m.Kind)
	return ok && rt.Alive(m.Name)
}

// managedBusy reports a managed session has a turn running or queued — the refusal
// condition for an exclusive driver switch (/driver, docs/log/27 §2: a switch always goes
// stop -> drain -> resume, and never happens while busy, which leaves the drain to the
// user waiting for idle).
func managedBusy(m session.Meta) bool {
	rt, ok := managedRuntimeOf(m.Kind)
	return ok && rt.Busy(m.Name)
}

// managedInFlight reports that a managed session's handle holds an entry it took from its queue
// and has not settled (the drivers' ManagedInFlight). Unlike managedBusy, input still queued
// does not count.
func managedInFlight(m session.Meta) bool {
	rt, ok := managedRuntimeOf(m.Kind)
	return ok && rt.InFlight(m.Name)
}

// dropManagedRuntime detaches a managed session from its runtime — the equivalent of the
// tmux kill-session that stop / halt / archive / recreate do: abort the running turn and
// forget the handle. The conversation's source of truth (SQLite / rollout) stays intact,
// so a resume reconnects to it.
func dropManagedRuntime(m session.Meta) {
	if rt, ok := managedRuntimeOf(m.Kind); ok {
		rt.Drop(m.Name)
	}
}

// removeManagedLedger drops the ClientMessageID ledger, discarding the slot's identity with
// it. Only purging a trash archive calls it (through RemoveManagedLedger): halt, archive and
// the trash itself keep it, because each of those can still be resumed.
func removeManagedLedger(m session.Meta) {
	if rt, ok := managedRuntimeOf(m.Kind); ok {
		rt.RemoveLedger(m.Name)
	}
	agents.DropHeld(m.Name, agents.DropTrashed)
	dropPendingPeers(m.Name, "purged from the trash")
	// The run rows stay while the session can still be restored from the trash.
	cloudruns.Drop(m.Name)
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
	ttl, autoArchive := session.StoppedTTL()
	sessions := []session.Session{}
	for name, m := range metas {
		if m.Archived {
			continue // hidden from the active list; restorable via the archive modal
		}
		if live[name] {
			// Running: clear any prior stopped mark so resume resets the clock.
			if m.StoppedAt != "" {
				cur, ok := UpdateSessionMeta(name, func(cur *session.Meta) bool {
					if cur.Archived {
						return false
					}
					cur.StoppedAt = ""
					return true
				})
				if !ok {
					continue // deleted or archived since ListMetas above
				}
				m = cur
				// The slot became alive again (ADR 0096 write site ③) — NOT "something
				// cleared StoppedAt", which HandleRestoreSession also does without starting
				// anything (docs/log/101 §101.8). This is the one branch where the clearing
				// really is a resume.
				fleetgraph.RecordRevive(name)
			}
			s := wireSession(m, true)
			fleetgraph.ObserveState(name, s.State) // write site ⑤: the list already computed it
			// decision 13's "became resolvable for the first time" case: a birth row's own
			// conv is usually empty (no conversation exists yet at create time), and for
			// codex/opencode this list poll — not a relaunch drift — is the only place that
			// ever learns it. Piggybacked on the same observation as ObserveState above, so
			// no new polling.
			if conv := fleetGraphResolveConv(m); conv != "" {
				fleetgraph.ObserveConv(name, conv)
			}
			sessions = append(sessions, s)
			continue
		}
		// Stopped (exited): stamp when first noticed, auto-archive once older than the TTL,
		// otherwise keep it listed as resumable.
		if m.StoppedAt == "" {
			// This is the ONLY point where af first notices the pane is gone, and it covers
			// a workspace stop, a container SIGKILL, a claude crash and a user /exit alike.
			// A session that vanished with a modal open is promoted to a carry-over here
			// (docs/log/75 §75.6.3, trigger 2). It runs before the resume boot hook clears
			// the payload: listing a stopped session always reaches this first.
			PromoteCarriedFor(m)
			cur, ok := UpdateSessionMeta(name, func(cur *session.Meta) bool {
				if cur.Archived {
					return false
				}
				cur.StoppedAt = now.Format(time.RFC3339)
				return true
			})
			if !ok {
				continue // deleted or archived since ListMetas above
			}
			m = cur
			// The end of a run, AS OBSERVED (write site ②) — this poll is the first thing
			// that noticed the pane is gone, which is not when it actually ended.
			reason, code, signal := "", 0, 0
			if e, ok := status.ReadExit(name); ok && e.Reason != "" && e.Reason != "exited" && e.Reason != "stopped" {
				reason, code, signal = e.Reason, e.Code, e.Signal
			}
			fleetgraph.RecordDeath(name, reason, code, signal)
		} else if t, e := time.Parse(time.RFC3339, m.StoppedAt); e == nil && autoArchive && now.Sub(t) > ttl && !m.Locked {
			// The TTL MOVES the row to the shelf; it never deletes (ADR 0097). Deleting here
			// reclaimed nothing — the transcript jsonl and the per-session side files are keyed
			// off the meta and outlive it — so all it bought was an unrecoverable removal no
			// person asked for, the only one in the product that skipped the gz safety net.
			// Reclaiming is deliberate: delete_session, which bundles before it removes.
			//
			// The deletion lock (docs/log/45) exempts a row from this sweep as well. Archiving
			// is reversible, so the lock does not have to refuse it; it stays here because a
			// pinned row is one the user wants to keep SEEING, and the shelf is out of sight.
			//
			// No worktree removal here (nor on any delete, ADR 0101 decision 3): the session is
			// still restorable, and restoring one whose working copy was deleted underneath it is
			// worse than a worktree left standing.
			// The cleanup survey (session_cleanup.go) proposes those, with a person deciding.
			// The lock is judged again on the meta as it is now: one set since ListMetas above
			// exempts the row just the same.
			cur, wrote := UpdateSessionMeta(name, func(cur *session.Meta) bool {
				if cur.Locked || cur.Archived {
					return false
				}
				markArchived(cur)
				return true
			})
			if wrote {
				forgetSessionCaches(name) // this path does not go through ArchiveSession
				finalizeSessionUsage(cur) // fold into the usage ledger at the same moment as before (docs/log/46 §3-b)
				continue
			}
			if cur.Name == "" || cur.Archived {
				continue // deleted or archived since ListMetas above
			}
			sessions = append(sessions, wireSession(cur, false)) // locked meanwhile: stays listed
			continue
		}
		sessions = append(sessions, wireSession(m, false))
	}
	pruneOverviewFacts(func(name string) bool { _, ok := metas[name]; return ok })
	// An archived or deleted session is never wired again, so its background period would
	// otherwise stay in the map for the life of the Agent.
	pruneBackgroundSince(func(name string) bool { m, ok := metas[name]; return ok && !m.Archived })
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
	dirBranch := map[string]string{}
	annotateSessions(sessions, func(dir string) dirInfo {
		b, wt := gitx.GitDirInfo(dir)
		dirBranch[dir] = b
		return dirInfo{branch: b, worktree: wt}
	})
	linkedAt := time.Now()
	annotateLinks(sessions, dirBranch, linkedAt)
	recordBranchPRs(sessions, linkedAt)
	recordInferredLinks(sessions, dirBranch, linkedAt)
	// Stable order: newest first by creation time.
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].CreatedAt > sessions[j].CreatedAt })
	// repoJobs and imageJobs are the only channels telling the CP "no session, but the
	// workspace IS busy" (docs/log/78). An import takes minutes to hours while GET polling
	// deliberately does not count as activity, so without this idle-stop kills a running
	// clone / checkout; the image queue dies with the container the same way.
	// The reaper reads this list on every sweep, so it costs no extra request.
	body := map[string]any{
		"sessions": sessions, "repoJobs": repoJobsRunning(), "imageJobs": imageJobsActive(),
	}
	// The limit snapshot (ADR 0111 decision 6) rides the same response for the reaper, which
	// asks for it. Other callers (the Console's list, through the CP) do not, and pay nothing.
	if r.URL.Query().Get("limits") == "1" {
		body["limits"] = limitsnap.Cached(time.Now())
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// HandleSessionCatalog is the sharing inventory. Unlike the ordinary list it
// includes archived rows, but only kinds with a structured transcript. With ?since= it
// answers only what changed after that cursor (catalog_delta.go).
func HandleSessionCatalog(w http.ResponseWriter, r *http.Request) {
	live := tmuxx.LiveSessionNames()
	items := []session.CatalogEntry{}
	for _, m := range session.ListMetas() {
		if !AgentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		alive := live[m.Name]
		if m.DriverKind() == session.DriverManaged {
			alive = ManagedAlive(m)
		}
		items = append(items, catalogEntry(m, alive))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	since, asked := r.URL.Query()["since"]
	if !asked {
		httpx.WriteJSON(w, http.StatusOK, session.CatalogResponse{Sessions: items})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, catalogChanges.respond(items, since[0]))
}

// catalogEntry builds a catalog row. Only a live session pays for wireSession, whose State
// (with the rate-limit reinterpretation) is the one field the catalog needs from it; a stopped
// one, most of a long history, is read from its meta alone.
func catalogEntry(m session.Meta, alive bool) session.CatalogEntry {
	e := session.CatalogEntry{Name: m.Name, Kind: m.Kind, Dir: m.Dir, Repo: m.Repo,
		WorkingCopyID: gitx.WorkingCopyID(m.Dir), Title: m.Title, Label: m.Label,
		CreatedAt: m.CreatedAt, Alive: alive, Archived: m.Archived}
	if alive {
		e.State = wireSession(m, true).State
	}
	return e
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
	// AllowSharedWorkingCopy is a session-spawned create's explicit opt-in to run in its
	// PARENT's own live working copy (ADR 0073 decision 7, amendment 2026-10-07). Honoured only
	// with worktree=false and only for that one directory; everywhere else it changes nothing,
	// so the spawn_working_copy_busy refusal stays the default.
	AllowSharedWorkingCopy bool `json:"allow_shared_working_copy"`
	// IdempotencyKey dedupes a retried/concurrent create so a client that times out
	// (but whose request the backend actually completed) can't spawn a duplicate on
	// retry. The stdio MCP create_session tool derives it deterministically from the
	// conversation id + launch args, so an LLM re-issuing the same call reproduces the
	// same key. Empty => the server falls back to a report_to-scoped intent fingerprint
	// (createIdempotencyKey); interactive Console launches send neither and aren't deduped.
	IdempotencyKey string `json:"idempotency_key"`
	// IdempotencyDurable keeps the key's outcome on disk until it is looked up or abandoned
	// (session_idempotency.go): the Control Plane's launch from a handoff offer, whose outcome
	// may be asked about days later (issue #2040). Ignored without IdempotencyKey.
	IdempotencyDurable bool   `json:"idempotency_durable"`
	Model              string `json:"model"`
	Effort             string `json:"effort"`
	Mode               string `json:"mode"` // "plan" | "normal"
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
	// StopAfterTurn arms the stop-after-turn (docs/log/85) on the new session, so it folds
	// itself away once it has finished the initial_prompt. Set by the CP scheduler for a
	// schedule with stop_after_run — an unattended fire otherwise leaves its session alive
	// (and its memory held) for the rest of the night.
	//
	// It rides the CREATE rather than a POST that follows, because the arm has to be on disk
	// before the prompt is delivered: an arm set afterwards races that delivery, and a prompt
	// arriving after an arm is exactly what releases it.
	StopAfterTurn bool `json:"stop_after_turn"`
	// SpendCapUSD is the new session's spend budget (#1054). Absent = the user's default
	// (ui-prefs sessionSpendCapUsd); an explicit 0 = no budget, even when a default is set.
	SpendCapUSD *float64 `json:"spend_cap_usd"`
	// Source attributes the initial_prompt injection's origin for the mirror badge
	// (docs/log/38): "schedule" / "schedule-manual" from the CP scheduler; anything else
	// (incl. empty — the operator MCP) records as "operator". Whitelisted server-side.
	Source string `json:"source"`
	// ScheduleID / ScheduleSlot name the scheduled run a CP scheduler create belongs to, as on
	// /input (#1257).
	ScheduleID   string `json:"schedule_id"`
	ScheduleSlot string `json:"schedule_slot"`
	// ScheduleDelivery is the run's own targets and silent sentinel, as on /input (#1560).
	ScheduleDelivery *chatx.ScheduleDelivery `json:"schedule_delivery"`
	// managed: a Managed create, which raises the instruction row as sending and sends
	// initial_prompt carrying its id (instr) (#1257). False on the Terminal route.
	managed bool
	instr   string
	// mcp is who sent this create over the CP's /mcp (HandleMCPCreateSession), or nil. Unexported,
	// so no JSON body can set it: the only way to a non-nil value is that route.
	mcp *mcpOrigin
	// name is the slug a move booked for its target before writing the handover file there
	// (ADR 0115 decision 9). Unexported, so no client can name a session: every other create
	// allocates a fresh slug.
	name string
	// Origin / OriginConv record who STARTED this session (docs/log/46 §2-c, ADR 0029 §6) —
	// a different axis from Source (which attributes one injected prompt). The MCP
	// create_session sends "operator" plus its own conversation slug; the Console sends
	// nothing and defaults to "user". Schedule creates are resolved from Source, so the
	// CP scheduler needs no new wire field. Whitelisted server-side (session.ValidOrigin).
	Origin     string `json:"origin"`
	OriginConv string `json:"origin_conv"`
	// OriginSession names the session this create came from (ADR 0073), under two origins:
	//   - origin=session: the parent. The tool has no argument for it — the MCP layer fills it
	//     from its own $AF_SESSION_NAME, the way peer_from is filled, so the model cannot name
	//     a parent it is not.
	//   - origin=user: the session whose handoff PROPOSAL the Console launch was seeded from,
	//     sent with OriginProposal. This one really does arrive from a client, so it is proved
	//     rather than believed. See CreateOrigin.
	OriginSession string `json:"origin_session"`
	// OriginProposal is which of OriginSession's outstanding handoff proposals seeded this
	// launch. It is not stored — it exists so the lineage can be VERIFIED: proposals live in a
	// file named after the session that made them, so a matching id proves that session really
	// proposed this work. Without it, anyone who can reach the Console API could stamp any
	// session as any other session's origin.
	OriginProposal string `json:"origin_proposal"`
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
	// WorkItem is the item a work-item launch was started for (ADR 0103 decision 8). It is
	// recorded in the meta so a later rename through POST /repos/{name}/branch-name keeps
	// {ref} and the kind. Every other launch leaves it nil.
	WorkItem *session.WorkItemRef `json:"work_item,omitempty"`
	// links / unlinked are a moved session's links (session_move.go), set in-process only: no
	// client can hand a new session links it did not earn.
	links    []session.WorkItemLink
	unlinked []string
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
	// Role chaining (issue #1109): when SSMRoleARN is set, the sso_* fields above describe
	// the source profile SSMSourceProfile and SSMProfile assumes the role from it.
	SSMSourceProfile   string `json:"ssm_source_profile"`
	SSMRoleARN         string `json:"ssm_role_arn"`
	SSMExternalID      string `json:"ssm_external_id"`
	SSMRoleSessionName string `json:"ssm_role_session_name"`
	SSMDurationSeconds int    `json:"ssm_duration_seconds"`
	// The shared sign-in (ADR 0125) the login source is in, and its token's owner; the Control
	// Plane fills them, and an older one leaves them out (a login of its own).
	SSOSession string `json:"sso_session"`
	SSOOwnerID string `json:"sso_owner_id"`
	// SSMForceLogin: drop this profile's cached login and run `aws sso login` at launch
	// (skip the cached-token short-circuit) so the user re-authenticates. One-shot.
	SSMForceLogin bool `json:"ssm_force_login"`
	// Studio binds the new session to an image studio (ADR 0100 decision 2): written onto the
	// meta BEFORE the launch, so the first turn already sees the studio tools. Only the Console
	// sends it; a session starting another session may not (see HandleCreateSession).
	Studio string `json:"studio,omitempty"`
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
	matched := agents.MatchModel(requested, choices)
	matches := make([]string, len(matched))
	for i, m := range matched {
		matches[i] = m.ID
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
		requested, joinModelIDs(nearestModels(strings.ToLower(requested), choices), modelSuggestLimit))
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

// cleanWorkItemRef trims a launch's work item and bounds it: it lives in the meta file,
// which every session listing reads. nil when there is no key, since {ref} and the kind are
// all a rename wants from it.
func cleanWorkItemRef(in *session.WorkItemRef) *session.WorkItemRef {
	if in == nil || strings.TrimSpace(in.Key) == "" {
		return nil
	}
	// Provider in lower case and a Jira key in upper case, as links are keyed (#3134), so a launch
	// item matches the same item linked any other way. A bare GitHub number is left bare: {ref}
	// renders from it.
	provider := strings.ToLower(clipRunes(strings.TrimSpace(in.Provider), 32))
	key := clipRunes(strings.TrimSpace(in.Key), 200)
	if provider == "jira" {
		key = strings.ToUpper(key)
	}
	out := &session.WorkItemRef{
		Provider: provider,
		Key:      key,
		Title:    clipRunes(strings.TrimSpace(in.Title), 300),
		Type:     clipRunes(strings.TrimSpace(in.Type), 64),
	}
	for _, l := range in.Labels {
		if len(out.Labels) == 30 {
			break
		}
		if l = clipRunes(strings.TrimSpace(l), 64); l != "" {
			out.Labels = append(out.Labels, l)
		}
	}
	return out
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
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
// conversation made this expensive purchase"), so it is dropped otherwise. The session name is
// the same idea one axis over (ADR 0073) and it is meaningful under exactly two origins —
// session (the parent that spawned this) and user (the session whose handoff proposal a person
// launched from). Under any other one the request loses it rather than growing a lineage
// nobody can read.
func CreateOrigin(req *CreateReq) (origin, conv, parent string) {
	if req.Origin != "" {
		origin = session.ValidOrigin(req.Origin)
	} else if s := injectionSource(req.Source); s == TurnSourceSchedule || s == TurnSourceScheduleManual {
		origin = session.OriginSchedule
	} else {
		origin = session.OriginUser
	}
	switch origin {
	case session.OriginOperator:
		conv = req.OriginConv
	case session.OriginSession:
		// A create claiming origin=session without naming a parent would produce a child
		// nobody owns: no steering, no budget, and the recursion limit reads it as a root.
		// Degrade to the unlabeled default rather than record that.
		if !session.ValidName(req.OriginSession) {
			return session.OriginUser, "", ""
		}
		parent = req.OriginSession
	case session.OriginUser:
		parent = handoffProposalLineage(req.OriginSession, req.OriginProposal)
	}
	return origin, conv, parent
}

// handoffProposalLineage answers which session's handoff proposal seeded a Console launch —
// "" when the request cannot prove one (ADR 0073 decision 1, amendment 2026-09-10).
//
// The origin stays user and nothing else about the create changes: the session is not a child.
// It does not spend the proposing session's budget, that session cannot steer it or see it in
// list_child_sessions, and it may spawn children of its own — a person opened it, and decision
// 5's re-entry is exactly that. What is recorded is only "this work came from over there".
//
// ⚠️ VERIFIED, not believed. Everywhere else origin_session is resolved from the MCP server's
// own $AF_SESSION_NAME, which no model can write; this pair arrives from a browser, so taken on
// trust it would let anyone who can reach POST /sessions stamp an arbitrary lineage on an
// arbitrary session. The proof costs one small file read, because proposals are stored per
// PROPOSING session (HandoffProposalPath): an id that is on that session's file could only have
// been put there by that session. Naming another session's proposal id fails for the same
// reason — it is not in the file being read.
//
// A proposal already badged as launched still counts. Launching is not consuming: the badge is
// display, discarding is the user's call, and a second session grown from the same proposal has
// the same provenance as the first.
func handoffProposalLineage(from, proposalID string) string {
	from, proposalID = strings.TrimSpace(from), strings.TrimSpace(proposalID)
	if !session.ValidName(from) || proposalID == "" {
		return ""
	}
	list, err := ReadHandoffProposals(from)
	if err != nil {
		return ""
	}
	for _, p := range list {
		if p.ID == proposalID {
			return from
		}
	}
	return ""
}

// HandleCreateSession launches a claude session inside a detached tmux session.
func HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	createSession(w, r, req)
}

// createSession is the create after its body is decoded, so a move can create its target
// in-process with the name it booked (CreateReq.name).
func createSession(w http.ResponseWriter, r *http.Request, req CreateReq) {
	c := &createRun{w: w, r: r, req: &req}
	if !c.validateKind() || !c.claim() {
		return
	}
	defer c.settleClaim()
	if !c.refuseStart() {
		return
	}
	// The failure net for a reserved slot: every path that ends without a meta. A no-op for a
	// create that is not a spawn.
	defer c.slot.release()
	if !c.validateOptions() || !c.resolveDriver() || !c.checkModel() || !c.resolveWorkingCopy() || !c.identify() {
		return
	}
	meta, ok := c.armMeta()
	if !ok {
		return
	}
	c.launch(meta)
}

// deliverInitialPromptFn is deliverInitialPrompt behind a seam. The ordering noteCreateOrigin
// documents — record, THEN deliver — is a requirement, not an incidental line order (ADR 0073
// decision 14), and the only way to observe it from a test is to be the delivery.
var deliverInitialPromptFn = deliverInitialPrompt

// createTurnOrigin is the TurnInput origin of create_session's initial prompt. It follows
// noteCreateOrigin's branches, in the same order, so the driver and the mirror's badge name the
// same origin for the launch task.
func createTurnOrigin(req *CreateReq, spawnParent string) agents.Origin {
	switch {
	case req.mcp != nil:
		return turnOrigin(TurnSourceMCP, "")
	case req.ReportTo != "":
		return turnOrigin(injectionSource(req.Source), "")
	case scheduleInjectionSource(req.Source) != "":
		return turnOrigin(scheduleInjectionSource(req.Source), "")
	case spawnParent != "":
		return turnOrigin(TurnSourceSpawn, spawnParent)
	default:
		return turnOrigin("", "")
	}
}

// delivery is the scheduled run's own delivery (#1560), or nil.
func (req *CreateReq) delivery() *chatx.ScheduleDelivery {
	d := scheduleDeliveryOf(req.Source, req.ScheduleID, req.ScheduleSlot, req.ScheduleDelivery)
	if d != nil {
		d.PromptSum = chatx.PromptSum(req.InitialPrompt) // the launch task as it lands in the transcript
	}
	return d
}

// noteCreateOrigin remembers who a new session's launch task came from, so the mirror can badge
// its first user turn instead of rendering it as something the user typed.
//
// It must run BEFORE either launch path delivers the prompt. The record is matched to the turn
// by text when the turn appears, and delivery is asynchronous on the tui path and immediate on
// the managed one — recording afterwards is a race that loses the badge exactly when the turn
// arrives quickly. (It used to sit after delivery on both paths; ADR 0073 decision 14 moved it,
// which is also what badgeOriginOf's comment was written in anticipation of.)
//
// docs/log/51 Phase 2: the instruction ledger row is raised only for report_to — even with no
// initial_prompt, because the operator may steer the session by hand afterwards. The other two
// branches record provenance only; there is nowhere for them to report to.
func noteCreateOrigin(name string, req *CreateReq, spawnParent, origin string) {
	hasPrompt := strings.TrimSpace(req.InitialPrompt) != ""
	switch {
	case req.mcp != nil:
		// The same record /mcp-input makes: a badge, and nothing else. No ledger row, no
		// Discord mirror and no fleet-graph arrow (fleetGraphActorFor drops TurnSourceMCP), and
		// ahead of the default branch, which would draw the prompt as the member's own.
		recordInjection(name, req.InitialPrompt, TurnSourceMCP)
	case req.ReportTo != "" || req.delivery() != nil:
		req.instr = chatx.AddScheduledInstruction(name, req.ReportTo, injectionSource(req.Source), req.delivery(), req.managed)
		recordInjection(name, req.InitialPrompt, injectionSource(req.Source)) // orchestrated start (docs/log/30 ② / docs/log/38)
		if hasPrompt {
			recordFleetGraphInstruct(name, injectionSource(req.Source), req.ReportTo, "", req.InitialPrompt)
		}
	case scheduleInjectionSource(req.Source) != "":
		// A session created by a schedule with reporting off: no ledger row, but remember
		// where the first prompt came from — otherwise that turn loses its badge (docs/log/38).
		recordInjection(name, req.InitialPrompt, scheduleInjectionSource(req.Source))
		if hasPrompt {
			recordFleetGraphInstruct(name, scheduleInjectionSource(req.Source), "", "", req.InitialPrompt)
		}
	case spawnParent != "":
		// A child another session started (ADR 0073). Its create carries no report_to, so
		// without this the launch task — envelope and all — renders as the user's own input.
		//
		// No fleet-graph InstructEvent here: the parent→child edge is already the BirthEvent
		// itself (origin=session, originSession=parent — recordFleetGraphBirth), and the
		// view draws it as a "spawn" arrow (ArrowVariant). An InstructEvent alongside it
		// would draw the same handoff twice.
		recordInjection(name, req.InitialPrompt, TurnSourceSpawn)
	default:
		// The ordinary Console launch (or one seeded from a handoff proposal): no
		// report_to, no schedule tag, not a spawn. No mirror badge either — it genuinely
		// is the user's own instruction — but the fleet graph still needs the arrow, or
		// the session's very first turn has no visible cause (ActorId "user", decision 4).
		// Restricted to origin=user/handoff: an operator/MCP call that simply chose not
		// to set report_to is NOT a person, and must not be drawn as one.
		if hasPrompt && (origin == session.OriginUser || origin == session.OriginHandoff) {
			fleetgraph.RecordInstruct("user", name, "", req.InitialPrompt)
		}
	}
}

// HandleIdempotencyLookup lets a client that lost the create response (a mid-launch
// timeout) find out what became of its request without risking a duplicate: it returns
// 200 + the created session once done, 202 while still launching, or 404 if no such
// create is (or is still) on record. The stdio MCP create_session tool polls this to
// reconcile a timed-out POST instead of retrying it. GET /sessions-idempotency/{key}.
//
// A durable key (session_idempotency.go) is also answered from its record on disk: 404
// not_created once it was abandoned or refused, 503 create_outcome_unknown when the record
// cannot be read or its create was cut off by a restart. A plain miss, or a key only reserved,
// stays not_found, which proves nothing: a create may not have begun yet. Only abandon turns a
// reserved key into a guarantee.
func HandleIdempotencyLookup(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	e, ok := createLedger.lookup(key)
	if !ok || e.state == createReserved {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no create on record for this key")
		return
	}
	writeCreateState(w, key, e)
}

// HandleIdempotencyReserve records a durable key before its create is sent (issue #2040), so
// that abandon may later answer "not created" for it. The answer names the key, which is how
// the caller tells this Agent from one without the route.
// POST /sessions-idempotency/{key}/reserve.
func HandleIdempotencyReserve(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if strings.TrimSpace(key) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_key", "an idempotency key is required")
		return
	}
	if err := createLedger.reserve(key); err != nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "create_outcome_unknown", "the key could not be recorded")
		return
	}
	w.Header().Set(createKeyHeader, key)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "reserved"})
}

// HandleIdempotencyAbandon is the lookup that settles a reserved key (issue #2040): when no
// create under it began, it records the key as abandoned, so a create under it that arrives
// later is refused (create_abandoned) and the 404 not_created it answers is a guarantee. A
// create that already began is reported as the lookup would; a key with no record at all is
// unknown (503), since an Agent that kept no record may have created under it.
// POST /sessions-idempotency/{key}/abandon.
func HandleIdempotencyAbandon(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if strings.TrimSpace(key) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_key", "an idempotency key is required")
		return
	}
	writeCreateState(w, key, createLedger.abandon(key))
}

func writeCreateState(w http.ResponseWriter, key string, e createLedgerEntry) {
	w.Header().Set(createKeyHeader, key)
	switch e.state {
	case createDone:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(e.body)
	case createInflight:
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "creating"})
	case createAbandoned, createRefused:
		httpx.WriteErr(w, http.StatusNotFound, "not_created", "no session was or will be created under this key")
	default:
		httpx.WriteErr(w, http.StatusServiceUnavailable, "create_outcome_unknown", "the outcome of the create under this key is unknown")
	}
}

// createKeyHeader and createOutcomeHeader are how a create's own answer names its key and
// outcome. A gateway or proxy between the caller and the Agent answers errors of its own
// (a 504 with a JSON body, say), and those must not read as "nothing was created".
const (
	createKeyHeader     = "X-AF-Create-Key"
	createOutcomeHeader = "X-AF-Create-Outcome"
)

// createOutcomeWriter stamps the answer of a create that owns its key: created for a 2xx,
// refused for anything else — the handler wrote an error and the ledger drops the key.
type createOutcomeWriter struct {
	http.ResponseWriter
	key   string
	wrote bool
}

func (c *createOutcomeWriter) WriteHeader(status int) {
	if !c.wrote {
		c.wrote = true
		outcome := "refused"
		if status >= 200 && status < 300 {
			outcome = "created"
		}
		c.Header().Set(createKeyHeader, c.key)
		c.Header().Set(createOutcomeHeader, outcome)
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *createOutcomeWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
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
	// The fork launches a new process of this kind: refuse it, with the reason, while its CLI is
	// missing, before anything is created.
	if ref := startRefusal(src.Kind); ref != nil {
		httpx.WriteErr(w, ref.Status, ref.Code, ref.Message)
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
	var forkAt, pinned string
	if req.At != "" {
		var at string
		var err error
		if p, ok := ag.(agents.ForkAtPinner); ok {
			at, pinned, err = p.ResolveForkAtPinned(src, agents.ForkPoint{Anchor: req.At, Include: req.Include})
		} else {
			at, err = resolver.ResolveForkAt(src, agents.ForkPoint{Anchor: req.At, Include: req.Include})
		}
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
	// The point was resolved against `pinned`; forking another conversation with it (or, for a
	// point that means "everything", forking one the user did not look at) is refused.
	if pinned != "" && forkFrom != pinned {
		httpx.WriteErr(w, http.StatusConflict, errCodeForkBadAnchor,
			"the conversation changed while the fork point was being resolved; pick the message again")
		return
	}
	forkName := allocSessionName(src.Dir)
	title, _ := CleanTitle(forkTitle(src))
	// The driver is inherited: forking a managed session stays managed (copied through the
	// runtime's fork API, docs/log/27 P2), while tui keeps the CLI fork launch.
	meta := forkMeta(src, forkName, title, forkFrom, forkAt)

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
		recordFleetGraphBirth(meta)
		httpx.WriteJSON(w, http.StatusCreated, wireSession(meta, true))
		return
	}
	if err := startSessionTmux(meta, false); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(meta)
	recordFleetGraphBirth(meta)
	httpx.WriteJSON(w, http.StatusCreated, wireSession(meta, true))
}

// HandleStopSession is the old name of "delete this session": it stops the session if it is
// running and moves it to the trash (ADR 0101 decision 1). The name stays so a Console older
// than the Agent still lands in the trash; a current Console sends DELETE /sessions/{name}?stop=1,
// which is the same thing. Stopping WITHOUT losing the row is /halt.
//
// It never touches the working copy (ADR 0101 decision 3): until then a delete that left a
// clean worktree without sessions removed it on the way out, so a session restored from the
// trash could come back to a folder that was gone.
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
	if !hadMeta {
		// A pane with no meta (an orphan) has nothing to put in the trash: end it, that is all.
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", fmt.Sprintf("%v: %s", err, out))
			return
		}
		fleetgraph.RecordDeath(name, "", 0, 0)
		removeTerminalHistory(name)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"stopped": name})
		return
	}
	arch, code, err := trashSession(meta, true)
	if err != nil {
		WriteTrashErr(w, code, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stopped": name, "deleted": name, "archive": arch})
}

// Error codes of TrashSession besides the shared errCodeLocked. Stable strings the Console
// maps (ERR_TEXT); session_running is the one handleDeleteSession has always returned.
const (
	TrashErrRunning = "session_running"
	// TrashErrResumed: the session came back to life (or its transcript grew) while it was
	// being moved to the trash, so nothing was deleted. Its own code, not session_running: a
	// Console that answers session_running by stopping and retrying must not stop a session
	// someone just resumed.
	TrashErrResumed = "session_resumed"
	TrashErrArchive = "archive_failed"
	TrashErrStop    = "tmux_failed"
)

// WriteTrashErr turns TrashSession's error code into the HTTP answer.
func WriteTrashErr(w http.ResponseWriter, code string, err error) {
	status := http.StatusInternalServerError
	switch code {
	case errCodeLocked:
		status = http.StatusForbidden
	case TrashErrRunning, TrashErrResumed:
		status = http.StatusConflict
	case "not_found":
		status = http.StatusNotFound
	}
	httpx.WriteErr(w, status, code, err.Error())
}

// HaltSession is haltSessionMeta for package main: stop a running session into the stopped,
// resumable state and return the meta as written. Deleting with ?stop=1 stops through it, so
// a delete folds a live session away by exactly the steps halt uses (the carry-over promoted
// first, agy given its graceful quit).
//
// It does NOT promote the carry-over (docs/log/75 P5): promoting is what tells a person "this
// session is waiting for your answer", and the session is about to go to the trash — the
// notice would point at a session that is gone.
func HaltSession(m session.Meta) (session.Meta, error) { return haltSession(m, false) }

// ForgetRuntime drops what the Agent holds in memory and in status files for a session it is
// about to forget: the managed handle, the live-state and exit records, the carried-over
// question. Called by the trash after the session's archive is written.
func ForgetRuntime(m session.Meta) {
	sid := session.UUID(m.Dir, m.Name)
	dropManagedRuntime(m)
	agents.DropHeld(m.Name, agents.DropTrashed)
	dropPendingPeers(m.Name, "moved to the trash")
	status.Remove(sid)
	status.RemoveExit(m.Name)
	status.RemoveCarried(sid)
	status.RemoveRestoreNotes(sid)
	forgetSessionCaches(m.Name)
}

// forgetSessionCaches drops the per-name read caches (folded file list, priced spend) of a
// session being folded away by delete, archive or recreate. Both are rebuilt from the
// transcript on the next read, so a restore loses nothing; left in place the spend map would
// grow by one entry per session for the Agent's lifetime. Halt does not call it: a halted
// session is resumed in place and its entries are still valid. Each forget takes only its own
// mutex, briefly, so it nests under no other lock.
func forgetSessionCaches(name string) {
	forgetSessionFiles(name)
	forgetSpend(name)
}

// RemoveManagedLedger is removeManagedLedger for package main. The trash keeps the ledger (a
// session in it can be restored and resumed); purging the archive is what drops it.
func RemoveManagedLedger(m session.Meta) { removeManagedLedger(m) }

// HandleHaltSession stops a RUNNING session into the stopped (resumable) state: it
// kills the live tmux but KEEPS the meta visible (Archived stays false), so the row
// stays listed and the user can resume it later (claude --resume). This is the
// button counterpart of quitting in the terminal — distinct from /stop (the old name of
// delete: it moves the session to the trash) and /archive (which hides it).
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
	m, err := haltSessionMeta(m)
	if body.DisarmReport {
		// The operator withdrew its instructions, so its prompts held for the next start go
		// too (#1257); peer messages and scheduled prompts stay. After the halt, so the live
		// queue cannot start one of them in between.
		agents.DropHeldOrigin(name, agents.OriginOperator, agents.DropWithdrawn)
	}
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
}

// haltSessionMeta is the halt itself, without the HTTP shell: kill the live pane (or drop the
// managed handle) and leave the meta listed and resumable. It returns the meta as written.
//
// Extracted so the "stop after this turn" arm (session_stop_after_turn.go, docs/log/85) folds a
// session away through THIS path rather than growing a second one. Every step below is
// ordering that was learned the hard way — the carry-over promoted before the process that
// holds the interaction dies, the bridge disconnected before the pane goes, agy given its
// graceful quit — and a second folding path silently misses them (ADR 0055 decision 12).
func haltSessionMeta(m session.Meta) (session.Meta, error) { return haltSession(m, true) }

// haltSession is haltSessionMeta with the carry-over promotion made optional: true for every
// halt that leaves a resumable row behind, false only for a delete (HaltSession).
func haltSession(m session.Meta, promote bool) (session.Meta, error) {
	name := m.Name
	if m.DriverKind() == session.DriverManaged {
		// Promote the carry-over BEFORE DropHandle (docs/log/75 P5): a pending Interaction
		// lives only inside the runtime handle and is gone the moment it is dropped —
		// calling later finds ManagedAlive false and gets nothing.
		if promote {
			PromoteCarriedFor(m)
		}
		// halt on managed means dropping the runtime handle; the daemon is shared, so it
		// keeps running. The meta stays, so the row reads as stopped (resumable) — the same
		// semantics as tui's kill-session. DropHandle aborts the running turn.
		dropManagedRuntime(m)
		status.Remove(session.UUID(m.Dir, name))
		fleetgraph.RecordDeath(name, "", 0, 0) // halt: a deliberate stop, same as write site ②
		return stampHalted(m), nil
	}
	tn := session.TmuxName(name)
	if !tmuxx.HasSession(tn) {
		return clearStopArm(m), nil // already stopped — nothing to do
	}
	// Best-effort: disconnect any active Remote Control bridge before killing the
	// pane, so a later resume's autoconnect registers fresh under the current
	// title instead of resuming the stale one (see disconnectRemoteControl).
	disconnectRemoteControl(name, m)
	// Promote the carry-over BEFORE killing the pane (docs/log/75 P5). claude's pending
	// state is the on-disk pending-* files and can still be read later, but kiro's approval
	// panel exists only as text in the pane and is gone after kill-session. Promoting is
	// idempotent for claude, and the status.Remove below does not erase the carry-over.
	if promote {
		PromoteCarriedFor(m)
	}
	// Kinds that only flush their resume state on a graceful exit (agy) get a
	// chance to quit on their own; true = the pane already ended, skip the kill.
	stopped := false
	if gs, ok := AgentOf(m.Kind).(agents.GracefulStopper); ok {
		stopped = gs.GracefulStop(m)
	}
	if !stopped {
		if out, err := tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).CombinedOutput(); err != nil {
			return m, fmt.Errorf("%v: %s", err, out)
		}
	}
	status.Remove(session.UUID(m.Dir, name))
	// Stamp StoppedAt now so the prune TTL starts here (HandleListSessions would
	// otherwise stamp it on the next poll; doing it here keeps the wire consistent).
	fleetgraph.RecordDeath(name, "", 0, 0) // halt: a deliberate stop, same as write site ②
	return stampHalted(m), nil
}

// stampHalted records a halt on the meta as it is on disk now, not on m: m was read before a
// kill / GracefulStop / DropHandle that can take seconds, and writing it back would roll back
// whatever changed meanwhile — a deletion lock among them (issue #950). A stop also consumes
// the arm whoever pressed it (docs/log/85): left on disk it would ride through the resume and
// fold the session away again at the end of a turn nobody armed. m, stamped the same way, is
// what comes back when the meta is gone (deleted meanwhile — nothing to write).
func stampHalted(m session.Meta) session.Meta {
	stoppedAt := time.Now().Format(time.RFC3339)
	halt := func(cur *session.Meta) bool {
		cur.StoppedAt = stoppedAt
		cur.StopAfterTurnAt = ""
		return true
	}
	if cur, ok := UpdateSessionMeta(m.Name, halt); ok {
		return cur
	}
	halt(&m)
	return m
}

// markArchived puts m on the shelf. A row that is already archived keeps its date: archiving
// twice must not push back the removal of its ~/.af-work folder.
func markArchived(m *session.Meta) {
	if !m.Archived || m.ArchivedAt == "" {
		m.ArchivedAt = time.Now().UTC().Format(time.RFC3339)
	}
	m.Archived = true
}

// clearArchived takes m off the shelf; the date goes with it so a later archive starts a new
// week.
func clearArchived(m *session.Meta) {
	m.Archived = false
	m.ArchivedAt = ""
}

// HandleArchiveSession hides a session from the active list but KEEPS its meta (and
// jsonl), so it can be restored later. Kills the live tmux session if any. This is
// the non-destructive counterpart to delete (which moves the session to the trash).
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
	ArchiveSession(m)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"archived": name})
}

// ArchiveSession is the archive itself, without the HTTP shell: fold the session away
// (killing a live pane or dropping the managed handle) and move it to the shelf. Deleting a
// working copy calls it too, for the stopped AI sessions that lived in it (ADR 0101
// decision 4): the working copy goes, the conversation stays on the shelf.
func ArchiveSession(m session.Meta) {
	name := m.Name
	// Captured BEFORE the kill below: HandleListSessions (write site ②) never sees this
	// session again once it is archived (`if m.Archived { continue }`), so this is the
	// ONLY chance to record that a run actually ended here. Skipping it for an
	// already-stopped session avoids a second death on top of the one list already wrote.
	tn := session.TmuxName(name)
	wasAlive := tmuxx.HasSession(tn) || (m.DriverKind() == session.DriverManaged && ManagedAlive(m))
	// Promote the carry-over BEFORE killing the pane (docs/log/75 P5, ADR 0055 decision 12).
	// Archiving, like halt, folds a session away while it may still hold a pending modal,
	// and cursor's ACP request, kiro's approval panel and a managed Interaction all live
	// only inside the process: called after kill-session / DropHandle it gets nothing, and
	// the question is lost silently.
	PromoteCarriedFor(m)
	if tmuxx.HasSession(tn) {
		_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).Run()
	}
	dropManagedRuntime(m) // managed: drop the runtime handle instead of a pane
	// A held input waits for the next start, and an archived session has none.
	agents.DropHeld(name, agents.DropArchived)
	dropPendingPeers(name, "archived")
	forgetSessionCaches(name)
	status.Remove(session.UUID(m.Dir, name))
	status.RemoveExit(name)
	if wasAlive {
		fleetgraph.RecordDeath(name, "", 0, 0) // write site ④, part 1: end the run being folded away
	}
	// Written onto the meta as it is now: m is a snapshot (a working-copy delete passes one from
	// ListMetas), and writing it back would roll back a lock set meanwhile — or bring back a
	// session deleted meanwhile.
	if _, ok := UpdateSessionMeta(name, func(cur *session.Meta) bool {
		markArchived(cur)
		// Archiving folds the session away too, so it consumes the stop-after-turn arm for the
		// same reason halt does (docs/log/85): an arm surviving into the restore would stop the
		// session again at the end of a turn nobody armed.
		cur.StopAfterTurnAt = ""
		return true
	}); !ok {
		return
	}
	fleetgraph.RecordArchived(name, true) // write site ④, part 2: AFTER the death above, never before
}

// HandleRestoreSession brings an archived session back into the active list as a
// stopped session (the user clicks it to resume). The conversation (jsonl) is intact.
func HandleRestoreSession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	m, ok := RestoreSession(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wireSession(m, false))
}

// RestoreSession takes a session off the shelf, without the HTTP shell. Restoring a deleted
// worktree from the trash calls it too, for the sessions that delete shelved.
func RestoreSession(name string) (session.Meta, bool) {
	// Under the meta lock, on the meta as it is now: a lock set, or a delete, since the read
	// above must not be rolled back (ADR 0101).
	m, ok := UpdateSessionMeta(name, func(m *session.Meta) bool {
		clearArchived(m)
		m.StoppedAt = "" // re-stamped on next list, resetting the prune clock
		return true
	})
	if !ok {
		return m, false
	}
	// Restore only un-hides the row (§101.8: it never starts anything — the caller answers a
	// STOPPED session). RecordRevive belongs to the branch in HandleListSessions that observes
	// the slot actually come back alive.
	fleetgraph.RecordArchived(name, false)
	return m, true
}

// HandleListArchived returns archived sessions (for the restore modal) as slim rows built from
// the meta (wireArchivedRow), not the full live wire.
func HandleListArchived(w http.ResponseWriter, r *http.Request) {
	sessions := []session.Session{}
	for _, m := range session.ListMetas() {
		if m.Archived {
			sessions = append(sessions, wireArchivedRow(m))
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
	lease, ok := enterSessionGate(w, r, name)
	if !ok {
		return
	}
	defer lease.Release()
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	// Before the old identity is archived and killed: a recreate whose CLI is missing would only
	// fail after destroying the running session.
	if ref := startRefusal(m.Kind); ref != nil {
		httpx.WriteErr(w, ref.Status, ref.Code, ref.Message)
		return
	}
	// Archive the old identity: kill its tmux, clear the live status cache, hide it from
	// the active list. Keep the meta + jsonl (and any captured resume id) so it restores.
	// Captured BEFORE the kill below, same reason as HandleArchiveSession: once Archived is
	// true, HandleListSessions never looks at this name again, so this is the only chance
	// to record that the run actually ended.
	tn := session.TmuxName(name)
	wasAlive := tmuxx.HasSession(tn) || (m.DriverKind() == session.DriverManaged && ManagedAlive(m))
	// Promote the carry-over BEFORE killing the pane (docs/log/75 P5, ADR 0055 decision 12).
	// Recreating also folds the old session away, and a pending question / approval request
	// does not survive kill-session / DropHandle — calling later gets nothing.
	PromoteCarriedFor(m)
	if tmuxx.HasSession(tn) {
		_ = tmuxx.Cmd("kill-session", "-t", session.ExactTarget(tn)).Run()
	}
	dropManagedRuntime(m) // managed: drop the runtime handle instead of a pane
	agents.DropHeld(m.Name, agents.DropRecreated)
	dropPendingPeers(m.Name, "recreated: the old session is archived")
	forgetSessionCaches(m.Name)
	status.Remove(session.UUID(m.Dir, m.Name))
	status.RemoveExit(m.Name)
	if wasAlive {
		fleetgraph.RecordDeath(m.Name, "", 0, 0) // write site ④, part 1: end the run being folded away
	}
	// Onto the meta as it is now, not m: the kill above takes seconds, and m written back would
	// roll back a lock set meanwhile (issue #950). Gone meanwhile = deleted, and a recreate of a
	// deleted session would bring its slot back, so stop here.
	if _, ok := UpdateSessionMeta(m.Name, func(cur *session.Meta) bool {
		markArchived(cur)
		return true
	}); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	fleetgraph.RecordArchived(m.Name, true) // write site ④, part 2: AFTER the death above, never before

	// Fresh identity, same slot. No ForkFrom — recreate means "start empty", not
	// "re-copy the fork source". The driver is inherited: a slot created managed is
	// recreated managed (docs/log/27 P2).
	newMeta := session.Meta{
		Name: allocSessionName(m.Dir), Dir: m.Dir, Subdir: m.Subdir, Model: m.Model, Effort: m.Effort, Mode: m.Mode,
		Kind: m.Kind, Driver: m.Driver, SkipPermissions: m.SkipPermissions, SpendCapUSD: m.SpendCapUSD,
		MuseContributorMode: m.MuseContributorMode, Title: m.Title, Color: m.Color, Repo: m.Repo, Branch: gitx.GitCurrentBranch(m.Dir),
		CreatedAt: time.Now().Format(time.RFC3339), SSM: m.SSM,
		// recreate means "make the same slot again, empty", so the origin is inherited (ADR 0029 §6).
		Origin: session.OriginOf(m), OriginConv: m.OriginConv, OriginSession: m.OriginSession,
		// The same slot keeps its studio (ADR 0100 decision 2); rebindStudioOnRecreate below moves
		// the studio's own `session` over, or drops this copy when it cannot.
		Studio: m.Studio, WorkItem: m.WorkItem, Links: m.Links, Unlinked: m.Unlinked,
	}
	if AgentOf(newMeta.Kind).Caps().UsesLabel {
		newMeta.Label = sessionLabelFor(newMeta.Dir, newMeta.Title, newMeta.Name)
	}
	// The new slot takes the studio over before it launches, for the same reason as a create;
	// a launch that fails hands it back to the old slot.
	rebindStudioOnRecreate(&newMeta, m.Name)
	undoStudio := func() {
		if newMeta.Studio != "" && imagegen.BindStudioSession != nil {
			_, _ = imagegen.BindStudioSession(newMeta.Studio, m.Name, newMeta.Name)
		}
	}
	if newMeta.DriverKind() == session.DriverManaged {
		d, ok := driverOf(newMeta)
		if !ok {
			undoStudio()
			// Same as fork: silently skipping the launch when the driver is missing would
			// fake alive=true.
			unarchiveAfterFailedRecreate(m.Name)
			httpx.WriteErr(w, http.StatusNotImplemented, "driver_unavailable",
				"managed driver はこの kind ではまだ利用できません")
			return
		}
		if _, err := mcpx.StartManagedSession(d, newMeta); err != nil {
			undoStudio()
			unarchiveAfterFailedRecreate(m.Name)
			writeRuntimeErr(w, err)
			return
		}
		session.WriteMeta(newMeta)
		recordFleetGraphBirth(newMeta)
		handOverSpawnLineage(m.Name)
		httpx.WriteJSON(w, http.StatusOK, wireSession(newMeta, true))
		return
	}
	if err := launchTmuxFn(newMeta, false); err != nil {
		undoStudio()
		// Un-archive the old session so a launch failure doesn't silently drop it from
		// the active list.
		unarchiveAfterFailedRecreate(m.Name)
		httpx.WriteErr(w, http.StatusInternalServerError, "tmux_failed", err.Error())
		return
	}
	session.WriteMeta(newMeta)
	recordFleetGraphBirth(newMeta)
	handOverSpawnLineage(m.Name)
	httpx.WriteJSON(w, http.StatusOK, wireSession(newMeta, true))
}

// unarchiveAfterFailedRecreate puts the old slot back on the active list when the successor
// never launched, so a launch failure does not silently drop it. On the meta as it is now: the
// failed launch took seconds, and a lock set meanwhile must survive (issue #950).
func unarchiveAfterFailedRecreate(name string) {
	if _, ok := UpdateSessionMeta(name, func(cur *session.Meta) bool {
		clearArchived(cur)
		return true
	}); ok {
		fleetgraph.RecordArchived(name, false)
	}
}

// forkSids is the ancestry a fork of src records: src's own, then src. The whole chain, not
// just the parent — a fork of a fork carries the grandparent's pasted paths too
// (session.Meta.ForkSids).
func forkSids(src session.Meta) []string {
	return append(slices.Clone(src.ForkSids), session.UUID(src.Dir, src.Name))
}

// forkMeta is the meta a fork of src starts with. Its own function so what a fork records —
// the ancestry the cache orphan scan relies on above all — can be checked without driving a
// real fork, which needs a real source conversation.
func forkMeta(src session.Meta, forkName, title, forkFrom, forkAt string) session.Meta {
	now := time.Now()
	return session.Meta{
		Name: forkName, Dir: src.Dir, Subdir: src.Subdir, Model: src.Model, Effort: src.Effort, Mode: src.Mode,
		Kind: src.Kind, Driver: src.Driver, Title: title, SkipPermissions: src.SkipPermissions,
		MuseContributorMode: src.MuseContributorMode,
		// The budget value carries over, the hit does not: the fork's spend starts from the
		// instant it was made, to the sub-second (SpendFrom), so it has spent nothing yet.
		SpendCapUSD: src.SpendCapUSD, SpendFrom: now.Format(time.RFC3339Nano),
		Repo:      filepath.Base(src.Dir),
		Branch:    gitx.GitCurrentBranch(src.Dir),
		CreatedAt: now.Format(time.RFC3339), ForkFrom: forkFrom, ForkAt: forkAt,
		ForkSids: forkSids(src),
		// The fork works in the same working copy, so it renames the same branch for the same item,
		// and is on the same tickets and PRs.
		WorkItem: src.WorkItem, Links: src.Links, Unlinked: src.Unlinked,
		// Studio is deliberately NOT inherited: one studio has one session, and a fork bound to
		// the same studio would be a second writer of its draft (ADR 0100 decision 2).
		// A session grown from a handoff has origin=handoff (ADR 0029 §6). Inheriting the
		// source's origin would blend it into "sessions a human opened" and hide the spend
		// handoffs add. The originating conversation IS inherited from the parent, so a
		// handoff from an operator-started session stays traceable in the same chain.
		// OriginSession rides along only for a source in an unattended chain (forkLineage):
		// forking a child keeps the lineage, so the successor still cannot spawn, while its
		// origin=handoff keeps it out of the parent's steering set. Forking a session a person
		// launched from a handoff proposal inherits nothing — see forkLineage.
		Origin: session.OriginHandoff, OriginConv: src.OriginConv, OriginSession: forkLineage(src),
	}
}
