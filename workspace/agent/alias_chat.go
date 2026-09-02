package main

// alias_chat.go — chat 家系（`internal/chatx`）の移送で開いた口を 1 枚で塞ぐ（ADR 0067 決定 1）。
//
// 家系は 44 ファイル（実装 20 / テスト 24 のうち 19）ごと internal/chatx へ動いた。
// **呼び出し側は 1 行も変えていない**（routes.go / main.go / session_*.go / bridge_* …）ので、
// 動いたことに気付くのはこのファイルだけである。剥がすのは RECLAIM-C の仕事。
//
// 依存は双方向なので、向きごとに扱いが違う:
//
//   - **main → chatx**（呼び出し側が使う名前）… 下の別名。
//   - **chatx → main**（chat が main の各家系へ伸ばしていた手 40 本）… `chatx.Configure` で
//     関数値として配線する。chatx は main を import できないので、これが唯一の方法
//     （詳細は internal/chatx/deps.go）。

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

type (
	chatConversation = chatx.ChatConversation
	chatMessage      = chatx.ChatMessage
	chatProvider     = chatx.ChatProvider
	chatStreamEvent  = chatx.ChatStreamEvent
	claudeChat       = chatx.ClaudeChat
	claudeModelUsage = chatx.ClaudeModelUsage
	claudeUsage      = chatx.ClaudeUsage
	codexUsage       = chatx.CodexUsage
)

const (
	assistantRecommendedModel   = chatx.AssistantRecommendedModel
	bridgeBodyCap               = chatx.BridgeBodyCap
	chatAutoTurnDelayDefault    = chatx.ChatAutoTurnDelayDefault
	chatAutoTurnDelayMax        = chatx.ChatAutoTurnDelayMax
	chatCtxAutoCompactTokens    = chatx.ChatCtxAutoCompactTokens
	chatCtxAutoCompactTokensMin = chatx.ChatCtxAutoCompactTokensMin
	compactReasonAuto           = chatx.CompactReasonAuto
	compactReasonManual         = chatx.CompactReasonManual
	compactReasonRecovery       = chatx.CompactReasonRecovery
	defaultAutoTurns            = chatx.DefaultAutoTurns
	maxAutoResumeAttempts       = chatx.MaxAutoResumeAttempts
	maxAutoTurnLimit            = chatx.MaxAutoTurnLimit
	reportKindAnswerReady       = chatx.ReportKindAnswerReady
	reportKindSelfReport        = chatx.ReportKindSelfReport
	reportReasonTurnAborted     = chatx.ReportReasonTurnAborted
	reportReasonTurnFailed      = chatx.ReportReasonTurnFailed
)

// 🔥 遠側が **var** の 2 つ。どちらも**参照型で、chatx 側で再代入されない**ので、
// 別名で受けても同じ実体を指す（`ChatProviders[k] = p` の差し替えは chatx にも届く）。
// **値型の var をここへ足してはいけない** —— 写しになってスタブが効かなくなる
// （ウェーブ A #295 F-2 / #297 F-1、ウェーブ B の usageMu）。同一実体であることは
// alias_chat_test.go が見る。
var (
	chatProviders        = chatx.ChatProviders
	defaultHeadlessOrder = chatx.DefaultHeadlessOrder
)

var (
	addInstruction                = chatx.AddInstruction
	appendUniqueStr               = chatx.AppendUniqueStr
	autoResumeAttempts            = chatx.AutoResumeAttempts
	backfillConvSlugs             = chatx.BackfillConvSlugs
	chatAutoCompactTokenThreshold = chatx.ChatAutoCompactTokenThreshold
	chatAutoTurnDelay             = chatx.ChatAutoTurnDelay
	chatDefaultTitle              = chatx.ChatDefaultTitle
	chatPersonaFor                = chatx.ChatPersonaFor
	chatProviderFor               = chatx.ChatProviderFor
	chatProviderKind              = chatx.ChatProviderKind
	chatReplySuggestPrompt        = chatx.ChatReplySuggestPrompt
	chatTitleSuggestPrompt        = chatx.ChatTitleSuggestPrompt
	codexOneShotWithRetry         = chatx.CodexOneShotWithRetry
	compactPrompt                 = chatx.CompactPrompt
	compactSummaryPromptFor       = chatx.CompactSummaryPromptFor
	compactTrigger                = chatx.CompactTrigger
	disarmSessionReport           = chatx.DisarmSessionReport
	handleChatAsk                 = chatx.HandleChatAsk
	handleChatCompact             = chatx.HandleChatCompact
	handleChatCreate              = chatx.HandleChatCreate
	handleChatDelete              = chatx.HandleChatDelete
	handleChatGet                 = chatx.HandleChatGet
	handleChatList                = chatx.HandleChatList
	handleChatPatch               = chatx.HandleChatPatch
	handleChatPlanGet             = chatx.HandleChatPlanGet
	handleChatPlanRefresh         = chatx.HandleChatPlanRefresh
	handleChatPlanSet             = chatx.HandleChatPlanSet
	handleChatReport              = chatx.HandleChatReport
	handleChatSend                = chatx.HandleChatSend
	handleChatStop                = chatx.HandleChatStop
	handleChatStream              = chatx.HandleChatStream
	handleChatSuggestReplies      = chatx.HandleChatSuggestReplies
	handleChatSuggestTitle        = chatx.HandleChatSuggestTitle
	handoffPreambleFor            = chatx.HandoffPreambleFor
	headRunes                     = chatx.HeadRunes
	injectCarryover               = chatx.InjectCarryover
	injectHandoff                 = chatx.InjectHandoff
	injectPendingReports          = chatx.InjectPendingReports
	injectPlan                    = chatx.InjectPlan
	isContextOverflowErr          = chatx.IsContextOverflowErr
	kickSessionReport             = chatx.KickSessionReport
	listConvs                     = chatx.ListConvs
	loadConv                      = chatx.LoadConv
	lockConv                      = chatx.LockConv
	markProviderSynced            = chatx.MarkProviderSynced
	markReportsDelivered          = chatx.MarkReportsDelivered
	maybeAutoCompact              = chatx.MaybeAutoCompact
	migrateReportArms             = chatx.MigrateReportArms
	newConvSlug                   = chatx.NewConvSlug
	noteContextOverflow           = chatx.NoteContextOverflow
	noteContextPressure           = chatx.NoteContextPressure
	nowMs                         = chatx.NowMs
	oneShotHeadless               = chatx.OneShotHeadless
	opencodeOneShotConfig         = chatx.OpencodeOneShotConfig
	planContextHeader             = chatx.PlanContextHeader
	planPreambleFor               = chatx.PlanPreambleFor
	planRefreshInstructions       = chatx.PlanRefreshInstructions
	planRefreshPersonaFor         = chatx.PlanRefreshPersonaFor
	planShapeFor                  = chatx.PlanShapeFor
	planTruncatedNote             = chatx.PlanTruncatedNote
	planUpdateInstructionFor      = chatx.PlanUpdateInstructionFor
	preferredHeadlessAgent        = chatx.PreferredHeadlessAgent
	randUUID                      = chatx.RandUUID
	readInstrRows                 = chatx.ReadInstrRows
	recoverForRetry               = chatx.RecoverForRetry
	registerLiveTurn              = chatx.RegisterLiveTurn
	reportArgs                    = chatx.ReportArgs
	reportPromptFor               = chatx.ReportPromptFor
	resetAutoResume               = chatx.ResetAutoResume
	resolveChatModel              = chatx.ResolveChatModel
	resolveConvRef                = chatx.ResolveConvRef
	saveConv                      = chatx.SaveConv
	seedFor                       = chatx.SeedFor
	sessionReportPending          = chatx.SessionReportPending
	setAutoResumeAttempts         = chatx.SetAutoResumeAttempts
	startReportReconciler         = chatx.StartReportReconciler
	syncProviderPrompt            = chatx.SyncProviderPrompt
	turnInFlight                  = chatx.TurnInFlight
	usageModelRows                = chatx.UsageModelRows
	verbPersona                   = chatx.VerbPersona
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

		AssistantAgentOrderPref:   assistantAgentOrderPref,
		AssistantChatModelPref:    assistantChatModelPref,
		AssistantUtilityModelPref: assistantUtilityModelPref,
		ChatAutoTurnLimit:         chatAutoTurnLimit,
		ChatAutoTurnModel:         chatAutoTurnModel,

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
