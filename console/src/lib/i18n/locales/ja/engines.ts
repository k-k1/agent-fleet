// 日本語 カタログ / ドメイン: engines（ADR 0084 のトップバー・インジケータ）
// キー接頭辞: engine
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const engines = {
  "engine.role_chat": "チャット",
  "engine.role_images": "画像",
  "engine.state_ready": "準備済み",
  "engine.state_running": "稼働中",
  "engine.state_starting": "起動中",
  "engine.state_stopping": "停止処理中",
  "engine.state_stopped": "停止中",
  "engine.state_available": "利用可",
  "engine.lifecycle_external": "外部管理",
  "engine.lifecycle_external_hint": "この配備の外で動いています。起動・停止はこの配備からはできません。",
  "engine.lifecycle_remote": "借用",
  "engine.lifecycle_remote_hint": "別のフリートから借りています。起動・停止はこの配備からはできません。",
  "engine.stops_at": "自動停止 {t}",
  "engine.stops_in": "あと {d}",
  "engine.idle_policy": "誰も使わなくなってから {d} で自動停止します",
  // typical_ms（起動の目安秒数）は会員行に乗らない（決定 3）。分数を偽って書くより、
  // 数を出さない方を選ぶ（決定 4）。
  "engine.cold_hint": "最初の要求でエンジンが起動します。数分かかることがあります。",
  "engine.queue_shared": "待ち {n}",
  "engine.dur_hm": "{h} 時間 {m} 分",
  "engine.dur_m": "{m} 分",
};
