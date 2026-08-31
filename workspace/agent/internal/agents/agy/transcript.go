package agy

// チャットミラー（generic /messages）用の transcript 読み口。agy は会話ごとに
// brain/<conversation-uuid>/.system_generated/logs/transcript_full.jsonl を
// **ターン進行中もライブで** 追記する（実機検証 2026-07-20: 初回プロンプト直後に
// brain ディレクトリと jsonl が出現し、応答完了時点で PLANNER_RESPONSE 行まで
// 読めた）。conversation_summaries.db（遅延書込）や会話 DB（steps の payload が
// 非公開スキーマの protobuf）と違い、素の JSONL なのでこれを唯一の transcript
// ソースにする。1 行 = 1 step:
//
//	{"step_index":N,"source":"USER_EXPLICIT|MODEL|SYSTEM","type":"USER_INPUT|
//	 PLANNER_RESPONSE|<TOOL_NAME>|…","status":"DONE","created_at":…,"content":…}
//
// マッピング: USER_INPUT → user ターン（<USER_REQUEST> 内側のみ抽出）、
// MODEL/PLANNER_RESPONSE → assistant テキスト、MODEL/その他 → tool パート
// （type がそのままツール名: RUN_COMMAND / VIEW_FILE / LIST_DIRECTORY / …）、
// SYSTEM/ERROR_MESSAGE → tool パートとして表面化、他の SYSTEM 行（
// CONVERSATION_HISTORY / CHECKPOINT / SYSTEM_MESSAGE = モデル向け文脈補助）は
// 表示対象外。

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// brainDir is agy's per-conversation artifact root (plans, logs, uploads).
func brainDir() string { return filepath.Join(stateDir(), "brain") }

// transcriptPath prefers transcript_full.jsonl (never truncated) over
// transcript.jsonl (the model-facing view, which drops steps once agy
// checkpoints a long conversation).
func transcriptPath(conv string) string {
	logs := filepath.Join(brainDir(), conv, ".system_generated", "logs")
	full := filepath.Join(logs, "transcript_full.jsonl")
	if _, err := os.Stat(full); err == nil {
		return full
	}
	return filepath.Join(logs, "transcript.jsonl")
}

func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	// The conversation UUID is adopted by captureConversation (brain-dir diff
	// while alive, cwd-map on graceful exit). Before the first prompt lands
	// there is no conversation yet — an empty transcript is the truth.
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return agents.TranscriptData{}, true
	}
	path := transcriptPath(conv)
	td := agents.TranscriptData{Path: path}
	// A pending ASK_QUESTION / permission prompt never reaches the jsonl (実測 —
	// 保留中は無記録), so the interactive card's payload comes from the
	// conversation-DB probe: real options for questions, a synthesized menu for
	// permissions (pending.go). The generic /messages handler only surfaces
	// Pending while the session is alive, which gates out a killed session's
	// stale status=9 row.
	if _, qs := Probe(m); len(qs) > 0 {
		td.Pending = qs
	}
	f, err := os.Open(path)
	if err != nil {
		return td, true
	}
	defer f.Close()
	td.Turns = parseTranscript(f)
	return td, true
}

// stepLine is one transcript_full.jsonl row (fields we read).
type stepLine struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// userRequestRe extracts the actual prompt from agy's USER_INPUT wrapper — the
// content also carries <ADDITIONAL_METADATA> (local time) and
// <USER_SETTINGS_CHANGE> blocks meant for the model, not the user.
var userRequestRe = regexp.MustCompile(`(?s)<USER_REQUEST>\n?(.*?)\n?</USER_REQUEST>`)

// stepMetaLineRe strips the "Created At:/Completed At:" bookkeeping lines agy
// prefixes to tool-step content.
var stepMetaLineRe = regexp.MustCompile(`(?m)^(Created At|Completed At): [^\n]*\n?`)

// toolOutputMax bounds a tool part's output shown in the mirror (the terminal
// remains the raw view; the mirror wants the gist).
const toolOutputMax = 4000

// commandIndentRe is the 4-tab indent agy's RUN_COMMAND result template puts on
// every line after the status sentence ("The command completed successfully.\n
// \t\t\t\tOutput:\n\t\t\t\t<line>…"), which renders as a ragged block in the mirror's
// tool part. Exactly the template's prefix is stripped per line — output that is
// itself tab-indented keeps its own tabs (they sit after the template's four).
var commandIndentRe = regexp.MustCompile(`(?m)^\t{4}`)

func stripCommandIndent(s string) string { return commandIndentRe.ReplaceAllString(s, "") }

func parseTranscript(f *os.File) []transcript.Turn {
	var turns []transcript.Turn
	var cur *transcript.Turn // open assistant turn
	flush := func() {
		if cur != nil {
			var texts []string
			for _, p := range cur.Parts {
				if p.Kind == "text" && p.Text != "" {
					texts = append(texts, p.Text)
				}
			}
			cur.Text = strings.Join(texts, "\n\n")
			turns = append(turns, *cur)
			cur = nil
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // steps can carry large tool output
	// Idx is the source line index (codex's semantics): a stable render key that also
	// has to be strictly increasing — the Console drops polled turns whose idx is not
	// greater than the newest one it holds, and clears a prompt's 反映待ち echo only on
	// a matching user turn with idx > the idx at send time. Leaving every agy turn at
	// the zero value stalled both (docs/log/32).
	line := -1
	for sc.Scan() {
		line++
		var s stepLine
		if json.Unmarshal(sc.Bytes(), &s) != nil {
			continue
		}
		switch {
		case s.Type == "USER_INPUT":
			flush()
			text := s.Content
			if m := userRequestRe.FindStringSubmatch(text); m != nil {
				text = m[1]
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			turns = append(turns, transcript.Turn{
				Role: "user", Text: text, Idx: line,
				Parts: []transcript.Part{{Kind: "text", Text: text}},
			})
		case s.Source == "MODEL" && s.Type == "PLANNER_RESPONSE":
			if text := strings.TrimSpace(s.Content); text != "" {
				if cur == nil {
					cur = &transcript.Turn{Role: "assistant", Idx: line}
				}
				cur.Parts = append(cur.Parts, transcript.Part{Kind: "text", Text: text})
			}
		case s.Source == "MODEL", s.Type == "ERROR_MESSAGE":
			// A tool step (type = tool name), or a surfaced SYSTEM error.
			out := strings.TrimSpace(stepMetaLineRe.ReplaceAllString(s.Content, ""))
			if s.Type == "RUN_COMMAND" {
				out = stripCommandIndent(out)
			}
			if len(out) > toolOutputMax {
				out = out[:toolOutputMax] + "…"
			}
			if cur == nil {
				cur = &transcript.Turn{Role: "assistant", Idx: line}
			}
			cur.Parts = append(cur.Parts, transcript.Part{Kind: "tool", Tool: s.Type, Output: out})
		default:
			// SYSTEM bookkeeping (CONVERSATION_HISTORY / CHECKPOINT / SYSTEM_MESSAGE):
			// model-facing context management, not conversation content.
		}
	}
	flush()
	return turns
}
