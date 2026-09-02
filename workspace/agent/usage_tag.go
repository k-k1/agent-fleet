package main

// 使用量タグのうち、**chat 家系に触るものだけ**がここに残る。
// タグの型・context への出し入れ・プロバイダ層 1 点記録・台帳は internal/usagex を直接呼ぶ
// （ウェーブ B の別名 alias_usagex.go は RECLAIM-B で回収済み）。

import "github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"

// chatTurnUsageTag はアシスタントチャット1ターン分のタグ。SeedVerb（Files 由来の翻訳/
// 要約スレッド）は feature を増やさず verb のサブ次元として割る — 独立カテゴリとして
// 見たいが、機能の enum を増やすと Console の色・i18n・フィルタ全部に波及する（docs/log/46 §1-a）。
//
// usagex へ移せないのは *chatConversation を取るため（chat 家系は AG-CHAT 所有・ウェーブ C）。
func chatTurnUsageTag(c *chatConversation, trigger string) usagex.Tag {
	return usagex.Tag{
		Feature: usagex.FeatureAssistantChat, Trigger: trigger, Ref: c.ID, Verb: c.SeedVerb,
	}
}
