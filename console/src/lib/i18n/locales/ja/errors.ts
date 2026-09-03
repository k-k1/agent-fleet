// 日本語 カタログ / ドメイン: errors
// キー接頭辞: err
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const errors = {
  // --- API エラー（core/api/client.ts の ERR_TEXT ＋ インライン fallback）---
  // Go 側の安定コード（control-plane/errcodes.go, workspace/agent/errcodes.go）と対。
  "err.ip_not_allowed":
    "このテナントは管理者が許可したネットワークからしか使えません。現在の接続元は許可されていません。",
  "err.ssm_search_forbidden":
    "AWS上のインスタンスを検索する権限がありません。AWS管理者に ssm:DescribeInstanceInformation の付与を依頼してください。",
  "err.quota_sessions":
    "同時に稼働できるセッション数の上限に達しています。稼働中のセッションをどれか停止してから作成してください。",
  "err.sessions_running":
    "この作業コピーでは稼働中のセッションがあります。切り替えると足元の作業ツリーが入れ替わり壊れるため、ここでは切り替えできません。ブランチは別の作業コピーとして開いてください。",
  "err.branch_in_use":
    "このブランチは別の作業コピーが既にチェックアウトしています。git は同じブランチを2つの作業コピーに置けません。そちらの作業コピーを開くか、別のブランチを選んでください。",
  "err.sessions_running_delete":
    "この作業コピーでは稼働中のセッションがあります。削除すると足元の作業ディレクトリが消えて壊れるため、先にそれらのセッションを停止してください。",
  "err.worktree_dirty":
    "この worktree には未コミット/未pushの変更があります。強制削除すると失われます。",
  "err.has_worktrees":
    "この作業コピーには派生した worktree がぶら下がっています。先に worktree 側を削除してください。",
  "err.locked":
    "削除ロックがかかっています。先にロックを解除してから削除してください。",
  "err.locked_sessions":
    "この作業コピーには削除ロック中のセッションがあります。削除すると再開できなくなるため、先にそのセッションのロックを解除してください。",
  "err.worktree_remove_failed": "worktree の削除に失敗しました。",
  "err.question_pending":
    "エージェントが質問への回答を待っています。質問カードから回答してから送信してください。",
  "err.plan_pending":
    "エージェントがプランの承認待ちです。プランカードから承認・却下してから送信してください（テキストは承認ダイアログに飲まれ、そのまま承認になります）。",
  "err.permission_pending":
    "エージェントが許可の判断待ちです。許可カードから許可・拒否してから送信してください（テキストは許可メニューに飲まれ、そのまま許可になります）。",
  "err.interaction_pending":
    "エージェントが対話中のプロンプトを表示しています。カードから回答してから送信してください。",
  "err.auth_expired":
    "このワークスペースの Claude のログインが期限切れです。設定 > エージェント から再認証してから送信してください（この状態で送るとターミナルは文字を受け取りますが、ターンは一つも始まりません）。",
  "err.not_running": "セッションが停止しています。再開してから送信してください。",
  // 起動途中（コンテナは走っているが Agent がまだ応答しない）に、Agent を要する操作が
  // 来たとき。失敗ではなく「まだ」なので、赤くしても再試行を促す言い方にする。
  "err.workspace_starting": "ワークスペースを起動中です。準備ができてからもう一度お試しください。",
  "err.driver_unavailable": "このエージェントではマネージド実行を利用できません。",
  "err.runtime_failed": "エージェントを起動できませんでした。しばらく待ってから再試行してください。",
  "err.send_failed": "送信に失敗しました",
  "err.network": "通信エラー",
  "err.unknown": "不明なエラーが発生しました",
  // テナント毎のログイン（docs/log/61 §61.9）。provider_required は専用モーダルが
  // 再サインイン導線を出すので、この文言はモーダル外で出た場合の保険。
  "err.provider_required": "このテナントには別のサインイン方法が必要です。サインインし直してください。",
  "err.not_provisioned": "所属するテナントがありません。管理者に追加を依頼してください。",
  "err.domain_not_allowed": "このテナントに招待できるメールアドレスのドメインではありません。",
  "err.email_required": "このテナントはドメインで招待を制限しています。メールアドレスで招待してください。",
  "err.auto_join_conflict": "その自動参加ドメインは既に別のテナントが使っています。",
  "err.unknown_provider": "そのサインイン方法はこのデプロイで有効になっていません。",
  "err.self_removal": "自分の最後のメンバーシップは外せません（戻る道が無くなるため）。他の管理者に依頼してください。",
  "err.bad_share": "共有リクエストが不正です。",
  "err.member_not_found": "指定した相手は同じテナントのメンバーではありません。検索候補から選び直してください。",
  "err.share_self": "自分自身を共有先に指定することはできません。",
  "err.workspace_not_running": "所有者のワークスペースが起動している必要があります。",
  "err.share_target_not_found": "共有対象が見つかりませんでした。",
  "err.owner_session_archived": "所有者がこのセッションをアーカイブしました。共有先の一覧からは外れています（所有者が復元すればまた表示されます）。",
  "err.settings_change_failed": "設定を変更できませんでした",
  "err.bad_path": "ファイルパスが不正です",
  "err.symlink_not_allowed": "シンボリックリンク経由のファイルは操作できません",
  "err.bad_request": "リクエストの形式が不正です",
  "err.unsupported_media_type": "JSON形式のリクエストだけを受け付けます",
  "err.denied": "このファイルは操作できません",
  "err.not_file": "対象の通常ファイルが見つかりません",
  "err.revision_conflict": "ファイルが読み込み後に変更されています",
  "err.too_large": "ファイルまたはリクエストが大きすぎます",
  "err.binary_not_supported": "バイナリまたは未対応の文字コードのファイルは編集できません",
  "err.unsupported_newline": "CRLFまたはCR改行のファイルはまだ編集できません",
  "err.read_failed": "ファイルの読み込みに失敗しました",
  "err.write_failed": "ファイルの保存に失敗しました",
  "err.write_state_unknown": "保存内容は反映されていますが、永続化の成否を確認できません",
  // docs/log/28 P3: workspace/agent ハンドラの安定コード（errcodes.go と対）。
  "err.chat_assistant_not_found": "アシスタントが見つかりません",
  "err.chat_agent_unsupported": "未対応のエージェントです",
  "err.chat_prompt_empty": "プロンプトが空です",
  "err.chat_title_empty": "表示名が空です",
  "err.chat_message_empty": "メッセージが空です",
  "err.chat_conversation_not_found": "会話が見つかりません",
  "err.chat_nothing_to_compact": "まだ圧縮できるコンテキストがありません（最初の応答の後に使えます）",
  "err.conn_api_key_required": "API キーを入力してください",
  "err.conn_grafana_fields_required": "Grafana の URL とサービスアカウントトークンを入力してください",
  "err.conn_jira_fields_required": "Jira のアカウントメールアドレスと API トークンを入力してください",
  "err.conn_url_scheme": "URL は http(s):// で始めてください",
  "err.conn_aws_profile_required": "AWS プロファイルを指定してください",
  "err.conn_sso_region_missing": "SSO リージョンがありません（SSM プロファイルの設定を確認してください）",
  "err.conn_discord_token_required": "Discord Bot トークンを入力してください",
  "err.conn_discord_destination_required": "宛先（チャンネル ID か ユーザー ID）をどちらか一方だけ入力してください",
  "err.conn_discord_destination_invalid": "宛先は数字の Discord ID を入力してください（開発者モードで「IDをコピー」）",
  "err.conn_discord_token_invalid": "Discord がトークンを拒否しました（Bot トークンを確認してください）",
  "err.conn_slack_token_required": "Slack Bot トークン（xoxb-）を入力してください",
  "err.conn_slack_destination_required": "チャンネル ID かユーザー ID を入力してください（受信には対象ユーザー ID も必要です）",
  "err.conn_slack_destination_invalid": "宛先は Slack の ID を入力してください（チャンネル C…、ユーザー U…）",
  "err.conn_slack_token_invalid": "Slack がトークンを拒否しました（Bot / App-level トークンを確認してください）",
  "err.conn_slack_app_token_required": "返信の受信には App-level トークン（xapp-）が必要です",
  // MCP レジストリ（docs/log/48 / workspace/agent/mcp_servers.go + internal/mcpreg/def.go）
  "err.mcp_not_found": "MCP サーバーが見つかりません",
  "err.mcp_read_only": "このサーバーは編集できません（無効化のみ可能です）",
  "err.mcp_name_taken": "同じ名前のサーバーが既に登録されています",
  "err.mcp_invalid": "MCP サーバーの定義が不正です",
  "err.mcp_name_invalid": "名前は英数字・ハイフン・アンダースコア 48 文字以内で、先頭は英数字にしてください",
  "err.mcp_name_reserved": "その名前は Agent Fleet が使用する予約名です",
  "err.mcp_transport_unsupported": "未対応の接続方式です（stdio / リモート HTTP のみ）",
  "err.mcp_command_required": "stdio サーバーにはコマンドが必要です",
  "err.mcp_stdio_no_url": "stdio サーバーに URL / ヘッダは指定できません",
  "err.mcp_tenant_stdio": "テナント配布の MCP サーバーは stdio を使えません（リモートのみ）",
  "err.mcp_url_required": "リモートサーバーには URL が必要です",
  "err.mcp_url_invalid": "URL を解釈できません",
  "err.mcp_url_scheme": "URL は http / https で指定してください",
  "err.mcp_url_host": "URL にホストがありません",
  "err.mcp_url_credentials": "URL に資格情報を埋め込まないでください（ヘッダを使ってください）",
  "err.mcp_http_no_command": "リモートサーバーにコマンド / 引数 / 環境変数は指定できません",
  "err.mcp_env_name_invalid": "環境変数名が不正です",
  "err.mcp_header_name_invalid": "ヘッダ名が不正です",
  "err.mcp_header_value_invalid": "ヘッダ値に改行は使えません",
  "err.mcp_kind_unknown": "未知のエージェント種別です",
  "err.mcp_timeout_range": "タイムアウトは 1000〜120000 ミリ秒で指定してください",
  "err.mcp_headers_unreadable": "保存済みのヘッダを復号できません。すべてのヘッダ値を再入力してください",
  // egress 許可リストの申請（docs/log/48 §9 / control-plane/egress_member.go）
  "err.egress_entry_invalid":
    "許可リストにはホスト名か .suffix.example.com の形式を指定してください（スキーム・ポート・パスは使えません）",
  "err.egress_entry_too_broad": "TLD 全体（.com など）は申請できません。ドメイン単位で指定してください",
  "err.egress_too_many_proposals": "承認待ちの申請が多すぎます。管理者に処理を依頼してください",
  "err.mcp_tenant_bridge_off": "テナント配布はこの環境では利用できません（CP の公開URL/トークンが未設定）",
  "err.mcp_tenant_fetch_failed": "テナント配布を取得できませんでした",
  "err.assistant_not_found": "アシスタントが見つかりません",
  "err.assistant_builtin_readonly_edit": "ビルトインは編集できません",
  "err.assistant_builtin_readonly_delete": "ビルトインは削除できません",
  "err.paste_too_large": "ファイルが大きすぎます",
  "err.paste_unsupported_kind": "このセッション種別には画像を渡せません",
  "err.paste_unsupported_agent": "画像を渡せるのは claude / codex のアシスタントのみです",
  "err.fork_unsupported_kind": "このセッション種別は分岐に対応していません",
  "err.fork_missing_dir": "作業フォルダが存在しないため分岐できません",
  "err.fork_at_unsupported": "このセッションは発言時点からの分岐に対応していません（managed のセッションでのみ使えます）",
  "err.fork_bad_anchor": "この分岐点は使えません。チャットを読み込み直してからやり直してください",
  "err.title_feature_disabled": "AI 提案が無効です（表示設定のタイトル自動提案をオンに）",
  "err.title_no_content": "会話がまだ足りません（数往復してから試してください）",
  "err.memory_bad_request": "リクエストの形式が不正です",
  "err.memory_bad_rev": "指定した時点のスナップショットが見つかりません",
  "err.memory_bad_path": "メモリの管理対象外のパスです",
  "err.memory_no_snapshots": "スナップショットがまだありません",
  "err.memory_snapshot_failed": "スナップショットの取得に失敗しました",
  "err.memory_diff_failed": "差分の取得に失敗しました",
  "err.memory_bad_scope": "戻す範囲の指定が不正です",
  "err.memory_restore_failed": "巻き戻しに失敗しました",
  "err.memory_export_failed": "書き出しに失敗しました",
  "err.memory_import_failed": "取り込みに失敗しました",
  "err.memory_bad_import": "取り込めない形式のファイルです（別環境の書き出しファイルを選んでください）",
  "err.memory_secret_detected": "書き出す内容に秘密情報らしき記述があります",
  "err.memory_too_large": "ファイルが大きすぎます",
  "err.tenant_idp_link_claim_required":
    "このデプロイには、同じ発行元のサインイン方法がすでにあります。この発行元はアプリ登録ごとに同じ人へ違う subject を割り当てるため、" +
    "「同一アカウントの見分け方」を指定しないと、すでにこのデプロイを使っている人が全員ログインできなくなります（メールアドレス重複として拒否されます）。",
};
