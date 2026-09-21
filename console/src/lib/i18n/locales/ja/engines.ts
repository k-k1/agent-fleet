// 日本語 カタログ / ドメイン: engines（ADR 0084 のトップバー・インジケータ）
// キー接頭辞: engine
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const engines = {
  "engine.role_chat": "チャット",
  "engine.role_images": "画像",
  // 「準備済み」と「使用中」の違いは warm ではなく、CP が今この行のために要求を握っているか
  // （ADR 0084 決定 6-A）。warm は最後のやり取りから idle window の間ずっと真なので、
  // セッションが動いていることはこの語でしか出ない。
  "engine.state_in_use": "使用中",
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
  // いま VRAM に載っているモデル（ADR 0072 決定 7 / 0090 決定 2）。目録に読める名前があれば
  // それを、無ければ id をそのまま描く。
  "engine.warm_model": "モデル: ",
  "engine.last_used": "最後の利用 {d}前",
  "engine.queue_shared": "待ち {n}",
  "engine.dur_hm": "{h} 時間 {m} 分",
  "engine.dur_m": "{m} 分",
  // 会員自身の lcpp 接続（docs/log/107 追補・ADR 0084 決定 10/11 の「役ごとに1つ」を踏襲）。
  // この接続は Control Plane を経由しない直結なので、CP のエンジン表の語彙（state_running 等）
  // をそのまま使うと「配備のエンジン」の話に読めてしまう——別の語を用意する。
  "engine.state_member_reachable": "接続中",
  "engine.state_member_unreachable": "届いていません",
  "engine.state_member_unknown": "確認中",
  "engine.member_conn_hint": "自分の接続先です。この配備のエンジンではありません。",
  // 直近の観測が見つけたモデル（2026-09-21 追補）。単機の llama-server は要求の model 欄を
  // 読まないので、箱を入れ替えてもここでしか気づけない——prefix として結合する。
  "engine.member_model": "モデル: ",
  "engine.member_model_more": " ほか {n} 件",
};
