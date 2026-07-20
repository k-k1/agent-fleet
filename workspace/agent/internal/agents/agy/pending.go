package agy

// 会話中インタラクティブプロンプト（ASK_QUESTION / ツール許可）の保留検知。
//
// 検知チャネルの選定（実機調査 2026-07-20, v1.1.4 — docs/32）: transcript jsonl は
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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// stepStatusAwaitingUser is the steps.status value agy writes while a step is
// blocked on the user (question widget or permission menu). 実測値。
const stepStatusAwaitingUser = 9

func conversationDBPath(conv string) string {
	return filepath.Join(stateDir(), "conversations", conv+".db")
}

// Probe reports whether the slot's conversation is blocked on an interactive
// prompt right now: ("question", parsed questions) for ASK_QUESTION,
// ("permission", nil) for a tool-permission menu, ("", nil) otherwise.
// Callers gate on liveness themselves — a killed session's DB may keep a stale
// status=9 last step, which must not surface as pending on a stopped session.
func Probe(m session.Meta) (string, []transcript.Question) {
	conv := sids.Read(session.UUID(m.Dir, m.Name))
	if conv == "" {
		return "", nil
	}
	db, err := sql.Open("sqlite", "file:"+conversationDBPath(conv)+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return "", nil
	}
	defer db.Close()
	var status int
	var payload []byte
	if err := db.QueryRow(`SELECT status, step_payload FROM steps ORDER BY idx DESC LIMIT 1`).
		Scan(&status, &payload); err != nil || status != stepStatusAwaitingUser {
		return "", nil
	}
	if qs := parseAskQuestions(payload); len(qs) > 0 {
		return "question", qs
	}
	return "permission", nil
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
