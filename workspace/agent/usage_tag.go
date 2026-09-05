package main

// Only the usage tags that touch the chat family live here. The tag type, putting it in
// and out of a context, the single recording point in the provider layer and the ledger
// all call internal/usagex directly.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// chatTurnUsageTag is the tag for one assistant-chat turn. SeedVerb (the translate /
// summarize threads seeded from Files) is filed as a sub-dimension of verb rather than
// a feature of its own: it would be nice to see as a separate category, but growing the
// feature enum ripples through the Console's colours, i18n and filters (docs/log/46 §1-a).
//
// It cannot move to usagex because it takes chat's conversation type (internal/chatx).
func chatTurnUsageTag(c *chatx.ChatConversation, trigger string) usagex.Tag {
	return usagex.Tag{
		Feature: usagex.FeatureAssistantChat, Trigger: trigger, Ref: c.ID, Verb: c.SeedVerb,
	}
}
