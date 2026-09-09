package uiprefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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

// Session steering depends on fleet observation (ADR 0073 decision 3), and the dependency is
// enforced HERE rather than only in the Console: a prefs file written by hand, restored from an
// export, or left over from turning observation off later must not produce the one combination
// that makes no sense — a session that can start children and then never look at them.
func TestFleetSpawnRequiresFleetObserve(t *testing.T) {
	for _, tc := range []struct {
		name           string
		spawn, observe bool
		want           bool
	}{
		{"both on", true, true, true},
		{"spawn without observation", true, false, false},
		{"observation alone opens nothing extra", false, true, false},
		{"neither", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writePrefs(t, map[string]any{"sessionFleetSpawn": tc.spawn, "sessionFleetObserve": tc.observe})
			if got := FleetSpawn(); got != tc.want {
				t.Fatalf("FleetSpawn() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Missing and malformed both read as off. A fleet must not inherit the ability to start
// sessions by upgrading, and a value of the wrong type is not consent either.
func TestFleetSpawnDefaultsOff(t *testing.T) {
	for _, obj := range []map[string]any{
		{},
		{"sessionFleetObserve": true},
		{"sessionFleetSpawn": "true", "sessionFleetObserve": true},
		{"sessionFleetSpawn": 1, "sessionFleetObserve": true},
	} {
		writePrefs(t, obj)
		if FleetSpawn() {
			t.Fatalf("FleetSpawn() = true for %v", obj)
		}
	}
}
