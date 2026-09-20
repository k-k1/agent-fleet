// 日本語 カタログ / ドメイン: fleetgraph
// キー接頭辞: fgraph
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const fleetgraph = {
  "fgraph.count": "{n} レーン",
  "fgraph.show_archived": "アーカイブ済み",
  "fgraph.show_archived_hint": "アーカイブ済みのレーンを表示/非表示",
  "fgraph.zoom_in": "拡大（表示範囲を狭める）",
  "fgraph.zoom_out": "縮小（表示範囲を広げる）",
  "fgraph.pan_left": "過去へ",
  "fgraph.pan_right": "未来へ",
  "fgraph.reset_window": "直近24時間に戻す",
  "fgraph.empty": "レーンがありません",
  "fgraph.load_failed": "フリート俯瞰図の読み込みに失敗しました",

  // 一覧ペインとの往復（ADR 0096 決定 10 の「入口」）。同じ面の 2 つの見え方なので、
  // 既定は同じペインでの差し替え。
  "fgraph.switch_to_sessions": "一覧",
  "fgraph.switch_to_sessions_hint": "セッション一覧に切り替え（Ctrl/⌘ で別ペイン）",

  // 家系の折り畳み。件数は「隠れている子孫の本数」で、畳んだことが分かるように親の行に出す。
  "fgraph.collapse": "子セッションを畳む",
  "fgraph.expand": "子セッションを開く",
  "fgraph.hidden_children": "＋{n}",
  "fgraph.hidden_children_hint": "子セッション {n} 本を隠しています",

  // 削除済みレーン（ADR 0096 決定 6）。契約が渡すのは素のスラグだけなので、単独で見せずに
  // ここで「削除済み」の語を添える。
  "fgraph.erased_label": "削除済み · {id}",

  // 稼働帯の状態語（LedgerState。types/fleetgraph.ts の SessionState とは別語彙）。
  "fgraph.state.working": "進行中",
  "fgraph.state.compacting": "圧縮中（進行中）",
  "fgraph.state.idle": "入力待ち",
  "fgraph.state.question": "質問あり",
  "fgraph.state.plan": "プランあり",
  "fgraph.state.permission": "許可待ち",
  "fgraph.state.blocked": "選択待ち",
  "fgraph.state.auth": "認証切れ",
  "fgraph.state.limited": "利用上限待ち",
  "fgraph.state.spend_limit": "残高上限",
  "fgraph.state.failed": "失敗",
  "fgraph.state.aborted": "中断",
  "fgraph.state.unknown": "不明な状態",

  // 稼働帯の「不明」は 2 つの源を持つ（ADR 0096 決定 3・型のコメント）。tooltip の文言は
  // 必ず分ける——観測済みの区間に「観測なし」と書かない。
  "fgraph.seg_not_observed": "この区間は誰も観測していません",
  "fgraph.seg_unrecognized": "観測済み・未知の綴り: {raw}",

  // 欠けた親（決定 9）。id は出さない——契約がここで生の綴りを出す許可を与えていない
  // （削除済みレーンとは違い、これは単に「この窓に描かれていない」だけのことも多い）。
  "fgraph.parent_missing": "親レーン {id} はこの図に描かれていません",

  // レーンの線種（ADR 0096 決定 12）。
  "fgraph.presence_live": "稼働中",
  "fgraph.presence_stopped": "停止中 — 再開できます",
  "fgraph.presence_archived": "アーカイブ済み — 復元できます",
  "fgraph.presence_gone": "もう居ません",
  // 同じ 4 語のチップ版。ラベル列に入る長さにする（稼働中のセッションは stateInfo() の語が出る
  // ので、ここに来るのは一覧に載らないレーン＝アーカイブ済みと削除済みだけ）。
  "fgraph.presence_short_live": "稼働中",
  "fgraph.presence_short_stopped": "停止中",
  "fgraph.presence_short_archived": "アーカイブ",
  "fgraph.presence_short_gone": "居ません",

  // ×（run の終端）の tooltip。「終わった時刻」ではなく「初めて観測された時刻」。
  "fgraph.death_at": "停止が確認された時刻: {time}（実際に止まった時刻より遅れることがあります）",
  "fgraph.death_reason": "理由: {reason}",
  "fgraph.revive_at": "{time} に再開されました",
  // 最後の run が死を書かれないまま切れた場合（ADR 0096 決定 12・LaneRun.cut）。
  "fgraph.run_cut": "最後に観測できたのは {time}。その先は不明です",

  // 遡及の 3 段減衰の境界（CoverageMark）。
  "fgraph.mark_activity_start": "ここより過去の稼働帯・矢印は保存されていません",
  "fgraph.mark_lineage_start": "ここより過去の系譜は保存されていません",
  "fgraph.mark_backfill_start": "ここより過去は起票時に Meta から書き起こした概形だけです",

  // 矢印（ADR 0096 決定 4・8-2）。
  "fgraph.arrow_spawn": "起動",
  "fgraph.arrow_fork": "フォーク",
  "fgraph.arrow_handoff": "引き継ぎ",
  "fgraph.arrow_instruct": "指示",
  "fgraph.arrow_report": "報告",
  "fgraph.arrow_peer": "セッション間メッセージ · {intent}",

  // レーンにならない発信元（ADR 0096 決定 8-2）。
  "fgraph.actor_conv": "会話",
  "fgraph.actor_user": "利用者",
  "fgraph.actor_schedule": "定時実行",
  "fgraph.actor_bridge_discord": "Discord 連携",
  "fgraph.actor_bridge_slack": "Slack 連携",
  "fgraph.actor_agent": "自動再開",
};
