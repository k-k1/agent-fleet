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
// 前提 — agy 1ccb63e の教訓）。

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
	td := agents.TranscriptData{Path: path, Turns: parseTranscript(path)}
	// TUI/-p はモデルを転写に書かない（docs/40 §モデル表示）ので、起動モデル（セッション
	// 固定）を各 assistant ターンにスタンプしてミラーのモデルバッジに出す。未選択＝Auto。
	stampModel(td.Turns, displayModel(m.Model))
	return td, true
}

// displayModel normalizes a cursor model id for the mirror's per-response badge:
// ACP の bracket パラメータ（`claude-opus-4-8[thinking=true,context=300k,effort=high]`）を
// 剥がして素の id にし、Auto 系（空文字列 /`auto`/`default[]`）は "Auto" に寄せる。ピッカーの
// dash 形式（`composer-2.5` 等）はそのまま。cursor は**セッションでモデル固定**（per-session
// child・DynamicModel:false）なので、全 assistant ターンに同じ値が載る（docs/40 §モデル表示）。
// 注意: これは**設定モデル**であって、Auto が各ターンで実際に解決した具体モデルではない
// （公式経路に解決先が出ない — docs/40）。
func displayModel(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.IndexByte(id, '['); i >= 0 {
		id = strings.TrimSpace(id[:i]) // ACP の [params] を剥がす
	}
	switch strings.ToLower(id) {
	case "", "auto", "default":
		return "Auto"
	}
	return id
}

// stampModel labels every assistant turn with the session's (fixed) model so the
// mirror renders a per-response model badge（MirrorView の turn.model 経路）。既に
// 値があるターンは尊重する（将来の per-turn 情報源に備えて上書きしない）。
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

// ── 編集の抽出（docs/68）────────────────────────────────────────────────────────
//
// 経路が 2 つあり、手掛かりが違う:
//   jsonl（TUI / -p）  … tool 名しか無い → toolEdits が名前の allowlist で判定する
//   ACP（managed）     … プロトコル自身が `kind` で分類している → 名前を見ない
// 共通なのは入力の形だけなので、before/after の取り出し（editsFromInput）を分けてある。

// editInput is the union of the field spellings an edit-family call has been seen to use.
// 実測（transcript jsonl, 2026-08）: `Write` は {"path","contents"}。他は同じ語彙群だが
// 実呼び出しを観測できていないので、claude 綴り（old_string/new_string）と copilot 綴り
// （old_str/new_str）の両方を受ける。
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
// ⚠️ 名前は allowlist で、知らないものは無視する。逆（read 以外を編集とみなす）にすると、
// 名前が変わった版で **Read しただけのファイルが「変更ファイル」に並ぶ** —— 一覧が黙って
// 嘘をつく側に倒れる。取りこぼしたときに起きるのは「行が出ない」だけで済む。
func toolEdits(name string, input json.RawMessage) (file, verb string, edits []transcript.Edit) {
	switch name {
	case "Write", "Create", "Edit", "MultiEdit":
		f, es := editsFromInput(input)
		if f == "" || len(es) == 0 {
			return "", "", nil
		}
		return f, "", es
	case "Delete":
		// 消えたファイルには before/after が無い。ここだけ verb を明示する
		// （「Edits が無い＝削除」という推定は差分本体を運ばない kind を壊すので使えない）。
		f, _ := editsFromInput(input)
		if f == "" {
			return "", "", nil
		}
		return f, "delete", nil
	}
	return "", "", nil
}
