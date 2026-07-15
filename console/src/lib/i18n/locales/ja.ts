// 日本語カタログ（ソース・正本）。キーは他ロケールの網羅チェックの基準になる（en.ts は
// Record<keyof typeof ja, string> で全キー必須）。値の {name} 等は t() が vars で置換する。
// docs/28-i18n.md。P0 は中央集約 sink 3 種（errText / notifications wording / 設定ラベル定数）分のみ。
// 以降のフェーズで features 単位に追記していく。
export const ja = {
  // --- 共通 / 設定ラベル ---
  "settings.language": "言語",
  "theme.dark": "ダーク",
  "theme.light": "ライト",
  "region_theme.inherit": "アプリに合わせる",

  // --- API エラー（core/api/client.ts の ERR_TEXT ＋ インライン fallback）---
  // Go 側の安定コード（control-plane/errcodes.go, workspace/agent/errcodes.go）と対。
  "err.ssm_search_forbidden":
    "AWS上のインスタンスを検索する権限がありません。AWS管理者に ssm:DescribeInstanceInformation の付与を依頼してください。",
  "err.quota_sessions":
    "同時に稼働できるセッション数の上限に達しています。稼働中のセッションをどれか停止してから作成してください。",
  "err.sessions_running":
    "この作業コピーでは稼働中のセッションがあります。切り替えると足元の作業ツリーが入れ替わり壊れるため、ここでは切り替えできません。ブランチは別の作業コピーとして開いてください。",
  "err.sessions_running_delete":
    "この作業コピーでは稼働中のセッションがあります。削除すると足元の作業ディレクトリが消えて壊れるため、先にそれらのセッションを停止してください。",
  "err.worktree_dirty":
    "この worktree には未コミット/未pushの変更があります。強制削除すると失われます。",
  "err.has_worktrees":
    "この作業コピーには派生した worktree がぶら下がっています。先に worktree 側を削除してください。",
  "err.worktree_remove_failed": "worktree の削除に失敗しました。",
  "err.question_pending":
    "エージェントが質問への回答を待っています。質問カードから回答してから送信してください。",
  "err.not_running": "セッションが停止しています。再開してから送信してください。",
  "err.driver_unavailable": "このエージェント種別ではまだ managed ドライバを利用できません。",
  "err.runtime_failed": "エージェントの共有 runtime に接続できませんでした。",
  "err.send_failed": "送信に失敗しました",
  "err.network": "通信エラー",
  "err.settings_change_failed": "設定を変更できませんでした",

  // --- 通知（features/notifications/store.ts の wording。speech は読み上げ用の別形）---
  "notif.default_name": "セッション",
  "notif.answer_ready.title": "回答が返ってきました",
  "notif.answer_ready.speech": "{name} の回答が返りました。",
  "notif.question.title": "質問が来ています",
  "notif.question.speech": "{name} が確認を求めています。",
  "notif.plan_approval.title": "プランの承認待ちです",
  "notif.plan_approval.speech": "{name} がプランの承認を求めています。",
  "notif.permission_request.title": "権限の確認が必要です",
  "notif.permission_request.speech": "{name} が権限の確認を求めています。",
  "notif.usage_reset.title": "{source} の制限がリセットされました",
  "notif.usage_reset.body": "{window}がリセットされました。",
  "notif.usage_reset.speech": "{source}の{window}がリセットされました。",
  "notif.window.5h": "5時間枠",
  "notif.window.week": "週間枠",
};
