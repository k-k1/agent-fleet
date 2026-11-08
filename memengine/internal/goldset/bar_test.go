package goldset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/memengine"
)

func tier(name, mode, hash string, wrongNoop, correctNoop int, applied, calls int) TierScore {
	return TierScore{
		Name: name, Mode: mode, GoldHash: hash,
		Classify: map[memengine.Operation]ClassifyScore{
			memengine.OpNoop: {Cases: 10, Correct: correctNoop, Wrong: wrongNoop},
			memengine.OpAdd:  {Cases: 4, Correct: 4},
		},
		Patch: PatchScore{Calls: calls, Applied: applied},
	}
}

func testBar() Bar {
	return Bar{
		Operations: map[string]Threshold{
			"no-op":      {MaxWrongChanges: 2, MinFixRate: 0.8},
			"add":        {MaxWrongChanges: 0, MinFixRate: 0.7},
			"patch-call": {MaxWrongChanges: 0, MinFixRate: 0.8},
		},
		Roles: map[memengine.Role][]string{memengine.RoleSmall: {"no-op", "add", "patch-call"}},
	}
}

func TestJudgeAgainstTheBar(t *testing.T) {
	b := testBar()
	good := b.Judge(tier("good", "live", "h", 1, 9, 9, 10))
	for op, v := range good {
		if !v.Pass {
			t.Errorf("%s should pass: %s", op, v.Reason)
		}
	}
	bad := b.Judge(tier("bad", "live", "h", 3, 7, 5, 10))
	if bad["no-op"].Pass || !strings.Contains(bad["no-op"].Reason, "wrong changes 3 > 2") || !strings.Contains(bad["no-op"].Reason, "fix rate 70%") {
		t.Errorf("no-op: %+v", bad["no-op"])
	}
	if bad["patch-call"].Pass || !bad["add"].Pass {
		t.Errorf("patch-call/add: %+v %+v", bad["patch-call"], bad["add"])
	}
	// An operation with no cases cannot pass.
	if v := b.Judge(TierScore{Classify: map[memengine.Operation]ClassifyScore{}})["add"]; v.Pass {
		t.Error("no cases must not pass")
	}
}

func TestSuggestsTheCheapestPassingCandidate(t *testing.T) {
	cost := map[string]float64{"pricey": 10, "cheap": 1, "cheapest-but-fails": 0.5, "replay": 2, "old-gold": 0.1}
	tiers := []TierScore{
		tier("pricey", "live", "h", 0, 10, 10, 10),
		tier("cheap", "live", "h", 1, 9, 9, 10),
		tier("cheapest-but-fails", "live", "h", 5, 5, 5, 10),
		tier("old-gold", "live", "other", 0, 10, 10, 10),
		tier("replay", "replay", "h", 0, 10, 10, 10),
	}
	md := BarMarkdown(testBar(), tiers, "h", func(n string) float64 { return cost[n] })
	if !strings.Contains(md, "- small (no-op, add, patch-call): **cheap**") {
		t.Errorf("suggestion:\n%s", md)
	}
	if !strings.Contains(md, "FAIL: ") || !strings.Contains(md, "old-gold") || !strings.Contains(md, "another gold-set version") {
		t.Errorf("a failing candidate and a stale-gold one must be shown as such:\n%s", md)
	}
	// A replay winner is flagged, not silently trusted.
	tiers = []TierScore{tiers[4], tiers[2]}
	md = BarMarkdown(testBar(), tiers, "h", func(n string) float64 { return cost[n] })
	if !strings.Contains(md, "**replay (replay)**") && !strings.Contains(md, "**replay**") || !strings.Contains(md, "confirm with a live run") {
		t.Errorf("replay winner:\n%s", md)
	}
	// No passer: say so.
	md = BarMarkdown(testBar(), []TierScore{tiers[1]}, "h", func(string) float64 { return 1 })
	if !strings.Contains(md, "no candidate passes") {
		t.Errorf("no passer:\n%s", md)
	}
}

func TestLoadBar(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bar.json")
	os.WriteFile(p, []byte(`{"operations":{"add":{"max_wrong_changes":1,"min_fix_rate":0.5}},"roles":{"small":["add"]}}`), 0o644)
	if b, err := LoadBar(p); err != nil || b.Operations["add"].MinFixRate != 0.5 {
		t.Fatalf("%+v %v", b, err)
	}
	os.WriteFile(p, []byte(`{"operations":{"add":{}},"roles":{"small":["merge"]}}`), 0o644)
	if _, err := LoadBar(p); err == nil {
		t.Error("a role that needs an operation without a threshold must be refused")
	}
	os.WriteFile(p, []byte(`{"operations":{"add":{"max_wrong":1}}}`), 0o644)
	if _, err := LoadBar(p); err == nil {
		t.Error("a misspelt threshold must be refused, not silently ignored")
	}
}

func TestGoldHashFollowsTheContent(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.jsonl")
	os.WriteFile(in, []byte(`{"id":"X","memories":[]}`+"\n"), 0o644)
	load := func(cases string) string {
		c := filepath.Join(dir, "c.jsonl")
		os.WriteFile(c, []byte(cases), 0o644)
		s, err := Load(c, in)
		if err != nil {
			t.Fatal(err)
		}
		return s.GoldHash
	}
	a := load(`{"id":"X","observation":"o","label":{"operation":"add","ok":"OK"}}` + "\n")
	b := load(`{"id":"X","observation":"o","label":{"operation":"add","ok":"OK"}}` + "\n")
	c := load(`{"id":"X","observation":"o2","label":{"operation":"add","ok":"OK"}}` + "\n")
	if len(a) != 12 || a != b || a == c {
		t.Errorf("hashes %s %s %s", a, b, c)
	}
}
