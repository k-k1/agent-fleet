package main

// internal/usagex（使用量の型・context 運搬・プロバイダ層 1 点記録・台帳）の別名
// （ADR 0067 のエイリアス移送）。呼び出し側（usage_fold / usage_rollup / usage_series /
// usage_dedup / chat 家系 / session 家系 / bridge）は 1 行も触らない。
// 回収はウェーブ境界の別セッションが行う。
//
// 🔥 **`usageMu` だけはポインタで受けている。** 遠側が `var`（sync.Mutex）なので
// `var usageMu = usagex.Mu` と書くと **mutex ごと写されて別物になり**、追記側（AppendRows）と
// 読み側（usage_rollup.go の readUsageDayForRollup）が違う錠を掛けて直列化が無言で消える。
// 呼び出し側の綴り（`usageMu.Lock()`）はポインタでもそのまま通る。
// これ以外の遠側は type / func / const で、写しになるものは無い。
//
// ⚠️ メソッドは公開名に変わった（Go はメソッドをエイリアスできない）:
// `setTotals`→`SetTotals` / `add`→`Add` / `any`→`Any` / `fallbackTotals`→`FallbackTotals` /
// `measuredOr`→`MeasuredOr`。呼び出し側 15 箇所は司令塔承認のロックのもとで書き換えた
// （chat_providers.go 7 / usage_provider_test.go 6 / usage_ledger_test.go 2）。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"

type (
	usageTag      = usagex.Tag
	contextUsage  = usagex.ContextUsage
	usageTokens   = usagex.Tokens
	usageModelRow = usagex.ModelRow
	usageCall     = usagex.Call
	usageRecord   = usagex.Record
)

const (
	usageFeatureAssistantChat    = usagex.FeatureAssistantChat
	usageFeatureAssistantAsk     = usagex.FeatureAssistantAsk
	usageFeatureAssistantAutoTur = usagex.FeatureAssistantAutoTur
	usageFeatureAssistantBridge  = usagex.FeatureAssistantBridge
	usageFeatureCompact          = usagex.FeatureCompact
	usageFeaturePlanUpdate       = usagex.FeaturePlanUpdate
	usageFeatureTitleSession     = usagex.FeatureTitleSession
	usageFeatureTitleChat        = usagex.FeatureTitleChat
	usageFeatureBranchSuggest    = usagex.FeatureBranchSuggest
	usageFeatureSuggestSession   = usagex.FeatureSuggestSession
	usageFeatureSuggestChat      = usagex.FeatureSuggestChat
	usageFeatureSuggestEdit      = usagex.FeatureSuggestEdit
	usageFeatureSession          = usagex.FeatureSession
	usageFeatureUnknown          = usagex.FeatureUnknown

	usageTriggerUser     = usagex.TriggerUser
	usageTriggerAuto     = usagex.TriggerAuto
	usageTriggerManual   = usagex.TriggerManual
	usageTriggerSchedule = usagex.TriggerSchedule
	usageTriggerOperator = usagex.TriggerOperator
	usageTriggerBridge   = usagex.TriggerBridge
	usageTriggerRecovery = usagex.TriggerRecovery

	usageModelReported = usagex.ModelReported
	usageModelRequest  = usagex.ModelRequest
	usageModelUnknown  = usagex.ModelUnknown

	usageMeasuredExact   = usagex.MeasuredExact
	usageMeasuredPartial = usagex.MeasuredPartial
	usageMeasuredNone    = usagex.MeasuredNone
)

// 🔥 ここだけポインタ（上のコメント参照）。値で受けると錠が割れる。
var usageMu = &usagex.Mu

var (
	withUsageTag       = usagex.WithTag
	usageTagOf         = usagex.TagOrUnknown
	contextWindowGuess = usagex.WindowGuess

	recordUsageCall    = usagex.RecordCall
	usageModelFallback = usagex.ModelFallback
	usageSpend         = usagex.Spend
	usageEnabled       = usagex.Enabled

	usageDir             = usagex.Dir
	usageRawDir          = usagex.RawDir
	usageRetentionDays   = usagex.RetentionDays
	appendUsageRows      = usagex.AppendRows
	usageRawDays         = usagex.RawDays
	readUsageDay         = usagex.ReadDay
	readUsageRows        = usagex.ReadRows
	resetUsagePruneClock = usagex.ResetPruneClock
	pruneUsageRawNow     = usagex.PruneRawNow
)
