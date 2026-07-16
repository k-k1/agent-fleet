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

  // --- 共通 ---
  "common.close": "閉じる",

  // --- トークン（features/settings/TokensTab.tsx。MCP 用 PAT）---
  "tokens.fetch_failed": "取得に失敗しました",
  "tokens.load_failed": "読み込みに失敗しました",
  "tokens.issue_failed": "発行に失敗: {msg}",
  "tokens.revoke_title": "トークンを失効",
  "tokens.revoke_body": "このトークンを失効します。使用中の接続は次回から 401 になります。",
  "tokens.revoke_confirm": "失効する",
  "tokens.intro":
    "手元の Claude（Claude Code / Desktop）から MCP で Workspace のセッションを操作するためのトークンです。発行者の権限を継承し、スコープはここで選びます。",
  "tokens.issued_head": "トークンを発行しました（この画面を閉じると再表示できません）",
  "tokens.copy_token": "トークンをコピー",
  "tokens.mcp_json_head_1": "Claude Code 用 ",
  "tokens.mcp_json_head_2": "（プロジェクト直下に保存、または既存ファイルへ ",
  "tokens.mcp_json_head_3": " を追記）",
  "tokens.copy_mcp_json": ".mcp.json をコピー",
  "tokens.name": "名前",
  "tokens.name_placeholder": "例: laptop-claude",
  "tokens.scope": "スコープ",
  "tokens.scope_read": "read（閲覧のみ）",
  "tokens.scope_write": "write（セッション駆動）",
  "tokens.scope_admin": "admin:dangerous（強権・管理）",
  "tokens.expiry": "有効期限",
  "tokens.ttl_90": "90 日（既定）",
  "tokens.ttl_30": "30 日",
  "tokens.ttl_365": "365 日",
  "tokens.ttl_never": "無期限",
  "tokens.issuing": "発行中…",
  "tokens.issue": "トークンを発行",
  "tokens.mcp_endpoint_pre": "MCP エンドポイント: ",
  "tokens.mcp_endpoint_mid1": "（Streamable HTTP。クライアントに ",
  "tokens.mcp_endpoint_mid2": " で設定）",
  "tokens.th_expiry": "期限",
  "tokens.th_last_used": "最終使用",
  "tokens.unnamed": "(無名)",
  "tokens.revoked": "失効済",
  "tokens.revoke": "失効",

  // --- 共通（ロード/起動）---
  "common.loading": "読み込み中…",
  "common.starting": "起動中…",

  // --- 接続カード共通（providerCard.tsx / conn 状態）---
  "conn.connected": "接続済み",
  "conn.disconnected": "未接続",
  "conn.connect": "接続",
  "conn.connect_failed": "接続に失敗: {msg}",
  "provider.click_to_copy": "クリックでコピー",
  "provider.disconnect": "切断",
  "provider.step_copy_code": "コードをコピー",
  "provider.step_open_link": "リンクを開いて貼り付け",
  "provider.open_url": "{url} を開く ↗",
  "provider.step_wait_approval": "承認を待つ",

  // --- 運用ツール接続（features/settings/OpsTab.tsx）---
  "ops.ws_required_title": "運用ツールの接続はワークスペース内で実行されます",
  "ops.ws_required_hint": "API キーはコンテナ内の Agent が暗号化保存するため、ワークスペースの起動が必要です。",
  "ops.start_ws": "ワークスペースを起動",
  "ops.intro":
    "インシデント対応・監視運用の連携です。接続すると「SRE アシスタント」がこれらを読み取り専用で参照して壁打ちに使います。接続の変更は次のチャット送信から反映されます（ワークスペースの再起動は不要）。",
  "ops.cat_incident": "インシデント管理",
  "ops.cat_monitoring": "監視 / メトリクス",
  "ops.pd_api_key_set": "API キー設定済み",
  "ops.pd_api_key_placeholder": "PagerDuty API キー",
  "ops.pd_eu_region": "EU リージョン",
  "ops.pd_eu_sub": "PagerDuty に app.eu.pagerduty.com でログインしている場合のみオン（通常はオフのまま）",
  "ops.pd_hint":
    "読み取り専用キーを推奨します（PagerDuty の Integrations > API Access Keys で「Read-only」を選択）。キーはワークスペース内に暗号化保存され、MCP サーバの起動時にだけ渡されます。書き込み操作（ack/resolve など）は有効化しません。",
  "ops.grafana_connected_fallback": "接続設定済み",
  "ops.grafana_url_placeholder": "Grafana URL（https://grafana.example.com）",
  "ops.grafana_token_placeholder": "サービスアカウントトークン",
  "ops.grafana_hint":
    "Viewer 権限のサービスアカウントトークンを推奨します。トークンはワークスペース内に暗号化保存され、MCP サーバの起動時にだけ渡されます（書き込み・管理ツールは無効で起動）。Amazon Managed Grafana の場合は URL に workspace endpoint（g-xxxx.grafana-workspace.リージョン.amazonaws.com）を指定してください（トークンは最長30日で失効するため、失効したら貼り直します）。",
  "ops.cw_profile_select": "プロファイルを選択…",
  "ops.cw_manual_option": "手動入力（自分の ~/.aws のプロファイル）",
  "ops.cw_manual_placeholder": "~/.aws のプロファイル名",
  "ops.cw_region_placeholder": "リージョン（任意）",
  "ops.cw_hint":
    "秘密は保存しません。SSM 接続のプロファイルを選ぶと、その SSO 設定（非秘密）から専用の設定ファイルを生成して使います。ログの検索・アラーム履歴・メトリクス分析など読み取り専用ツールのみです。SSO ログインがまだ（または期限切れ）の場合は、該当の SSM セッションを一度開くか、ターミナルで `AWS_CONFIG_FILE=~/.aws/af-ops/cloudwatch.config aws sso login --profile プロファイル名` を実行してください。",
};
