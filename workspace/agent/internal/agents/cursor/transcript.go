package cursor

// Claude Code 互換 JSONL → transcript.Turn 正規化（read 正本、docs/40）。実測
// （v2026.07.20）の行形式:
//
//	{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>…</timestamp>\n<user_query>\n…\n</user_query>"}]}}
//	{"role":"assistant","message":{"content":[{"type":"text","text":"…"},{"type":"tool_use","name":"Shell","input":{"command":"…","description":"…"}}]}}
//	{"type":"turn_ended","status":"success"}
//
// claude パーサは流用できない（uuid/timestamp 無し・独自エンベロープ）が専用は容易。
// tool_result はこの JSONL に載らない（ツール出力は store.db のみ — docs/40）ので
// ミラーはツール名/引数まで、出力は空。1 assistant ターンは複数行に跨り得る
// （tool_use 行＋最終テキスト行）ので、user 行か turn_ended で flush する。
// Turn.Idx は行番号由来の単調増加（Console の pendingEcho/MirrorView は idx 単調
// 前提 — agy 30c5e21 の教訓）。

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
	// managed（ACP）: ローカル転写が無いので driver がメモリ構築したものを返す（driver.go
	// managedTranscript）。停止中で handle が無ければ空ミラー（resume で session/load
	// リプレイが再構築する）。
	if m.DriverKind() == session.DriverManaged {
		return managedTranscript(m), true
	}
	chatID := ChatID(m)
	if chatID == "" {
		return agents.TranscriptData{}, true // まだ会話なし（起動前）— 空ミラー
	}
	path := transcriptPath(m.Dir, chatID)
	return agents.TranscriptData{Path: path, Turns: parseTranscript(path)}, true
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

// contentBlock is one Anthropic-style content block. input is tool_use args
// (arbitrary shape — we pull a few common label fields).
type contentBlock struct {
	Type  string `json:"type"` // "text" | "thinking" | "tool_use"
	Text  string `json:"text"`
	Think string `json:"thinking"`
	Name  string `json:"name"` // tool_use: tool name
	Input struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Path        string `json:"path"`
		FilePath    string `json:"file_path"`
		TargetFile  string `json:"target_file"`
	} `json:"input"`
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
					info := b.Input.Description
					for _, alt := range []string{b.Input.Command, b.Input.Path, b.Input.FilePath, b.Input.TargetFile} {
						if info == "" {
							info = alt
						}
					}
					cur.Parts = append(cur.Parts, transcript.Part{Kind: "tool", Tool: b.Name, Info: clip(info)})
				}
			}
		}
	}
	flush()
	return turns
}
