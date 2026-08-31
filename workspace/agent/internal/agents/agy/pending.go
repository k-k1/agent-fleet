package agy

// 会話中インタラクティブプロンプト（ASK_QUESTION / ツール許可）の保留検知。
//
// 検知チャネルの選定（実機調査 2026-07-20, v1.1.4 — docs/log/32）: transcript jsonl は
// 保留中に何も書かず、OSC/タイトル・stderr・lock ファイルも無し。CLI ログ
// （log/cli-*.log の "Surfacing ask_question" 行）はイベントのみで本文が無い。
// 唯一構造まで載るのが **会話 DB（conversations/<uuid>.db）の steps 最終行**:
// 保留中は status=9（実測: 2=実行中・3=完了・9=ユーザー入力待ち）で、
// ask_question はツール引数の JSON（{"questions":[{question, options,
// is_multi_select}]}）が step_payload に**平文文字列として**埋まっている
// （protobuf の length-delimited 文字列なのでスキーマ逆解析は不要 — JSON の
// 開始位置を探して 1 値だけデコードする）。ツール許可の保留も同じ status=9
// （該当ツール step 上）で、こちらは質問構造を持たないため state だけ返す。
// pane スクレイプ（定型見出しの正規表現）は折返し・文言変更に脆く、この DB
// 経路が上位互換なので採用しない。

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite"), as in opencode

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// steps.status の実測値（v1.1.4 — docs/log/32）。最終 step の status が会話の
// 現在地をそのまま表す: 実行中 / 完了 / ユーザー入力待ち。
const (
	stepStatusRunning      = 2
	stepStatusDone         = 3
	stepStatusAwaitingUser = 9
)

// lastStep returns the newest step's status and payload for the slot's
// conversation. ok=false when the conversation isn't adopted yet or the DB is
// unreadable — callers treat that as "no opinion", never as a state.
func lastStep(m session.Meta) (int, []byte, bool) {
	// The conversation UUID may not be adopted yet when the mirror polls before
	// any sessions-list poll ran (capture normally fires in WireLive) — a first
	// prompt straight into a question would then stay invisible. Capture is
	// idempotent and cheap, so run it here too.
	captureConversation(m)
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return 0, nil, false
	}
	db, err := sql.Open("sqlite", "file:"+conversationDBPath(conv)+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return 0, nil, false
	}
	defer db.Close()
	var st int
	var payload []byte
	if err := db.QueryRow(`SELECT status, step_payload FROM steps ORDER BY idx DESC LIMIT 1`).
		Scan(&st, &payload); err != nil {
		return 0, nil, false
	}
	return st, payload, true
}

// LiveState is agy's session state derived from the conversation DB, mirroring
// opencode.LiveState: "question"/"permission" while blocked on the user,
// "working" mid-turn, "idle" once the last step completed, "" when the DB has
// no opinion yet. agy ships no status hooks, so this is the ONLY turn-end
// signal — /input persists an optimistic "working" that nothing else clears,
// which left the operator's 完了報告 arm unconsumed forever (docs/log/30 ②).
// Callers gate on liveness themselves: a killed session's DB keeps its last
// status, which must not surface as live state on a stopped session.
func LiveState(m session.Meta) string {
	st, payload, ok := lastStep(m)
	if !ok {
		return ""
	}
	switch st {
	case stepStatusAwaitingUser:
		if qs := parseAskQuestions(payload); len(qs) > 0 {
			return "question"
		}
		return "permission"
	case stepStatusRunning:
		return "working"
	case stepStatusDone:
		return "idle"
	}
	return ""
}

func conversationDBPath(conv string) string {
	return filepath.Join(stateDir(), "conversations", conv+".db")
}

// Probe reports whether the slot's conversation is blocked on an interactive
// prompt right now: ("question", parsed questions) for ASK_QUESTION,
// ("permission", synthesized menu) for a tool-permission prompt, ("", nil)
// otherwise. Callers gate on liveness themselves — a killed session's DB may
// keep a stale status=9 last step, which must not surface as pending on a
// stopped session.
func Probe(m session.Meta) (string, []transcript.Question) {
	st, payload, ok := lastStep(m)
	if !ok || st != stepStatusAwaitingUser {
		return "", nil
	}
	if qs := parseAskQuestions(payload); len(qs) > 0 {
		return "question", qs
	}
	return "permission", permissionQuestions(payload)
}

// PendingModal は畳まれる直前の人待ちを持ち越しへ渡す（docs/log/75 P5）。Probe の上の
// 薄い写像だが、Kind の落とし方だけは Probe と違う: 許可は **permission** になる。
//
// Probe が許可に対して合成メニュー（Yes / No …）を返すのは、**生きている TUI へ
// キー列を撃つため**である。畳まれた後にそのメニューを描くと、当てる先がもう無い
// 答えを利用者に選ばせることになる（docs/log/75 §75.6.4）。運べるのは「どのコマンドを
// 訊かれていたか」という事実だけで、それは合成した質問文がそのまま持っている。
//
// agy の保留は**会話 DB に残る**ので、ペインが死んだ後でも読める — ACP 3 種と違い、
// halt より遅い契機（一覧が停止を見つけたとき）でも拾える。
func (agentImpl) PendingModal(m session.Meta) (agents.PendingModal, bool) {
	st, qs := Probe(m)
	switch st {
	case "question":
		if len(qs) == 0 {
			return agents.PendingModal{}, false
		}
		return agents.PendingModal{Kind: "question", Questions: qs}, true
	case "permission":
		detail := ""
		if len(qs) > 0 {
			detail = qs[0].Question
		}
		return agents.PendingModal{Kind: "permission", Detail: detail}, true
	}
	return agents.PendingModal{}, false
}

// permissionQuestions synthesizes the pending permission menu as a Question so
// the Console's menu-mode card (Down×i + Enter) can drive it — labels and row
// COUNT must mirror the TUI's menu exactly or the keys land on the wrong row
// (v1.1.4 実測: run_command は 4 行、write_to_file / replace_file_content は
// 2 行)。The pending tool's name rides in the payload as a plain string, so an
// unknown tool (different menu shape we haven't verified) yields NO card —
// state=permission alone, answer in the terminal — rather than a mis-keying one.
func permissionQuestions(payload []byte) []transcript.Question {
	args := parseArgsJSON(payload)
	switch {
	case bytes.Contains(payload, []byte("run_command")):
		return []transcript.Question{{
			Question: "Requesting permission for: " + args["CommandLine"],
			Options: []transcript.Option{
				{Label: "Yes"},
				{Label: "Yes — always in this conversation"},
				{Label: "Yes — always (persist to settings.json)"},
				{Label: "No"},
			},
		}}
	case bytes.Contains(payload, []byte("write_to_file")):
		return []transcript.Question{{
			Question: "Allow creation of this file? " + args["TargetFile"],
			Options:  []transcript.Option{{Label: "Yes, allow creation"}, {Label: "No, deny creation"}},
		}}
	case bytes.Contains(payload, []byte("replace_file_content")):
		return []transcript.Question{{
			Question: "Accept this file edit? " + args["TargetFile"],
			Options:  []transcript.Option{{Label: "Yes, accept this change"}, {Label: "No, reject this change"}},
		}}
	}
	return nil
}

// parseArgsJSON pulls the pending tool's argument JSON (the first decodable
// {"…"} object embedded in the payload) into a flat string map, best-effort —
// a miss just leaves the synthesized question without its detail suffix.
func parseArgsJSON(payload []byte) map[string]string {
	out := map[string]string{}
	for i := 0; i+2 < len(payload); {
		j := bytes.Index(payload[i:], []byte(`{"`))
		if j < 0 {
			break
		}
		i += j
		var doc map[string]any
		if json.NewDecoder(bytes.NewReader(payload[i:])).Decode(&doc) == nil && len(doc) > 0 {
			for k, v := range doc {
				if s, ok := v.(string); ok {
					out[k] = s
				}
			}
			return out
		}
		i += 2
	}
	return out
}

// parseAskQuestions extracts the ask_question tool-args JSON embedded in a
// step payload. The surrounding bytes are protobuf wire garbage; json.Decoder
// stops at the end of the first value, so locating `{"questions":` is enough.
// The TUI appends its own trailing "Write-in..." row that is NOT in this list,
// so option indices here align 1:1 with the widget's numbered rows — the
// Console's menu-mode key driving (Down×i, Enter) lands on the right option.
func parseAskQuestions(payload []byte) []transcript.Question {
	i := bytes.Index(payload, []byte(`{"questions":`))
	if i < 0 {
		return nil
	}
	var doc struct {
		Questions []struct {
			Question      string   `json:"question"`
			Options       []string `json:"options"`
			IsMultiSelect bool     `json:"is_multi_select"`
		} `json:"questions"`
	}
	if json.NewDecoder(bytes.NewReader(payload[i:])).Decode(&doc) != nil || len(doc.Questions) == 0 {
		return nil
	}
	out := make([]transcript.Question, 0, len(doc.Questions))
	for _, q := range doc.Questions {
		tq := transcript.Question{Question: q.Question, MultiSelect: q.IsMultiSelect}
		for _, o := range q.Options {
			tq.Options = append(tq.Options, transcript.Option{Label: o})
		}
		out = append(out, tq)
	}
	return out
}
