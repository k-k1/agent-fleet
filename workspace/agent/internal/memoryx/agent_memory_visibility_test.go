package memoryx

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// visFixture saves one memory per row of decision 3's table and a few combinations, all with the
// word "zzword" in the body so one search finds every one of them.
type visRow struct {
	name   string
	mut    func(e *agentMemEntry)
	listed string // "described" | "tail" | "none": where memory_index puts it
	read   string // "full" | "stub"
}

var visReason = []agentMemReason{{By: "lint", Rule: "path-missing", Note: "gone.md is not on main", Revision: 1}}

var visRows = []visRow{
	{"act", func(e *agentMemEntry) {}, "described", "full"},
	{"pinact", func(e *agentMemEntry) { e.Pinned = true }, "described", "full"},
	{"sus", func(e *agentMemEntry) { e.Reasons = visReason }, "described", "full"},
	{"qui", func(e *agentMemEntry) { e.Status, e.StatusBy = "quiet", "member" }, "tail", "full"},
	{"pinqui", func(e *agentMemEntry) { e.Status, e.StatusBy, e.Pinned = "quiet", "member", true }, "tail", "full"},
	{"logk", func(e *agentMemEntry) { e.MemKind = "log" }, "tail", "full"},
	{"pinlog", func(e *agentMemEntry) { e.MemKind, e.Pinned = "log", true }, "tail", "full"},
	{"suslog", func(e *agentMemEntry) { e.MemKind, e.Reasons = "log", visReason }, "tail", "full"},
	{"arc", func(e *agentMemEntry) { e.Status, e.StatusBy = "archived", "member" }, "none", "stub"},
	{"pinarc", func(e *agentMemEntry) { e.Status, e.StatusBy, e.Pinned = "archived", "member", true }, "none", "stub"},
	{"arclog", func(e *agentMemEntry) { e.MemKind, e.Status, e.StatusBy = "log", "archived", "member" }, "none", "stub"},
	// Superseded with the status still active: only superseded_by (and invalid_at) is set.
	{"sup", func(e *agentMemEntry) {
		e.SupersededBy, e.InvalidAt = &agentMemSuperseded{Memory: "act"}, "2026-11-01T00:00:00Z"
	}, "none", "stub"},
	{"suplog", func(e *agentMemEntry) { e.MemKind, e.SupersededBy = "log", &agentMemSuperseded{Memory: "act"} }, "none", "stub"},
	{"supdoc", func(e *agentMemEntry) {
		e.SupersededBy = &agentMemSuperseded{Document: &agentMemDocTarget{Repo: "example.invalid/o/r", Path: "docs/a.md", Commit: "0123abc"}}
	}, "none", "stub"},
	{"suspin", func(e *agentMemEntry) { e.Pinned, e.SupersededBy = true, &agentMemSuperseded{Memory: "act"} }, "none", "stub"},
}

func visSeed(t *testing.T) agentMemCaller {
	t.Helper()
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	for _, r := range visRows {
		lifeSave(t, c, agentMemSaveReq{Name: r.name, Description: "about " + r.name, Body: "zzword in " + r.name + " " + strings.Repeat("x", 40)})
		r := r
		lifeSet(t, c, agentMemScopeProject, r.name, r.mut)
	}
	return c
}

func visWant(listed string) []string {
	var out []string
	for _, r := range visRows {
		if r.listed == listed {
			out = append(out, r.name)
		}
	}
	sort.Strings(out)
	return out
}

func visNames(es []agentMemEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

func visCheckIndex(t *testing.T, path string, out agentMemIndex) {
	t.Helper()
	if got, want := visNames(out.Entries), visWant("described"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: described = %v, want %v", path, got, want)
	}
	tail := strings.Join(out.More, " ")
	for _, n := range visWant("tail") {
		if !strings.Contains(tail, n) {
			t.Errorf("%s: %s is not in the names-only tail %q", path, n, tail)
		}
	}
	for _, n := range visWant("none") {
		if strings.Contains(tail, n) {
			t.Errorf("%s: excluded %s is in the tail %q", path, n, tail)
		}
	}
	for _, e := range out.Entries {
		if e.Name == "sus" && len(e.Reasons) != 1 {
			t.Errorf("%s: the suspect memory lost its reasons: %+v", path, e)
		}
	}
	if out.PinnedOmitted != 0 {
		t.Errorf("%s: a pinned quiet memory counted as a pin that did not fit: %d", path, out.PinnedOmitted)
	}
}

// memory_index (and lcpp's per-turn injection, which is the same call) places by the table.
func TestVisibilityIndexAndInjection(t *testing.T) {
	c := visSeed(t)
	out, err := agentMemListIndex(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	visCheckIndex(t, "memory_index", out)
	if line := agentMemIndexLine(agentMemEntry{Scope: "project", Name: "sus", Description: "d", Updated: "2026-11-08T00:00:00Z",
		agentMemLife: agentMemLife{Reasons: visReason}}); line != "- [project] sus — [suspect: lint path-missing: gone.md is not on main] d (2026-11-08)\n" {
		t.Errorf("index line = %q", line)
	}

	// lcpp: the same answer, as the JSON the system prompt is built from.
	raw, err := IndexJSONFor(c.Project.Root, "lcpp", 0)
	if err != nil {
		t.Fatal(err)
	}
	var inj agentMemIndex
	if err := json.Unmarshal([]byte(raw), &inj); err != nil {
		t.Fatal(err)
	}
	visCheckIndex(t, "lcpp injection", inj)
	for _, r := range visRows {
		if r.listed == "none" && strings.Contains(raw, `"`+r.name+`"`) {
			t.Errorf("lcpp injection names excluded %s: %s", r.name, raw)
		}
	}
}

// A pinned memory fills the described part first, but only an active one: a pin never lifts a
// quiet memory or a log, and never undoes an archive.
func TestVisibilityPinLast(t *testing.T) {
	c := visSeed(t)
	out, _ := agentMemListIndex(c, 0)
	if len(out.Entries) == 0 || out.Entries[0].Name != "pinact" {
		t.Fatalf("the pinned active memory should lead: %v", visNames(out.Entries))
	}
}

// memory_search never returns an archived or superseded memory and shows reasons on a suspect one.
func TestVisibilitySearch(t *testing.T) {
	c := visSeed(t)
	hits, err := agentMemSearch(c, "zzword", 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range hits {
		got = append(got, h.Name)
		if h.Name == "sus" && len(h.Reasons) != 1 {
			t.Errorf("suspect hit without reasons: %+v", h)
		}
	}
	sort.Strings(got)
	var want []string
	for _, r := range visRows {
		if r.read == "full" {
			want = append(want, r.name)
		}
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("search = %v, want %v", got, want)
	}
	// A query for an excluded memory by its own name finds nothing.
	if hits, _ := agentMemSearch(c, "pinarc", 10); len(hits) != 0 {
		t.Errorf("an archived memory was found by name: %+v", hits)
	}
}

// memory_read answers an excluded memory with a stub and never its text; the others in full.
func TestVisibilityRead(t *testing.T) {
	c := visSeed(t)
	for _, r := range visRows {
		e, err := agentMemRead(c, "", r.name)
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if r.read == "stub" {
			if !e.Stub || e.Body != "" || e.Description != "" || e.Status == "" || e.Revision < 1 {
				t.Errorf("%s: want a stub, got %+v", r.name, e)
			}
			if strings.HasPrefix(r.name, "sup") && e.SupersededBy == nil {
				t.Errorf("%s: the stub lost superseded_by", r.name)
			}
			continue
		}
		if e.Stub || !strings.Contains(e.Body, "zzword") {
			t.Errorf("%s: want the full memory, got %+v", r.name, e)
		}
		if (r.name == "sus" || r.name == "suslog") && len(e.Reasons) != 1 {
			t.Errorf("%s: reasons missing on a read", r.name)
		}
	}
	// A stub is not a use.
	if n := agentMemLoadUsage("projects/" + c.Project.ID)["arc"]; n != 0 {
		t.Errorf("a stub read counted as a use: %d", n)
	}
}

// The other agent-facing doors: the session routes, the external routes the control plane relays,
// the near-name hint of a create, and patch.
func TestVisibilityRoutesNearNamesAndPatch(t *testing.T) {
	c := visSeed(t)
	sess := "?session=claude-main"

	_, b := memberDo(t, http.MethodGet, "/agents/memory/entries"+sess, nil)
	var idx agentMemIndex
	if json.Unmarshal(b, &idx) != nil {
		t.Fatalf("index: %s", b)
	}
	visCheckIndex(t, "GET /agents/memory/entries", idx)
	_, b = memberDo(t, http.MethodGet, "/agents/memory/entries/search"+sess+"&q=zzword&limit=50", nil)
	if strings.Contains(string(b), `"pinarc"`) || strings.Contains(string(b), `"sup"`) || !strings.Contains(string(b), `"pinqui"`) {
		t.Errorf("search route: %s", b)
	}
	_, b = memberDo(t, http.MethodGet, "/agents/memory/entries/read"+sess+"&name=arclog", nil)
	if strings.Contains(string(b), "zzword") || !strings.Contains(string(b), `"stub":true`) {
		t.Errorf("read route leaked: %s", b)
	}

	externalMode(t, ExternalMemoryRead)
	q := "?repository=" + c.Project.Remote
	if c.Project.Remote == "" {
		q = "?project=" + c.Project.ID
	}
	_, b = memberDo(t, http.MethodGet, extBase+"entries"+q, nil)
	if json.Unmarshal(b, &idx) != nil {
		t.Fatalf("external index: %s", b)
	}
	visCheckIndex(t, "external index", idx)
	_, b = memberDo(t, http.MethodGet, extBase+"search"+q+"&q=zzword&limit=50", nil)
	if strings.Contains(string(b), `"pinarc"`) || strings.Contains(string(b), `"suplog"`) || !strings.Contains(string(b), `"pinlog"`) {
		t.Errorf("external search: %s", b)
	}
	_, b = memberDo(t, http.MethodGet, extBase+"read"+q+"&name=supdoc", nil)
	if strings.Contains(string(b), "zzword") || !strings.Contains(string(b), `"stub":true`) {
		t.Errorf("external read leaked: %s", b)
	}

	// A new memory about an archived one is not pointed at it ("prefer memory_patch on X").
	if near := agentMemNearNames(c, agentMemScopeProject, "pinarc.x"); len(near) != 0 {
		t.Errorf("near names offered an archived memory: %v", near)
	}
	if near := agentMemNearNames(c, agentMemScopeProject, "pinqui.x"); len(near) != 1 || near[0] != "pinqui" {
		t.Errorf("near names of a quiet memory = %v", near)
	}
	// An agent cannot patch what it cannot see.
	arc := lifeStored(t, c, agentMemScopeProject, "arc")
	_, err := agentMemPatch(c, agentMemPatchReq{Name: "arc", Revision: arc.Revision, Edits: []agentMemPatchEdit{{Old: "zzword", New: sp("x")}}}, lifeNow)
	if agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("patch of an archived memory: %v", err)
	}
}

// The member's routes see everything: the memory list, the member read and the member search.
func TestVisibilityMemberSeesEverything(t *testing.T) {
	c := visSeed(t)
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/member-read?scope=project&project="+c.Project.ID+"&name=pinarc", nil)
	var e agentMemEntry
	if code != http.StatusOK || json.Unmarshal(b, &e) != nil || e.Stub || !strings.Contains(e.Body, "zzword") || e.Status != "archived" {
		t.Fatalf("member read: %d %s", code, b)
	}
	es, _, err := agentMemLoadDir(agentMemScopeProject, "projects/"+c.Project.ID)
	if err != nil || len(es) != len(visRows) {
		t.Fatalf("store holds %d, want %d: %v", len(es), len(visRows), err)
	}
}
