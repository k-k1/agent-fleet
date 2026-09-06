package claude

import "testing"

const genImageResult = `{"files":[{"path":"/home/u/.cache/agent-fleet/generated/sid/image-1.png","name":"image-1.png","mime":"image/png","bytes":563308,"width":1536,"height":1024}],"provider":"codex","model":"gpt-5.4-mini","warnings":["size=1024x1024 requested, 1536x1024 produced"]}`

func genImageLines() [][]byte {
	return [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"作ります"},{"type":"tool_use","id":"g1","name":"mcp__af_40ed9852__generate_image","input":{"prompt":"a red circle"}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"g1","content":` + jsonString(genImageResult) + `}]}}`),
	}
}

func jsonString(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		default:
			out = append(out, string(r)...)
		}
	}
	return string(append(out, '"'))
}

// The picture lands right after the tool trace that made it, as a userfile part the mirror's
// existing FileCard renders — no frontend change, the same route codex's own image_gen takes.
func TestGeneratedImageBecomesAUserFilePart(t *testing.T) {
	lines := genImageLines()
	turns := CollectTurns(lines, 0, len(lines))
	if len(turns) != 1 {
		t.Fatalf("turns = %+v, want one", turns)
	}
	parts := turns[0].Parts
	if len(parts) != 3 || parts[0].Kind != "text" || parts[1].Kind != "tool" || parts[2].Kind != "userfile" {
		t.Fatalf("parts = %+v, want text, tool, userfile in that order", parts)
	}
	if len(parts[2].Files) != 1 || parts[2].Files[0] != "/home/u/.cache/agent-fleet/generated/sid/image-1.png" {
		t.Fatalf("userfile = %+v", parts[2])
	}
}

// The window that holds the call but not yet its result is the normal live case: claude
// writes the tool_use immediately and the tool_result some 30 s later. Nothing is invented,
// and the handler is told which line to hold the cursor at so the turn comes back.
func TestGeneratedImageHoldsTheCursorUntilTheResultLands(t *testing.T) {
	lines := genImageLines()

	partial := CollectTurns(lines, 0, 1)
	if n := len(partial[0].Parts); n != 2 {
		t.Fatalf("parts = %+v, want no picture before the result exists", partial[0].Parts)
	}
	if got := PendingGeneratedImageLine(partial); got != 0 {
		t.Fatalf("hold = %d, want line 0 so the turn is re-sent", got)
	}

	// Once the result is in the window the hold is released — otherwise the cursor would
	// stick on that line forever and every poll would re-send the same turn.
	full := CollectTurns(lines, 0, len(lines))
	if got := PendingGeneratedImageLine(full); got != -1 {
		t.Fatalf("hold = %d, want none once the picture is attached", got)
	}
}

// A failed generation must not hold the cursor for good: the result is prose, no file exists,
// and there is nothing more to wait for.
func TestGeneratedImageFailureDoesNotHoldForever(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"g1","name":"mcp__af_40ed9852__generate_image","input":{"prompt":"x"}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"g1","is_error":true,"content":"画像を生成できませんでした: codex is not logged in"}]}}`),
	}
	turns := CollectTurns(lines, 0, len(lines))
	for _, p := range turns[0].Parts {
		if p.Kind == "userfile" {
			t.Fatalf("a failed generation produced a picture card: %+v", p)
		}
	}
	if got := PendingGeneratedImageLine(turns); got != -1 {
		t.Fatalf("hold = %d, want none — a failure has nothing left to wait for", got)
	}
}

// Another server's tool of the same name is not af's picture.
func TestGeneratedImageIgnoresAnotherServersTool(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"g1","name":"mcp__pictures__generate_image","input":{"prompt":"x"}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"g1","content":` + jsonString(genImageResult) + `}]}}`),
	}
	turns := CollectTurns(lines, 0, len(lines))
	for _, p := range turns[0].Parts {
		if p.Kind == "userfile" {
			t.Fatalf("another server's tool produced a picture card: %+v", p)
		}
	}
	if got := PendingGeneratedImageLine(turns); got != -1 {
		t.Fatalf("hold = %d, want none", got)
	}
}

// Two calls in one turn each get their own card, next to their own trace.
func TestGeneratedImageSplicesEachCardAfterItsOwnCall(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"assistant","message":{"content":[` +
			`{"type":"tool_use","id":"g1","name":"mcp__af_40ed9852__generate_image","input":{"prompt":"one"}},` +
			`{"type":"tool_use","id":"g2","name":"mcp__af_40ed9852__generate_image","input":{"prompt":"two"}}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"g1","content":` + jsonString(`{"files":[{"path":"/a/1.png"}]}`) + `}]}}`),
		[]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"g2","content":` + jsonString(`{"files":[{"path":"/a/2.png"}]}`) + `}]}}`),
	}
	parts := CollectTurns(lines, 0, len(lines))[0].Parts
	var got []string
	for _, p := range parts {
		if p.Kind == "userfile" {
			got = append(got, p.Files...)
		}
	}
	if len(parts) != 4 || parts[1].Kind != "userfile" || parts[3].Kind != "userfile" {
		t.Fatalf("parts = %+v, want each card after its own call", parts)
	}
	if len(got) != 2 || got[0] != "/a/1.png" || got[1] != "/a/2.png" {
		t.Fatalf("files = %v, want them in call order", got)
	}
}
