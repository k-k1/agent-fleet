package main

// Error codes the Console localises, paired with ERR_TEXT in
// console/src/core/api/client.ts. Change a string here and the Console's lookup misses and
// falls back to the developer message, so both sides must move together; the Agent's
// counterpart is workspace/agent/errcodes.go.
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
