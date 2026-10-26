package chatx

// The chat side of routehealth (#1564): which backends a turn may go to while another is
// rate-limited, and the re-run of a failed turn on the next one. One-shots and the deny-all
// runner apply the same table in their own resolvers.

import (
	"context"
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routehealth"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// routeNow is the routing clock, a seam for tests.
var routeNow = time.Now

// routeAvoid reports whether (kind, model) is parked for long enough that a resolver should
// take the next candidate.
func routeAvoid(kind, model string) bool {
	return routehealth.Default.Avoid(kind, model, routeNow())
}

// chatKindUsable is the send-time test for one backend of c: logged in, and not parked for the
// model this conversation would run on it.
func chatKindUsable(c *ChatConversation, kind string) bool {
	return headlessAgentAvailable(kind) && !routeAvoid(kind, chatRouteModel(c, kind))
}

// chatRouteModel is the model a turn of c on kind is parked and looked up under: chatModelFor,
// except that muse never runs on its CLI default (the contributor row) — museChatModel turns ""
// into the safe model, so the key has to name that one, as oneShotRouteModel does.
func chatRouteModel(c *ChatConversation, kind string) string {
	m := chatModelFor(c, kind)
	if m == "" && kind == session.KindMuse {
		m = museSafeOneShotModel(prefsVisibility)
	}
	return m
}

// NoteTurnResult reports one chat call's outcome to the cooldown table and returns the verdict
// (the zero Verdict on success). A turn the user stopped or that ran out of time says nothing
// about the backend and is not reported. kind is the backend that ran (the caller's
// actualAgent).
func NoteTurnResult(ctx context.Context, c *ChatConversation, kind string, err error) routehealth.Verdict {
	model := chatRouteModel(c, kind)
	if err == nil {
		routehealth.Default.Succeed(kind, model)
		return routehealth.Verdict{}
	}
	if ctx.Err() != nil {
		return routehealth.Verdict{}
	}
	return routehealth.Default.Fail(kind, model, err.Error(), routeNow())
}

// FailoverTurn decides whether a turn that failed on from with verdict v is re-run on another
// backend. It is only when the failure parks the backend for longer than NearRecovery and the
// priority order has another usable backend; the caller then rebuilds the prompt for kind (the
// provider-context sync carries the history over) and sends again. notice tells the user the
// turn moved; the conversation's own agent setting is left as it is, so the next turn goes back
// once the park ends. The caller appends notice only after a successful re-run, and only after
// MarkReportsDelivered: the pending reports point into c.Messages.
func FailoverTurn(c *ChatConversation, from string, v routehealth.Verdict) (next ChatProvider, kind string, notice ChatMessage, ok bool) {
	if !v.Cools() {
		return nil, "", ChatMessage{}, false
	}
	next, kind = chatRoute(c)
	if kind == from {
		return nil, "", ChatMessage{}, false
	}
	log.Printf("chat %s: %s failed (%s), re-running the turn on %s", c.ID, from, v.Class, kind)
	// from is the backend that failed, which need not be the conversation's own: that one may
	// already have been passed over before this turn started.
	agent := c.Agent
	if agent == "" {
		agent = from
	}
	notice = newNotice(noticeKeyAgentFailover, map[string]string{"from": from, "to": kind, "agent": agent},
		from+" が利用制限または一時的な障害で使えなかったため、このターンは "+kind+" で応答しました。この会話のエージェント設定（"+agent+"）は変わらず、使えるようになれば次のターンから "+agent+" に戻ります。")
	return next, kind, notice, true
}
