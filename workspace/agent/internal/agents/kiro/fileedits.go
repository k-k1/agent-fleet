package kiro

// fileedits.go maps kiro's write tool onto the mirror's (File, Verb, Edits) part fields —
// the population behind "the files this session changed" (decisions/0049) and behind the diff
// a tool trace opens inline. A tool part with no File is skipped by the aggregation, so
// without this the strip stays empty for kiro no matter how many files it wrote.
//
// MEASURED 2026-09-22 against a live kiro session's own store
// (~/.kiro/sessions/cli/<sid>.jsonl, kiro-cli 2026.09), plus the tool catalogue carried in the
// kiro-cli-chat binary. Two facts that only a measurement gives:
//
//   - kiro ships TWO spellings of one tool. The binary's catalogue documents `fs_write` with
//     snake_case arguments (`file_text`, `old_str`, `new_str`, commands create / str_replace /
//     insert / append). What the v2 engine this kind pins (--agent-engine v2, program.go)
//     actually RECORDS is `write` with camelCase arguments (`content`, `oldStr`, `newStr`,
//     commands create / strReplace / insert). Reading only the documented spelling would have
//     produced an empty strip on every real session — the catalogue is not the wire.
//   - paths come through absolute ("/home/dev/probe-kiro/probe.txt"), which is why no turn Cwd
//     is needed here (sessionx anchors a relative path against it and drops one without it).
//
// The tool's own name is the whole gate: a read (`fsRead`/`read`) or a shell call must never
// produce a row, or the list names files nobody edited.

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// writeToolNames are the names the write tool is recorded under. `write` is what v2 emits;
// the other two are the catalogue's spelling and its camelCase alias, accepted so a session
// launched against the older engine still fills the strip.
//
// Deliberately NOT here: `delete_file`, `fs_append` and the bare `str_replace` that kiro's own
// TUI classifier also treats as writes. Those belong to surfaces this kind does not drive
// (kiro's ACP/MCP backends), their argument shapes have not been measured, and a guessed shape
// produces a row with no path rather than a useful one.
var writeToolNames = map[string]bool{"write": true, "fsWrite": true, "fs_write": true}

// toolEdits returns the file a write call targeted, the verb to label it with, and its
// before/after. ("", "", nil) for every other tool.
//
// Verb is stated rather than left to transcript.EditVerb for insert/append only: those carry
// no before-image, so the derived verb would read "add" for what is an edit to an existing
// file. create and strReplace derive correctly on their own.
func toolEdits(tu toolUseData) (file, verb string, edits []transcript.Edit) {
	if !writeToolNames[tu.Name] {
		return "", "", nil
	}
	in := tu.Input
	path := strings.TrimSpace(in.Path)
	if path == "" {
		path = strings.TrimSpace(in.FilePath)
	}
	if path == "" {
		return "", "", nil
	}
	content := in.Content
	if content == "" {
		content = in.FileText
	}
	oldStr, newStr := in.OldStr, in.NewStr
	if oldStr == "" && newStr == "" {
		oldStr, newStr = in.OldString, in.NewString
	}

	switch in.Command {
	case "create":
		// An overwrite of an existing file also arrives as `create` and carries no
		// before-image, so it reads as an add — the same shape claude's Write and
		// opencode's write already produce.
		return path, "", []transcript.Edit{{Old: "", New: transcript.CapEdit(content)}}
	case "strReplace", "str_replace":
		if oldStr == "" && newStr == "" {
			return "", "", nil
		}
		// replaceAll is not expanded into one hunk per occurrence (claude's Edit makes the
		// same simplification): one before/after, however many times it applied.
		return path, "", []transcript.Edit{{
			Old: transcript.CapEdit(oldStr),
			New: transcript.CapEdit(newStr),
		}}
	case "insert", "append":
		added := content
		if added == "" {
			added = newStr
		}
		return path, "edit", []transcript.Edit{{Old: "", New: transcript.CapEdit(added)}}
	}
	// A write call with a command this version does not have: still a file this session
	// wrote, so the row is kept — with no diff to open, which the strip renders as a plain
	// row rather than pretending to a +/- it does not have.
	return path, "edit", nil
}
