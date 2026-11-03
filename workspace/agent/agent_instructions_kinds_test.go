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
// for every kind in instrFrozenKinds, and for a spelling the old exact-match loop never matched.
var oldInstrUnsupported = []struct{ kind, reason string }{
	{"cursor", "no_user_scope"},
}

// instrFrozenKinds are the kinds that existed when the pin was taken. A kind registered later is
// checked by declaration (below), never against oldInstrUnsupported, so adding a kind needs no
// edit here.
var instrFrozenKinds = []string{
	"claude", "codex", "opencode", "lcpp", "copilot", "muse", "kiro", "cursor", "agy", "shell", "ssm",
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
	// The frozen set is itself pinned: dropping an old kind from it would silently turn that
	// kind into a "new" one and lift its pin.
	if got := slices.Sorted(slices.Values(instrFrozenKinds)); !slices.Equal(got,
		[]string{"agy", "claude", "codex", "copilot", "cursor", "kiro", "lcpp", "muse", "opencode", "shell", "ssm"}) {
		t.Errorf("instrFrozenKinds changed: %v", got)
	}
	probes := append([]string{"Cursor", "CURSOR", " cursor", "", "unknown-kind", "cursor-agent"}, instrFrozenKinds...)
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

	// Within the frozen set the derived list is exactly the old one; a later kind may add to it
	// only by declaring itself unsupported (checked next).
	frozen := func(k string) bool { return slices.Contains(instrFrozenKinds, k) }
	var gotFrozen []string
	for _, k := range instrUnsupportedKinds() {
		if frozen(k) {
			gotFrozen = append(gotFrozen, k)
		}
	}
	var wantKinds []string
	for _, k := range instrFrozenKinds {
		if old(k) != "" {
			wantKinds = append(wantKinds, k)
		}
	}
	slices.Sort(gotFrozen)
	slices.Sort(wantKinds)
	if !slices.Equal(gotFrozen, wantKinds) {
		t.Errorf("instrUnsupportedKinds() restricted to the frozen kinds = %v, want %v", gotFrozen, wantKinds)
	}

	// A kind outside the frozen set: whatever it declares must hold together, for any kind.
	listed := instrUnsupportedKinds()
	for _, kind := range sessionx.RegisteredKinds() {
		if frozen(kind) {
			continue
		}
		a, _ := sessionx.RegisteredAgent(kind)
		_, takes := a.(agents.Instructions)
		reason := instrUnsupportedOf(kind)
		switch {
		case takes:
			if reason != "" || slices.Contains(listed, kind) {
				t.Errorf("%s takes instructions but reports unsupported %q (listed=%v)", kind, reason, slices.Contains(listed, kind))
			}
		case slices.Contains(noInstructionTargets, kind):
		default:
			if reason == "" {
				t.Errorf("%s has no agents.Instructions and reports no unsupported reason", kind)
			}
			if !slices.Contains(listed, kind) {
				t.Errorf("%s reports unsupported %q but is missing from instrUnsupportedKinds() = %v", kind, reason, listed)
			}
		}
	}

	// The catalog and the screen rows carry the same reason, byte for byte.
	for _, e := range buildKindCatalog(false).Kinds {
		want := instrUnsupportedOf(e.Kind)
		if frozen(e.Kind) {
			want = old(e.Kind)
		}
		if e.InstructionsReason != want {
			t.Errorf("catalog %s: instructionsReason = %q, want %q", e.Kind, e.InstructionsReason, want)
		}
	}
	rows := map[string]instrTarget{}
	for _, tg := range instrState().Targets {
		if !tg.Supported {
			rows[tg.Kind] = tg
		}
	}
	for _, u := range oldInstrUnsupported {
		if rows[u.kind] != (instrTarget{Kind: u.kind, Reason: u.reason}) {
			t.Errorf("row %s = %+v, want reason %s", u.kind, rows[u.kind], u.reason)
		}
	}
	// Every registered kind that declares itself unsupported must have its row, with the reason.
	for _, kind := range sessionx.RegisteredKinds() {
		if reason := instrUnsupportedOf(kind); reason != "" {
			if row, ok := rows[kind]; !ok || row.Reason != reason {
				t.Errorf("unsupported kind %s: row = %+v (present=%v), want reason %q", kind, row, ok, reason)
			}
		}
	}
	for kind, row := range rows {
		if !frozen(kind) && (row.Reason == "" || row.Reason != instrUnsupportedOf(kind)) {
			t.Errorf("row %s = %+v, want the declared reason %q", kind, row, instrUnsupportedOf(kind))
		}
	}
	if len(rows) < len(oldInstrUnsupported) {
		t.Errorf("unsupported rows = %v, want at least %d", rows, len(oldInstrUnsupported))
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
