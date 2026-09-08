// Package fleetskills registers the workspace guide's topic files as skills.
//
// The always-loaded policy (workspace-notes.md) stays short by moving procedures into
// `/usr/local/share/agent-fleet/notes/<topic>.md` and pointing at them from an index. A
// pointer in prose is one way an agent finds the file; a skill is the other, and the better
// one where the CLI has a user skills root: at start only the frontmatter description is in
// context, the body is read when the skill fires, and the CLI's own trigger logic carries the
// agent to the file instead of relying on it to remember the index. Verified roots (each
// lists `<root>/<name>/SKILL.md`):
//   - claude   $CLAUDE_CONFIG_DIR/skills — AF's own mount, so claude is reachable here even
//     though its policy file under /etc is not (measured 2026-09-08, claude 2.1.263: a probe
//     placed there appears in the stream-json init `skills` list).
//   - codex    $CODEX_HOME/skills (0.153; its own bundle lives under skills/.system).
//   - opencode ~/.config/opencode/skills (1.18; it also scans ~/.claude/skills and
//     ~/.agents/skills, which is why nothing is written to those — it would list twice).
//
// agy reads only the project-side .agents/skills, and copilot / kiro show no user skills root
// in their binaries, so those kinds keep the index route only.
//
// The root is shared with the user's own skills, so ownership is explicit: every directory
// written here is `af-<topic>` and its SKILL.md carries the marker line; only directories
// that carry the marker are ever removed. An empty topic set (an image without notes, or a
// unit test) is a no-op rather than a removal, for the same reason userinstr.FleetNotes
// treats an unreadable guide as "leave the previous copy in place".
package fleetskills

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Marker is the ownership line written right after the frontmatter. It is what makes a
// directory AF's to overwrite or delete; a user's own `af-foo` skill without it is left alone.
const Marker = "<!-- agent-fleet:fleet-skill — written by the workspace agent at every start; edits here are overwritten, the source is the image's notes/ directory -->"

// Prefix is the directory-name prefix of every skill this package writes.
const Prefix = "af-"

var stemRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Load reads the topic files: `<dir>/<stem>.md` -> content, for every stem that is a valid
// skill name. A missing directory yields an empty map.
func Load(dir string) map[string]string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		if !stemRe.MatchString(stem) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out[stem] = string(b)
	}
	return out
}

// Apply makes `<root>/af-<stem>/SKILL.md` exist for every topic and removes AF-owned
// directories whose topic is gone. Idempotent: a file whose content already matches is not
// rewritten. Empty topics is a no-op.
func Apply(root string, topics map[string]string) error {
	if len(topics) == 0 {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	// Prune first, so a rename (old stem gone, new stem added) never leaves two copies.
	ents, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), Prefix) {
			continue
		}
		if _, live := topics[strings.TrimPrefix(e.Name(), Prefix)]; live {
			continue
		}
		if !owned(filepath.Join(root, e.Name(), "SKILL.md")) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			return err
		}
	}
	for stem, body := range topics {
		dir := filepath.Join(root, Prefix+stem)
		path := filepath.Join(dir, "SKILL.md")
		want := withMarker(body)
		if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
			continue
		} else if err == nil && !owned(path) {
			// Someone else's `af-<stem>`: not ours to overwrite. Leave it, and leave it out.
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		tmp := path + ".af-tmp"
		if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}

// withMarker inserts the ownership line after the frontmatter (or at the top when there is
// none), so the CLIs' frontmatter parsers see exactly the source file's header.
func withMarker(body string) string {
	if strings.HasPrefix(body, "---\n") {
		if i := strings.Index(body[4:], "\n---\n"); i >= 0 {
			end := 4 + i + len("\n---\n")
			return body[:end] + Marker + "\n" + body[end:]
		}
	}
	return Marker + "\n\n" + body
}

func owned(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), Marker)
}
