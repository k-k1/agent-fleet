package main

// chat_wiring.go — chat 家系（`internal/chatx`）の**配線**だけを持つ。
// ウェーブ C の別名 alias_chat.go は RECLAIM-C で回収し、呼び出し側は chatx を直接呼ぶ。
// ここに残るのは別名ではなく、**chatx → main の逆向き依存 40 本**を関数値で渡す継ぎ目である。
// chatx は main を import できないので、これが唯一の方法（internal/chatx/deps.go）。

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// --- chatx → main -----------------------------------------------------------
//
// 配線漏れは chatx.Configure が **reflect で全フィールドを見て panic** で落とす
// （internal/chatx/deps.go）。**既定値で黙って埋めない** — 報告の宛先やモデル選択が
// 未配線のまま素通りする方が、起動しないことより悪い。
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
		// AI 補助生成（一発生成）の優先順位とモデル。チャットと別系統（docs/log/84）。
		AiAssistOrderPref: aiAssistOrderPref,
		AiShortModelPref:  aiShortModelPref,
		AiProseModelPref:  aiProseModelPref,
		ChatAutoTurnLimit: chatAutoTurnLimit,
		ChatAutoTurnModel: chatAutoTurnModel,

		FilterVisibleModels: filterVisibleModels,
		VisibleModel:        visibleModel,
		VisibleModelIDs:     visibleModelIDs,

		AssistantDeps:          assistantDeps,
		EnsureBuiltinKnowledge: ensureBuiltinKnowledge,

		CleanSuggestedTitle:      cleanSuggestedTitle,
		TitleModel:               titleModel,
		TitleSuggestFooter:       titleSuggestFooter,
		TitleSuggestInstructions: titleSuggestInstructions,
		TitleSuggestPersona:      titleSuggestPersona,
		TitleSuggestTimeout:      titleSuggestTimeout,

		CleanSuggestedReplies:    cleanSuggestedReplies,
		ReplyCounterpartChat:     replyCounterpartChat,
		ReplySuggestEnabled:      replySuggestEnabled,
		ReplySuggestInstructions: replySuggestInstructions,
		ReplySuggestLogHeader:    replySuggestLogHeader,
		ReplySuggestModel:        replySuggestModel,
		ReplySuggestPersona:      replySuggestPersona,
		ReplySuggestTimeout:      replySuggestTimeout,
		ReplySuggestWindow: func(b *strings.Builder, msgs []chatx.ReplyMsg) {
			// chatx は main の replyMsg を名指しできないので、ここで詰め替える。
			out := make([]replyMsg, 0, len(msgs))
			for _, m := range msgs {
				out = append(out, replyMsg{role: m.Role, text: m.Text})
			}
			replySuggestWindow(b, out)
		},

		AbortResumeHolds: abortResumeHolds,
		ChatTurnUsageTag: func(convID, seedVerb, trigger string) usagex.Tag {
			return usagex.Tag{
				Feature: usagex.FeatureAssistantChat, Trigger: trigger, Ref: convID, Verb: seedVerb,
			}
		},
		CleanTitle:             cleanTitle,
		NormalizeKind:          normalizeKind,
		SafeBrowsePath:         safeBrowsePath,
		MaybePushOperatorReply: maybePushOperatorReply,
		// 🔥 遠側（rate_limit_resume.go）が **var** なので、値ではなく**読み口**を渡す。
		// エイリアス変数で受けると写しになり、書き換えが届かない。
		RateLimitState: func(name string) (scheduleID, resumeAt string, ok bool) {
			st, found := rateLimitStates.Read(name)
			if !found {
				return "", "", false
			}
			return st.ScheduleID, st.ResumeAt, true
		},
	})
}
