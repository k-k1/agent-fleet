package main

// System notices (role=="notice") are text WE generate — not model output — so they
// belong to the Console's display layer, not to the stored thread's language. Until
// ADR 0033 they were written as Japanese prose and rendered verbatim, which left an
// English Console showing Japanese cards. Now each notice carries a catalog key plus
// its arguments, and the Console renders `t(NoticeKey, NoticeArgs)` — so the text
// follows settings.locale and re-renders when the user switches language.
//
// Content still holds the same sentence in the source language (ja, ADR 0016 §4) as a
// fallback: records written before this change have no key, and a Console that does
// not know a key falls back to it rather than showing an empty card. Nothing else
// consumes it — notices are never replayed into the provider context
// (syncProviderPrompt), never bridged (bridge.eventKeyFor), and are skipped by the
// title / reply-suggestion prompts.
const (
	noticeKeyCtxPressure     = "chat.notice.ctx_pressure"
	noticeKeyCtxOverflow     = "chat.notice.ctx_overflow"
	noticeKeyAutoPaused      = "chat.notice.auto_paused"
	noticeKeyCompactManual   = "chat.notice.compact_manual"
	noticeKeyCompactAuto     = "chat.notice.compact_auto"
	noticeKeyCompactRecovery = "chat.notice.compact_recovery"
	noticeKeyAgentSwitched   = "chat.notice.agent_switched"
	noticeKeyPlanUpdated     = "chat.notice.plan_updated" // 作業計画が動いた（docs/log/33 第5段）
)

// newNotice builds a notice message from its catalog key, arguments and source-language
// fallback text.
func newNotice(key string, args map[string]string, fallback string) chatMessage {
	return chatMessage{
		Role:       "notice",
		Content:    fallback,
		NoticeKey:  key,
		NoticeArgs: args,
		TS:         nowMs(),
	}
}
