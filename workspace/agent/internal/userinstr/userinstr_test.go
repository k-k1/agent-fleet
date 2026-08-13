package userinstr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestSaveLoadRoundTrip(t *testing.T) {
	isolate(t)
	if err := SaveText("日本語で報告して\n"); err != nil {
		t.Fatal(err)
	}
	if got := Load().Text; got != "日本語で報告して\n" {
		t.Fatalf("got %q", got)
	}
}

func TestSaveTextRejectsOverLimitAndKeepsPrevious(t *testing.T) {
	isolate(t)
	if err := SaveText("keep me\n"); err != nil {
		t.Fatal(err)
	}
	if err := SaveText(strings.Repeat("x", MaxBytes+1)); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if got := Load().Text; got != "keep me\n" {
		t.Fatalf("over-limit save clobbered the body: %q", got)
	}
}

// 空にしたら残骸を残さない（「消したのにまだ効いている」を作らない）。
func TestSaveEmptyRemovesFile(t *testing.T) {
	isolate(t)
	if err := SaveText("something\n"); err != nil {
		t.Fatal(err)
	}
	if err := SaveText("  \n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(NotesPath()); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
	if Load().Text != "" {
		t.Fatal("body survived the clear")
	}
}

func TestBodyRespectsSwitches(t *testing.T) {
	isolate(t)
	off := false
	s := State{Text: "hello\n"}
	if s.Body("codex") == "" {
		t.Fatal("unset prefs must default to on")
	}
	if !strings.Contains(s.Body("codex"), "hello") {
		t.Fatal("body missing the user's text")
	}
	if !strings.Contains(s.Body("codex"), "workspace guide wins") {
		t.Fatal("precedence sentence missing — a flat file has no other hierarchy signal")
	}

	s.Prefs.Targets = map[string]*bool{"codex": &off}
	if s.Body("codex") != "" {
		t.Fatal("unticked target must deliver nothing (so the applier removes it)")
	}
	if s.Body("claude") == "" {
		t.Fatal("other kinds must stay on")
	}

	s.Prefs.Enabled = &off
	if s.Body("claude") != "" {
		t.Fatal("master switch off must deliver nothing")
	}
}

func TestRenderEmptyStaysEmpty(t *testing.T) {
	if Render("   \n\n") != "" {
		t.Fatal("blank text must render empty, not a lone header")
	}
}

func TestFleetNotesMissingImageIsEmptyNotError(t *testing.T) {
	isolate(t)
	t.Setenv("AF_WORKSPACE_NOTES", filepath.Join(t.TempDir(), "absent.md"))
	if FleetNotes() != "" {
		t.Fatal("absent guide must read as empty")
	}
}

func TestPrefsRoundTrip(t *testing.T) {
	isolate(t)
	off := false
	if err := SavePrefs(Prefs{Targets: map[string]*bool{"copilot": &off}}); err != nil {
		t.Fatal(err)
	}
	s := Load()
	if s.TargetOn("copilot") {
		t.Fatal("explicit false lost")
	}
	if !s.TargetOn("codex") {
		t.Fatal("absent key must mean on")
	}
	if !s.Enabled() {
		t.Fatal("absent master switch must mean on")
	}
}
