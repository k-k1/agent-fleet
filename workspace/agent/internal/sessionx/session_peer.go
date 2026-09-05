package sessionx

// The server-side fort for session-to-session messages (docs/log/58 / ADR 0041).
//
// Sending itself reuses the existing delivery path (/input's {prompt}). What lives here is
// only the set of constraints that being a peer send imposes, and putting them on the server
// is deliberate: the envelope, the target policy, leaving the arm alone and the rate limit
// can all be bypassed if the caller (the MCP process) implements them. MCP stays a thin
// layer that adds only `peer_from`, and every invariant that has to hold is closed here.
//
// Why the arm is not touched (ADR 0041 decision 4): the reconciler in docs/log/51 infers
// completion from mechanical idleness as its evidence. A peer message carries no conv and
// starts a new turn against an idle target, so putting it on the instruction ledger is read
// as "a new instruction from the user" and causes an early settle / early consumption. On
// top of that AF delivers by typing into the TUI, so on the receiving side the transcript
// cannot tell it apart from ordinary input (there is no way to add after the fact the mark
// the native path has as `origin.kind:"peer"` — measured, docs/log/58 §58.12). With no
// escape hatch of "reject it later by looking at its origin", not putting it on the ledger
// in the first place is the only defence.

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// peerMaxMessageBytes caps the body. Long enough to hold one reasoned finding handed to
	// whoever implements it (a review result, say), while staying realistic as something
	// typed into a TUI. Going over is an error, never a silent truncation: truncating gives
	// the worst failure of all, "it was sent, but the part that mattered is gone".
	peerMaxMessageBytes = 16 * 1024

	// peerRateWindow / peerRatePerWindow rate-limit per sending session. The existing
	// send_to_session has no limit because there was one sender, the operator; with N
	// senders, A->B->A happens on its own.
	peerRateWindow    = time.Minute
	peerRatePerWindow = 6

	// An identical (target, body) arriving within peerDuplicateWindow is dropped. A ping-pong
	// loop tends to throw the same text back and forth, and the rate limit alone lets it run
	// pointlessly up to the cap.
	peerDuplicateWindow = 2 * time.Minute
)

// peerRejection is the decision not to send. Code becomes the HTTP error code as is.
type peerRejection struct {
	Code string
	Msg  string
}

func (e *peerRejection) Error() string { return e.Msg }

func peerReject(code, format string, a ...any) *peerRejection {
	return &peerRejection{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// peerIntents maps the kind of the body to the reply policy it imposes on the receiver
// (docs/log/58 §58.14).
//
// The point is that the sender does not get to choose the reply policy. As a separate field
// it would allow a contradictory envelope, e.g. a `notice` (just letting you know) that
// "requires a reply". One kind is mandatory and the reply policy is derived from it here.
//
// It is held to four values because the more options there are the more the model hesitates
// and the weaker the envelope's meaning gets. answer / notice carrying `none` (stop here) is
// what structurally ends a chain of acknowledgements, and it is the only valve against the
// polite loop whose wording differs every time, which the existing duplicate drop cannot
// stop.
var peerIntents = map[string]string{
	"request":  "only-if-blocked", // asks for action; reply only when you can't or the premise is wrong
	"question": "required",        // asks for information; one reply with the conclusion only
	"answer":   "none",            // answers the other side's question; ends here
	"notice":   "none",            // just letting you know; no reply
}

// PeerIntentNames is the order used for deterministic error text and tool descriptions (map
// iteration is unordered).
var PeerIntentNames = []string{"request", "question", "answer", "notice"}

// peerResolveIntent validates the kind and returns the reply policy to put in the envelope.
//
// An empty value is not defaulted, because any default is wrong: defaulting to `notice` (no
// reply needed) means a request is silently ignored, and defaulting to `request` means a
// plain share draws a reply. Either one destroys the very goal of cutting down the noise. A
// missing intent is a bug in the caller, so it comes back as an error.
func peerResolveIntent(intent string) (string, error) {
	reply, ok := peerIntents[strings.TrimSpace(intent)]
	if !ok {
		return "", peerReject("bad_peer_intent",
			"intent（本文の種別）は %s のいずれかにしてください", strings.Join(PeerIntentNames, " / "))
	}
	return reply, nil
}

// peerEnvelope builds the single line placed at the head of the delivered body.
//
// It is prepended to the prompt because typing into each kind's TUI / driver is the only
// common delivery layer, and outside claude there is no side band (ADR 0041 decision 6). It
// is the same layer and the same convention as selfReportHintLine's `[agent-fleet]` note,
// with the standing rules for how to receive one held in workspace-notes.md.
//
// The server always attaches the envelope. Letting the caller build it lets a missing
// envelope, or a forged one claiming another session's name, straight through.
//
// `intent` / `reply` go in the envelope because reply discipline bites at the moment of
// arrival, and writing them only in workspace-notes.md dilutes them far back in a long
// context (docs/log/58 §58.14). The values stay English: they are the same machine-token
// layer as the existing `from=`, and the mirror reads them back here too. `reply=` uses
// self-describing words so it still makes sense to a reader who missed the standing rules.
func peerEnvelope(from, intent, reply, message string) string {
	return "[agent-fleet:peer from=" + from + " intent=" + intent + " reply=" + reply + "] " +
		strings.TrimSpace(message)
}

// peerTargetAllowed answers whether a peer message may be sent to this kind.
//
// Rejecting shell / ssm is the whole point (ADR 0041 decision 5). Sending to a raw shell is
// arbitrary command execution: a session that read a poisoned repository would be able to
// run arbitrary commands elsewhere. The check reads the raw value rather than going through
// NormalizeKind, because NormalizeKind maps unknown/empty to claude — a single meta with an
// empty Kind would otherwise open a hole beyond shell.
func peerTargetAllowed(kind string) bool {
	switch kind {
	case session.KindShell, session.KindSSM, "":
		return false
	}
	for _, k := range mcpreg.MaterializedKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// PeerReachableSessions is the set of targets visible from `from` (the population behind
// list_peer_sessions). It excludes the sender itself, archived sessions and kinds that
// cannot be sent to. Stopped sessions are included: AF can resume a stopped session and
// deliver to it, so dropping them from the list would hide reachable targets.
func PeerReachableSessions(from string) []session.Meta {
	var out []session.Meta
	for _, m := range session.ListMetas() {
		if m.Archived || m.Name == from || !session.ValidName(m.Name) {
			continue
		}
		if !peerTargetAllowed(m.Kind) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// peerPolicy decides whether the send is allowed. It returns the target's meta so the caller
// does not have to look the kind up again.
func peerPolicy(from, to string) (session.Meta, error) {
	if !session.ValidName(from) {
		return session.Meta{}, peerReject("bad_peer_from", "peer_from が不正なセッション名です")
	}
	if !session.ValidName(to) {
		return session.Meta{}, peerReject("bad_name", "宛先が不正なセッション名です")
	}
	if from == to {
		return session.Meta{}, peerReject("peer_self", "自分自身には送れません")
	}
	src, ok := session.ReadMeta(from)
	if !ok || src.Archived {
		return session.Meta{}, peerReject("peer_from_unknown", "送信元セッション %s が見つかりません", from)
	}
	// The sender goes through the same allowlist. A `peer_from` claimed by a kind that was
	// never handed the tools does not get through — this closes the path of calling REST
	// directly from a shell that has no MCP.
	if !peerTargetAllowed(src.Kind) {
		return session.Meta{}, peerReject("peer_from_forbidden",
			"この種別のセッション（%s）はメッセージを送れません", src.Kind)
	}
	dst, ok := session.ReadMeta(to)
	if !ok || dst.Archived {
		return session.Meta{}, peerReject("peer_target_unknown", "宛先セッション %s が見つかりません", to)
	}
	if !peerTargetAllowed(dst.Kind) {
		return session.Meta{}, peerReject("peer_target_forbidden",
			"この種別のセッション（%s）へは送れません", dst.Kind)
	}
	return dst, nil
}

// peerValidateMessage checks the body.
func peerValidateMessage(message string) error {
	m := strings.TrimSpace(message)
	if m == "" {
		return peerReject("empty_message", "message（送信本文）が必要です")
	}
	if !utf8.ValidString(m) {
		return peerReject("bad_message", "message は UTF-8 文字列にしてください")
	}
	if len(m) > peerMaxMessageBytes {
		return peerReject("message_too_long",
			"message は %d byte 以内にしてください（現在 %d byte）", peerMaxMessageBytes, len(m))
	}
	return nil
}

// peerLimiter is the state behind the rate limit and the duplicate drop. It is held in
// memory because the Agent process is resident; the MCP process lives and dies per call, so
// it cannot live there. A restart resets it, which is acceptable: this is a valve for
// stopping loops, not an audit trail.
type peerLimiter struct {
	mu     sync.Mutex
	sends  map[string][]time.Time // sender -> recent send times
	recent map[string]time.Time   // sender|target|body -> when it was last let through
}

var peerRate = &peerLimiter{
	sends:  map[string][]time.Time{},
	recent: map[string]time.Time{},
}

// allow decides on the rate limit and duplicates, and updates the state only when it lets
// the message through.
func (l *peerLimiter) allow(from, to, message string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Duplicate: repeating the same text to the same peer in quick succession is the shape
	// of a ping-pong loop.
	key := from + "|" + to + "|" + message
	if at, ok := l.recent[key]; ok && now.Sub(at) < peerDuplicateWindow {
		return peerReject("peer_duplicate",
			"同じ内容を同じ宛先へ連続して送ろうとしています（%s 以内は捨てます）", peerDuplicateWindow)
	}

	// Rate: peerRatePerWindow messages per peerRateWindow, per sender.
	times := l.sends[from][:0:0]
	for _, t := range l.sends[from] {
		if now.Sub(t) < peerRateWindow {
			times = append(times, t)
		}
	}
	if len(times) >= peerRatePerWindow {
		l.sends[from] = times
		return peerReject("peer_rate_limited",
			"送信が多すぎます（%s あたり %d 通まで）", peerRateWindow, peerRatePerWindow)
	}

	l.sends[from] = append(times, now)
	l.recent[key] = now
	l.pruneLocked(now)
	return nil
}

// pruneLocked drops stale duplicate keys. Left alone they grow memory monotonically in a
// long-lived Agent.
func (l *peerLimiter) pruneLocked(now time.Time) {
	for k, at := range l.recent {
		if now.Sub(at) >= peerDuplicateWindow {
			delete(l.recent, k)
		}
	}
	for from, times := range l.sends {
		if len(times) == 0 {
			delete(l.sends, from)
		}
	}
}
