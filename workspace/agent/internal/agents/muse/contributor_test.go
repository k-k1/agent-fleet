package muse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestContributorModeArgsNameBothDirections(t *testing.T) {
	if got := ContributorModeArgs(true); !slices.Equal(got, []string{"--contributor-mode", "on"}) {
		t.Errorf("on = %v", got)
	}
	if got := ContributorModeArgs(false); !slices.Equal(got, []string{"--contributor-mode", "off"}) {
		t.Errorf("off = %v", got)
	}
	for _, on := range []bool{false, true} {
		if args := serveArgs(on); !hasPair(args, ContributorModeArgs(on)) {
			t.Errorf("serveArgs(%v) = %v, missing %v", on, args, ContributorModeArgs(on))
		}
	}
}

// The flag is per process, so the mode a session was created with must reach every host spawned
// for it: a stop and start, an Agent restart and a crashed host all come back through resume.
// The recorded value is the only input — a meta written before the field existed is off.
func TestResumeSpawnsTheHostWithTheRecordedContributorMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta func(t *testing.T) session.Meta
		want string
	}{
		{"recorded on", func(t *testing.T) session.Meta {
			m := metaFor(t, "contrib-on")
			m.MuseContributorMode = true
			return m
		}, "on"},
		{"recorded off", func(t *testing.T) session.Meta { return metaFor(t, "contrib-off") }, "off"},
		{"meta older than the field", func(t *testing.T) session.Meta {
			var m session.Meta
			legacy := `{"name":"contrib-legacy-` + strings.ReplaceAll(t.Name(), "/", "-") + `","dir":"` + t.TempDir() +
				`","kind":"muse","driver":"managed","model":"muse-spark-1.3"}`
			if err := json.Unmarshal([]byte(legacy), &m); err != nil {
				t.Fatal(err)
			}
			return m
		}, "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			writeAuth(t, accountJSON)
			argvFile := filepath.Join(t.TempDir(), "argv")
			// Records its argv and dies before the handshake, so resume fails after the spawn.
			fakeMuse(t, "printf '%s\\n' \"$@\" > '"+argvFile+"'\nexit 1\n")
			m := tc.meta(t)
			t.Cleanup(func() {
				handlesMu.Lock()
				delete(handles, m.Name)
				handlesMu.Unlock()
			})
			if _, err := (managedDriver{}).resume(m); err == nil {
				t.Fatal("resume succeeded against a CLI that exits at once")
			}
			b, err := os.ReadFile(argvFile)
			if err != nil {
				t.Fatalf("the host was never spawned: %v", err)
			}
			args := strings.Split(strings.TrimSpace(string(b)), "\n")
			if args[0] != "serve" || !hasPair(args, []string{"--contributor-mode", tc.want}) {
				t.Errorf("argv = %v, want serve with --contributor-mode %s", args, tc.want)
			}
		})
	}
}

// The catalog probe runs no turn, so it never carries the member's mode: always off.
func TestCatalogProbeRunsWithContributorModeOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	argvFile := filepath.Join(t.TempDir(), "argv")
	fakeMuse(t, "printf '%s\\n' \"$@\" > '"+argvFile+"'\nexit 1\n")
	if _, _, err := dialProbe(); err == nil {
		t.Fatal("dialProbe succeeded against a CLI that exits at once")
	}
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("the probe host was never spawned: %v", err)
	}
	if args := strings.Split(strings.TrimSpace(string(b)), "\n"); !hasPair(args, []string{"--contributor-mode", "off"}) {
		t.Errorf("probe argv = %v, want --contributor-mode off", args)
	}
}

// hasPair reports whether args holds pair as adjacent elements.
func hasPair(args, pair []string) bool {
	for i := 0; i+len(pair) <= len(args); i++ {
		if slices.Equal(args[i:i+len(pair)], pair) {
			return true
		}
	}
	return false
}
