package main

import (
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/kiro"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// noInstructionTargets are the registered kinds that run no agent CLI, so there is nothing to
// deliver instructions to. Every other registered kind must be supported or listed in
// instrUnsupported with a reason.
var noInstructionTargets = []string{"shell", "ssm"}

// legacyInstr is what applyInstructionsLocked, instrState, instrDeliveryOf and the preview
// carried as per-kind switches before agents.Instructions: one row per supported kind.
type legacyInstrRow struct {
	delivery, measure, path string
	preview                 string // "" = path
	fleet, skills, user     bool   // which apply steps the kind took part in
}

func legacyInstr(opencodeV2 bool) map[string]legacyInstrRow {
	oc := legacyInstrRow{delivery: agents.DeliveryConfig, measure: agents.MeasureConfig, path: instrOpencodeFile(),
		fleet: true, skills: true, user: true}
	if opencodeV2 {
		oc.delivery, oc.measure, oc.path, oc.preview = agents.DeliveryCompose, agents.MeasureBlock, opencode.AgentsPath(), instrOpencodeFile()
	}
	return map[string]legacyInstrRow{
		"claude":   {agents.DeliveryFile, agents.MeasureBlock, claude.UserInstructionsPath(), "", false, true, true},
		"codex":    {agents.DeliveryCompose, agents.MeasureBlock, codex.AgentsPath(), "", true, true, true},
		"opencode": oc,
		"copilot":  {agents.DeliveryFile, agents.MeasureFile, copilot.UserInstructionsPath(), "", true, false, true},
		"agy":      {agents.DeliveryCompose, agents.MeasureBlock, agy.AgentsPath(), "", true, false, true},
		"kiro":     {agents.DeliveryFile, agents.MeasureFile, kiro.UserInstructionsPath(), "", true, false, true},
		"lcpp":     {agents.DeliveryPrompt, agents.MeasurePrompt, "", "", false, false, false},
		"muse":     {agents.DeliveryCompose, agents.MeasureBlock, muse.AgentsPath(), "", true, true, true},
	}
}

func TestInstructionsShapeMatchesLegacySwitches(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		instrEnv(t)
		if v2 {
			useOpencodeV2(t)
		}
		legacy := legacyInstr(v2)
		for _, kind := range append(sessionx.RegisteredKinds(), "", "Claude", "CODEX", "kiro-cli", "open-code", "fleet", "unknown") {
			row, supported := legacy[kind]
			ops, ok := instrOpsOf(kind)
			if ok != supported {
				t.Errorf("v2=%v %q: instrOpsOf = %v, legacy supported = %v", v2, kind, ok, supported)
				continue
			}
			if got := instrDeliveryOf(kind); got != row.delivery {
				t.Errorf("v2=%v %q: delivery %q, legacy %q", v2, kind, got, row.delivery)
			}
			if !ok {
				continue
			}
			s := ops.Shape()
			if s.Delivery != row.delivery || s.Measure != row.measure || s.Path != row.path || s.PreviewPath != row.preview {
				t.Errorf("v2=%v %q: shape %+v, legacy %+v", v2, kind, s, row)
			}
			if (ops.Fleet != nil) != row.fleet || (ops.SkillsDir != nil) != row.skills ||
				(ops.User != nil) != row.user || (ops.Memory != nil) != row.user {
				t.Errorf("v2=%v %q: apply steps fleet=%v skills=%v user=%v memory=%v, legacy %+v", v2, kind,
					ops.Fleet != nil, ops.SkillsDir != nil, ops.User != nil, ops.Memory != nil, row)
			}
		}
	}
}

// The package functions each kind dispatches to, by identity: a row pointing at another kind's
// writer would still pass the shape table.
func TestInstructionsOpsDispatchPinned(t *testing.T) {
	instrEnv(t)
	same := func(kind, field string, got, want any) {
		t.Helper()
		if reflect.ValueOf(got).Pointer() != reflect.ValueOf(want).Pointer() {
			t.Errorf("%s.%s dispatches to the wrong function", kind, field)
		}
	}
	want := map[string]struct{ fleet, user, memory any }{
		"claude":   {nil, claude.ApplyUserInstructions, claude.ApplyMemoryGuide},
		"codex":    {codex.ApplyFleetNotes, codex.ApplyUserInstructions, codex.ApplyMemoryGuide},
		"copilot":  {copilot.ApplyFleetNotes, copilot.ApplyUserInstructions, copilot.ApplyMemoryGuide},
		"agy":      {agy.ApplyFleetNotes, agy.ApplyUserInstructions, agy.ApplyMemoryGuide},
		"kiro":     {kiro.ApplyFleetNotes, kiro.ApplyUserInstructions, kiro.ApplyMemoryGuide},
		"muse":     {muse.ApplyFleetNotes, muse.ApplyUserInstructions, muse.ApplyMemoryGuide},
		"opencode": {opencode.ApplyFleetNotes, nil, opencode.ApplyMemoryGuide}, // User is a closure over the AF-owned file
	}
	for kind, w := range want {
		ops, ok := instrOpsOf(kind)
		if !ok {
			t.Fatalf("%s has no instruction ops", kind)
		}
		if w.fleet != nil {
			same(kind, "Fleet", ops.Fleet, w.fleet)
		}
		if w.user != nil {
			same(kind, "User", ops.User, w.user)
		}
		same(kind, "Memory", ops.Memory, w.memory)
	}
	skills := map[string]string{
		"claude":   filepath.Join(paths.ClaudeConfigDir(), "skills"),
		"codex":    filepath.Join(paths.CodexHome(), "skills"),
		"opencode": filepath.Join(paths.OpencodeConfigDir(), "skills"),
		"muse":     muse.SkillsDir(),
	}
	for kind, dir := range skills {
		ops, _ := instrOpsOf(kind)
		if ops.SkillsDir == nil || ops.SkillsDir() != dir {
			t.Errorf("%s: skills dir differs from %s", kind, dir)
		}
	}
}

// A registered kind must say whether it takes the member's instructions: it implements
// agents.Instructions and is in instrSupportedKinds (display order), or it is in
// instrUnsupported with a reason, or it runs no agent CLI at all.
func TestInstructionsClassifyEveryRegisteredKind(t *testing.T) {
	for _, kind := range sessionx.RegisteredKinds() {
		a, _ := sessionx.RegisteredAgent(kind)
		_, impl := a.(agents.Instructions)
		listed := slices.Contains(instrSupportedKinds, kind)
		unsupported := slices.ContainsFunc(instrUnsupported, func(u struct{ kind, reason string }) bool { return u.kind == kind })
		none := slices.Contains(noInstructionTargets, kind)
		if impl != listed {
			t.Errorf("%s: implements agents.Instructions = %v, instrSupportedKinds lists it = %v", kind, impl, listed)
		}
		if n := b2i(listed) + b2i(unsupported) + b2i(none); n != 1 {
			t.Errorf("%s: must be exactly one of supported / unsupported (with reason) / no target, got %d", kind, n)
		}
	}
	for _, kind := range noInstructionTargets {
		if _, ok := sessionx.RegisteredAgent(kind); !ok {
			t.Errorf("noInstructionTargets has %q, which is not a registered kind", kind)
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Instructions resolve by the exact registry key: an alias or alternate spelling reaches no
// target, no delivery, and a preview refusal — never claude's (the AgentOf fallback).
func TestInstructionsRefuseAlternateSpellings(t *testing.T) {
	instrEnv(t)
	for _, kind := range []string{"Claude", "CLAUDE", " claude", "claude-code", "Codex", "codex ", "kiro-cli", "Opencode", "open-code", "gh-copilot", "Muse", "LCPP", "cursor", "shell", "ssm", ""} {
		if _, ok := instrOpsOf(kind); ok {
			t.Errorf("instrOpsOf(%q) resolved", kind)
		}
		if d := instrDeliveryOf(kind); d != "" {
			t.Errorf("instrDeliveryOf(%q) = %q", kind, d)
		}
		if kind != strings.TrimSpace(kind) {
			continue // the preview handler trims its query value, as it always did
		}
		if w := doJSON(t, "GET", "/user-notes/preview?kind="+url.QueryEscape(kind), ""); w.Code != http.StatusBadRequest {
			t.Errorf("preview kind=%q: status %d, want 400", kind, w.Code)
		}
	}
}
