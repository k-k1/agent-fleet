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
// user-notes -> rtk), so reconcile must always call them in that order.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/userinstr"
)

// instrMu serializes every write into a CLI's instruction artifacts. rtk shares it:
// codex's AGENTS.md carries both the rtk block and the user block, and two
// independent read-modify-writes would lose one side (docs/log/60 §60.7).
var instrMu sync.Mutex

// instrErrs holds the last apply error per kind, so the Console can say "written but not
// in effect" instead of showing a green row for a write that failed.
var instrErrs = map[string]string{}

// The kinds of distribution. The Console picks its wording from this.
const (
	deliveryFile    = "file"    // drop a file AF owns outright
	deliveryCompose = "compose" // compose into a file shared with others, between markers
	deliveryConfig  = "config"  // an AF-owned file, referenced from the CLI's own config
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
var instrSupportedKinds = []string{"claude", "codex", "opencode", "copilot", "agy", "kiro"}

// instrUnsupported are the kinds it cannot reach, with the reason (measured, docs/log/60
// §60.3).
var instrUnsupported = []struct{ kind, reason string }{
	// No user layer exists locally: User Rules live on the Cursor account side
	// (aiserver.v1.UserRules) and every local rules source is project-scoped. That is a
	// structural impossibility rather than missing wiring, so it is not shown as pending
	// implementation.
	{"cursor", "no_user_scope"},
}

// instrOpencodeFile is the AF-owned body opencode's config points at. It lives under
// ~/.config/agent-fleet so the only writer is af (opencode just reads it).
func instrOpencodeFile() string {
	return filepath.Join(paths.AgentConfigDir(), "instructions", "opencode.md")
}

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

	// 1. The fleet policy, before the user instructions because the order matters. claude
	// is left alone: its copy is baked into the image as managed policy
	// (/etc/claude-code/CLAUDE.md). cursor cannot be reached at all (no local user scope).
	// For the other five this is the only distribution path — agy / copilot / kiro read no
	// operating policy whatsoever until docs/log/60 §60.13 P2.
	note("codex", codex.ApplyFleetNotes(fleet))
	note("opencode", opencode.ApplyFleetNotes(fleet))
	note("agy", agy.ApplyFleetNotes(fleet))
	note("copilot", copilot.ApplyFleetNotes(fleet))
	note("kiro", kiro.ApplyFleetNotes(fleet))

	// 2. The user's own instructions.
	note("claude", claude.ApplyUserInstructions(st.Body("claude")))
	note("codex", codex.ApplyUserInstructions(st.Body("codex")))
	note("opencode", opencode.ApplyUserInstructions(instrOpencodeFile(), st.Body("opencode")))
	note("copilot", copilot.ApplyUserInstructions(st.Body("copilot")))
	note("agy", agy.ApplyUserInstructions(st.Body("agy")))
	note("kiro", kiro.ApplyUserInstructions(st.Body("kiro")))

	// 3. rtk is always last (it comes last within the file too).
	applyRTKLocked()

	instrErrs = errs
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
		switch kind {
		case "claude":
			t.Delivery, t.Path = deliveryFile, claude.UserInstructionsPath()
			t.Applied = fileHasBlock(t.Path, "user-notes") == want
		case "codex":
			t.Delivery, t.Path = deliveryCompose, codex.AgentsPath()
			t.Applied = fileHasBlock(t.Path, "user-notes") == want
		case "opencode":
			t.Delivery, t.Path = deliveryConfig, instrOpencodeFile()
			t.Applied = fileExists(t.Path) == want && opencodeRefers(t.Path) == want
		case "copilot":
			t.Delivery, t.Path = deliveryFile, copilot.UserInstructionsPath()
			t.Applied = fileExists(t.Path) == want
		case "agy":
			t.Delivery, t.Path = deliveryCompose, agy.AgentsPath()
			t.Applied = fileHasBlock(t.Path, "user-notes") == want
		case "kiro":
			t.Delivery, t.Path = deliveryFile, kiro.UserInstructionsPath()
			t.Applied = fileExists(t.Path) == want
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
// codex, the referenced file for the others. It is the last step in keeping "written"
// apart from "in effect", the one that lets the user check with their own eyes.
func handleUserNotesPreview(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	var path string
	switch kind {
	case "claude":
		path = claude.UserInstructionsPath()
	case "codex":
		path = codex.AgentsPath()
	case "opencode":
		path = instrOpencodeFile()
	case "copilot":
		path = copilot.UserInstructionsPath()
	case "agy":
		path = agy.AgentsPath()
	case "kiro":
		path = kiro.UserInstructionsPath()
	case "fleet":
		path = paths.FleetNotesPath()
	default:
		httpx.WriteErr(w, http.StatusBadRequest, "unknown_kind", "no instruction target for this kind")
		return
	}
	body, err := os.ReadFile(path)
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
