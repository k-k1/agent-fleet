package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentroutes"
	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// hidingCP answers HiddenKinds per tenant.
type hidingCP struct {
	*memCP
	byTenant map[string]map[string]bool
	err      error
	// paths records every Agent path asked, when set.
	paths *[]string
}

func (c hidingCP) AgentText(ctx context.Context, rt runtime.Runtime, method, path string, body []byte) (string, error) {
	if c.paths != nil {
		*c.paths = append(*c.paths, path)
	}
	return c.memCP.AgentText(ctx, rt, method, path, body)
}

func (c hidingCP) HiddenKinds(_ context.Context, tenantID string) (map[string]bool, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.byTenant[tenantID], nil
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
	want := memberToolsHiding(nil, nil)
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
	for _, tl := range memberToolsHiding(all, nil) {
		if strings.Contains(tl.desc, ";  ") || strings.Contains(tl.desc, "  If") || strings.Contains(tl.desc, "`kind`.  ") {
			t.Errorf("%s: malformed description %q", tl.name, tl.desc)
		}
	}
	if !reflect.DeepEqual(createKinds(all), []string(nil)) {
		t.Errorf("createKinds %v", createKinds(all))
	}
}

// notAKind are the places a kind's name is an ordinary word: get_session_output's pagination
// cursor. Each is stripped, exactly, before a text is searched for a hidden kind.
var notAKind = []string{
	"assistant output since an optional cursor",
	"{output, cursor, status}",
	"pass the returned cursor as since",
	"cursor from a previous call (optional)",
}

// serializedToolList is tools/list as a client receives it for prin, with notAKind stripped.
func serializedToolList(t *testing.T, a API, prin *mcpPrincipal) string {
	t.Helper()
	b, err := json.Marshal(a.mcpToolList(context.Background(), prin))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, w := range notAKind {
		out = strings.ReplaceAll(out, w, "")
	}
	return out
}

func namesKind(text, kind string) bool {
	return regexp.MustCompile(`\b` + kind + `\b`).MatchString(text)
}

// The whole tools/list a member receives, every tool and schema, names no kind their tenant hides:
// each model kind hidden alone, and all of them at once. Shown kinds stay named.
func TestToolListNamesNoHiddenKind(t *testing.T) {
	base := newMemCP(t, memStore(t), "running")
	prin := mcpPrincipal{identityID: base.prin.identityID, membershipID: base.prin.membershipID, scope: "write"}
	list := func(h map[string]bool) string {
		return serializedToolList(t, New(hidingCP{memCP: base, byTenant: map[string]map[string]bool{base.tenantID: h}}), &prin)
	}
	for _, k := range agentroutes.ModelKinds() {
		text := list(map[string]bool{k: true})
		if namesKind(text, k) {
			t.Errorf("with %s hidden, tools/list still names it", k)
		}
		for _, other := range []string{"claude", "codex"} {
			if other != k && !namesKind(text, other) {
				t.Errorf("with %s hidden, tools/list no longer names %s", k, other)
			}
		}
	}
	all := map[string]bool{}
	for _, k := range agentroutes.ModelKinds() {
		all[k] = true
	}
	text := list(all)
	for k := range all {
		if namesKind(text, k) {
			t.Errorf("with every kind hidden, tools/list names %s", k)
		}
	}
}

// When the tenant's policy, or the caller's membership, cannot be read, discovery fails closed:
// tools/list names no model kind, and list_models and create_session refuse every one before
// anything reaches the Workspace.
func TestToolListFailsClosed(t *testing.T) {
	base := newMemCP(t, memStore(t), "running")
	prin := mcpPrincipal{identityID: base.prin.identityID, membershipID: base.prin.membershipID, scope: "write"}
	stranger := mcpPrincipal{identityID: base.prin.identityID, membershipID: "no-such-membership", scope: "write"}
	for name, c := range map[string]struct {
		cp   hidingCP
		prin *mcpPrincipal
	}{
		"policy unreadable":  {hidingCP{memCP: base, err: errors.New("database is locked")}, &prin},
		"membership unknown": {hidingCP{memCP: base}, &stranger},
	} {
		t.Run(name, func(t *testing.T) {
			a := New(c.cp)
			text := serializedToolList(t, a, c.prin)
			for _, k := range agentroutes.ModelKinds() {
				if namesKind(text, k) {
					t.Errorf("tools/list names %s", k)
				}
			}
			tools := a.toolsFor(context.Background(), c.prin)
			for _, tl := range tools {
				switch tl.name {
				case "list_models":
					if _, err := tl.run(context.Background(), a, &Resolved{}, map[string]any{"kind": "codex"}); err == nil {
						t.Error("list_models(kind=codex) ran")
					}
				case "create_session":
					for _, kind := range []string{"", "claude", "codex"} {
						if _, err := tl.run(context.Background(), a, &Resolved{}, map[string]any{"kind": kind}); err == nil || !strings.Contains(err.Error(), "kind must be one of") {
							t.Errorf("create_session(kind=%q): %v", kind, err)
						}
					}
				}
			}
		})
	}
}

// runTool runs one member tool as prin would, with the hidden set of cp.
func runTool(t *testing.T, cp hidingCP, prin *mcpPrincipal, name string, args map[string]any) (string, error) {
	t.Helper()
	a := New(cp)
	for _, tl := range a.toolsFor(context.Background(), prin) {
		if tl.name == name {
			res, _ := cp.ResolveByMembership(context.Background(), "", "")
			return tl.run(context.Background(), a, res, args)
		}
	}
	t.Fatalf("tool %s not listed", name)
	return "", nil
}

// create_session's refusal of a kind, hidden or unknown, names no hidden kind, under a policy that
// hides some and under one that cannot be read. With nothing hidden the catalogue's own refusal
// answers, as before.
func TestCreateSessionRefusalNamesNoHiddenKind(t *testing.T) {
	base := newMemCP(t, memStore(t), "running")
	prin := mcpPrincipal{identityID: base.prin.identityID, membershipID: base.prin.membershipID, scope: "write"}
	some := map[string]bool{"agy": true, "copilot": true, "cursor": true, "kiro": true}
	for name, c := range map[string]struct {
		cp     hidingCP
		hidden []string
	}{
		"restricted": {hidingCP{memCP: base, byTenant: map[string]map[string]bool{base.tenantID: some}}, []string{"agy", "copilot", "cursor", "kiro"}},
		"unreadable": {hidingCP{memCP: base, err: errors.New("database is locked")}, agentroutes.ModelKinds()},
	} {
		for _, kind := range []string{"bogus", "agy", "ssm"} {
			_, err := runTool(t, c.cp, &prin, "create_session", map[string]any{"kind": kind})
			if err == nil {
				t.Fatalf("%s: create_session(kind=%s) was not refused", name, kind)
			}
			for _, h := range c.hidden {
				if namesKind(err.Error(), h) && h != kind {
					t.Errorf("%s: create_session(kind=%s) refusal names the hidden %s: %v", name, kind, h, err)
				}
			}
		}
	}
	_, err := runTool(t, hidingCP{memCP: base}, &prin, "create_session", map[string]any{"kind": "bogus"})
	if err == nil || !strings.Contains(err.Error(), "accepted kinds") {
		t.Errorf("control, nothing hidden: %v", err)
	}
}

// get_agent_usage asks the Agent for, and answers with, only the kinds the tenant shows: each of
// claude, codex and agy hidden in turn, all of them, and a policy that cannot be read. With nothing
// hidden it reports all three, as before.
func TestAgentUsageLeavesOutHiddenKinds(t *testing.T) {
	base := newMemCP(t, memStore(t), "running")
	prin := mcpPrincipal{identityID: base.prin.identityID, membershipID: base.prin.membershipID, scope: "write"}
	run := func(cp hidingCP) (map[string]any, []string) {
		t.Helper()
		var paths []string
		cp.paths = &paths
		text, err := runTool(t, cp, &prin, "get_agent_usage", nil)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("%v: %s", err, text)
		}
		return out, paths
	}
	keys := func(m map[string]any) []string {
		var out []string
		for _, k := range []string{"claude", "codex", "agy"} {
			if _, ok := m[k]; ok {
				out = append(out, k)
			}
		}
		return out
	}
	if out, paths := run(hidingCP{memCP: base}); !reflect.DeepEqual(keys(out), []string{"claude", "codex", "agy"}) || len(paths) != 3 {
		t.Fatalf("control: keys %v, paths %v", keys(out), paths)
	}
	cases := map[string]hidingCP{
		"all hidden": {memCP: base, byTenant: map[string]map[string]bool{base.tenantID: {"claude": true, "codex": true, "agy": true}}},
		"unreadable": {memCP: base, err: errors.New("database is locked")},
	}
	for _, k := range []string{"claude", "codex", "agy"} {
		cases[k+" hidden"] = hidingCP{memCP: base, byTenant: map[string]map[string]bool{base.tenantID: {k: true}}}
	}
	for name, cp := range cases {
		out, paths := run(cp)
		h, _ := cp.HiddenKinds(context.Background(), base.tenantID)
		if cp.err != nil {
			h = allModelKinds()
		}
		for _, k := range []string{"claude", "codex", "agy"} {
			_, answered := out[k]
			asked := false
			for _, p := range paths {
				asked = asked || strings.Contains(p, "/"+k+"/")
			}
			if h[k] && (answered || asked) {
				t.Errorf("%s: %s hidden but answered=%v asked=%v (%v)", name, k, answered, asked, paths)
			}
			if !h[k] && !answered {
				t.Errorf("%s: %s shown but not answered", name, k)
			}
		}
	}
}
