package mcpsrv

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// hidingCP answers HiddenKinds per tenant.
type hidingCP struct {
	*memCP
	byTenant map[string]map[string]bool
}

func (c hidingCP) HiddenKinds(_ context.Context, tenantID string) map[string]bool {
	return c.byTenant[tenantID]
}

func toolTexts(t *testing.T, tools []mcpTool, name string) string {
	t.Helper()
	for _, tl := range tools {
		if tl.name == name {
			b, err := json.Marshal(tl.schema)
			if err != nil {
				t.Fatal(err)
			}
			return tl.desc + "\n" + string(b)
		}
	}
	t.Fatalf("tool %s not listed", name)
	return ""
}

// The tool texts a member reads leave out the harnesses their tenant's policy hides (ADR 0138
// D10a, #4241): list_models' and create_session's kind lists and per-kind notes, and list_models'
// refusal of an unknown kind lists only what is shown. A tenant that hides nothing gets the texts
// unchanged.
func TestToolTextsLeaveOutHiddenKinds(t *testing.T) {
	base := newMemCP(t, memStore(t), "running")
	hidden := map[string]bool{"agy": true, "copilot": true, "cursor": true, "kiro": true}
	cp := hidingCP{memCP: base, byTenant: map[string]map[string]bool{base.tenantID: hidden}}
	prin := mcpPrincipal{identityID: base.prin.identityID, membershipID: base.prin.membershipID, scope: "write"}
	tools := New(cp).toolsFor(context.Background(), &prin)

	for _, name := range []string{"list_models", "create_session"} {
		text := toolTexts(t, tools, name)
		for k := range hidden {
			if regexp.MustCompile(`\b` + k + `\b`).MatchString(text) {
				t.Errorf("%s names the hidden kind %s:\n%s", name, k, text)
			}
		}
		for _, k := range []string{"claude", "codex", "opencode", "muse"} {
			if !strings.Contains(text, k) {
				t.Errorf("%s no longer names %s", name, k)
			}
		}
	}
	if !strings.Contains(toolTexts(t, tools, "list_models"), "codex and opencode return the live catalog") {
		t.Errorf("the shared live-catalog clause was not rebuilt: %s", toolTexts(t, tools, "list_models"))
	}
	for _, tl := range tools {
		if tl.name != "list_models" {
			continue
		}
		_, err := tl.run(context.Background(), New(cp), &Resolved{}, map[string]any{"kind": "agy"})
		if err == nil || strings.Contains(err.Error(), "agy") || !strings.Contains(err.Error(), "codex") {
			t.Errorf("list_models(kind=agy): %v, want a refusal listing only the shown kinds", err)
		}
	}

	// Another tenant, hiding nothing: the texts are exactly the unhidden ones.
	plain := hidingCP{memCP: base, byTenant: nil}
	got := New(plain).toolsFor(context.Background(), &prin)
	want := memberToolsHiding(nil)
	for _, name := range []string{"list_models", "create_session"} {
		if toolTexts(t, got, name) != toolTexts(t, want, name) {
			t.Errorf("%s changed for a tenant that hides nothing", name)
		}
	}
}

// Every kind hidden leaves well-formed texts: no dangling separators, no empty list.
func TestToolTextsWithEveryKindHidden(t *testing.T) {
	all := hiddenKinds{}
	for _, e := range toolKinds() {
		all[e.Kind] = true
	}
	if s := managedKindsSentence(all); s != "" {
		t.Errorf("managed sentence %q", s)
	}
	if s := listModelsKindNotes(all); s != "" {
		t.Errorf("list_models notes %q", s)
	}
	for _, tl := range memberToolsHiding(all) {
		if strings.Contains(tl.desc, ";  ") || strings.Contains(tl.desc, "  If") || strings.Contains(tl.desc, "`kind`.  ") {
			t.Errorf("%s: malformed description %q", tl.name, tl.desc)
		}
	}
	if !reflect.DeepEqual(createKinds(all), []string(nil)) {
		t.Errorf("createKinds %v", createKinds(all))
	}
}
