package main

// Error codes the Console localizes into Japanese; the counterpart is ERR_TEXT in
// console/src/core/api/client.ts (docs/log/23 P0-3). Change a string here and the Console's
// text lookup fails and falls back to the developer message — always change both sides
// together. The CP-side counterpart is control-plane/errcodes.go (quota_sessions).
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
	// Deletion lock (docs/log/45): the target itself is locked, or a deletion was refused
	// because it would have taken locked sessions down with it.
	errCodeLocked         = "locked"
	errCodeLockedSessions = "locked_sessions"
)

// Stable codes for the user-facing errors (docs/log/28 P3). The backend message is a
// language-neutral English developer fallback; the displayed text is resolved by the
// Console's "err.<code>" catalogs (console/src/lib/i18n/locales). Each code is unique per
// meaning — when adding or renaming one, always add "err.<code>" to both language catalogs
// (ja.ts / en.ts) as well.
const (
	// Assistant chat (chat_handlers.go)
	errCodeChatAssistantNotFound  = "chat_assistant_not_found"
	errCodeChatAgentUnsupported   = "chat_agent_unsupported"
	errCodeChatPromptEmpty        = "chat_prompt_empty"
	errCodeChatTitleEmpty         = "chat_title_empty"
	errCodeChatMessageEmpty       = "chat_message_empty"
	errCodeChatConversationNotFnd = "chat_conversation_not_found"
	errCodeChatNothingToCompact   = "chat_nothing_to_compact"

	// Connection settings (connections.go)
	errCodeConnAPIKeyRequired     = "conn_api_key_required"
	errCodeConnGrafanaFields      = "conn_grafana_fields_required"
	errCodeConnURLScheme          = "conn_url_scheme"
	errCodeConnAWSProfileRequired = "conn_aws_profile_required"
	errCodeConnSSORegionMissing   = "conn_sso_region_missing"

	// Jira connections (connections_jira.go, docs/log/80 P1)
	errCodeConnJiraFields   = "conn_jira_fields_required"
	errCodeConnJiraRejected = "conn_jira_rejected"

	// Chat bridge connections (connections.go, docs/log/37 P1)
	errCodeConnDiscordTokenRequired = "conn_discord_token_required"
	errCodeConnDiscordDestRequired  = "conn_discord_destination_required"
	errCodeConnDiscordDestInvalid   = "conn_discord_destination_invalid"
	errCodeConnDiscordTokenInvalid  = "conn_discord_token_invalid"

	// Slack chat bridge connections (connections_slack.go, docs/log/37 Slack follow-up)
	errCodeConnSlackTokenRequired    = "conn_slack_token_required"
	errCodeConnSlackDestRequired     = "conn_slack_destination_required"
	errCodeConnSlackDestInvalid      = "conn_slack_destination_invalid"
	errCodeConnSlackTokenInvalid     = "conn_slack_token_invalid"
	errCodeConnSlackAppTokenRequired = "conn_slack_app_token_required"

	// Assistant CRUD (assistants.go)
	errCodeAssistantNotFound      = "assistant_not_found"
	errCodeAssistantBuiltinEdit   = "assistant_builtin_readonly_edit"
	errCodeAssistantBuiltinDelete = "assistant_builtin_readonly_delete"

	// Image paste (session_paste.go)
	errCodePasteTooLarge         = "paste_too_large"
	errCodePasteUnsupportedKind  = "paste_unsupported_kind"
	errCodePasteUnsupportedAgent = "paste_unsupported_agent"

	// Session fork (session_handlers.go)
	errCodeForkUnsupportedKind = "fork_unsupported_kind"
	errCodeForkMissingDir      = "fork_missing_dir"
	// Forking at a message (docs/log/55). The boundary that stops a point fork quietly
	// degrading into a fork of the whole conversation. Split in two because the meanings
	// differ: unsupported means this kind or launch method has no point-fork feature at all
	// (so the affordance should never have been offered), bad_anchor means the feature
	// exists but this fork point cannot be used (not in the conversation, a subagent
	// message, or a stale mirror).
	errCodeForkAtUnsupported = "fork_at_unsupported"
	errCodeForkBadAnchor     = "fork_bad_anchor"

	// AI title suggestion (session_title.go)
	// generation_failed is deliberately left out of the catalogs so it can keep its detail
	// (the CLI/auth reason): it stays a literal code and shows the English developer
	// message (see session_title.go).
	errCodeTitleFeatureDisabled = "title_feature_disabled"
	errCodeTitleNoContent       = "title_no_content"

	// Agent memory versioning (memory_handlers.go, docs/log/39 / ADR 0022)
	errCodeMemoryBadRequest     = "memory_bad_request"
	errCodeMemoryBadRev         = "memory_bad_rev"
	errCodeMemoryBadPath        = "memory_bad_path"
	errCodeMemoryNoSnapshots    = "memory_no_snapshots"
	errCodeMemorySnapshotFailed = "memory_snapshot_failed"
	errCodeMemoryDiffFailed     = "memory_diff_failed"
	errCodeMemoryBadScope       = "memory_bad_scope"
	errCodeMemoryRestoreFailed  = "memory_restore_failed"
	// P3 (export / import: memory_export.go / memory_import.go)
	errCodeMemoryExportFailed   = "memory_export_failed"
	errCodeMemoryImportFailed   = "memory_import_failed"
	errCodeMemoryBadImport      = "memory_bad_import"
	errCodeMemorySecretDetected = "memory_secret_detected"
	errCodeMemoryTooLarge       = "memory_too_large"

	// The reason for failing to wake the managed runtime (the shared daemon) that waiting
	// will not fix. Its only content: the daemon was not started because the CLI is not
	// logged in or connected. Kept apart from runtime_failed (a transient failure, 502)
	// because both the Console's wording and isTransientErr turn on whether waiting helps.
	// Used by internal/sessionx/runtime_err.go; the value is passed in by session_wiring.go
	// through sessionx.Deps — do not redefine it inside sessionx (see the note in deps.go).
	errCodeAgentNotConnected = "agent_not_connected"
)
