package copilot

// events.jsonl → transcript.Turn 正規化（read 正本、docs/log/36）。イベント形は
// v1.0.73 実測（docs/log/36 実測記録）: user.message / assistant.turn_start /
// assistant.message / tool.execution_start|complete / assistant.turn_end が
// 描画対象で、deltas・reasoning などの ephemeral イベントはファイルに書かれない。
// Turn.Idx は行番号由来の単調増加（Console の pendingEcho/MirrorView は idx
// 単調前提 — agy 7354916 の教訓）。

import (
	"bufio"
	"encoding/json"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	sid := SessionID(m)
	if sid == "" {
		return agents.TranscriptData{}, true // まだ会話なし（起動前）— 空ミラー
	}
	td := agents.TranscriptData{Path: EventsPath(sid)}
	td.Turns = parseEvents(td.Path)
	managedEnrich(m, &td)
	return td, true
}

// event is the shared envelope of an events.jsonl line.
type event struct {
	Type string `json:"type"`
	TS   string `json:"timestamp"`
	// ID/ParentID: every events.jsonl line carries them (実測) — a uuid chain like
	// claude's. The user.message id is the fork anchor (docs/log/55 §55.5).
	ID       string          `json:"id"`
	ParentID string          `json:"parentId"`
	Data     json.RawMessage `json:"data"`
}

// outClip bounds tool output carried to the Console (same spirit as the other
// parsers — the mirror shows a preview, not the full stream).
const outClip = 4000

func clip(s string) string {
	if len(s) <= outClip {
		return s
	}
	return s[:outClip] + "\n…（省略）"
}

// parseEvents renders the whole events.jsonl into turns. Line numbers double as
// the monotonic Idx: user turns use their own line, assistant turns the line of
// their first contributing event.
func parseEvents(path string) []transcript.Turn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var turns []transcript.Turn
	var cur *transcript.Turn
	toolIdx := map[string]int{} // toolCallId → cur.Parts index

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
		toolIdx = map[string]int{}
	}
	// ensureCur opens the assistant turn when needed and advances its end time. copilot
	// records a turn as a SPAN (assistant.turn_start … assistant.turn_end) that is folded
	// into one Turn here, so TS can only ever be the turn's start — on a long tool-running
	// turn that is minutes (and possibly a day) before the answer the user is reading.
	// Every later event of the same turn pushes EndTS forward, so the footer is right even
	// when turn_end never arrives (turn still running, or the CLI died mid-turn).
	ensureCur := func(idx int, ts, model string) {
		if cur == nil {
			cur = &transcript.Turn{Role: "assistant", TS: ts, Idx: idx, Model: model}
		}
		if ts != "" {
			cur.EndTS = ts
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	idx := 0
	for sc.Scan() {
		idx++
		var ev event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "user.message":
			var d struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(ev.Data, &d) != nil || d.Content == "" {
				continue
			}
			flush()
			turns = append(turns, transcript.Turn{
				Role: "user", Text: d.Content, TS: ev.TS, Idx: idx,
				Parts:    []transcript.Part{{Kind: "text", Text: d.Content}},
				AnchorID: ev.ID,
			})
		case "assistant.turn_start":
			var d struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(ev.Data, &d)
			flush()
			cur = &transcript.Turn{Role: "assistant", TS: ev.TS, Idx: idx, Model: d.Model}
		case "assistant.message":
			var d struct {
				Model        string `json:"model"`
				Content      string `json:"content"`
				OutputTokens int    `json:"outputTokens"`
			}
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			ensureCur(idx, ev.TS, d.Model)
			if d.Model != "" {
				cur.Model = d.Model
			}
			cur.OutTok += d.OutputTokens
			if d.Content != "" {
				cur.Parts = append(cur.Parts, transcript.Part{Kind: "text", Text: d.Content})
			}
		case "tool.execution_start":
			var d struct {
				ToolCallID string          `json:"toolCallId"`
				ToolName   string          `json:"toolName"`
				Model      string          `json:"model"`
				Arguments  json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			ensureCur(idx, ev.TS, d.Model)
			part := transcript.Part{Kind: "tool", Tool: d.ToolName, Info: clip(toolLabel(d.Arguments))}
			if f, verb, es := toolEdits(d.ToolName, d.Arguments); f != "" {
				part.File, part.Verb, part.Edits = f, verb, es
			}
			cur.Parts = append(cur.Parts, part)
			if d.ToolCallID != "" {
				toolIdx[d.ToolCallID] = len(cur.Parts) - 1
			}
		case "tool.execution_complete":
			var d struct {
				ToolCallID string `json:"toolCallId"`
				Success    bool   `json:"success"`
				Result     struct {
					Content string `json:"content"`
				} `json:"result"`
			}
			if json.Unmarshal(ev.Data, &d) != nil || cur == nil {
				continue
			}
			if ev.TS != "" {
				cur.EndTS = ev.TS
			}
			i, ok := toolIdx[d.ToolCallID]
			if !ok || i >= len(cur.Parts) {
				continue
			}
			out := d.Result.Content
			if !d.Success && out == "" {
				out = "(failed)"
			}
			cur.Parts[i].Output = clip(out)
		case "assistant.turn_end":
			if cur != nil && ev.TS != "" {
				cur.EndTS = ev.TS // the authoritative end of the span
			}
			flush()
		}
	}
	flush()
	return turns
}

// toolLabel picks the short human-facing label for a tool trace (unchanged order —
// description, then the command, then the path; it just reads the raw arguments now).
func toolLabel(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var a struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Path        string `json:"path"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	for _, s := range []string{a.Description, a.Command, a.Path} {
		if s != "" {
			return s
		}
	}
	return ""
}

// toolEdits extracts the edit-family payload of a copilot tool call, so a session's
// changed-files list (docs/log/68) has a coordinate to list and a before/after to count.
//
// 実測（~/.copilot/session-state/*/events.jsonl, 2026-08）: 観測できた tool は
// `view` / `bash` / `edit` / `grep` の 4 つで、編集は **`edit`** ——
// {"path","old_str","new_str"}。`create` / `write` は同じ引数語彙を持つ兄弟として
// 受けておく（存在しなければ一致しないだけ）。ファイルの削除は `bash` の rm で行われる
// ので、ここでは表現できない（git 側の D が拾う）。
//
// ⚠️ 名前は allowlist。逆にすると名前が変わった版で **`view` しただけのファイルが
// 「変更ファイル」に並ぶ**。取りこぼしは「行が出ない」で済む。
func toolEdits(name string, args json.RawMessage) (file, verb string, edits []transcript.Edit) {
	switch name {
	case "edit", "create", "write", "str_replace":
	default:
		return "", "", nil
	}
	if len(args) == 0 {
		return "", "", nil
	}
	var a struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		OldStr   string `json:"old_str"`
		NewStr   string `json:"new_str"`
		Content  string `json:"content"`
		Contents string `json:"contents"`
	}
	if json.Unmarshal(args, &a) != nil {
		return "", "", nil
	}
	file = a.Path
	if file == "" {
		file = a.FilePath
	}
	if file == "" {
		return "", "", nil
	}
	if body := a.Content + a.Contents; a.OldStr == "" && a.NewStr == "" && body != "" {
		return file, "", []transcript.Edit{{Old: "", New: transcript.CapEdit(body)}}
	}
	if a.OldStr == "" && a.NewStr == "" {
		return "", "", nil // an edit-family name with no payload we understand
	}
	return file, "", []transcript.Edit{{Old: transcript.CapEdit(a.OldStr), New: transcript.CapEdit(a.NewStr)}}
}
