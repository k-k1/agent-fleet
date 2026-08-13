// Package userinstr owns the *user layer* of agent instructions (docs/60 / ADR 0042):
// one body of markdown the workspace's owner writes once, which agent-fleet delivers
// into every supported CLI's user-scope instruction position.
//
// 3 層のうち真ん中に当たる。上は**フリート方針**（イメージに焼き込まれた
// workspace-notes.md・オペレーター所有）、下は**プロジェクト指示**（作業コピーの
// CLAUDE.md / AGENTS.md・コミットされる）。ここはその中間で、
// 「その人の働き方」— 報告の言語・確認の粒度・使ってほしい道具 — を置く。
//
// このパッケージは**正本の読み書きと本文の組み立てだけ**を持ち、どの CLI のどこへ
// 配るかは各 internal/agents/<kind> と package main の配布器（agent_instructions.go）
// が持つ。理由は docs/60 §60.5-6 の原則「他人のファイルに書くより AF 専用ファイル＋
// 参照を優先する」が kind ごとに手段を変えるため — 綴り方をここへ集めると、
// kind を足すたびにこの層が太る。
package userinstr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// MaxBytes は本文の上限。根拠は**費用**であって truncation ではない（docs/60 §60.9 —
// codex の project_doc_max_bytes は global を含まないと実測済み）。フリート方針だけで
// 毎セッション約 30KB が固定費として乗っており、その上に無制限を積ませない。
const MaxBytes = 8 * 1024

// ErrTooLarge は上限超過。REST は 1 理由 = 1 コードで返す（docs/57 §4）。
var ErrTooLarge = errors.New("too_large")

func notesPath() string { return filepath.Join(paths.AgentConfigDir(), "user-notes.md") }
func prefsPath() string { return filepath.Join(paths.AgentConfigDir(), "user-notes.json") }

// NotesPath / PrefsPath expose the durable locations (the Console never reads them
// directly — ~/.config/agent-fleet is denylisted in the file browser — but the REST
// snapshot names them so the user can see where their text lives).
func NotesPath() string { return notesPath() }
func PrefsPath() string { return prefsPath() }

// Prefs は適用先の選択。ポインタで「未設定（＝既定 ON）」と明示 false を区別する
// （rtk トグルと同じ作法）。
type Prefs struct {
	Enabled *bool            `json:"enabled"`
	Targets map[string]*bool `json:"targets"`
}

// State は正本のスナップショット。
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

// header は AF が必ず前置する固定文。フラットな 1 ファイルへ合成する kind では
// 階層の信号が消えるので、優先順位は**散文で**言うしかない（docs/60 §60.5-4）。
// 読み手はモデルなので、フリート方針と同じ英語で書く（表示言語とは別軸 — ADR 0033）。
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
