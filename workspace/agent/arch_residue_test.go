package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

func residueHome(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if body != "" {
		p := filepath.Join(home, ".local", "share", "agent-fleet")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "arch-residue"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// outbox reads the queued notifications the Control Plane would drain.
func outbox(t *testing.T, home string) []notice.Event {
	t.Helper()
	dir := filepath.Join(home, ".local", "state", "agent-fleet", "notification-outbox")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []notice.Event
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var ev notice.Event
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func TestParseArchResidue(t *testing.T) {
	repos, bins, pkgs, scan := parseArchResidue("repos: demo/node_modules other/target\nbin: mytool oldbin\npkgs: pip:greenlet==3.0.3 npm:cowsay@1.6.0\nscan: af-arch-repair amd64 arm64\n")
	if !reflect.DeepEqual(repos, []string{"demo/node_modules", "other/target"}) {
		t.Fatalf("repos = %v", repos)
	}
	if !reflect.DeepEqual(bins, []string{"mytool", "oldbin"}) {
		t.Fatalf("bins = %v", bins)
	}
	if !reflect.DeepEqual(pkgs, []string{"pip:greenlet==3.0.3", "npm:cowsay@1.6.0"}) {
		t.Fatalf("pkgs = %v", pkgs)
	}
	if scan != "af-arch-repair amd64 arm64" {
		t.Fatalf("scan = %q", scan)
	}
}

// A pass that never finished is a give-up with nothing listed; it must still notify.
func TestNotifyArchResidueNamesUnfinishedScan(t *testing.T) {
	home := residueHome(t, "scan: af-arch-repair amd64 arm64\n")
	runNotifyArchResidue([]string{""})
	got := outbox(t, home)
	if len(got) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(got))
	}
	if got[0].Payload["scan"] != "af-arch-repair amd64 arm64" {
		t.Fatalf("payload.scan = %v", got[0].Payload["scan"])
	}
}

// Reinstalls af-arch-repair stopped retrying are residue on their own: with nothing under
// ~/repos or ~/.local/bin, the notification still has to name them.
func TestNotifyArchResidueNamesAbandonedReinstalls(t *testing.T) {
	home := residueHome(t, "pkgs: uv:ruff==0.4.1\n")
	runNotifyArchResidue([]string{"amd64"})
	got := outbox(t, home)
	if len(got) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(got))
	}
	b, _ := json.Marshal(got[0].Payload["pkgs"])
	if string(b) != `["uv:ruff==0.4.1"]` {
		t.Fatalf("payload.pkgs = %s", b)
	}
}

// No residue is the common case (the repair puts most things back). It must be silent —
// a notification saying nothing is worse than none.
func TestNotifyArchResidueSilentWhenNothingLeft(t *testing.T) {
	home := residueHome(t, "")
	runNotifyArchResidue([]string{"amd64"})
	if got := outbox(t, home); len(got) != 0 {
		t.Fatalf("expected no notification, got %d", len(got))
	}
}

func TestNotifyArchResidueEmitsWorkspaceTarget(t *testing.T) {
	home := residueHome(t, "repos: demo/node_modules\nbin: mytool\n")
	runNotifyArchResidue([]string{"amd64"})
	got := outbox(t, home)
	if len(got) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(got))
	}
	ev := got[0]
	if ev.Kind != "arch-residue" {
		t.Errorf("kind = %q", ev.Kind)
	}
	// ⚠️ Without this the Control Plane defaults to "session" and produces a
	// notification pointing at a session named "" — one the Console cannot open.
	if ev.TargetType != "workspace" {
		t.Errorf("targetType = %q, want workspace", ev.TargetType)
	}
	if ev.SessionName != "" {
		t.Errorf("sessionName = %q, want empty", ev.SessionName)
	}
	if ev.Payload["from"] != "amd64" {
		t.Errorf("payload.from = %v", ev.Payload["from"])
	}
}

// The whole point of keying on content: the same residue must not pile up a new
// notification on every boot, but a CHANGED residue must produce exactly one more.
func TestNotifyArchResidueDedupesByContent(t *testing.T) {
	home := residueHome(t, "repos: demo/node_modules other/target\n")
	runNotifyArchResidue([]string{"amd64"})
	runNotifyArchResidue([]string{"amd64"})
	runNotifyArchResidue([]string{"amd64"})
	if got := outbox(t, home); len(got) != 1 {
		t.Fatalf("same residue queued %d notifications, want 1", len(got))
	}

	// The member fixes one repo. That is a different state and deserves to be said.
	p := filepath.Join(home, ".local", "share", "agent-fleet", "arch-residue")
	if err := os.WriteFile(p, []byte("repos: other/target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runNotifyArchResidue([]string{"amd64"})
	if got := outbox(t, home); len(got) != 2 {
		t.Fatalf("changed residue queued %d notifications total, want 2", len(got))
	}

	// They fix the rest; af-arch-repair removes the file. Nothing more, ever.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	runNotifyArchResidue([]string{"amd64"})
	if got := outbox(t, home); len(got) != 2 {
		t.Fatalf("cleared residue queued %d notifications total, want 2", len(got))
	}
}
