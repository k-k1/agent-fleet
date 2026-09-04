// Package kiro is the vertical package for the Kiro CLI kind (`kiro-cli`, formerly the Amazon
// Q Developer CLI; docs/log/43 Track A). It keeps the read layer inside the kind: the Agent
// implementation, the transcript over the v2 JSONL session store, and the state read from the
// TUI string contract. The managed driver (`kiro-cli acp`, a per-session child speaking ACP
// JSON-RPC over stdio) is implemented in driver.go/acp.go/mirror.go under Track A2 - the same
// shape as cursor/copilot, with cross-process resume through session/load and context retention
// measured.
//
// Session identity, the biggest difference from cursor: kiro MINTS THE SESSION ID ITSELF.
// Passing an unknown self-minted UUID to `--resume-id` is not honoured; the CLI cuts its own
// new id (measured on 2.14.1). So cursor's "AF allocates the UUID first" approach is unusable.
// Instead the `~/.kiro/sessions/cli/<sid>.json` written after launch (it records the cwd) is
// discovered by cwd plus modification time and cached in the sidstore once found, the same
// shape as codex's rollout discovery. Resume passes the cached sid to `--resume-id`.
//
// The read source of truth is the v2 JSONL (`~/.kiro/sessions/cli/<sid>.jsonl`, append-only),
// shared by the new TUI and ACP and carrying toolUse inputs and toolResult outputs
// (transcript.go). The unguaranteed classic SQLite (`~/.local/share/kiro-cli/data.sqlite3`,
// headless only) is deliberately not depended on - the lesson from the false-idle caused by
// opencode's store contract change. Auth is the Builder ID / device flow kind, with credentials
// under `~/.local/share/kiro-cli/` (auth.go / fs.go denylist).
package kiro

import (
	"os"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sids maps our deterministic slot sid to kiro's own (CLI-minted) session id.
// Written LAZILY the first time a read/wire path discovers the minted id on disk
// (kiro mints it at launch — we can't pre-allocate as cursor does), and read at
// resume time to pass `--resume-id`.
var sids = agents.NewSidStore("kiro-sid")

// New returns the kiro Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindKiro }

// No fork (kiro has no non-interactive fork) and no display label. The chat mirror
// IS supported: transcript.go reads the v2 JSONL that the TUI appends live.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, PermissionChoice: true}
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	ensureSettings() // idempotent: pin autoupdate off + skip the --trust-all danger dialog
	// Resume only a session we've already learned for THIS slot. A fresh slot must NOT
	// grab an unrelated newest session in the same cwd, so we read the cache directly
	// (discovery is post-launch only — see resolveSid).
	resumeID := sids.Read(session.UUID(m.Dir, m.Name))
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Effort, m.Mode, resumeID, agents.BypassPermissions(m)), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// kiro's TUI state is read from an explicit text contract (state.go): "Kiro is working" /
	// "ask a question or describe a task" / "requires approval". 2.14.1 has no Stop hook (the
	// triggers are only AgentSpawn/PrePrompt/PreToolUse/PostToolUse, measured), so this poll is
	// the state source for the TUI route. On the managed (ACP) route the driver's runTurn
	// boundary is the state source (Track A2).
	li := agents.LiveInfo{Resumable: true}
	if alive {
		if st := LiveState(m); st != "" {
			li.State = st
		}
	}
	if !alive && !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

// PendingModal hands the human-wait that existed just before shutdown to the carry-over
// (docs/log/75 P5).
//
// kiro only ever waits on an approval, and where it lives depends on the route: the pane's
// approval panel for the TUI (a string contract, state.go), ACP's `session/request_permission`
// for managed (the driver's handle). Both vanish WITH THE PROCESS, so nothing can be read at a
// point later than halt / gracefulShutdown (a SIGKILL of the whole container simply loses it -
// docs/log/75 §75.7).
//
// Kind is permission: with the destination for the answer gone (the TUI panel, the ACP JSON-RPC
// id), offering a choice would have nowhere to deliver it. Only the fact carries over
// (docs/log/75 §75.6.4).
func (agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	detail := ""
	if m.DriverKind() == session.DriverManaged {
		h := handleFor(m.Name)
		if h == nil {
			return agents.PendingModal{}, false
		}
		detail = h.pendingPermission()
	} else {
		detail = approvalDetail(m)
	}
	if detail == "" {
		return agents.PendingModal{}, false
	}
	return agents.PendingModal{Kind: "permission", Detail: detail}, true
}

func (agentImpl) ClearResume(sid string) { sids.Remove(sid) }

// resolveSid returns the slot's kiro session id, discovering it on first use. kiro
// mints its own id at launch, so the resume-cache is populated here (a read/wire
// path) rather than at BuildLaunch. Once cached it sticks — a stable render key and
// the `--resume-id` source for the next launch.
func resolveSid(m session.Meta) string {
	slot := session.UUID(m.Dir, m.Name)
	if cached := sids.Read(slot); cached != "" {
		if _, err := os.Stat(sessionJSONPath(cached)); err == nil {
			return cached // still on disk
		}
		// The cached session's files were deleted (kiro --delete-session / manual): fall
		// through and rediscover so the mirror doesn't stick on a vanished conversation.
	}
	// Fence discovery to this slot's creation time so a predecessor session lingering
	// in the same dir (recreate cuts a new slug into the same dir) is never adopted
	// during the fresh-launch window — A-1. An unparseable CreatedAt degrades to no
	// fence rather than never resolving.
	if sid := discoverSid(m.Dir, slotCreatedAt(m)); sid != "" {
		sids.Write(slot, sid)
		return sid
	}
	return ""
}

// slotCreatedAt parses the slot's creation time (Meta.CreatedAt, set at create and
// stable across resumes). Zero time when absent/unparseable = no discovery fence.
func slotCreatedAt(m session.Meta) time.Time {
	if m.CreatedAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}
