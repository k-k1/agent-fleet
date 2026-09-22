package muse

// fileedits.go maps muse's edit-family tool calls onto the mirror's (File, Verb, Edits) part
// fields — the population behind "the files this session changed" (decisions/0049) and behind
// the diff a tool trace opens inline. A tool part with no File is skipped by the aggregation,
// so without this the strip stays empty for muse however many files it wrote.
//
// MEASURED 2026-09-22 from a live muse session's own view journal
// (~/.local/share/muse/sessions/.msp-view-v1/<sid>/journal-*.bin):
//
//	{"kind":"toolCall","tool":"write_file","args":"{\"content\":\"hello\\n\",\"path\":\"/abs/probe.txt\"}"}
//	{"kind":"toolCall","tool":"edit_file","args":"{\"find\":\"hello\",\"path\":\"/abs/probe.txt\",\"replace\":\"world\"}"}
//
// Two things that only the measurement gives: `edit_file` names its sides find/replace (not
// old/new, not oldString/newString), and paths arrive absolute — which is why no turn Cwd is
// needed here (sessionx anchors a relative path against it and drops one without it).
//
// muse's own mutator vocabulary, read out of the binary, is
// {apply_patch, delete_file, edit_file, write_file}. The first two are deliberately left out:
// their argument shapes have not been measured, and a guessed shape yields a row with no path
// rather than a useful one. Each item also carries a server-authored `patchSummary`
// (added/removed/files) beside a `patchRef` to the stored patch document; neither is used
// here, because the +/- shown in the strip has to come from the SAME before/after the row
// opens as a diff, or the two disagree.

import (
	"encoding/json"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// toolEdits returns the file an edit-family tool targeted, the verb to label it with, and its
// before/after. ("", "", nil) for every other tool — the name is the whole gate, since a read
// or a shell call must never produce a row.
func toolEdits(tool, args string) (file, verb string, edits []transcript.Edit) {
	if args == "" {
		return "", "", nil
	}
	switch tool {
	case "write_file":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(args), &in) != nil || in.Path == "" {
			return "", "", nil
		}
		// An overwrite of an existing file arrives the same way and carries no before-image,
		// so it reads as an add — the shape claude's Write and opencode's write produce too.
		return in.Path, "", []transcript.Edit{{Old: "", New: transcript.CapEdit(in.Content)}}
	case "edit_file":
		var in struct {
			Path    string `json:"path"`
			Find    string `json:"find"`
			Replace string `json:"replace"`
		}
		if json.Unmarshal([]byte(args), &in) != nil || in.Path == "" {
			return "", "", nil
		}
		// Stated rather than derived: a replacement whose `find` came through empty would
		// otherwise be labelled an add, and an edit_file call is never a file's creation.
		return in.Path, "edit", []transcript.Edit{{
			Old: transcript.CapEdit(in.Find),
			New: transcript.CapEdit(in.Replace),
		}}
	}
	return "", "", nil
}
