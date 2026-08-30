package agents

// MsgLedger は ClientMessageID の永続台帳（docs/log/27 §4・§9.5 — 会話内容を含まない
// 運用メタデータ）。driver の accept が「この ID は投入済みか」を引き、再送・
// reconnect 後の二重投入を冪等化する。P2 は handle 生存中の in-memory 台帳だけ
// だった（§12.2-3 の将来課題）— P3 でプロセス跨ぎ（Agent 再起動・daemon 再起動を
// 挟んだ Console の再送）にも効くようファイルへ永続化した。
//
// 1 セッション = 1 JSON ファイル（<AgentConfigDir>/<subdir>/<name>.json、直近
// ledgerCap 件のリングで肥大しない）。読み書きはセッション毎に直列（package 単位の
// mutex — 頻度は人間の送信なので競合コストは無視できる）。

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

const ledgerCap = 200

type MsgLedger struct {
	mu    sync.Mutex
	files fstore.Store[[]string]
}

// NewMsgLedger builds a ledger persisting under <AgentConfigDir>/<subdir>.
func NewMsgLedger(subdir string) *MsgLedger {
	return &MsgLedger{files: fstore.JSON[[]string](paths.AgentConfigDir, subdir, ".json")}
}

// SeenOrRecord reports whether id was already submitted for name, recording it
// when new. Empty ids are never deduplicated (caller normalizes first).
func (l *MsgLedger) SeenOrRecord(name, id string) bool {
	if id == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ids, _ := l.files.Read(name)
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	ids = append(ids, id)
	if len(ids) > ledgerCap {
		ids = ids[len(ids)-ledgerCap:]
	}
	_ = l.files.Write(name, ids)
	return false
}

// Remove drops a session's ledger (stop — the slot identity is retired).
func (l *MsgLedger) Remove(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files.Remove(name)
}

// ErrQuestionPending is the driver-agnostic "answer the question first" guard.
// /turn ハンドラが question_pending のワイヤエラー（tui の submitPromptTUI と同じ
// 契約）へ写像する。各 driver の accept が返す。
var ErrQuestionPending = errQuestionPending{}

type errQuestionPending struct{}

func (errQuestionPending) Error() string { return "question pending" }

// NormalizeMsgID fills in an AF-issued ClientMessageID when the wire didn't carry
// one（§4: 採番者は AF）。opencode 側は "msg" prefix の追加正規化を重ねる。
func NormalizeMsgID(id string) string {
	id = strings.TrimSpace(id)
	if id != "" {
		return id
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "af_" + hex.EncodeToString(b)
}
