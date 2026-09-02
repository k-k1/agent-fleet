package browserx

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// randUUID returns a random RFC-4122 v4 UUID. newBrowserHandoffID takes 10 hex
// digits out of it for the ledger row id.
//
// ⚠️ package main の chat_store.go にある同名関数と同じ実装の**写し**である。移送
// (ADR 0067 WP-A3) の時点で本体は所有外のファイルに在り、動かせなかった。関数変数で
// package main から借りる形も採らなかった: それだと browserx 単体のテストバイナリで
// nil になり、id を作るだけのテストが「結線されていない」という理由で落ちる——乱数
// 8 行のためにテストの実行条件を分ける価値がない。
// 重複はエイリアス回収のウェーブで畳むこと（共通の util パッケージへ 1 本化する）。
func randUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
