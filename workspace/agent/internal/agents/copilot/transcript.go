package copilot

// events.jsonl → transcript.Turn 正規化（read 正本、docs/36）。イベント形は
// v1.0.73 実測（docs/36 実測記録）: user.message / assistant.turn_start /
// assistant.message / tool.execution_start|complete / assistant.turn_end が
// 描画対象で、deltas・reasoning などの ephemeral イベントはファイルに書かれない。
// Turn.Idx は行番号由来の単調増加（Console の pendingEcho/MirrorView は idx
// 単調前提 — agy 1ccb63e の教訓）。

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
	Type string          `json:"type"`
	TS   string          `json:"timestamp"`
	Data json.RawMessage `json:"data"`
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
				Parts: []transcript.Part{{Kind: "text", Text: d.Content}},
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
				ToolCallID string `json:"toolCallId"`
				ToolName   string `json:"toolName"`
				Model      string `json:"model"`
				Arguments  struct {
					Command     string `json:"command"`
					Description string `json:"description"`
					Path        string `json:"path"`
				} `json:"arguments"`
			}
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			ensureCur(idx, ev.TS, d.Model)
			info := d.Arguments.Description
			if info == "" {
				info = d.Arguments.Command
			}
			if info == "" {
				info = d.Arguments.Path
			}
			cur.Parts = append(cur.Parts, transcript.Part{Kind: "tool", Tool: d.ToolName, Info: clip(info)})
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
