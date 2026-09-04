package cursor

// Normalization of the Claude Code-compatible JSONL into transcript.Turn (the read path's
// source of truth, docs/log/40). Measured line shapes (v2026.07.20):
//
//	{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>…</timestamp>\n<user_query>\n…\n</user_query>"}]}}
//	{"role":"assistant","message":{"content":[{"type":"text","text":"…"},{"type":"tool_use","name":"Shell","input":{"command":"…","description":"…"}}]}}
//	{"type":"turn_ended","status":"success"}
//
// The claude parser cannot be reused (no uuid/timestamp, own envelope), but a dedicated one
// is easy. tool_result never appears in this JSONL (tool output lives only in store.db —
// docs/log/40), so the mirror gets the tool name and arguments but no output. One assistant
// turn can span several lines (tool_use lines plus a final text line), so flush on a user
// line or on turn_ended. Turn.Idx increases monotonically from the line number, which the
// Console's pendingEcho/MirrorView requires (the agy 7354916 lesson).

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	// managed (ACP): there is no local transcript, so return the one the driver built in
	// memory (driver.go managedTranscript). A stopped session has no handle and hence an
	// empty mirror — resume rebuilds it from the session/load replay.
	if m.DriverKind() == session.DriverManaged {
		return managedTranscript(m), true
	}
	chatID := ChatID(m)
	if chatID == "" {
		return agents.TranscriptData{}, true // no conversation yet (before launch) — empty mirror
	}
	path := transcriptPath(m.Dir, chatID)
	td := agents.TranscriptData{Path: path, Turns: parseTranscript(path)}
	// TUI/-p write no model into the transcript (docs/log/40 §model display), so stamp the
	// launch model (fixed per session) onto every assistant turn for the mirror's model
	// badge. Nothing selected = Auto.
	stampModel(td.Turns, displayModel(m.Model))
	return td, true
}

// displayModel normalizes a cursor model id for the mirror's per-response badge: it strips
// ACP's bracket parameters (`claude-opus-4-8[thinking=true,context=300k,effort=high]`) down
// to the bare id and folds the Auto family (empty string / `auto` / `default[]`) into
// "Auto". The picker's dash form (`composer-2.5` and friends) is left alone. cursor pins the
// model per session (per-session child, DynamicModel:false), so every assistant turn carries
// the same value (docs/log/40 §model display). Note that this is the CONFIGURED model, not
// the concrete model Auto resolved to on each turn — no official path exposes that
// (docs/log/40).
func displayModel(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.IndexByte(id, '['); i >= 0 {
		id = strings.TrimSpace(id[:i]) // strip ACP's [params]
	}
	switch strings.ToLower(id) {
	case "", "auto", "default":
		return "Auto"
	}
	return id
}

// stampModel labels every assistant turn with the session's (fixed) model so the
// mirror renders a per-response model badge (MirrorView's turn.model path). A turn that
// already carries a value is left alone, in case a per-turn source appears later.
func stampModel(turns []transcript.Turn, model string) {
	if model == "" {
		return
	}
	for i := range turns {
		if turns[i].Role == "assistant" && turns[i].Model == "" {
			turns[i].Model = model
		}
	}
}

// line is one JSONL row: either a role-bearing message or a control marker
// (turn_ended). content is decoded lazily via contentBlock.
type line struct {
	Role    string `json:"role"` // "user" | "assistant" (message rows)
	Type    string `json:"type"` // "turn_ended" (control rows); "" for message rows
	Message struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
}

// contentBlock is one Anthropic-style content block. input is tool_use args of an
// arbitrary shape, kept raw: the label wants a couple of common fields (toolLabel) and
// the changed-files list wants the edit payload (toolEdits), and the two disagree about
// which fields matter.
type contentBlock struct {
	Type  string          `json:"type"` // "text" | "thinking" | "tool_use"
	Text  string          `json:"text"`
	Think string          `json:"thinking"`
	Name  string          `json:"name"` // tool_use: tool name
	Input json.RawMessage `json:"input"`
}

// toolLabel picks the short human-facing label for a tool trace, in the order that has
// the most information first (unchanged behaviour — it just reads the raw input now).
func toolLabel(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Path        string `json:"path"`
		FilePath    string `json:"file_path"`
		TargetFile  string `json:"target_file"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, s := range []string{in.Description, in.Command, in.Path, in.FilePath, in.TargetFile} {
		if s != "" {
			return s
		}
	}
	return ""
}

// outClip bounds any carried text (parity with the other parsers — a preview).
const outClip = 4000

func clip(s string) string {
	if len(s) <= outClip {
		return s
	}
	return s[:outClip] + "\n…（省略）"
}

// userQueryRe unwraps cursor's `<user_query>…</user_query>` envelope so the mirror
// shows the user's actual prompt, not the injected timestamp/query wrapper.
var userQueryRe = regexp.MustCompile(`(?s)<user_query>\s*(.*?)\s*</user_query>`)
var timestampRe = regexp.MustCompile(`(?s)<timestamp>.*?</timestamp>\s*`)

// cleanUserText extracts the human prompt from a user text block.
func cleanUserText(s string) string {
	if m := userQueryRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(timestampRe.ReplaceAllString(s, ""))
}

// parseTranscript renders the whole JSONL into turns.
func parseTranscript(path string) []transcript.Turn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var turns []transcript.Turn
	var cur *transcript.Turn // open assistant turn

	flush := func() {
		if cur == nil {
			return
		}
		text := ""
		for _, p := range cur.Parts {
			if p.Kind == "text" {
				if text != "" {
					text += "\n\n"
				}
				text += p.Text
			}
		}
		cur.Text = text
		if len(cur.Parts) > 0 {
			turns = append(turns, *cur)
		}
		cur = nil
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	idx := 0
	for sc.Scan() {
		idx++
		var ln line
		if json.Unmarshal(sc.Bytes(), &ln) != nil {
			continue
		}
		if ln.Type == "turn_ended" {
			flush()
			continue
		}
		switch ln.Role {
		case "user":
			flush()
			txt := ""
			for _, b := range ln.Message.Content {
				if b.Type == "text" {
					txt += b.Text
				}
			}
			txt = cleanUserText(txt)
			if txt == "" {
				continue
			}
			turns = append(turns, transcript.Turn{
				Role: "user", Text: txt, Idx: idx,
				Parts: []transcript.Part{{Kind: "text", Text: txt}},
			})
		case "assistant":
			if cur == nil {
				cur = &transcript.Turn{Role: "assistant", Idx: idx}
			}
			for _, b := range ln.Message.Content {
				switch b.Type {
				case "text":
					if b.Text != "" {
						cur.Parts = append(cur.Parts, transcript.Part{Kind: "text", Text: b.Text})
					}
				case "thinking":
					if b.Think != "" {
						cur.Parts = append(cur.Parts, transcript.Part{Kind: "thinking", Text: clip(b.Think)})
					}
				case "tool_use":
					part := transcript.Part{Kind: "tool", Tool: b.Name, Info: clip(toolLabel(b.Input))}
					if f, verb, es := toolEdits(b.Name, b.Input); f != "" {
						part.File, part.Verb, part.Edits = f, verb, es
					}
					cur.Parts = append(cur.Parts, part)
				}
			}
		}
	}
	flush()
	return turns
}

// ── Extracting edits (docs/log/68) ─────────────────────────────────────────────────
//
// There are two paths, with different clues:
//   jsonl (TUI / -p)  … only the tool name is available → toolEdits decides by a name allowlist
//   ACP (managed)     … the protocol itself classifies with `kind` → the name is not consulted
// Only the input shape is common to both, so pulling out before/after (editsFromInput) is
// kept separate.

// editInput is the union of the field spellings an edit-family call has been seen to use.
// Measured (transcript jsonl, 2026-08): `Write` uses {"path","contents"}. The others share
// the same vocabulary but no real call has been observed, so both the claude spelling
// (old_string/new_string) and the copilot spelling (old_str/new_str) are accepted.
type editInput struct {
	Path       string `json:"path"`
	FilePath   string `json:"file_path"`
	TargetFile string `json:"target_file"`
	Contents   string `json:"contents"`
	Content    string `json:"content"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	OldStr     string `json:"old_str"`
	NewStr     string `json:"new_str"`
	Edits      []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		OldStr    string `json:"old_str"`
		NewStr    string `json:"new_str"`
	} `json:"edits"`
}

func (in editInput) file() string {
	for _, p := range []string{in.Path, in.FilePath, in.TargetFile} {
		if p != "" {
			return p
		}
	}
	return ""
}

func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// editsFromInput reads the before/after payload from a tool call's raw input WITHOUT
// consulting the tool name — for the ACP path, where the protocol has already said this
// call is an edit and the name is only a display title.
func editsFromInput(raw json.RawMessage) (string, []transcript.Edit) {
	if len(raw) == 0 {
		return "", nil
	}
	var in editInput
	if json.Unmarshal(raw, &in) != nil {
		return "", nil
	}
	file := in.file()
	switch {
	case len(in.Edits) > 0:
		var out []transcript.Edit
		for _, e := range in.Edits {
			out = append(out, transcript.Edit{
				Old: transcript.CapEdit(pick(e.OldString, e.OldStr)),
				New: transcript.CapEdit(pick(e.NewString, e.NewStr)),
			})
		}
		return file, out
	case pick(in.OldString, in.OldStr) != "" || pick(in.NewString, in.NewStr) != "":
		return file, []transcript.Edit{{
			Old: transcript.CapEdit(pick(in.OldString, in.OldStr)),
			New: transcript.CapEdit(pick(in.NewString, in.NewStr)),
		}}
	case pick(in.Contents, in.Content) != "":
		return file, []transcript.Edit{{Old: "", New: transcript.CapEdit(pick(in.Contents, in.Content))}}
	}
	return file, nil
}

// toolEdits is the jsonl path's entry point: there is no protocol-level classification
// there, only the tool's name.
//
// The names are an allowlist and anything unknown is ignored. The opposite rule (treat
// everything but reads as an edit) would, on a version that renamed a tool, list a file that
// was merely read as a changed file — the list would silently lie. Missing an edit only
// costs a row that does not appear.
func toolEdits(name string, input json.RawMessage) (file, verb string, edits []transcript.Edit) {
	switch name {
	case "Write", "Create", "Edit", "MultiEdit":
		f, es := editsFromInput(input)
		if f == "" || len(es) == 0 {
			return "", "", nil
		}
		return f, "", es
	case "Delete":
		// A deleted file has no before/after, so this is the one case that states the verb
		// explicitly: inferring "no Edits means a delete" would break every kind that
		// carries no diff body.
		f, _ := editsFromInput(input)
		if f == "" {
			return "", "", nil
		}
		return f, "delete", nil
	}
	return "", "", nil
}
