package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Scheduled execution — session_mode=assistant: a due fire
// runs ONE assistant-chat turn instead of driving a session. The prompt lands as a user
// turn in the target conversation and the assistant (persona + af tools it carries)
// executes it — "have the operator summarise the fleet every morning" without any
// session.
//
// Target resolution: reuse_target names the conversation (an "a…" slug or a UUID);
// empty falls back to the schedule's owner_conv — the operator conversation that
// created the schedule, which docs/log/30 reports already flow into, so the zero-config
// default is "kick my operator".
//
// The turn is SYNCHRONOUS on the Agent (POST /assistant-turns wraps runOperatorTurn):
// a 200 means the turn ran to completion, an error means it did not — so this path
// needs no separate delivery confirmation (unlike the reuse send's confirm). A turn
// already in flight answers 409 turn_in_progress → recorded as skipped_overlap, the
// same unattended-non-delivery surface as a busy reuse target.

// assistantTurnTimeout bounds one scheduled assistant turn end-to-end. It must exceed
// the Agent-side operatorTurnTimeout (6m — which itself allows for a bridge approval
// pause) so the Agent, not this client, is what gives up first.
const assistantTurnTimeout = 8 * time.Minute

func (f *wakeFirer) fireAssistant(ctx context.Context, res *resolved, sch store.Schedule, slot time.Time) (string, string, error) {
	conv := strings.TrimSpace(sch.ReuseTarget)
	if conv == "" {
		conv = strings.TrimSpace(sch.OwnerConv)
	}
	if conv == "" {
		return "skipped_target_missing", "", nil // no conversation to kick (recorded, not an error)
	}
	body, _ := json.Marshal(map[string]string{
		"conv":   conv,
		"prompt": expandSchedulePrompt(sch, slot),
	})
	tctx, cancel := context.WithTimeout(ctx, assistantTurnTimeout)
	defer cancel()
	// agentLongCallClient: this synchronous turn is bounded by tctx's 8 minutes. On the
	// shared client's 2-minute timeout, a turn that waits for approval (up to 4 minutes)
	// would be recorded as a spurious error.
	respBody, status, err := f.agentReqClient(tctx, agentLongCallClient, res.rt, "POST", "/assistant-turns", body)
	if err != nil {
		return "", "", fmt.Errorf("assistant turn: %w", err)
	}
	switch {
	case status == 409 && respHasCode(respBody, "turn_in_progress"):
		return "skipped_overlap", assistantRunLink(respBody, conv), nil
	case status == 404 && respHasCode(respBody, "conv_not_found"):
		// The pinned conversation was deleted. There is no recreate here (a conversation
		// is not schedule-owned state) — surface it like a missing reuse target.
		return "skipped_target_missing", "", nil
	case status >= 300:
		return "", "", fmt.Errorf("assistant turn %d: %s", status, strings.TrimSpace(string(respBody)))
	}
	return "fired", assistantRunLink(respBody, conv), nil
}

// assistantRunLink picks what the run history stores as the fire's target link: the
// conversation's slug when the Agent returned one (the Console opens the chat from it),
// else the ref we addressed.
func assistantRunLink(respBody []byte, fallback string) string {
	var resp struct {
		Slug string `json:"slug"`
	}
	if json.Unmarshal(respBody, &resp) == nil && resp.Slug != "" {
		return resp.Slug
	}
	return fallback
}
