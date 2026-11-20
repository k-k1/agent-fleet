package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestPrepareFreshLaunchClearsOnlyCodexUserDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	path := codexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "settings.toml")
	const inline = "features = {\n  memories = false,\n}\n"
	const original = "# Keep\n" + inline + "\"model\" = \"\"\"\nluna\"\"\" # Keep too\nmodel_reasoning_effort = 'high'\ntext = \"\"\"\nmodel = 'inside string'\n\"\"\"\n[profiles.work]\nmodel = 'work'\nmodel_reasoning_effort = 'low'\n"
	const want = "# Keep\n" + inline + " # Keep too\n\ntext = \"\"\"\nmodel = 'inside string'\n\"\"\"\n[profiles.work]\nmodel = 'work'\nmodel_reasoning_effort = 'low'\n"
	if err := os.WriteFile(target, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	previous := RememberLaunchDefaults
	t.Cleanup(func() { RememberLaunchDefaults = previous })
	RememberLaunchDefaults = func() bool { return true }
	if err := prepareFreshLaunch(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != original {
		t.Fatalf("config with remembering enabled = %q, %v; want original", b, err)
	}
	RememberLaunchDefaults = func() bool { return false }
	if err := prepareFreshLaunch(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != want {
		t.Fatalf("config = %q, %v; want %q", b, err, want)
	}
	if got, err := os.Readlink(path); err != nil || got != target {
		t.Fatalf("config symlink = %q, %v; want %q", got, err, target)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("config file mode changed: %v, %v", info, err)
	}
}

func TestClearSavedLaunchDefaultsRejectsMalformedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const original = "model = 'keep'\n[unterminated\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearSavedLaunchDefaults(path); err == nil || !strings.Contains(err.Error(), "TOML") {
		t.Fatalf("malformed config error = %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != original {
		t.Fatalf("malformed config = %q, %v; want original", b, err)
	}
}

func TestTerminalClearsDefaultsOnlyForFreshLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	path := codexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "settings.toml")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	previous := RememberLaunchDefaults
	t.Cleanup(func() { RememberLaunchDefaults = previous })
	RememberLaunchDefaults = func() bool { return false }
	for _, tc := range []struct {
		name     string
		fork     string
		resume   string
		wantKeep bool
	}{
		{name: "fresh"},
		{name: "fork", fork: "fork-thread-id", wantKeep: true},
		{name: "resume", resume: "resume-thread-id", wantKeep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := session.Meta{Name: tc.name, Dir: t.TempDir(), Kind: session.KindCodex, Model: "chosen-model", Effort: "low", ForkFrom: tc.fork}
			if tc.resume != "" {
				RememberSid(session.UUID(m.Dir, m.Name), tc.resume)
			}
			if err := os.WriteFile(path, []byte("model = 'saved-model'\nmodel_reasoning_effort = 'high'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			plan, err := (agentImpl{}).BuildLaunch(m, agents.LaunchOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan.Program, "chosen-model") || !strings.Contains(plan.Program, `model_reasoning_effort="low"`) {
				t.Fatal("launch lost the selected model or effort")
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if keep := strings.Contains(string(b), "saved-model") && strings.Contains(string(b), "'high'"); keep != tc.wantKeep {
				t.Fatalf("config = %q, want saved defaults retained: %v", b, tc.wantKeep)
			}
			if link, err := os.Readlink(path); err != nil || link != target {
				t.Fatalf("Codex config symlink changed: %q, %v", link, err)
			}
		})
	}
}

func TestManagedClearsDefaultsOnlyWhenStartingAThread(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("AF_CODEX_APP_SERVER_DISABLE", "")
	path := codexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	previousPref, previousSupervisor := RememberLaunchDefaults, supervisor
	t.Cleanup(func() { RememberLaunchDefaults, supervisor = previousPref, previousSupervisor })
	RememberLaunchDefaults = func() bool { return false }
	mock, client := newMockCodexServer(t)
	supervisor = &Supervisor{up: true, client: client, gen: 1}
	const saved = "model = 'saved-model'\nmodel_reasoning_effort = 'high'\n"
	for _, tc := range []struct {
		name, first, last string
	}{
		{"fresh", "thread/start", "thread/start"},
		{"resume", "thread/resume", "thread/resume"},
		{"fork", "thread/fork", "thread/fork"},
		{"no-rollout", "thread/resume", "thread/start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := session.Meta{Name: "prefs-" + tc.name, Dir: t.TempDir(), Kind: session.KindCodex}
			t.Cleanup(func() {
				handlesMu.Lock()
				delete(handles, m.Name)
				handlesMu.Unlock()
			})
			switch tc.first {
			case "thread/resume":
				RememberSid(session.UUID(m.Dir, m.Name), "prior-thread-id")
			case "thread/fork":
				m.ForkFrom = "parent-thread-id"
			}
			if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
				t.Fatal(err)
			}
			requests := make(chan string, 2)
			mock.mu.Lock()
			mock.threadRPC = func(method string) (map[string]any, string) {
				requests <- method
				b, err := os.ReadFile(path)
				want := saved
				if method == "thread/start" {
					want = "\n\n"
				}
				if err != nil || string(b) != want {
					t.Errorf("config read by %s = %q, %v; want %q", method, b, err, want)
				}
				if tc.name == "no-rollout" && method == "thread/resume" {
					return nil, "no rollout found"
				}
				return map[string]any{"thread": map[string]string{"id": "thread-" + tc.name}}, ""
			}
			mock.mu.Unlock()
			if _, err := (managedDriver{}).resume(m); err != nil {
				t.Fatal(err)
			}
			wantRequests := []string{tc.first}
			if tc.last != tc.first {
				wantRequests = append(wantRequests, tc.last)
			}
			for _, want := range wantRequests {
				select {
				case got := <-requests:
					if got != want {
						t.Fatalf("request = %s, want %s", got, want)
					}
				default:
					t.Fatalf("missing request %s", want)
				}
			}
		})
	}
}
