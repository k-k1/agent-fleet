package writeevidence

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// FileEdits reconciles a native turn's shell evidence with all its rollout rows.
// Explicit edits keep their per-call counts and stats. Shell evidence adds one edit per
// otherwise unrepresented path. A store failure still returns the explicit edits.
// Pass the complete rollout of scope.ThreadID, since a patch can be in a later row.
func FileEdits(scope Scope, rows []transcript.Turn) ([]transcript.FileEdit, error) {
	var edits []transcript.FileEdit
	var anchor *transcript.Turn
	for i := range rows {
		row := &rows[i]
		if row.AnchorID != scope.TurnID {
			continue
		}
		edits = append(edits, transcript.FileEditsInTurn(*row)...)
		if row.Role == "assistant" {
			anchor = row
		}
	}
	if anchor == nil || scope.TurnID == "" {
		return edits, nil
	}
	evidence, err := load(scope)
	if err != nil || len(evidence.Paths) == 0 {
		return edits, err
	}

	explicit := make(map[string]bool, len(edits))
	for _, edit := range edits {
		path := edit.Path
		if !filepath.IsAbs(path) {
			if !filepath.IsAbs(edit.Cwd) {
				continue
			}
			path = filepath.Join(edit.Cwd, path)
		}
		explicit[editPath(path)] = true
	}
	for _, path := range evidence.Paths {
		abs := filepath.Join(evidence.Root, path)
		if !explicit[abs] {
			edits = append(edits, transcript.FileEdit{Path: abs, Verb: "edit", Idx: anchor.Idx, TS: anchor.TS})
		}
	}
	return edits, nil
}

// Resolve existing ancestors even after a patch deleted a file or directory.
// Keep the final component so a patch to a symlink names the link itself.
func editPath(path string) string {
	dir, err := paths.ResolveFileWritePath(filepath.Dir(path))
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Join(dir, filepath.Base(path))
}
