// 日本語カタログ（ソース・正本）。キーは他ロケールの網羅チェックの基準になる（en.ts は
// Record<keyof typeof ja, string> で全キー必須）。値の {name} 等は t() が vars で置換する。
// docs/28-i18n.md。P0 は中央集約 sink 3 種（errText / notifications wording / 設定ラベル定数）分のみ。
// 以降のフェーズで features 単位に追記していく。
export const ja = {
  // --- 共通 / 設定ラベル ---
  "common.just_now": "たった今",
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

  // --- 共通トグル / フォント名（controls.tsx / lib/settings.ts のフォント配列）---
  "common.on": "オン",
  "common.off": "オフ",
  "font.sys_mono": "システム等幅",
  "font.sys": "システム",
  "font.serif": "セリフ",
  "font.mincho": "明朝",
  "font.gothic": "ゴシック",

  // --- アイコンセット（lib/settings.ts ICON_SETS）---
  "iconset.vscode": "VS Code Icons（カラー）",
  "iconset.material": "Material（カラー）",
  "iconset.devicon": "Devicon（カラー）",
  "iconset.seti": "Seti（単色・タイプ別着色）",

  // --- サーフェス色（lib/settings.ts SURFACE_COLORS）---
  "surface_color.default": "デフォルト",
  "surface_color.slate": "スレート",
  "surface_color.blue": "ブルー",
  "surface_color.green": "グリーン",
  "surface_color.purple": "パープル",
  "surface_color.warm": "ウォーム",

  // --- サーフェス対象（lib/settings.ts SURFACE_TARGETS。short=外観ポップ, long=設定行）---
  "surface.topbar.short": "上部バー",
  "surface.topbar.long": "上部バーの背景",
  "surface.leftpane.short": "左ペイン",
  "surface.leftpane.long": "左ペインの背景",
  "surface.viewer.short": "ビュアー",
  "surface.viewer.long": "ファイルビュアーの背景",
  "surface.session.short": "セッション",
  "surface.session.long": "セッションの背景",
  "surface.assistant.short": "アシスタント",
  "surface.assistant.long": "アシスタントの背景",

  // --- ミラー送信キー（lib/settings.ts MIRROR_SEND_MODES）---
  "mirror_send.mod_enter": "Ctrl+Enter で送信",
  "mirror_send.enter": "Enter で送信",

  // --- 表示設定（features/settings/DisplayTab.tsx）---
  "display.color_theme": "カラーテーマ",
  "display.theme": "テーマ",
  "display.session_theme": "セッションのテーマ",
  "display.assistant_theme": "アシスタントのテーマ",
  "display.region_theme_note":
    "セッション（ミラー）とアシスタントチャットは、アプリ本体とは別のテーマ（ダーク／ライト）で表示できます（「アプリに合わせる」で本体に追従）。背景色も下でそれぞれ指定できます。",
  "display.terminal": "ターミナル",
  "display.font": "フォント",
  "display.font_size": "文字サイズ",
  "display.file_viewer": "ファイルビュアー",
  "display.tab_width": "タブ幅",
  "display.line_numbers": "行番号",
  "display.wrap": "折り返し",
  "display.minimap": "ミニマップ",
  "display.session_mirror": "セッション（Markdownミラー）",
  "display.send_key": "送信キー",
  "display.send_note_enter": "Enter で送信、Shift+Enter で改行。",
  "display.send_note_mod": "Ctrl+Enter（⌘+Enter）で送信、Enter で改行。スマホ向け。",
  "display.reader_view": "朗読ビュー",
  "display.file_icons": "ファイルアイコン",
  "display.icon_set": "アイコンセット",
  "display.preview": "プレビュー",
  "display.smaller": "小さく",
  "display.larger": "大きく",
};
