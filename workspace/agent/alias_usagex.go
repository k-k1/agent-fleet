package main

// internal/usagex へ移した使用量の下位層の別名（ADR 0067 のエイリアス移送）。
// 呼び出し側（chat 家系・session 家系・usage_* の集計）は 1 行も触らない。
// 回収はウェーブ境界の別セッションが行う。
//
// 遠側は type / func のみ（`var x = pkg.Y` が写しになる罠は遠側が var のときだけ）。
//
// フェーズ2（usage_ledger.go が PREP の手を離れてから）で移すもの:
// usageTokens / usageModelRow / usageCall とそのメソッド、recordUsageCall、
// usageFeature* / usageMeasured* / usageModel* の定数。usageCall のメソッドは
// 公開名に変えるとメソッド呼び出しが所有外のファイル（chat_providers.go 7 箇所・
// usage_provider_test.go 6 箇所・usage_ledger_test.go 2 箇所）で壊れるので、
// 台帳本体と同時に動かす。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"

type (
	usageTag     = usagex.Tag
	contextUsage = usagex.ContextUsage
)

var (
	withUsageTag       = usagex.WithTag
	contextWindowGuess = usagex.WindowGuess
)
