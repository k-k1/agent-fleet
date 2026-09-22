package agy

// fileedits.go recovers the file an agy tool step wrote from the step's own PROSE — the
// population behind "the files this session changed" (decisions/0049) and behind the diff a
// tool trace opens inline.
//
// Why prose, when every other kind reads structured arguments: agy's transcript_full.jsonl
// row is {source, type, content} and nothing else (transcript.go's stepLine). There are no
// tool arguments in it at all. The step TYPE was the obvious place to look — docs/log/32
// recorded CODE_ACTION for create/edit in 2026-07 — but MEASURED 2026-09-22 against a live
// agy session, every one of these steps came through as type GENERIC. So the type is not a
// gate either, and what is left is the sentence agy writes for the model:
//
//	create: "Created file file:///abs/path.txt with requested content."
//	edit:   "The following changes were made by the replace_file_content tool to: /abs/path.txt. …"
//	        followed by a [diff_block_start] … [diff_block_end] unified diff
//	read:   "File Path: `file:///abs/path.txt`\nTotal Lines: 2\n…"   ← must NOT count
//
// This is an upstream TEXT contract, the same class as agy's own state detection: it breaks
// when agy rewrites the sentence, and it breaks SILENTLY (an empty strip and a session that
// edited nothing look identical). Two things keep that honest — both patterns are anchored at
// the start of the step content, so a sentence quoted inside a command's output cannot trip
// them, and the read shape above is pinned as a negative control in the tests.

import (
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

var (
	// "Created file file:///abs/path with requested content."
	agyCreatedRe = regexp.MustCompile(`^Created file (\S+) with requested content\.`)
	// "The following changes were made by the <tool> tool to: /abs/path. …"
	agyChangedRe = regexp.MustCompile(`^The following changes were made by the \S+ tool to: (.+?)\.(?:\s|$)`)
	// The diff agy prints under the edit sentence.
	agyDiffBlockRe = regexp.MustCompile(`(?s)\[diff_block_start\]\n(.*?)\[diff_block_end\]`)
)

// stepEdit returns the file this step wrote, the verb to label it with, and the before/after
// when the step printed a diff. ("", "", nil) for every other step — including a file READ,
// which names a path just as prominently and must never produce a row.
//
// `content` is the step content with the Created At / Completed At bookkeeping lines already
// stripped (transcript.go does that before calling here), because the sentences this matches
// sit directly under them.
func stepEdit(content string) (file, verb string, edits []transcript.Edit) {
	if m := agyCreatedRe.FindStringSubmatch(content); m != nil {
		// agy never echoes the content it wrote, so there is no diff to open: the row
		// carries the fact and no +/-, rather than a fabricated one.
		return agyPath(m[1]), "add", nil
	}
	if m := agyChangedRe.FindStringSubmatch(content); m != nil {
		return agyPath(m[1]), "edit", agyDiffEdits(content)
	}
	return "", "", nil
}

// agyPath normalizes the coordinate agy prints. It uses a file:// URL in some sentences and a
// plain absolute path in others, and the mirror can only open the latter.
func agyPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, "`\"'")
	return strings.TrimPrefix(p, "file://")
}

// agyDiffEdits turns the step's unified diff into one before/after per hunk. Context lines
// belong to both sides, which is what makes the strip's +/- (transcript.EditStat) agree with
// the diff the row opens.
//
// A step with no diff block yields no edits rather than an empty one: "this file changed, no
// diff available" is honest, "this file changed by nothing" is not.
func agyDiffEdits(content string) []transcript.Edit {
	m := agyDiffBlockRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	var oldB, newB strings.Builder
	for _, ln := range strings.Split(strings.TrimRight(m[1], "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, "@@"):
			continue // hunk header
		case strings.HasPrefix(ln, "-"):
			oldB.WriteString(ln[1:] + "\n")
		case strings.HasPrefix(ln, "+"):
			newB.WriteString(ln[1:] + "\n")
		default: // context (a leading space, or the trailing empty line)
			ln = strings.TrimPrefix(ln, " ")
			oldB.WriteString(ln + "\n")
			newB.WriteString(ln + "\n")
		}
	}
	old, updated := oldB.String(), newB.String()
	if old == updated {
		return nil
	}
	return []transcript.Edit{{Old: transcript.CapEdit(old), New: transcript.CapEdit(updated)}}
}
