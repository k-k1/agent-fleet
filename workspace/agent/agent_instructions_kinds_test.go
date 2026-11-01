package main

import (
	"encoding/json"
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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/userinstr"
)

// noInstructionTargets are the registered kinds that run no agent CLI, so there is nothing to
// deliver instructions to. Every other registered kind must be supported or listed in
// agents.InstructionsUnsupported with a reason.
var noInstructionTargets = []string{"shell", "ssm"}

// legacyInstr is what applyInstructionsLocked, instrState, instrDeliveryOf and the preview
// carried as per-kind switches before agents.Instructions: one row per supported kind.
type legacyInstrRow struct {
	delivery, measure, path string
	preview                 string // marker block the preview shows; "" = the whole file
	fleet, skills, user     bool   // which apply steps the kind took part in
}

func legacyInstr() map[string]legacyInstrRow {
	oc := legacyInstrRow{delivery: agents.DeliveryCompose, measure: agents.MeasureBlock, path: opencode.AgentsPath(),
		preview: "user-notes", fleet: true, skills: true, user: true}
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
	instrEnv(t)
	legacy := legacyInstr()
	for _, kind := range append(sessionx.RegisteredKinds(), "", "Claude", "CODEX", "kiro-cli", "open-code", "fleet", "unknown") {
		row, supported := legacy[kind]
		ops, ok := instrOpsOf(kind)
		if ok != supported {
			t.Errorf("%q: instrOpsOf = %v, legacy supported = %v", kind, ok, supported)
			continue
		}
		if got := instrDeliveryOf(kind); got != row.delivery {
			t.Errorf("%q: delivery %q, legacy %q", kind, got, row.delivery)
		}
		if !ok {
			continue
		}
		s := ops.Shape()
		if s.Delivery != row.delivery || s.Measure != row.measure || s.Path != row.path || s.PreviewBlock != row.preview {
			t.Errorf("%q: shape %+v, legacy %+v", kind, s, row)
		}
		if (ops.Fleet != nil) != row.fleet || (ops.SkillsDir != nil) != row.skills ||
			(ops.User != nil) != row.user || (ops.Memory != nil) != row.user {
			t.Errorf("%q: apply steps fleet=%v skills=%v user=%v memory=%v, legacy %+v", kind,
				ops.Fleet != nil, ops.SkillsDir != nil, ops.User != nil, ops.Memory != nil, row)
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
// agents.Instructions and is in instrSupportedKinds (display order), or it
// implements agents.InstructionsUnsupported with a reason, or it runs no agent CLI at all.
func TestInstructionsClassifyEveryRegisteredKind(t *testing.T) {
	for _, kind := range sessionx.RegisteredKinds() {
		a, _ := sessionx.RegisteredAgent(kind)
		_, impl := a.(agents.Instructions)
		listed := slices.Contains(instrSupportedKinds, kind)
		unsupported := instrUnsupportedOf(kind) != ""
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

type previewWire struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Content string `json:"content"`
}

func previewOf(t *testing.T, kind string) previewWire {
	t.Helper()
	w := doJSON(t, "GET", "/user-notes/preview?kind="+kind, "")
	if w.Code != http.StatusOK {
		t.Fatalf("preview %s: status %d: %s", kind, w.Code, w.Body.String())
	}
	var p previewWire
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// The preview and the state row must describe the same artifact for every kind: same path, and
// once the member's text is applied the artifact the row calls applied exists in the preview.
func TestInstructionsPreviewAgreesWithState(t *testing.T) {
	instrEnv(t)
	if err := userinstr.SaveText("AGREEME\n"); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	for _, row := range instrState().Targets {
		if !row.Supported {
			continue
		}
		p := previewOf(t, row.Kind)
		if p.Path != row.Path {
			t.Errorf("%s: preview path %q, state path %q", row.Kind, p.Path, row.Path)
		}
		if !row.Applied || !p.Exists || !strings.Contains(p.Content, "AGREEME") {
			t.Errorf("%s: applied=%v preview exists=%v content has text=%v", row.Kind, row.Applied, p.Exists,
				strings.Contains(p.Content, "AGREEME"))
		}
	}
}

// opencode 2 keeps the member's text in the user-notes block of the global AGENTS.md, next to
// the fleet and memory blocks: the preview shows that block alone.
func TestInstructionsPreviewOpencode2ShowsOnlyTheUserBlock(t *testing.T) {
	instrEnv(t)
	useOpencodeV2(t)

	// AGENTS.md missing.
	if p := previewOf(t, "opencode"); p.Path != opencode.AgentsPath() || p.Exists || p.Content != "" {
		t.Errorf("no AGENTS.md: %+v", p)
	}

	// The file exists with the fleet block only.
	reconcileAgentInstructions()
	if !fileHasBlock(opencode.AgentsPath(), "fleet") {
		t.Fatal("setup: the fleet block was not written")
	}
	if p := previewOf(t, "opencode"); p.Exists || p.Content != "" {
		t.Errorf("AGENTS.md without a user block: %+v", p)
	}

	// With the member's text: the block's content, not the fleet policy beside it.
	if err := userinstr.SaveText("Always report in Japanese.\n"); err != nil {
		t.Fatal(err)
	}
	reconcileAgentInstructions()
	p := previewOf(t, "opencode")
	if p.Path != opencode.AgentsPath() || !p.Exists || !strings.Contains(p.Content, "Always report in Japanese.") {
		t.Errorf("with a user block: %+v", p)
	}
	if strings.Contains(p.Content, "do not delete other sessions") || strings.Contains(p.Content, "agent-fleet:") {
		t.Errorf("preview echoes more than the user block:\n%s", p.Content)
	}
}

// oldInstrUnsupported is the hand-kept list instrUnsupported held before the declaration moved
// into each kind's package. It is deliberately a literal copy: the derived answer must equal it
// for every registered kind, and for a spelling or kind the old exact-match loop never matched.
var oldInstrUnsupported = []struct{ kind, reason string }{
	{"cursor", "no_user_scope"},
}

func TestInstrUnsupportedMatchesOldList(t *testing.T) {
	old := func(kind string) string {
		for _, u := range oldInstrUnsupported {
			if u.kind == kind {
				return u.reason
			}
		}
		return ""
	}
	probes := append([]string{"Cursor", "CURSOR", " cursor", "", "unknown-kind", "cursor-agent"}, sessionx.RegisteredKinds()...)
	for _, kind := range probes {
		if got, want := instrUnsupportedOf(kind), old(kind); got != want {
			t.Errorf("instrUnsupportedOf(%q) = %q, old list says %q", kind, got, want)
		}
		wantSupport := agents.TriUnverified
		switch {
		case slices.Contains(instrSupportedKinds, kind):
			wantSupport = agents.TriYes
		case old(kind) != "":
			wantSupport = agents.TriNo
		}
		if got := instrSupport(kind); got != wantSupport {
			t.Errorf("instrSupport(%q) = %v, want %v", kind, got, wantSupport)
		}
	}
	var wantKinds []string
	for _, k := range sessionx.RegisteredKinds() {
		if old(k) != "" {
			wantKinds = append(wantKinds, k)
		}
	}
	if got := instrUnsupportedKinds(); !slices.Equal(got, wantKinds) {
		t.Errorf("instrUnsupportedKinds() = %v, want %v", got, wantKinds)
	}
	// The catalog and the screen rows carry the same reason, byte for byte.
	for _, e := range buildKindCatalog(false).Kinds {
		if e.InstructionsReason != old(e.Kind) {
			t.Errorf("catalog %s: instructionsReason = %q, want %q", e.Kind, e.InstructionsReason, old(e.Kind))
		}
	}
	var rows []instrTarget
	for _, tg := range instrState().Targets {
		if !tg.Supported {
			rows = append(rows, tg)
		}
	}
	if len(rows) != len(oldInstrUnsupported) {
		t.Fatalf("unsupported rows = %v, want %d", rows, len(oldInstrUnsupported))
	}
	for i, u := range oldInstrUnsupported {
		if rows[i] != (instrTarget{Kind: u.kind, Reason: u.reason}) {
			t.Errorf("row %d = %+v, want %s/%s", i, rows[i], u.kind, u.reason)
		}
	}
}

// A kind says either that it takes instructions or that it cannot, never both.
func TestInstructionsAndUnsupportedAreExclusive(t *testing.T) {
	for _, kind := range sessionx.RegisteredKinds() {
		a, _ := sessionx.RegisteredAgent(kind)
		_, yes := a.(agents.Instructions)
		u, no := a.(agents.InstructionsUnsupported)
		if yes && no {
			t.Errorf("%s implements both agents.Instructions and agents.InstructionsUnsupported", kind)
		}
		if no && u.InstructionsUnsupported() == "" {
			t.Errorf("%s: InstructionsUnsupported returns no reason code", kind)
		}
	}
}
