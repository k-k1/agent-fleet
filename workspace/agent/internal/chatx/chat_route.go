package chatx

// The chat side of routehealth (#1564): which backends a turn may go to while another is
// rate-limited, and the re-run of a failed turn on the next one. One-shots and the deny-all
// runner apply the same table in their own resolvers.

import (
	"context"
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routehealth"
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
	return headlessAgentAvailable(kind) && !routeAvoid(kind, chatModelFor(c, kind))
}

// NoteTurnResult reports one chat call's outcome to the cooldown table and returns the verdict
// (the zero Verdict on success). A turn the user stopped or that ran out of time says nothing
// about the backend and is not reported. kind is the backend that ran (the caller's
// actualAgent).
func NoteTurnResult(ctx context.Context, c *ChatConversation, kind string, err error) routehealth.Verdict {
	model := chatModelFor(c, kind)
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
// backend. It is only when the failure parks the backend for longer than NearRecovery and the priority
// order has another usable backend; the caller then rebuilds the prompt for kind (the
// provider-context sync carries the history over) and sends again. notice tells the user the
// turn moved; the conversation's own agent setting is left as it is, so the next turn goes back
// once the park ends.
func FailoverTurn(c *ChatConversation, from string, v routehealth.Verdict) (next ChatProvider, kind string, notice ChatMessage, ok bool) {
	if !v.Cools() {
		return nil, "", ChatMessage{}, false
	}
	next, kind = chatRoute(c)
	if kind == from {
		return nil, "", ChatMessage{}, false
	}
	log.Printf("chat %s: %s failed (%s), re-running the turn on %s", c.ID, from, v.Class, kind)
	notice = newNotice(noticeKeyAgentFailover, map[string]string{"from": from, "to": kind},
		from+" が利用制限または一時的な障害で使えなかったため、このターンは "+kind+" で応答しました。会話のエージェント設定は "+from+" のままです。")
	return next, kind, notice, true
}
