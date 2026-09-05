// Package userinstr owns the *user layer* of agent instructions (docs/log/60 / ADR 0042):
// one body of markdown the workspace's owner writes once, which agent-fleet delivers
// into every supported CLI's user-scope instruction position.
//
// It is the middle of three layers. Above it is the fleet policy (workspace-notes.md baked
// into the image, owned by the operator); below it are the project instructions (a working
// copy's CLAUDE.md / AGENTS.md, committed). This layer holds how one person works: the
// language of reports, how finely to confirm, which tools they want used.
//
// The package owns only reading/writing the source of truth and assembling the body; where
// in which CLI it gets delivered belongs to each internal/agents/<kind> and to package
// main's distributor (agent_instructions.go). The principle in docs/log/60 §60.5-6 — prefer
// an AF-owned file plus a reference over writing into someone else's file — takes a
// different form per kind, so collecting the spellings here would make this layer grow with
// every kind added.
package userinstr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MaxBytes caps the body. The reason is cost, not truncation (docs/log/60 §60.9 — measured:
// codex's project_doc_max_bytes does not cover the global one). The fleet policy alone adds
// about 30KB of fixed cost per session; nothing unbounded is stacked on top of that.
const MaxBytes = 8 * 1024

// ErrTooLarge means the body is over MaxBytes. REST answers one reason with one code
// (docs/log/57 §4).
var ErrTooLarge = errors.New("too_large")

func notesPath() string { return filepath.Join(paths.AgentConfigDir(), "user-notes.md") }
func prefsPath() string { return filepath.Join(paths.AgentConfigDir(), "user-notes.json") }

// NotesPath / PrefsPath expose the durable locations (the Console never reads them
// directly — ~/.config/agent-fleet is denylisted in the file browser — but the REST
// snapshot names them so the user can see where their text lives).
func NotesPath() string { return notesPath() }
func PrefsPath() string { return prefsPath() }

// Prefs selects where the body applies. Pointers distinguish unset (i.e. on by default)
// from an explicit false, the same way the rtk toggles do.
type Prefs struct {
	Enabled *bool            `json:"enabled"`
	Targets map[string]*bool `json:"targets"`
}

// State is a snapshot of the source of truth.
type State struct {
	Text  string
	Prefs Prefs
}

// Load reads the body and prefs. Absent files are not an error — they mean
// "the user has not written anything yet", which is the normal state.
func Load() State {
	s := State{}
	if b, err := os.ReadFile(notesPath()); err == nil {
		s.Text = string(b)
	}
	if b, err := os.ReadFile(prefsPath()); err == nil {
		_ = json.Unmarshal(b, &s.Prefs)
	}
	return s
}

// SaveText writes the body (empty text removes the file, so "clear it" leaves no
// stale artifact behind).
func SaveText(text string) error {
	text = normalize(text)
	if len(text) > MaxBytes {
		return ErrTooLarge
	}
	if strings.TrimSpace(text) == "" {
		if err := os.Remove(notesPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeFile(notesPath(), []byte(text), 0o600)
}

// SavePrefs replaces the pref document (the caller merges).
func SavePrefs(p Prefs) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(prefsPath(), append(b, '\n'), 0o600)
}

// Enabled reports the master switch (unset ⇒ on: a user who wrote a body meant it).
func (s State) Enabled() bool { return boolOr(s.Prefs.Enabled, true) }

// TargetOn reports whether a kind should receive the body (unset ⇒ on).
func (s State) TargetOn(kind string) bool {
	if s.Prefs.Targets == nil {
		return true
	}
	return boolOr(s.Prefs.Targets[kind], true)
}

// Body is what actually gets delivered to `kind`: the rendered block, or "" when
// there is nothing to deliver (no text / master switch off / this kind unticked).
// Callers pass "" straight through to their applier, which removes the artifact —
// so turning a target off cleans up after itself.
func (s State) Body(kind string) string {
	if !s.Enabled() || !s.TargetOn(kind) {
		return ""
	}
	return Render(s.Text)
}

// header is the fixed preamble AF always puts in front. Kinds that merge everything into
// one flat file lose the signal of the layering, so the precedence has to be stated in prose
// (docs/log/60 §60.5-4). The reader is a model, so it is written in English like the fleet
// policy — a separate axis from the display language (ADR 0033).
const header = "# User instructions (from the person who owns this workspace)\n\n" +
	"These are the personal working preferences of this workspace's user. They apply to\n" +
	"every session here. Where they conflict with the workspace guide above, the\n" +
	"workspace guide wins; where they conflict with a repository's own instructions,\n" +
	"ask rather than guess.\n"

// Render wraps the user's text with the header. Empty text renders empty.
func Render(text string) string {
	text = strings.Trim(normalize(text), "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return header + "\n" + text + "\n"
}

// FleetNotes returns the baked workspace guide, or "" when the image has none
// (a packaging variant, or a unit test). "" means "do not write a fleet block" —
// never "write an empty one", which would silently drop the guide from every CLI.
func FleetNotes() string {
	b, err := os.ReadFile(paths.FleetNotesPath())
	if err != nil {
		return ""
	}
	return string(b)
}

func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func writeFile(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
