package lcpp

// fileedits.go maps this kind's own edit-family tool calls onto the mirror's (File, Edits)
// part fields — the population behind "the files this session changed" (decisions/0049,
// docs/log/68) and behind the diff a tool trace opens inline. Without it a part carries the
// tool name and its arguments as a one-line trace, `transcript.FileEditsInTurn` skips it
// (File == ""), and /messages sends no `files` at all, so the strip is not drawn: the strip
// is data-driven, never gated on kind.
//
// Every kind parses its own tool arguments (claude's toolEdits, opencode's, codex's patch
// header): the names and the argument shapes belong to the CLI, not to us. Here the CLI is
// our own harness, so the shapes are tools_fs.go's writeArgs / editArgs — fileedits_test.go
// runs the REAL tool from harness.BuiltinTools() over the same JSON and checks the parser
// named the file that actually changed, so a renamed argument cannot silently empty the strip.
//
// Two deliberate omissions:
//   - bash is not read. `sed -i` through the shell edits a file too, but nothing in the call
//     says which one, and guessing would list files nobody touched.
//   - an MCP tool that happens to be named write/edit shadows the builtin in the registry
//     (harness.NewRegistry: last write wins). It is read here as the builtin would be, which
//     at worst records the path that call itself named.

import (
	"encoding/json"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// toolEdits returns the file an edit-family builtin targeted plus its before/after, or
// ("", nil) for every other tool.
//
// The path comes back exactly as the model wrote it, which is normally relative: every
// builtin resolves its path argument against the session's working directory
// (harness/cwd.go confines them to it). Anchoring it is the caller's job — the aggregation
// DROPS a relative path when the turn carries no Cwd (sessionx absEditPath), so Transcript
// must fill Turn.Cwd or this work is invisible.
func toolEdits(name, args string) (string, []transcript.Edit) {
	if args == "" {
		return "", nil
	}
	switch name {
	case "write":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(args), &in) != nil || in.Path == "" {
			return "", nil
		}
		// Old stays empty even when the write REPLACED an existing file: the call carries no
		// before-image and the tool result does not report one either, so the row reads as an
		// add. claude's Write and opencode's write already produce exactly this shape, and
		// the alternative — calling every overwrite a delete-then-add — is the mistake
		// docs/log/68 §68.2.1 warns about from the other direction.
		return in.Path, []transcript.Edit{{Old: "", New: transcript.CapEdit(in.Content)}}
	case "edit":
		var in struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if json.Unmarshal([]byte(args), &in) != nil || in.Path == "" {
			return "", nil
		}
		// replace_all is not expanded into one hunk per occurrence: the same single
		// before/after is reported however many times it applied, so a replace_all over N
		// matches counts as one. claude's Edit makes the same simplification.
		return in.Path, []transcript.Edit{{
			Old: transcript.CapEdit(in.OldString),
			New: transcript.CapEdit(in.NewString),
		}}
	}
	return "", nil
}
