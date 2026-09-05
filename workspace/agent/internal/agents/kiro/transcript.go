package kiro

// Normalizing the v2 JSONL session store into transcript.Turn (the read source of truth,
// docs/log/43). The measured (2.14.1) row format, one record per line, append-only:
//
//	{"version":"v1","kind":"Prompt","data":{"message_id":"…","content":[{"kind":"text","data":"…"}],"meta":{"timestamp":1784869360}}}
//	{"version":"v1","kind":"AssistantMessage","data":{"message_id":"…","content":[{"kind":"text","data":"…"},{"kind":"toolUse","data":{"toolUseId":"…","name":"shell","input":{"command":"…","__tool_use_purpose":"…"}}}]}}
//	{"version":"v1","kind":"ToolResults","data":{"message_id":"…","content":[{"kind":"toolResult","data":{"toolUseId":"…","content":[{"kind":"json","data":{"exit_status":"exit status: 0","stdout":"…","stderr":""}}],"status":"success"}}]}}
//
// Unlike cursor there is no turn_ended marker (state detection lives in the TUI string
// contract, state.go). Turn boundaries come from the Prompt records: 1 Prompt = 1 user turn,
// and the AssistantMessage rows that follow (possibly with toolUse between them) fold into
// one assistant turn. ToolResults pastes its output onto the tool part that matches by
// toolUseId (tool output that could not be obtained for cursor is available here). A content
// block's `data` is a string for text and an object for toolUse/toolResult, so it is taken
// as RawMessage and decoded per kind. Turn.Idx increases monotonically from the line number
// (the Console's pendingEcho/MirrorView assume a monotonic idx — the lesson of agy 7354916).

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	// managed (ACP): with a live handle, the driver returns the in-memory transcript it built
	// from session/update (live streaming). While stopped it falls back to fileTranscript
	// below — kiro's acp persists the transcript into the v2 JSONL, so unlike cursor the
	// history is still available when stopped (driver.go managedTranscript).
	if m.DriverKind() == session.DriverManaged {
		return managedTranscript(m), true
	}
	return fileTranscript(m), true
}

// fileTranscript renders the session's v2 JSONL store into turns. Used by the TUI route
// and, for a stopped/detached managed session, by managedTranscript as the persisted
// fallback (kiro's acp writes the same store the TUI does).
func fileTranscript(m session.Meta) agents.TranscriptData {
	sid := resolveSid(m)
	if sid == "" {
		return agents.TranscriptData{} // no conversation yet (before launch) — an empty mirror
	}
	path := transcriptPath(sid)
	td := agents.TranscriptData{Path: path, Turns: parseTranscript(path), Mode: modeOf(m)}
	// The v2 JSONL does not write the model onto an assistant record (measured), so the launch
	// model (fixed for the session) is stamped on every assistant turn to feed the mirror's
	// model badge.
	stampModel(td.Turns, displayModel(m.Model))
	return td
}

// modeOf normalizes the slot's launch mode for the mirror's plan indicator. kiro's
// plan posture is launch-fixed (buildProgram drops --trust-all-tools for plan), so
// meta.Mode is the truth.
func modeOf(m session.Meta) string {
	if m.Mode == "plan" {
		return "plan"
	}
	return "normal"
}

// displayModel normalizes a kiro model id for the mirror's per-response badge. kiro
// ids are plain ("claude-sonnet-4.5" / "auto"); "auto" (the default, 1M ctx) is folded
// into "Auto".
func displayModel(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "", "auto", "default":
		return "Auto"
	}
	return strings.TrimSpace(id)
}

// stampModel labels every assistant turn with the session's (fixed) model so the
// mirror renders a per-response model badge. A turn that already has one is left alone.
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

// record is one JSONL row. data.content blocks carry a kind-dependent `data`
// payload, decoded lazily via block.Data (RawMessage).
type record struct {
	Kind string `json:"kind"` // "Prompt" | "AssistantMessage" | "ToolResults"
	Data struct {
		Content []block `json:"content"`
		Meta    struct {
			Timestamp int64 `json:"timestamp"` // unix seconds (Prompt rows)
		} `json:"meta"`
	} `json:"data"`
}

// block is one content block. Data is a JSON string for kind=text and an object for
// kind=toolUse/toolResult.
type block struct {
	Kind string          `json:"kind"` // "text" | "toolUse" | "toolResult"
	Data json.RawMessage `json:"data"`
}

// toolUseData is the toolUse block payload.
type toolUseData struct {
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
	Input     struct {
		Command  string `json:"command"`
		Purpose  string `json:"__tool_use_purpose"`
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	} `json:"input"`
}

// toolResultData is the toolResult block payload; the nested json content carries the
// executed command's stdout/stderr.
type toolResultData struct {
	ToolUseID string `json:"toolUseId"`
	Content   []struct {
		Data struct {
			ExitStatus string `json:"exit_status"`
			Stdout     string `json:"stdout"`
			Stderr     string `json:"stderr"`
		} `json:"data"`
	} `json:"content"`
	Status string `json:"status"`
}

// outClip bounds carried text/output (parity with the other parsers — a preview).
const outClip = 4000

func clip(s string) string {
	if len(s) <= outClip {
		return s
	}
	return s[:outClip] + "\n…（省略）"
}

// parseTranscript renders the whole v2 JSONL into turns.
func parseTranscript(path string) []transcript.Turn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var turns []transcript.Turn
	var cur *transcript.Turn    // open assistant turn
	toolIdx := map[string]int{} // toolUseId → index into cur.Parts (valid only while cur is open)

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

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	idx := 0
	for sc.Scan() {
		idx++
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		switch r.Kind {
		case "Prompt":
			flush()
			txt := ""
			for _, b := range r.Data.Content {
				if b.Kind == "text" {
					txt += decodeText(b.Data)
				}
			}
			txt = strings.TrimSpace(txt)
			if txt == "" {
				continue
			}
			turns = append(turns, transcript.Turn{
				Role: "user", Text: txt, Idx: idx, TS: tsRFC3339(r.Data.Meta.Timestamp),
				Parts: []transcript.Part{{Kind: "text", Text: txt}},
			})
		case "AssistantMessage":
			if cur == nil {
				cur = &transcript.Turn{Role: "assistant", Idx: idx}
			}
			for _, b := range r.Data.Content {
				switch b.Kind {
				case "text":
					if t := decodeText(b.Data); t != "" {
						cur.Parts = append(cur.Parts, transcript.Part{Kind: "text", Text: t})
					}
				case "toolUse":
					var tu toolUseData
					if json.Unmarshal(b.Data, &tu) != nil {
						continue
					}
					info := tu.Input.Command
					for _, alt := range []string{tu.Input.Purpose, tu.Input.Path, tu.Input.FilePath} {
						if info == "" {
							info = alt
						}
					}
					cur.Parts = append(cur.Parts, transcript.Part{Kind: "tool", Tool: tu.Name, Info: clip(info)})
					if tu.ToolUseID != "" {
						toolIdx[tu.ToolUseID] = len(cur.Parts) - 1
					}
				}
			}
		case "ToolResults":
			if cur == nil {
				continue // an orphaned result (normally impossible, since the whole file is read)
			}
			for _, b := range r.Data.Content {
				if b.Kind != "toolResult" {
					continue
				}
				var tr toolResultData
				if json.Unmarshal(b.Data, &tr) != nil {
					continue
				}
				i, ok := toolIdx[tr.ToolUseID]
				if !ok || i >= len(cur.Parts) {
					continue
				}
				out := ""
				for _, c := range tr.Content {
					if c.Data.Stdout != "" {
						out += c.Data.Stdout
					} else if c.Data.Stderr != "" {
						out += c.Data.Stderr
					}
				}
				if out != "" {
					cur.Parts[i].Output = clip(out)
				}
			}
		}
	}
	flush()
	return turns
}

// decodeText pulls the string payload of a kind=text block (`data` is a JSON string).
func decodeText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// tsRFC3339 formats a unix-second timestamp to RFC3339 (UTC), "" for the zero value.
func tsRFC3339(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}
