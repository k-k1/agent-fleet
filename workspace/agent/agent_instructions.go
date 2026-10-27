package main

// Distributor for the user's own instructions (docs/log/60 / ADR 0042).
//
// The middle of the three layers — "how this person works" — is written once and takes
// effect in every supported kind's sessions. The source of truth is internal/userinstr
// (~/.config/agent-fleet/user-notes.md); this file DISTRIBUTES it into each CLI's user
// scope, following the same shape as the rtk toggle (durable setting -> reconcile at
// startup + live apply from the Console).
//
// The distribution rule (docs/log/60 §60.5-6): prefer "an AF-owned file plus a reference"
// over "writing into somebody else's file". Measured, a reference is enough for claude /
// opencode / copilot, and the only kind that still needs composition is codex, which has
// no way to point at an extra instruction file (agy is P1).
//
// Placing the fleet policy (the image's workspace-notes.md) lives here too. The entrypoint
// used to `cp -f` the whole of AGENTS.md on every start, so anything the user had added to
// that file silently vanished at the next start (docs/log/60). Now a single writer
// composes it with markers, and everything outside the markers survives.
//
// Order matters: the order within the file IS the order it is applied in (fleet ->
// user-notes -> memory-guide -> rtk), so reconcile must always call them in that order.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetskills"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/userinstr"
)

// instrMu serializes every write into a CLI's instruction artifacts. rtk shares it:
// codex's AGENTS.md carries both the rtk block and the user block, and two
// independent read-modify-writes would lose one side (docs/log/60 §60.7).
var instrMu sync.Mutex

// instrErrs holds the last apply error per kind, so the Console can say "written but not
// in effect" instead of showing a green row for a write that failed.
var instrErrs = map[string]string{}

// The kinds of distribution (agents.Delivery*); the Console picks its wording from them.
const (
	deliveryFile    = agents.DeliveryFile
	deliveryCompose = agents.DeliveryCompose
	deliveryConfig  = agents.DeliveryConfig
	deliveryPrompt  = agents.DeliveryPrompt
)

// instrTarget is where one kind's instructions go. Unsupported kinds are LISTED as rows
// too: dropping them silently reads as an oversight and invites the same question again
// (the docs/log/57 §2 practice).
type instrTarget struct {
	Kind      string `json:"kind"`
	Supported bool   `json:"supported"`
	// Reason is the code for why the kind is unsupported (the Console translates it
	// through its err/reason catalogue).
	Reason   string `json:"reason,omitempty"`
	Delivery string `json:"delivery,omitempty"`
	Path     string `json:"path,omitempty"`
	// On is the user's choice (always false for an unsupported kind).
	On bool `json:"on"`
	// Applied is whether that is ACTUALLY the state on disk right now — measured, so that
	// "written" and "in effect" stay separable.
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

// instrSupportedKinds are the kinds the body can be distributed to, in the Console's
// display order.
var instrSupportedKinds = []string{"claude", "codex", "opencode", "copilot", "agy", "kiro", "lcpp", "muse"}

// instrOpsOf resolves a kind's instruction functions (agents.Instructions). The match is exact:
// no NormalizeKind and no claude fallback, so an alternate spelling has none.
func instrOpsOf(kind string) (agents.InstructionsOps, bool) {
	a, ok := sessionx.RegisteredAgent(kind)
	if !ok {
		return agents.InstructionsOps{}, false
	}
	i, ok := a.(agents.Instructions)
	if !ok {
		return agents.InstructionsOps{}, false
	}
	return i.Instructions(), true
}

// instrDeliveryOf is how a supported kind receives the body (the deliveryX constants), ""
// for a kind that takes none. Shared by the instructions screen and the kind catalog so the
// two cannot answer differently.
func instrDeliveryOf(kind string) string {
	ops, ok := instrOpsOf(kind)
	if !ok {
		return ""
	}
	return ops.Shape().Delivery
}

// instrUnsupported are the kinds it cannot reach, with the reason (measured, docs/log/60
// §60.3).
var instrUnsupported = []struct{ kind, reason string }{
	// No user layer exists locally: User Rules live on the Cursor account side
	// (aiserver.v1.UserRules) and every local rules source is project-scoped. That is a
	// structural impossibility rather than missing wiring, so it is not shown as pending
	// implementation.
	{"cursor", "no_user_scope"},
}

func instrOpencodeFile() string { return opencode.InstructionsFile() }

// reconcileAgentInstructions applies the durable state to every artifact. Called at
// startup (after the entrypoint) and on every Console save.
func reconcileAgentInstructions() {
	instrMu.Lock()
	defer instrMu.Unlock()
	applyInstructionsLocked()
}

func applyInstructionsLocked() {
	st := userinstr.Load()
	fleet := userinstr.FleetNotes()
	errs := map[string]string{}
	note := func(kind string, err error) {
		if err != nil {
			errs[kind] = errCode(err)
		}
	}

	// Each step walks the kinds' own functions (agents.Instructions); a kind with nothing to
	// write at a step leaves that function nil. The kinds are independent files, so only the
	// ORDER OF THE STEPS matters.
	//
	// 1. The fleet policy, before the user instructions because the order matters. claude
	// has none: its copy is baked into the image as managed policy
	// (/etc/claude-code/CLAUDE.md). cursor cannot be reached at all (no local user scope).
	// For the others this is the only distribution path — agy / copilot / kiro read no
	// operating policy whatsoever until docs/log/60 §60.13 P2.
	// muse shares ONE file between both apply paths (ADR 0095 decision 12), so its fleet and
	// user notes are two marker blocks in the same AGENTS.md rather than two artefacts; the
	// order they land in is the step order, which is why fleet stays above user here.
	for _, kind := range instrSupportedKinds {
		if ops, ok := instrOpsOf(kind); ok && ops.Fleet != nil {
			note(kind, ops.Fleet(fleet))
		}
	}

	// 1b. The topic files behind the policy, registered as skills where the CLI has a user
	// skills root, so its own index (description at start, body on demand) carries an agent
	// to the right file. claude IS reachable here: the root is $CLAUDE_CONFIG_DIR, AF's own
	// mount, unlike the policy file under /etc. The other kinds keep the index in the policy
	// text as their only route (see package fleetskills for what was measured).
	topics := fleetskills.Load(paths.FleetNotesDir())
	for _, kind := range instrSupportedKinds {
		if ops, ok := instrOpsOf(kind); ok && ops.SkillsDir != nil {
			note(kind, fleetskills.Apply(ops.SkillsDir(), topics))
		}
	}

	// 2. The user's own instructions. lcpp has nothing to write: harness.SystemPrompt reads
	// userinstr on every turn.
	for _, kind := range instrSupportedKinds {
		if ops, ok := instrOpsOf(kind); ok && ops.User != nil {
			note(kind, ops.User(st.Body(kind)))
		}
	}

	// 2b. The memory guidance (ADR 0108 decision 5): fixed text, present exactly while the Agent
	// memory switch is on, in a block or file of its own so the switch never touches the user's
	// notes. lcpp gets it from harness.MemoryPrompt instead. cursor has no local user layer;
	// its tool descriptions carry the same guidance.
	guide := ""
	if uiprefs.AgentMemory() {
		guide = userinstr.MemoryGuide
	}
	for _, kind := range instrSupportedKinds {
		if ops, ok := instrOpsOf(kind); ok && ops.Memory != nil {
			note(kind, ops.Memory(guide))
		}
	}

	// 3. rtk is always last (it comes last within the file too).
	applyRTKLocked()

	instrErrs = errs
}

// lcppMemoryBudget is the described part of the index folded into lcpp's system prompt, in
// bytes: the floor memory_index accepts. A local model's window is small and the prompt is paid
// on every turn; the tool is still there for the rest.
const lcppMemoryBudget = 4 << 10

func init() {
	harness.MemoryPrompt = lcppMemoryPrompt
	// claude's own auto-memory is off for launches while the Agent memory switch is on (ADR 0108
	// decision 6 step 2); read at launch, so a running session keeps what it started with. It
	// follows the reconciler's phase, not the raw switch: native memory stays off until a
	// write-back has finished (#1972).
	claude.AutoMemoryOff = memoryx.AutoSyncNativeMemoryOff
	// Likewise muse's native memory (#1990): MUSE_EXPERIMENTAL_MEMORY=0 on launches while the switch is on.
	muse.NativeMemoryOff = uiprefs.AgentMemory
}

// lcppMemoryPrompt is harness.MemoryPrompt: while the Agent memory switch is on, the fixed
// guidance and the project's budgeted index. A session with no working copy (the Console
// preview) gets nothing, so the preview never prints memories.
func lcppMemoryPrompt(cwd, kind string) string {
	if !uiprefs.AgentMemory() || cwd == "" {
		return ""
	}
	raw, err := memoryx.IndexJSONFor(cwd, kind, lcppMemoryBudget)
	if err != nil {
		log.Printf("lcpp: memory index for the system prompt: %v", err)
		return userinstr.MemoryGuide
	}
	return userinstr.MemoryGuide + "\n\n### Memories now\n\n" + mcpx.FormatMemoryIndex(raw)
}

func errCode(err error) string {
	if errors.Is(err, opencode.ErrUnreadableConfig) {
		return "config_unreadable"
	}
	return "write_failed"
}

// instrState builds the REST snapshot, checking each artifact on disk so "applied"
// is measured rather than assumed.
func instrState() instrStateWire {
	st := userinstr.Load()
	targets := make([]instrTarget, 0, len(instrSupportedKinds)+len(instrUnsupported))
	for _, kind := range instrSupportedKinds {
		t := instrTarget{Kind: kind, Supported: true, On: st.TargetOn(kind), Error: instrErrs[kind]}
		want := st.Body(kind) != ""
		if ops, ok := instrOpsOf(kind); ok {
			shape := ops.Shape()
			t.Delivery, t.Path = shape.Delivery, shape.Path
			t.Applied = instrApplied(shape, want)
		}
		targets = append(targets, t)
	}
	for _, u := range instrUnsupported {
		targets = append(targets, instrTarget{Kind: u.kind, Reason: u.reason})
	}
	return instrStateWire{
		Text:     st.Text,
		Bytes:    len(st.Text),
		MaxBytes: userinstr.MaxBytes,
		Enabled:  st.Enabled(),
		Path:     userinstr.NotesPath(),
		Targets:  targets,
		// A read-only way into the fleet policy, so the screen can show why it cannot be
		// overwritten.
		FleetBytes: len(userinstr.FleetNotes()),
	}
}

// instrApplied measures one kind's artifact the way its shape says. A kind whose file is shared
// with the member's own text has to be measured by the block, never by the file's existence
// (see agents.InstructionsShape).
func instrApplied(shape agents.InstructionsShape, want bool) bool {
	switch shape.Measure {
	case agents.MeasureBlock:
		return fileHasBlock(shape.Path, "user-notes") == want
	case agents.MeasureFile:
		return fileExists(shape.Path) == want
	case agents.MeasureConfig:
		return fileExists(shape.Path) == want && opencodeRefers(shape.Path) == want
	case agents.MeasurePrompt:
		return true
	}
	return false
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func fileHasBlock(path, name string) bool {
	b, err := os.ReadFile(path)
	return err == nil && mdblock.Has(string(b), name)
}

func opencodeRefers(instrPath string) bool {
	b, err := os.ReadFile(opencode.ConfigPath())
	if err != nil {
		return false
	}
	var root struct {
		Instructions []string `json:"instructions"`
	}
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	for _, p := range root.Instructions {
		if p == instrPath {
			return true
		}
	}
	return false
}

func handleUserNotesGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, instrState())
}

type userNotesReq struct {
	Text    *string          `json:"text"`
	Enabled *bool            `json:"enabled"`
	Targets map[string]*bool `json:"targets"`
}

func handleUserNotesPut(w http.ResponseWriter, r *http.Request) {
	var req userNotesReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	instrMu.Lock()
	defer instrMu.Unlock()

	if req.Text != nil {
		if err := userinstr.SaveText(*req.Text); err != nil {
			if errors.Is(err, userinstr.ErrTooLarge) {
				httpx.WriteErr(w, http.StatusBadRequest, "too_large", "user instructions exceed the size limit")
				return
			}
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}
	if req.Enabled != nil || req.Targets != nil {
		p := userinstr.Load().Prefs
		if req.Enabled != nil {
			p.Enabled = req.Enabled
		}
		for kind, on := range req.Targets {
			if p.Targets == nil {
				p.Targets = map[string]*bool{}
			}
			p.Targets[kind] = on
		}
		if err := userinstr.SavePrefs(p); err != nil {
			httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
			return
		}
	}
	applyInstructionsLocked()
	httpx.WriteJSON(w, http.StatusOK, instrState())
}

// handleUserNotesPreview shows what a kind will actually read — the composed file for
// codex, the system prompt for lcpp, the referenced file for the others. It is the last step in keeping "written"
// apart from "in effect", the one that lets the user check with their own eyes.
func handleUserNotesPreview(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	var path string
	if kind == "fleet" {
		path = paths.FleetNotesPath()
	} else if ops, ok := instrOpsOf(kind); ok {
		// lcpp has no file: path stays empty and the content below is the system prompt the
		// harness would send, minus the project layer, which depends on the session's working
		// copy.
		shape := ops.Shape()
		if path = shape.PreviewPath; path == "" {
			path = shape.Path
		}
	} else {
		httpx.WriteErr(w, http.StatusBadRequest, "unknown_kind", "no instruction target for this kind")
		return
	}
	var body []byte
	var err error
	if path == "" {
		body = []byte(harness.SystemPrompt("", kind))
	} else {
		body, err = os.ReadFile(path)
	}
	if err != nil && !os.IsNotExist(err) {
		httpx.WriteErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"kind": kind, "path": path, "exists": err == nil, "content": string(body),
	})
}

// instrStateWire is the GET/PUT /instructions response (the Console's `Payload`,
// console/src/features/settings/personal/InstructionsTab.tsx).
//
// was: map[string]any{"text":…, "bytes":…, "max_bytes":…, "enabled":…, "path":…,
//
//	"targets":…, "fleet_bytes":…}
//
// All seven keys are unconditional, so do NOT add omitempty: text can legitimately be the
// empty string (nothing written yet), and omitempty would drop the key altogether and
// change what the Console shows on first render.
// One shape type, so both sites (GET and PUT) get their type from it.
type instrStateWire struct {
	Text       string        `json:"text"`
	Bytes      int           `json:"bytes"`
	MaxBytes   int           `json:"max_bytes"`
	Enabled    bool          `json:"enabled"`
	Path       string        `json:"path"`
	Targets    []instrTarget `json:"targets"`
	FleetBytes int           `json:"fleet_bytes"`
}
