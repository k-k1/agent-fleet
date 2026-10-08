package memoryx

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// agentMemIndexFixture is n generated entries shaped like the imported claude memories:
// mostly project, some feedback and reference, long hyphenated names, Japanese descriptions.
func agentMemIndexFixture(n int) []agentMemEntry {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	types := []string{"project", "project", "feedback", "project", "reference", ""}
	pfx := []string{"adr", "mirror", "codex", "cli", "fe", "opencode", "console"}
	es := make([]agentMemEntry, n)
	for i := range es {
		es[i] = agentMemEntry{
			Name:        fmt.Sprintf("%s-%03d-some-long-memory-name-padding", pfx[i%len(pfx)], i),
			Scope:       agentMemScopeProject,
			Description: strings.Repeat("説明あいう", 20) + fmt.Sprint(i),
			Type:        types[i%len(types)],
			Updated:     base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
		}
	}
	return es
}

func TestAgentMemBudgetIndexBounds472(t *testing.T) {
	es := agentMemIndexFixture(472)
	agentMemRank(es)
	desc, more, omitted, _ := agentMemBudgetIndex(es, 0)

	size := 0
	for _, e := range desc {
		size += len(agentMemIndexLine(e))
	}
	if size > agentMemIndexBudgetDefault || len(desc) == 0 {
		t.Fatalf("described part = %d bytes (%d entries), budget %d", size, len(desc), agentMemIndexBudgetDefault)
	}
	if tail := agentMemTailSize(more); tail > agentMemIndexTailBudget-agentMemIndexTailOverhead || len(more) == 0 {
		t.Fatalf("tail = %d bytes, budget %d", tail, agentMemIndexTailBudget)
	}
	// Every entry is described, named in the tail, or counted.
	named := 0
	for _, g := range more {
		if i := strings.Index(g, "{"); i >= 0 {
			named += strings.Count(g, ",") + 1
		} else {
			named++
		}
	}
	if len(desc)+named+omitted != len(es) {
		t.Fatalf("described %d + named %d + omitted %d != %d", len(desc), named, omitted, len(es))
	}
	// Rank: feedback precedes project; within a tier newer comes first.
	seenOther := false
	for i, e := range desc {
		if agentMemRankTier(e) == 1 {
			seenOther = true
		} else if seenOther {
			t.Fatalf("feedback %q after a lower tier", e.Name)
		}
		if i > 0 && agentMemRankTier(desc[i-1]) == agentMemRankTier(e) && desc[i-1].Updated < e.Updated {
			t.Fatalf("%q is older than %q but listed first", desc[i-1].Name, e.Name)
		}
	}
}

func TestAgentMemBudgetIndexSmallStoreFitsAndKeepsOrder(t *testing.T) {
	es := agentMemIndexFixture(5)
	agentMemRank(es)
	desc, more, omitted, _ := agentMemBudgetIndex(es, 0)
	if len(desc) != 5 || more != nil || omitted != 0 {
		t.Fatalf("desc=%d more=%v omitted=%d", len(desc), more, omitted)
	}
}

func TestAgentMemClampBudget(t *testing.T) {
	for in, want := range map[int]int{-5: agentMemIndexBudgetMin, 0: agentMemIndexBudgetDefault, 1: agentMemIndexBudgetMin,
		10000: 10000, 1 << 30: agentMemIndexBudgetMax} {
		if got := agentMemClampBudget(in); got != want {
			t.Errorf("clamp(%d) = %d, want %d", in, got, want)
		}
	}
	// A budget below the minimum is raised, not honoured.
	es := agentMemIndexFixture(472)
	agentMemRank(es)
	desc, _, _, _ := agentMemBudgetIndex(es, 100)
	size := 0
	for _, e := range desc {
		size += len(agentMemIndexLine(e))
	}
	if size > agentMemIndexBudgetMin || size < agentMemIndexBudgetMin/2 {
		t.Errorf("tiny budget rendered %d bytes", size)
	}
}

func TestAgentMemCutRunesAtBoundary(t *testing.T) {
	s := strings.Repeat("あ", 100)
	got := agentMemCutRunes(s, 80)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 81 || !strings.HasSuffix(got, "…") {
		t.Fatalf("cut = %q", got)
	}
	if agentMemCutRunes("short", 80) != "short" || agentMemCutRunes(strings.Repeat("a", 80), 80) != strings.Repeat("a", 80) {
		t.Fatal("a description within the limit must not change")
	}
}

func TestAgentMemNameAbbrevAndGrouping(t *testing.T) {
	long := strings.Repeat("x", 40)
	if got := agentMemAbbrevName(long); got != strings.Repeat("x", 32)+"…" {
		t.Errorf("abbrev = %q", got)
	}
	if agentMemAbbrevName(strings.Repeat("y", 32)) != strings.Repeat("y", 32) {
		t.Error("a 32-byte name must stay whole")
	}
	got := agentMemGroupNames([]string{"adr-0079-b", "solo", "adr-0072-a", "mirror-x", "zeta-1", "zeta-2"})
	want := []string{"adr-{0072-a,0079-b}", "mirror-x", "solo", "zeta-{1,2}"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("grouped = %v, want %v", got, want)
	}
	// A hyphen-less name never merges into a group, and a lone trailing-hyphen name stays whole.
	if got := strings.Join(agentMemGroupNames([]string{"adr", "adr-x", "adr-y"}), "|"); got != "adr|adr-{x,y}" {
		t.Errorf("adr group = %q", got)
	}
	if got := strings.Join(agentMemGroupNames([]string{"adr-"}), "|"); got != "adr-" {
		t.Errorf("lone adr- = %q", got)
	}
	if got := strings.Join(agentMemGroupNames([]string{"adr-", "adr-x"}), "|"); got != "adr-{,x}" {
		t.Errorf("adr-/adr-x = %q", got)
	}
}

func TestAgentMemTailOverflowCountsTheRest(t *testing.T) {
	// 3000 distinct single-name groups cannot fit in 8 KiB.
	es := make([]agentMemEntry, 3000)
	for i := range es {
		es[i] = agentMemEntry{Name: fmt.Sprintf("n%04d", i), Scope: "user", Description: "d", Updated: "2026-01-01T00:00:00Z"}
	}
	desc, more, omitted, _ := agentMemBudgetIndex(es, agentMemIndexBudgetMin)
	if omitted == 0 || agentMemTailSize(more) > agentMemIndexTailBudget-agentMemIndexTailOverhead {
		t.Fatalf("omitted=%d tail=%d", omitted, agentMemTailSize(more))
	}
	if len(desc)+len(more)+omitted != len(es) {
		t.Fatalf("accounting: %d+%d+%d", len(desc), len(more), omitted)
	}
}

func TestAgentMemListIndexRanksAndReportsTruncation(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	save := func(name, typ string, at time.Time) {
		if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d " + name, Type: typ, Body: "b"}, at); err != nil {
			t.Fatal(err)
		}
	}
	save("old-feedback", "feedback", now)
	save("new-project", "project", now.Add(2*time.Hour))
	save("mid-project", "project", now.Add(time.Hour))
	idx, err := agentMemListIndex(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range idx.Entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "old-feedback,new-project,mid-project" || idx.Truncated || idx.More != nil {
		t.Fatalf("index = %+v", idx)
	}
}

func TestAgentMemIndexLineIsPinned(t *testing.T) {
	e := agentMemEntry{Name: "n", Scope: "user", Description: "d", Type: "feedback", Kinds: []string{"claude"}, Updated: "2026-10-04T09:00:00Z"}
	if got, want := agentMemIndexLine(e), "- [user] n — d (feedback; for claude; 2026-10-04)\n"; got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

// An index line carries the description up to 150 characters, cut after that; the cut is what
// the budget is measured on, so the constant is pinned by value.
func TestAgentMemIndexDescriptionLength(t *testing.T) {
	for _, tc := range []struct {
		runes, want int
	}{{149, 149}, {150, 150}, {151, 151}, {400, 151}} {
		e := agentMemEntry{Name: "n", Scope: "user", Description: strings.Repeat("あ", tc.runes), Updated: "2026-10-09T00:00:00Z"}
		desc, _, _, _ := agentMemBudgetIndex([]agentMemEntry{e}, 0)
		if len(desc) != 1 {
			t.Fatalf("%d runes: %d lines", tc.runes, len(desc))
		}
		if got := utf8.RuneCountInString(desc[0].Description); got != tc.want {
			t.Errorf("%d-character description is %d characters in the index, want %d", tc.runes, got, tc.want)
		}
		if cut := tc.runes > 150; cut != strings.HasSuffix(desc[0].Description, "…") {
			t.Errorf("%d characters: cut marker = %v", tc.runes, !cut)
		}
	}
}
