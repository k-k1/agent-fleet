package sessionx

// Peer peek (#1061): one session reading another session's recent output, read-only.
//
// It rides GET /sessions/{name}/output with peek_from=<reader>, and every rule is closed here
// for the same reason the peer send's are (session_peer.go): MCP only names the reader, taken
// from its own session binding, never from a tool argument. The rules:
//
//   - Same population as a peer send: the workspace-wide peer-messaging switch must be on, and
//     both ends must be kinds that can hold a peer conversation (no shell / ssm). Every meta this
//     Agent holds belongs to this workspace's one user; sessions shared in from other users live
//     in the control plane and never appear here.
//   - Read-only and silent: the target is not interrupted, notified or state-healed (/output
//     already reads with heal=false). The read is audited instead — a log line and a "peek"
//     line in the fleet-graph activity ledger.
//   - Bounded: at most peekMaxLines lines and peekMaxBytes bytes, whatever the caller asks.
//   - Its own rate limit, generous because a read costs the target nothing, but present
//     because a polling loop would still fill the reader's context and the audit ledger.

import (
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

const (
	peekDefaultLines = 100
	peekMaxLines     = 200
	// peekMaxBytes is half of get_session_output's default tail: a peek is a glance at a
	// session the reader does not own, not the report of its own child's task.
	peekMaxBytes = 16 * 1024

	peekRateWindow    = time.Minute
	peekRatePerWindow = 30
)

// peekPolicy decides whether `from` may read `to`'s output.
func peekPolicy(from, to string) error {
	if !uiprefs.PeerMessaging() {
		return peerReject("peek_disabled", "セッション間メッセージが無効なので、他セッションの出力は読めません")
	}
	if !session.ValidName(from) {
		return peerReject("bad_peek_from", "peek_from が不正なセッション名です")
	}
	if from == to {
		return peerReject("peek_self", "自分自身の出力は読めません")
	}
	src, ok := session.ReadMeta(from)
	if !ok || src.Archived {
		return peerReject("peek_from_unknown", "読み手のセッション %s が見つかりません", from)
	}
	if !peerTargetAllowed(src.Kind) {
		return peerReject("peek_from_forbidden", "この種別のセッション（%s）は他セッションの出力を読めません", src.Kind)
	}
	dst, ok := session.ReadMeta(to)
	if !ok || dst.Archived {
		return peerReject("peek_target_unknown", "セッション %s が見つかりません", to)
	}
	if !peerTargetAllowed(dst.Kind) {
		return peerReject("peek_target_forbidden", "この種別のセッション（%s）の出力は読めません", dst.Kind)
	}
	return nil
}

// peekStateAllowed refuses a target whose agent is signed out. The output is built from the
// transcript, never the pane, so a login screen is not in it; the refusal is for the moment a
// re-login is in flight, when what the session shows next is a sign-in flow and not work.
func peekStateAllowed(state string) error {
	if state == agents.StateAuth {
		return peerReject("peek_target_auth", "相手のエージェントはログインが切れています。ログインし直すまで出力は読めません")
	}
	return nil
}

// peekCaps clamps what a peek may return: unset or oversized values fall to the caps.
func peekCaps(tail, lines int) (int, int) {
	if tail <= 0 || tail > peekMaxBytes {
		tail = peekMaxBytes
	}
	if lines <= 0 {
		lines = peekDefaultLines
	}
	if lines > peekMaxLines {
		lines = peekMaxLines
	}
	return tail, lines
}

// recordPeek is the audit trail. Nothing reaches the target.
func recordPeek(from, to string, since, n int) {
	log.Printf("peek: %s read %s's output (since=%d, %d bytes)", from, to, since, n)
	fleetgraph.RecordPeek(from, to)
}

// peekLimiter is a per-reader sliding window, in memory like peerLimiter and reset by a restart
// for the same reason: it is a valve, not an audit trail.
type peekLimiter struct {
	mu    sync.Mutex
	reads map[string][]time.Time
}

var peekRate = &peekLimiter{reads: map[string][]time.Time{}}

func (l *peekLimiter) allow(from string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.reads[from][:0:0]
	for _, t := range l.reads[from] {
		if now.Sub(t) < peekRateWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= peekRatePerWindow {
		l.reads[from] = kept
		return peerReject("peek_rate_limited", "読み取りが多すぎます（%s あたり %d 回まで）", peekRateWindow, peekRatePerWindow)
	}
	l.reads[from] = append(kept, now)
	return nil
}
