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
