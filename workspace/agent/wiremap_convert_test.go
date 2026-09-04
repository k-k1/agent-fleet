// wiremap_convert_test.go — proof that a site converted from map to struct changed not one
// byte of the wire (CONTRACT-MAP / leg 3, Agent side).
//
// The old map literals are copied here and kept. Once a site is converted, production holds
// the original shape nowhere, so the baseline would simply be gone: it moves into the test
// instead of being deleted. The copies are mechanical copies of production and must not be
// edited.
//
// The harness itself and the controls for its traps are in wiremap_equiv_test.go.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/wiretest"
)

// --- (1) handleFSLineMarks (Console: LineMarks) ---

type lineMarksIn struct {
	Added    []int
	Modified []int
	Deleted  []int
}

func TestWireEquivLineMarks(t *testing.T) {
	inputs := []lineMarksIn{
		{Added: []int{1, 2}, Modified: []int{5}, Deleted: []int{9}},
		// All three production paths (emptyMarks / untracked / diff) initialise with make
		// or []int{}, so it is never nil there. A nil slice is `null` and an empty one is
		// `[]` — different, so measure the empty-slice shape explicitly.
		{Added: []int{}, Modified: []int{}, Deleted: []int{}},
	}
	got := wiretest.AssertEquiv(t, "handleFSLineMarks", inputs,
		func(in lineMarksIn) any { // old (copy of the map literal in fs_git.go)
			return map[string]any{"added": in.Added, "modified": in.Modified, "deleted": in.Deleted}
		},
		func(in lineMarksIn) any {
			return lineMarksWire{Added: in.Added, Modified: in.Modified, Deleted: in.Deleted}
		})
	t.Logf("comparison mode: %s", got)
}

// --- (2) instrState (Console: Payload) ---
//
// A shape function, so this one conversion types two sites (GET and PUT).

type instrStateIn struct {
	Text       string
	MaxBytes   int
	Enabled    bool
	Path       string
	Targets    []instrTarget
	FleetBytes int
}

func TestWireEquivInstrState(t *testing.T) {
	inputs := []instrStateIn{
		{Text: "hello", MaxBytes: 8192, Enabled: true, Path: "/home/dev/notes.md",
			Targets: []instrTarget{{Kind: "claude", Supported: true, On: true}}, FleetBytes: 12},
		// Nothing entered = empty string; the key must keep appearing (omitempty would drop
		// it). Production has already done make(…, 0, n), so Targets is never nil there, but
		// nil (`null`) and empty (`[]`) are different, so measure both shapes.
		{Text: "", MaxBytes: 8192, Enabled: false, Path: "/x", Targets: []instrTarget{}, FleetBytes: 0},
	}
	got := wiretest.AssertEquiv(t, "instrState", inputs,
		func(in instrStateIn) any { // old (copy of the map literal in agent_instructions.go)
			return map[string]any{
				"text":        in.Text,
				"bytes":       len(in.Text),
				"max_bytes":   in.MaxBytes,
				"enabled":     in.Enabled,
				"path":        in.Path,
				"targets":     in.Targets,
				"fleet_bytes": in.FleetBytes,
			}
		},
		func(in instrStateIn) any {
			return instrStateWire{
				Text: in.Text, Bytes: len(in.Text), MaxBytes: in.MaxBytes,
				Enabled: in.Enabled, Path: in.Path, Targets: in.Targets, FleetBytes: in.FleetBytes,
			}
		})
	t.Logf("comparison mode: %s", got)
}

// TestWireEquivConvertedSitesAreAllCovered checks by machine that every converted shape has
// exactly one equivalence test.
//
// The further the conversion goes, the more this single test carries: a converted site is no
// longer a map, so wiremap.golden does not guard it. Rename a json tag and both the golden
// and the old table-driven tests still PASS — only the equivalence test goes red. So the
// existence of that test is itself checked here.
func TestWireEquivConvertedSitesAreAllCovered(t *testing.T) {
	// The whole module's list, including the types whose equivalence test lives in ANOTHER
	// package (wire types are unexported, so the proof can only sit inside its own package).
	covered := map[string]string{
		"lineMarksWire":             "TestWireEquivLineMarks (package main)",
		"instrStateWire":            "TestWireEquivInstrState (package main)",
		"mcpRegistryWire":           "TestWireEquivMCPRegistry (internal/mcpx)",
		"memoryRootsWire":           "TestWireEquivMemoryRoots (internal/memoryx)",
		"managedThreadSettingsWire": "TestWireEquivManagedThreadSettings (internal/sessionx)",
	}
	declared := wiremapConvertedWireTypes(t, ".")
	for _, name := range declared {
		if _, ok := covered[name]; !ok {
			t.Errorf("%s is a wire type CONTRACT-MAP added, but no equivalence test is registered for it. "+
				"Converting and forgetting the proof passes every gate green, so stop here.", name)
		}
	}
	for name := range covered {
		found := false
		for _, d := range declared {
			if d == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("an equivalence test is registered for %s, but the type is not in the source (if you deleted it, delete it from this table too)", name)
		}
	}
	if len(declared) == 0 {
		t.Fatal("not a single wire type was found (the scan is broken)")
	}
	t.Logf("converted wire types: %d", len(declared))
}

// wiremapConvertedMarker is the mark that has to appear in the doc comment of every type
// born from a conversion.
//
// A name suffix (`…Wire`) cannot tell them apart: types that predate CONTRACT-MAP, such as
// `mcpServerWire`, spell it the same way, so picking by name mixes "needs a proof" with "was
// always a struct". Whether a type replaced a map is provenance, not naming, so the
// provenance goes into the comment and the machine reads that.
const wiremapConvertedMarker = "was: map[string]any"

func wiremapConvertedWireTypes(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE || gd.Doc == nil {
				continue
			}
			if !strings.Contains(gd.Doc.Text(), wiremapConvertedMarker) {
				continue
			}
			for _, sp := range gd.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); ok {
					out = append(out, ts.Name.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	sort.Strings(out)
	return out
}
