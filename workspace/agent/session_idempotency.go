package main

// create_session の冪等台帳。POST /sessions を idempotency キーで重複排除し、
// 「クライアントがタイムアウトしたがバックエンドは実際にセッションを作っていた」
// レース（＝LLM が失敗と誤認して再実行し、独立した 2 つ目のセッションを生む事故・
// docs/log/36 の二重起動）を潰す。ClientMessageID の MsgLedger（agents.MsgLedger, docs/log/27
// §4/§9.5）と同じ思想の、create 専用・軽量版。
//
// 永続化は不要（重複リトライは数秒後・同一プロセスに届く）。TTL リングで肥大しない。

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// createLedgerTTL は 1 レコードの寿命。最も遅い起動（worktree の fetch＋CLI ブート）を
// 余裕を持って上回る必要がある — そうでないとタイムアウトして再実行した（あるいは
// 純粋に並走した）create が第 1 リクエストへ収束せず、重複セッションを生む。一方で、
// 後から意図的に同一内容を起動する分まで塞がない程度には短くする。
const createLedgerTTL = 3 * time.Minute

type createLedgerState int

const (
	createInflight createLedgerState = iota // 作成中（第 1 リクエストが重作業を実行中）
	createDone                              // 完了（body に wireSession JSON を保持）
)

type createLedgerEntry struct {
	state createLedgerState
	body  []byte // idempotent リプレイ用の wireSession JSON（state==createDone のとき）
	at    time.Time
}

// createSessionLedger は POST /sessions を idempotency キーで重複排除する in-memory 台帳。
type createSessionLedger struct {
	mu sync.Mutex
	m  map[string]*createLedgerEntry
}

var createLedger = &createSessionLedger{m: map[string]*createLedgerEntry{}}

// begin は key の作成権を主張する。呼び出し元がこの create を所有して続行すべきときは
// (nil, false) を返す。既にレコードがあれば、そのスナップショットと true を返すので、
// 呼び出し元は done ならリプレイ、inflight なら「作成中」として弾ける。
func (l *createSessionLedger) begin(key string) (createLedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked()
	if e := l.m[key]; e != nil {
		return *e, true
	}
	l.m[key] = &createLedgerEntry{state: createInflight, at: time.Now()}
	return createLedgerEntry{}, false
}

// complete は inflight レコードを done へ遷移させ、リプレイ用 body を格納する。
func (l *createSessionLedger) complete(key string, body []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil {
		e.state = createDone
		e.body = body
		e.at = time.Now()
	}
}

// fail は inflight のまま終わったレコードを取り除き、真のリトライを通す。done は
// リプレイのために残す。
func (l *createSessionLedger) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil && e.state == createInflight {
		delete(l.m, key)
	}
}

// lookup は現在のレコードのスナップショットを返す（GET /sessions/idempotency/{key} 用）。
func (l *createSessionLedger) lookup(key string) (createLedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked()
	if e := l.m[key]; e != nil {
		return *e, true
	}
	return createLedgerEntry{}, false
}

func (l *createSessionLedger) gcLocked() {
	cutoff := time.Now().Add(-createLedgerTTL)
	for k, e := range l.m {
		if e.at.Before(cutoff) {
			delete(l.m, k)
		}
	}
}

// createIdempotencyKey は create リクエストの重複排除キーを決める。
//   - クライアント（stdio MCP の create_session）が明示キーを送っていればそれを使う。
//     ツールは会話 id＋引数から決定論的に算出するので、LLM が同じ引数で再実行すると
//     同じキーが再現し、タイムアウト再実行が第 1 セッションへ収束する。
//   - 明示キーが無くても、report_to（作成元の会話）でスコープした意図フィンガープリントに
//     フォールバックし、キーを送らないクライアント（CP MCP）も再実行で二重作成できない
//     ようにする。会話スコープが無い（interactive な Console 起動）ときは重複排除しない
//     — 人が意図的に同一内容を 2 つ起動する自由を残す。
func createIdempotencyKey(r *createReq) string {
	if k := strings.TrimSpace(r.IdempotencyKey); k != "" {
		return k
	}
	if strings.TrimSpace(r.ReportTo) == "" {
		return ""
	}
	h := sha256.New()
	for _, f := range []string{
		r.ReportTo, r.Dir, r.Subdir, r.Kind, r.Model, r.Effort, r.Mode, r.Driver,
		r.InitialPrompt, r.Branch, r.NewBranch, r.RemoteURL, r.RepoName, r.Folder, r.Title,
		strconv.FormatBool(r.Worktree), strconv.FormatBool(r.UseExisting),
	} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return "fp_" + hex.EncodeToString(h.Sum(nil))
}
