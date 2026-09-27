package muse

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
)

func strp(s string) *string { return &s }

// Measured on 1.3.0-R3401.1: a plugin skill really does come back twice, `threejs` and
// `threejs:threejs`, with the same display name and description.
func TestDedupePluginSpellingsKeepsTheBareWinner(t *testing.T) {
	got := dedupePluginSpellings([]msp.SkillCatalogEntry{
		{Selector: "plan", DisplayName: "plan", Description: "d", Source: msp.SkillSourceBundled},
		{Selector: "threejs", DisplayName: "threejs", Description: "3d", Source: msp.SkillSourcePlugin, PluginID: strp("threejs")},
		{Selector: "threejs:threejs", DisplayName: "threejs", Description: "3d", Source: msp.SkillSourcePlugin, PluginID: strp("threejs")},
	})
	var sels []string
	for _, s := range got {
		sels = append(sels, s.Selector)
	}
	if len(sels) != 2 || sels[0] != "plan" || sels[1] != "threejs" {
		t.Errorf("selectors = %v, want [plan threejs]", sels)
	}
}

// A skill whose bare name ANOTHER plugin won has only the qualified spelling, and dropping it
// would hide a skill rather than a duplicate.
func TestDedupePluginSpellingsKeepsAQualifiedOnlySkill(t *testing.T) {
	got := dedupePluginSpellings([]msp.SkillCatalogEntry{
		{Selector: "deploy", DisplayName: "deploy", Description: "a", Source: msp.SkillSourcePlugin, PluginID: strp("acme")},
		{Selector: "acme:deploy", DisplayName: "deploy", Description: "a", Source: msp.SkillSourcePlugin, PluginID: strp("acme")},
		{Selector: "other:deploy", DisplayName: "deploy", Description: "b", Source: msp.SkillSourcePlugin, PluginID: strp("other")},
	})
	var sels []string
	for _, s := range got {
		sels = append(sels, s.Selector)
	}
	if len(sels) != 2 || sels[0] != "deploy" || sels[1] != "other:deploy" {
		t.Errorf("selectors = %v, want [deploy other:deploy]", sels)
	}
}

func TestDedupePluginSpellingsFillsTheDisplayName(t *testing.T) {
	got := dedupePluginSpellings([]msp.SkillCatalogEntry{
		{Selector: "bare", Source: msp.SkillSourceUser},
		{Selector: "", DisplayName: "nameless", Source: msp.SkillSourceUser},
	})
	if len(got) != 1 || got[0].DisplayName != "bare" {
		t.Errorf("got %+v, want one row whose display name falls back to the selector", got)
	}
}

// skillPart needs a host to ask, so these run against the in-process MSP host.
func skillHost(t *testing.T, skills ...string) (*msptest.Host, *msp.Client) {
	t.Helper()
	host, cl := msptest.New(t, msp.Handler{})
	host.Handle(msp.MethodSkillList, func(msptest.Message) (any, *msp.Error) {
		rows := make([]msp.SkillCatalogEntry, 0, len(skills))
		for _, s := range skills {
			rows = append(rows, msp.SkillCatalogEntry{Selector: s, DisplayName: s, Source: msp.SkillSourceBundled})
		}
		return msp.SkillListResult{Skills: rows}, nil
	})
	return host, cl
}

func textParts(s string) []msp.TurnInputPart {
	return []msp.TurnInputPart{{Type: msp.TurnInputPartTypeText, Text: strp(s)}}
}

// 🔴 The whole point: over MSP a leading slash is plain text, and only a `skill` part is expanded
// by the host. A picker that sent "/plan" as text would look like it worked and do nothing.
func TestSkillPartConvertsAKnownSelector(t *testing.T) {
	host, cl := skillHost(t, "plan")
	defer host.Close()
	got := skillPart(cl, "sid", textParts("/plan"))
	if len(got) != 1 || got[0].Type != msp.TurnInputPartTypeSkill {
		t.Fatalf("got %+v, want one skill part", got)
	}
	if got[0].Selector == nil || *got[0].Selector != "plan" {
		t.Errorf("selector = %v", got[0].Selector)
	}
	if got[0].Arguments != nil {
		t.Errorf("arguments = %q, want none", *got[0].Arguments)
	}
}

func TestSkillPartCarriesTheArguments(t *testing.T) {
	host, cl := skillHost(t, "plan")
	defer host.Close()
	got := skillPart(cl, "sid", textParts("/plan  rewrite the importer "))
	if len(got) != 1 || got[0].Arguments == nil || *got[0].Arguments != "rewrite the importer" {
		t.Fatalf("got %+v, want the trimmed remainder as arguments", got)
	}
}

// A message that merely starts with a slash is a message. This is why the conversion asks the
// host what exists instead of trusting the prefix.
func TestSkillPartLeavesAnUnknownTokenAsText(t *testing.T) {
	host, cl := skillHost(t, "plan")
	defer host.Close()
	for _, in := range []string{"/tmp/notes.md is stale", "/usr/bin/muse --version", "/ ", "/plan-something-else"} {
		got := skillPart(cl, "sid", textParts(in))
		if len(got) != 1 || got[0].Type != msp.TurnInputPartTypeText || *got[0].Text != in {
			t.Errorf("%q became %+v, want the text untouched", in, got)
		}
	}
}

func TestSkillPartLeavesAPlainMessageAlone(t *testing.T) {
	host, cl := skillHost(t, "plan")
	defer host.Close()
	got := skillPart(cl, "sid", textParts("please run the plan"))
	if len(got) != 1 || got[0].Type != msp.TurnInputPartTypeText {
		t.Errorf("got %+v, want the text untouched", got)
	}
	if len(host.Received()) != 0 {
		t.Errorf("a message with no leading slash asked the host anyway: %d calls", len(host.Received()))
	}
}

// An attachment rides along with the skill invocation rather than being dropped.
func TestSkillPartKeepsTheOtherParts(t *testing.T) {
	host, cl := skillHost(t, "plan")
	defer host.Close()
	parts := append(textParts("/plan"), msp.TurnInputPart{Type: msp.TurnInputPartTypeImage, MediaType: strp("image/png")})
	got := skillPart(cl, "sid", parts)
	if len(got) != 2 || got[0].Type != msp.TurnInputPartTypeSkill || got[1].Type != msp.TurnInputPartTypeImage {
		t.Errorf("got %+v, want [skill image]", got)
	}
}

func TestValidSelectorRejectsWhatCannotBeAToken(t *testing.T) {
	for _, ok := range []string{"plan", "acme:deploy", "fix-bug", "fix_bug2"} {
		if !validSelector(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "tmp/notes.md", "plan!", "two words", "日本語"} {
		if validSelector(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
