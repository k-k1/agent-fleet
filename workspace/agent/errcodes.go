package main

// Console 側で和文ローカライズされるエラーコード（console/src/core/api/client.ts の
// ERR_TEXT と対、docs/log/23 P0-3）。ここの文字列を変えると Console の文言解決が落ちて
// developer メッセージへフォールバックする — 変更は必ず両側同時に。CP 側の対は
// control-plane/errcodes.go（quota_sessions）。
const (
	// File editor API (docs/log/44 Phase 1). Keep these synchronized with the CP
	// proxy constants and Console err.<code> catalogs.
	errCodeFSBadPath            = "bad_path"
	errCodeFSSymlinkNotAllowed  = "symlink_not_allowed"
	errCodeFSBadRequest         = "bad_request"
	errCodeFSUnsupportedMedia   = "unsupported_media_type"
	errCodeFSDenied             = "denied"
	errCodeFSNotFile            = "not_file"
	errCodeFSRevisionConflict   = "revision_conflict"
	errCodeFSTooLarge           = "too_large"
	errCodeFSBinaryNotSupported = "binary_not_supported"
	errCodeFSUnsupportedNewline = "unsupported_newline"
	errCodeFSReadFailed         = "read_failed"
	errCodeFSWriteFailed        = "write_failed"
	errCodeFSWriteStateUnknown  = "write_state_unknown"
	// Sent only when the client already abandoned the request (timeout /
	// disconnect observed at mutex acquisition), so no live client ever renders
	// it — deliberately absent from the Console i18n catalogs.
	errCodeFSWriteCancelled = "write_cancelled"

	errCodeSessionsRunning       = "sessions_running"
	errCodeSessionsRunningDelete = "sessions_running_delete"
	// A branch git can only hold in one working copy at a time was requested for a
	// second one (checkout or worktree launch). The payload carries `worktree` — the
	// occupying copy's folder — so the Console can offer to open it; the localized
	// err.branch_in_use text is the fallback for callers that only toast errText.
	errCodeBranchInUse          = "branch_in_use"
	errCodeWorktreeDirty        = "worktree_dirty"
	errCodeWorktreeRemoveFailed = "worktree_remove_failed"
	errCodeHasWorktrees         = "has_worktrees"
	// 削除ロック（docs/log/45）: 対象そのものがロックされている / ロックされた
	// セッションを巻き添えにする削除を拒んだとき。
	errCodeLocked         = "locked"
	errCodeLockedSessions = "locked_sessions"
)

// docs/log/28 P3: 以前は各ハンドラに和文でハードコードされていたユーザー向けエラーを
// 安定コード化したもの。backend の message は言語非依存の英語 developer fallback、
// 表示文言は Console の "err.<code>" カタログ（console/src/lib/i18n/locales）が解決する。
// コードは意味ごとに一意（旧 "not_found"/"empty" 等の使い回しを解消）— 追加・改名時は
// 必ず i18n カタログ両言語（ja.ts / en.ts）にも "err.<code>" を足す。
const (
	// アシスタントチャット（chat_handlers.go）
	errCodeChatAssistantNotFound  = "chat_assistant_not_found"
	errCodeChatAgentUnsupported   = "chat_agent_unsupported"
	errCodeChatPromptEmpty        = "chat_prompt_empty"
	errCodeChatTitleEmpty         = "chat_title_empty"
	errCodeChatMessageEmpty       = "chat_message_empty"
	errCodeChatConversationNotFnd = "chat_conversation_not_found"
	errCodeChatNothingToCompact   = "chat_nothing_to_compact"

	// 接続設定（connections.go）
	errCodeConnAPIKeyRequired     = "conn_api_key_required"
	errCodeConnGrafanaFields      = "conn_grafana_fields_required"
	errCodeConnURLScheme          = "conn_url_scheme"
	errCodeConnAWSProfileRequired = "conn_aws_profile_required"
	errCodeConnSSORegionMissing   = "conn_sso_region_missing"

	// Jira 接続（connections_jira.go, docs/log/80 P1）
	errCodeConnJiraFields   = "conn_jira_fields_required"
	errCodeConnJiraRejected = "conn_jira_rejected"

	// チャットブリッジ接続（connections.go, docs/log/37 P1）
	errCodeConnDiscordTokenRequired = "conn_discord_token_required"
	errCodeConnDiscordDestRequired  = "conn_discord_destination_required"
	errCodeConnDiscordDestInvalid   = "conn_discord_destination_invalid"
	errCodeConnDiscordTokenInvalid  = "conn_discord_token_invalid"

	// Slack チャットブリッジ接続（connections_slack.go, docs/log/37 Slack 追随）
	errCodeConnSlackTokenRequired    = "conn_slack_token_required"
	errCodeConnSlackDestRequired     = "conn_slack_destination_required"
	errCodeConnSlackDestInvalid      = "conn_slack_destination_invalid"
	errCodeConnSlackTokenInvalid     = "conn_slack_token_invalid"
	errCodeConnSlackAppTokenRequired = "conn_slack_app_token_required"

	// アシスタント CRUD（assistants.go）
	errCodeAssistantNotFound      = "assistant_not_found"
	errCodeAssistantBuiltinEdit   = "assistant_builtin_readonly_edit"
	errCodeAssistantBuiltinDelete = "assistant_builtin_readonly_delete"

	// 画像貼り付け（session_paste.go）
	errCodePasteTooLarge         = "paste_too_large"
	errCodePasteUnsupportedKind  = "paste_unsupported_kind"
	errCodePasteUnsupportedAgent = "paste_unsupported_agent"

	// セッション分岐（session_handlers.go）
	errCodeForkUnsupportedKind = "fork_unsupported_kind"
	errCodeForkMissingDir      = "fork_missing_dir"
	// 発言時点からの分岐（docs/log/55）。会話まるごとの分岐へ黙って倒さないための境界で、
	// 2 つに割ってあるのは意味が違うため: unsupported は「この種別/起動方式では地点分岐
	// という機能が無い」（＝導線を出すべきでなかった）、bad_anchor は「機能はあるが、
	// この分岐点が使えない」（会話に無い・サブエージェント発言・ミラーが古い）。
	errCodeForkAtUnsupported = "fork_at_unsupported"
	errCodeForkBadAnchor     = "fork_bad_anchor"

	// AI タイトル提案（session_title.go）
	// generation_failed は detail（CLI/auth の理由）を保持するため意図的にカタログ化せず、
	// literal コードのまま英語 developer message を表示する（session_title.go 参照）。
	errCodeTitleFeatureDisabled = "title_feature_disabled"
	errCodeTitleNoContent       = "title_no_content"

	// エージェントメモリの版管理（memory_handlers.go, docs/log/39 / ADR 0022）
	errCodeMemoryBadRequest     = "memory_bad_request"
	errCodeMemoryBadRev         = "memory_bad_rev"
	errCodeMemoryBadPath        = "memory_bad_path"
	errCodeMemoryNoSnapshots    = "memory_no_snapshots"
	errCodeMemorySnapshotFailed = "memory_snapshot_failed"
	errCodeMemoryDiffFailed     = "memory_diff_failed"
	errCodeMemoryBadScope       = "memory_bad_scope"
	errCodeMemoryRestoreFailed  = "memory_restore_failed"
	// P3（export / import・memory_export.go / memory_import.go）
	errCodeMemoryExportFailed   = "memory_export_failed"
	errCodeMemoryImportFailed   = "memory_import_failed"
	errCodeMemoryBadImport      = "memory_bad_import"
	errCodeMemorySecretDetected = "memory_secret_detected"
	errCodeMemoryTooLarge       = "memory_too_large"

	// managed runtime（共有 daemon）を起こせなかった理由のうち、**待っても直らない**もの。
	// CLI にログイン/接続していないので daemon を起こさなかった、が唯一の中身。
	// runtime_failed（＝一時的な失敗・502）と分けてあるのは、Console の文言も
	// isTransientErr の判定も「待てば直るか」で変わるため。
	// 使うのは internal/sessionx/runtime_err.go で、値は session_wiring.go が
	// sessionx.Deps 経由で渡す（sessionx 側で定義し直さない — deps.go の注記）。
	errCodeAgentNotConnected = "agent_not_connected"
)
