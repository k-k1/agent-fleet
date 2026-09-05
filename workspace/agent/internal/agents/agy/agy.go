// Package agy is the vertical package for the Antigravity CLI (agy) kind (docs/log/32
// Track A). Laid out like the codex package, it keeps the Agent implementation, the launch
// command assembly, the Connections auth handlers and the rtk block application inside the
// kind. The execution method can only be Terminal (CLI)/tmux: v1.1.4 has no structured
// output, so Managed cannot be built (docs/decisions/0008). The host requirements (RDRAND,
// whether the binary is present) are decided by internal/hostcaps, and Status() /
// BuildLaunch wire that in (the docs/log/32 Track B contract).
package agy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/hostcaps"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sids maps our deterministic slot sid to agy's conversation UUID, read back as
// `--conversation <UUID>` on resume. agy has no hooks to report its own id, so
// the UUID is adopted from its cwd→last-conversation map once it changes from
// the pre-launch snapshot — see captureConversation.
var sids = agents.NewSidStore("agy-sid")

// prelaunch snapshots the cwd's last-conversation UUID at fresh-launch time, so
// captureConversation can tell a conversation this slot created from a stale
// entry left by an earlier session in the same dir. `--continue` (= the raw
// cwd map) is NOT used for resume: any other agy run in the dir overwrites the
// mapping and slots would swap conversations (docs/log/32 Track D-3).
var prelaunch = agents.NewSidStore("agy-prelaunch")

// brainPrelaunch snapshots the brain/ conversation-dir listing (newline-joined)
// at fresh-launch time. agy creates brain/<uuid>/ the moment the FIRST prompt is
// submitted (verified on real hardware), so "a dir that wasn't in the snapshot" pins the
// slot's conversation UUID while the session is still ALIVE — the cwd map alone
// only flushes on graceful exit, which is too late for the live chat mirror.
var brainPrelaunch = agents.NewSidStore("agy-brain-prelaunch")

// New returns the agy Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindAgy }

// No fork (agy exposes no fork affordance) and no display label (agy has no
// --name). The chat mirror IS supported: transcript.go reads the per-conversation
// brain/…/transcript_full.jsonl, which agy appends live.
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{CanTranscript: true, PermissionChoice: true}
}

func (agentImpl) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// Same host gate as the Console's kind selector: on a host where agy can't run
	// (binary absent / no RDRAND → SIGABRT at launch) refuse to build the pane
	// program instead of letting the session die on start (docs/log/32 Track B).
	if supported, reason := hostcaps.AgyStatus(); !supported {
		return agents.LaunchPlan{}, fmt.Errorf("agy はこのホストで利用できません（%s）", reason)
	}
	if !session.DirExists(m.Dir) {
		return agents.LaunchPlan{}, agents.DirGoneErr(m.Dir)
	}
	// Pre-trust the launch dir so agy doesn't stall on its "Do you trust the
	// contents of this project?" prompt (not skippable by flags).
	EnsureWorkspaceTrusted(m.Dir)
	// Re-pin the telemetry opt-out on every launch: the auth-time pin alone
	// doesn't survive the key being flipped or dropped later, and docs/log/32 adopted agy
	// on the condition that it stays off at all times.
	enforceTelemetryOff()
	slotSid := session.UUID(m.Dir, m.Name)
	resumeID := sids.Read(slotSid)
	if resumeID == "" {
		// Fresh conversation: snapshot the dir's current map entry AND the brain
		// dir listing so the poll-time capture only adopts a UUID this launch
		// actually created (cwd-map diff on exit, brain-dir diff while alive).
		prelaunch.Write(slotSid, LastConversationFor(m.Dir))
		brainPrelaunch.Write(slotSid, strings.Join(listBrainDirs(), "\n"))
	}
	return agents.LaunchPlan{Program: buildProgram(m.Model, m.Mode, resumeID, agents.BypassPermissions(m)), Cwd: m.CWD()}, nil
}

func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	// agy has no status hooks, so no working/idle state is surfaced. A pending
	// interactive prompt IS detectable though (conversation-DB probe — pending.go),
	// so the sessions list can badge "question" / "waiting for permission" while the TUI
	// is blocked.
	li := agents.LiveInfo{Resumable: true}
	if alive {
		if st, _ := Probe(m); st != "" {
			li.State = st
		}
	}
	// Capture on BOTH sides of alive. Alive polls adopt the UUID via the
	// brain-dir diff as soon as the first prompt lands (what lights the live
	// chat mirror); dead polls cover a user /exit whose cwd-map flush the halt
	// path never saw (v1.1.4 flushes the map only on graceful exit — measured in the
	// integration E2E).
	captureConversation(m)
	if !alive && !session.DirExists(m.Dir) {
		li.Resumable = false
	}
	return li
}

// captureConversation adopts the slot's conversation UUID as soon as either
// source can attribute one to THIS launch:
//
//  1. cwd→last-conversation map differing from the pre-launch snapshot — agy
//     only flushes it on graceful exit (v1.1.4), so this lands via GracefulStop
//     (halt) or the first poll after the user's own /exit. Checked first: it is
//     keyed by cwd, hence unambiguous.
//  2. brain-dir diff while ALIVE — a brain/<uuid>/ dir absent from the launch
//     snapshot appeared, i.e. a first prompt was submitted somewhere. Adopted
//     only when there is exactly ONE such dir: two agy sessions racing their
//     first prompts between polls can't be told apart here, so that (rare) case
//     defers to the cwd map at exit rather than guessing.
//
// The early adoption is what lights the chat mirror during the session (the
// transcript path is brain/<uuid>/…), and it also makes resume survive a
// non-graceful death (kill/OOM), which the map-only path never covered.
func captureConversation(m session.Meta) {
	slotSid := session.UUID(m.Dir, m.Name)
	if sids.Read(slotSid) != "" {
		return
	}
	adopt := func(uuid string) {
		sids.Write(slotSid, uuid)
		prelaunch.Remove(slotSid)
		brainPrelaunch.Remove(slotSid)
	}
	if cur := LastConversationFor(m.Dir); cur != "" && cur != prelaunch.Read(slotSid) {
		adopt(cur)
		return
	}
	if snap, ok := brainSnapshot(slotSid); ok {
		// brain/ is global and carries no cwd information, so do not adopt via the brain
		// diff while other slots are still running without having adopted one: even a
		// single fresh dir may be another directory's slot submitting its first prompt,
		// and adopting it wrongly would wire this slot's mirror and resume to somebody
		// else's conversation permanently. In that case defer to the cwd map at exit,
		// which is keyed by cwd and therefore unambiguous.
		if pendingBrainSnapshots() > 1 {
			return
		}
		if fresh := diffStrings(listBrainDirs(), snap); len(fresh) == 1 {
			adopt(fresh[0])
		}
	}
}

// pendingBrainSnapshots counts slots that launched fresh and have not adopted a
// conversation yet (their brain snapshot file still exists — adopt removes it).
func pendingBrainSnapshots() int {
	ents, err := os.ReadDir(filepath.Dir(brainPrelaunch.Path("probe")))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// brainSnapshot returns the launch-time brain-dir set for the slot. ok=false
// when no snapshot exists (a resumed slot, or a launch predating this feature)
// — then the brain diff must not run at all: without a baseline every existing
// conversation would look "fresh".
func brainSnapshot(slotSid string) (map[string]bool, bool) {
	raw := brainPrelaunch.Read(slotSid)
	if raw == "" {
		// Distinguish "empty snapshot" (no conversations yet — valid) from
		// "no snapshot file": the store returns "" for both, so probe the file.
		if _, err := os.Stat(brainPrelaunch.Path(slotSid)); err != nil {
			return nil, false
		}
	}
	set := map[string]bool{}
	for _, d := range strings.Split(raw, "\n") {
		if d != "" {
			set[d] = true
		}
	}
	return set, true
}

// listBrainDirs returns the conversation-uuid dir names under brain/ ("" safe).
func listBrainDirs() []string {
	ents, err := os.ReadDir(brainDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// diffStrings returns the members of cur missing from prev.
func diffStrings(cur []string, prev map[string]bool) []string {
	var out []string
	for _, s := range cur {
		if !prev[s] {
			out = append(out, s)
		}
	}
	return out
}

func (agentImpl) ClearResume(sid string) {
	sids.Remove(sid)
	prelaunch.Remove(sid)
	brainPrelaunch.Remove(sid)
}
