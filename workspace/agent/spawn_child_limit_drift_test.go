package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The child limit is picked in the Console and enforced in the Agent, and the two ends carry
// the range separately: `SPAWN_CHILD_LIMITS` is the list of buttons, `session.SpawnChildLimitMax`
// is the number the Agent will accept. Nothing but a comment held them together.
//
// The direction that hurts is lowering the Go ceiling: the Console keeps offering the old top
// choice, picking it stores an out-of-range value, and NormalizeSpawnChildLimit answers it with
// the DEFAULT rather than the nearest bound (ADR 0073 decision 6 amendment) — so the user asks
// for six children and silently gets three. Raising it only wastes capacity, but that is a
// silent downgrade of the user's own choice, which is the shape decision 6 exists to refuse.
//
// Reading the catalogue off disk is the same idiom as consoleCatalog: a distribution built
// without console/ skips, but a console/ that is present and unparseable FAILS. A drift test
// that degrades to a skip when the thing it watches is renamed is the same as not existing.
func consoleSettingsSource(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "console", "src", "lib")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("console sources not available (%v)", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.ts"))
	if err != nil {
		t.Fatalf("console/src/lib exists but settings.ts is unreadable (did it move?): %v", err)
	}
	return string(raw)
}

// TestDriftSpawnChildLimitChoices: the buttons the Console offers are exactly 1..SpawnChildLimitMax.
func TestDriftSpawnChildLimitChoices(t *testing.T) {
	src := consoleSettingsSource(t)

	m := regexp.MustCompile(`SPAWN_CHILD_LIMITS\s*=\s*\[([^\]]*)\]`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("no SPAWN_CHILD_LIMITS array in console/src/lib/settings.ts (renamed? then update this test and session.SpawnChildLimitMax together)")
	}

	var got []int
	for _, f := range strings.Split(m[1], ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("SPAWN_CHILD_LIMITS holds a non-number %q: the Agent stores this straight into ui-prefs", f)
		}
		got = append(got, n)
	}

	var want []int
	for n := 1; n <= session.SpawnChildLimitMax; n++ {
		want = append(want, n)
	}
	if len(got) != len(want) {
		t.Fatalf("SPAWN_CHILD_LIMITS = %v, want %v (session.SpawnChildLimitMax = %d)",
			got, want, session.SpawnChildLimitMax)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SPAWN_CHILD_LIMITS = %v, want %v (session.SpawnChildLimitMax = %d)",
				got, want, session.SpawnChildLimitMax)
		}
	}

	// Every offered choice has to survive the Agent's normalization. This is the assertion that
	// states the consequence rather than the shape: an option the Agent answers with something
	// else is an option that silently does not do what the button says.
	for _, n := range got {
		if session.NormalizeSpawnChildLimit(n) != n {
			t.Errorf("the Console offers %d but the Agent normalizes it to %d",
				n, session.NormalizeSpawnChildLimit(n))
		}
	}
}

// TestDriftSpawnChildLimitDefault: the Console's stored default is the Agent's unconfigured
// answer. They are written twice — DEFAULTS in settings.ts and SpawnChildLimitDefault — and a
// workspace that never opened the setting must behave the same whichever end is asked.
func TestDriftSpawnChildLimitDefault(t *testing.T) {
	src := consoleSettingsSource(t)

	m := regexp.MustCompile(`sessionSpawnChildLimit:\s*(\d+)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("no sessionSpawnChildLimit default in console/src/lib/settings.ts (renamed?)")
	}
	got, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("unparseable default %q: %v", m[1], err)
	}
	if got != session.SpawnChildLimitDefault {
		t.Fatalf("Console default sessionSpawnChildLimit = %d, session.SpawnChildLimitDefault = %d",
			got, session.SpawnChildLimitDefault)
	}
}
