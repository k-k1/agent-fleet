package uiprefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func writePrefs(t *testing.T, obj map[string]any) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(obj)
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Session steering is its own switch, read straight from the prefs.
//
// It used to be conjoined with fleet observation, which is how a caller watches what it
// started. That conjunction is gone because observation is no longer a setting — every session
// has get_session_status — so a workspace that never turned observation on can still turn
// steering on, and a stored sessionFleetObserve (of either value) changes nothing.
func TestFleetSpawnIsIndependentOfTheRetiredObserveKey(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prefs map[string]any
		want  bool
	}{
		{"on", map[string]any{"sessionFleetSpawn": true}, true},
		{"off", map[string]any{"sessionFleetSpawn": false}, false},
		{"on, with the retired key left behind as false",
			map[string]any{"sessionFleetSpawn": true, "sessionFleetObserve": false}, true},
		{"off, with the retired key left behind as true",
			map[string]any{"sessionFleetSpawn": false, "sessionFleetObserve": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writePrefs(t, tc.prefs)
			if got := FleetSpawn(); got != tc.want {
				t.Fatalf("FleetSpawn() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The child limit, END TO END: a prefs file on disk changes what session.SpawnChildLimit()
// answers. Asserting SpawnChildLimit() alone would pass with the init hook deleted — the
// setting would be read by nobody and the budget would silently stay at three, which is the
// "written but never called" shape this area has produced twice (docs/log/86 §86.10).
func TestSpawnChildLimitReachesTheSessionPackage(t *testing.T) {
	for _, tc := range []struct {
		name string
		obj  map[string]any
		want int
	}{
		{"unset", map[string]any{}, session.SpawnChildLimitDefault},
		{"one", map[string]any{"sessionSpawnChildLimit": 1}, 1},
		{"the ceiling", map[string]any{"sessionSpawnChildLimit": session.SpawnChildLimitMax},
			session.SpawnChildLimitMax},
		// Past the ceiling, and the wrong type: neither is a value the Console can produce, and
		// both fall back to the default rather than to a number nobody picked.
		{"past the ceiling", map[string]any{"sessionSpawnChildLimit": 99}, session.SpawnChildLimitDefault},
		{"a string", map[string]any{"sessionSpawnChildLimit": "5"}, session.SpawnChildLimitDefault},
		{"zero", map[string]any{"sessionSpawnChildLimit": 0}, session.SpawnChildLimitDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writePrefs(t, tc.obj)
			if got := session.SpawnChildLimit(); got != tc.want {
				t.Fatalf("session.SpawnChildLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}

// Missing and malformed both read as off. A fleet must not inherit the ability to start
// sessions by upgrading, and a value of the wrong type is not consent either.
func TestFleetSpawnDefaultsOff(t *testing.T) {
	for _, obj := range []map[string]any{
		{},
		{"sessionFleetSpawn": "true"},
		{"sessionFleetSpawn": 1},
	} {
		writePrefs(t, obj)
		if FleetSpawn() {
			t.Fatalf("FleetSpawn() = true for %v", obj)
		}
	}
}
