package agents

import "sync"

// CLI 広告コマンドの共有ストア（docs/log/50 v2）。ACP 系 kind（cursor 等）は CLI 自身が
// session/update の available_commands_update でスキル/コマンド一覧を流してくる —
// これがその kind の唯一の完全なソース（builtin skill ＋ global ＋ project 全部入り、
// cursor 実測 2026-07-28）。driver の onNotify が受信のたびにここへ publish し、
// REST の skills handler が読む。in-memory で十分: 一覧は runtime の spawn/load の
// たびに再到着するし、消えている間（agent 再起動直後など）は FS フォールバックが
// 受け持つ。driver 側から消す義務も無し（多少の stale は空より役に立つ）。

// AdvertisedCommand is one entry of a CLI-advertised command/skill list.
type AdvertisedCommand struct {
	Name        string // 起動名（先頭の "/" は正規化して剥がす）
	Description string
}

var advCommands sync.Map // session name → []AdvertisedCommand

// PublishCommands records the latest CLI-advertised command list for a session.
// Called from driver notify loops — MUST stay cheap (single map store).
func PublishCommands(session string, cmds []AdvertisedCommand) {
	if session == "" {
		return
	}
	advCommands.Store(session, cmds)
}

// AdvertisedCommands returns the last-published list for a session (nil if none).
func AdvertisedCommands(session string) []AdvertisedCommand {
	v, ok := advCommands.Load(session)
	if !ok {
		return nil
	}
	cmds, _ := v.([]AdvertisedCommand)
	return cmds
}
