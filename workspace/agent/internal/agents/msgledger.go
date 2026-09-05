package agents

// MsgLedger is the persistent ledger of ClientMessageIDs (docs/log/27 §4, §9.5 — operational
// metadata that carries no conversation content). A driver's accept asks it whether an id was
// already submitted, which makes a resend, or a resubmission after a reconnect, idempotent.
// It lives on disk rather than in memory so it still holds across processes: the Console
// resending over an Agent or daemon restart.
//
// One session = one JSON file (<AgentConfigDir>/<subdir>/<name>.json, a ring of the most
// recent ledgerCap entries so it cannot grow without bound). Reads and writes are serialized
// per session by a package-wide mutex — the traffic is a human sending messages, so the
// contention costs nothing.

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
// The /turn handler maps it to the question_pending wire error (the same contract as the
// TUI's submitPromptTUI). Every driver's accept returns it.
var ErrQuestionPending = errQuestionPending{}

type errQuestionPending struct{}

func (errQuestionPending) Error() string { return "question pending" }

// NormalizeMsgID fills in an AF-issued ClientMessageID when the wire didn't carry
// one (§4: AF is the issuer). The opencode side layers an extra "msg"-prefix normalization on
// top of this.
func NormalizeMsgID(id string) string {
	id = strings.TrimSpace(id)
	if id != "" {
		return id
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "af_" + hex.EncodeToString(b)
}
