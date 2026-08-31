package main

// Console 側で和文ローカライズされるエラーコード（console/src/core/api/client.ts の
// ERR_TEXT と対、docs/log/23 P0-3）。ここの文字列を変えると Console の文言解決が落ちて
// developer メッセージへフォールバックする — 変更は必ず両側同時に。Agent 側の対は
// workspace/agent/errcodes.go。
const (
	errCodeQuotaSessions = "quota_sessions"

	// File editor API (docs/log/44 Phase 1). The CP validates the public envelope
	// before proxying and preserves the Agent's matching stable codes.
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
)
