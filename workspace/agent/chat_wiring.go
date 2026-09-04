package main

// Wiring for the chat family (`internal/chatx`), and nothing else. What lives here is not an
// alias but the seam that hands chatx the 40 reverse dependencies on main as function values.
// chatx cannot import main, so this is the only way (internal/chatx/deps.go).

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// --- chatx → main -----------------------------------------------------------
//
// A missing wire is caught by chatx.Configure, which reflects over every field and panics
// (internal/chatx/deps.go). Nothing is silently filled in with a default: a report destination
// or a model choice slipping through unwired is worse than failing to start.
func init() {
	chatx.Configure(chatx.Deps{
		ErrCodeChatAgentUnsupported:   errCodeChatAgentUnsupported,
		ErrCodeChatAssistantNotFound:  errCodeChatAssistantNotFound,
		ErrCodeChatConversationNotFnd: errCodeChatConversationNotFnd,
		ErrCodeChatMessageEmpty:       errCodeChatMessageEmpty,
		ErrCodeChatNothingToCompact:   errCodeChatNothingToCompact,
		ErrCodeChatPromptEmpty:        errCodeChatPromptEmpty,
		ErrCodeChatTitleEmpty:         errCodeChatTitleEmpty,
		ErrCodeLocked:                 errCodeLocked,
		ErrCodeTitleFeatureDisabled:   errCodeTitleFeatureDisabled,
		ErrCodeTitleNoContent:         errCodeTitleNoContent,

		AssistantAgentOrderPref: assistantAgentOrderPref,
		AssistantChatModelPref:  assistantChatModelPref,
		// Order and models for the AI assist one-shot generation, a lineage separate from
		// chat (docs/log/84).
		AiAssistOrderPref: aiAssistOrderPref,
		AiShortModelPref:  aiShortModelPref,
		AiProseModelPref:  aiProseModelPref,
		ChatAutoTurnLimit: chatAutoTurnLimit,
		ChatAutoTurnModel: chatAutoTurnModel,

		FilterVisibleModels: sessionx.FilterVisibleModels,
		VisibleModel:        sessionx.VisibleModel,
		VisibleModelIDs:     sessionx.VisibleModelIDs,

		AssistantDeps:          assistantDeps,
		EnsureBuiltinKnowledge: ensureBuiltinKnowledge,

		CleanSuggestedTitle:      sessionx.CleanSuggestedTitle,
		TitleModel:               sessionx.TitleModel,
		TitleSuggestFooter:       sessionx.TitleSuggestFooter,
		TitleSuggestInstructions: sessionx.TitleSuggestInstructions,
		TitleSuggestPersona:      sessionx.TitleSuggestPersona,
		TitleSuggestTimeout:      sessionx.TitleSuggestTimeout,

		CleanSuggestedReplies:    sessionx.CleanSuggestedReplies,
		ReplyCounterpartChat:     sessionx.ReplyCounterpartChat,
		ReplySuggestEnabled:      sessionx.ReplySuggestEnabled,
		ReplySuggestInstructions: sessionx.ReplySuggestInstructions,
		ReplySuggestLogHeader:    sessionx.ReplySuggestLogHeader,
		ReplySuggestModel:        sessionx.ReplySuggestModel,
		ReplySuggestPersona:      sessionx.ReplySuggestPersona,
		ReplySuggestTimeout:      sessionx.ReplySuggestTimeout,
		ReplySuggestWindow: func(b *strings.Builder, msgs []chatx.ReplyMsg) {
			// chatx cannot name main's replyMsg, so the values are repacked here.
			out := make([]sessionx.ReplyMsg, 0, len(msgs))
			for _, m := range msgs {
				out = append(out, sessionx.ReplyMsg{Role: m.Role, Text: m.Text})
			}
			sessionx.ReplySuggestWindow(b, out)
		},

		AbortResumeHolds: sessionx.AbortResumeHolds,
		ChatTurnUsageTag: func(convID, seedVerb, trigger string) usagex.Tag {
			return usagex.Tag{
				Feature: usagex.FeatureAssistantChat, Trigger: trigger, Ref: convID, Verb: seedVerb,
			}
		},
		CleanTitle:             sessionx.CleanTitle,
		NormalizeKind:          sessionx.NormalizeKind,
		SafeBrowsePath:         safeBrowsePath,
		MaybePushOperatorReply: maybePushOperatorReply,
		// The far side (rate_limit_resume.go) is a var, so what is passed is a reader rather
		// than the value. Receiving it into an alias variable makes a copy, and later writes
		// never arrive.
		RateLimitState: func(name string) (scheduleID, resumeAt string, ok bool) {
			st, found := sessionx.RateLimitStates.Read(name)
			if !found {
				return "", "", false
			}
			return st.ScheduleID, st.ResumeAt, true
		},
	})
}
