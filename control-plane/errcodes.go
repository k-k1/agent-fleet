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

	// The engine admin panel and the model catalogue (ADR 0072). These were string
	// literals until the ingest shipped, which is exactly why they had no Japanese: the
	// check below only sees constants declared here, so a literal is a code nobody is
	// watching. 🔴 Measured on the dev deployment (2026-09-09): a mistyped filename
	// answered "the repository does not list flux1-dev.safetensor" — the developer
	// message, in English, on a Japanese screen.
	//
	// Every one of these carries the WHY in its message (which file, which host, which
	// engine), so the catalogue supplies the framing only and the panel reads them with
	// errDetail. A translation alone would replace the reason with a generality.
	errCodeEngineUnknown       = "engine_unknown"
	errCodeEngineModelUnknown  = "model_unknown"
	errCodeEngineBadBody       = "bad_body"
	errCodeEngineECSError      = "engine_ecs_error"
	errCodeEnginePublishFailed = "engine_publish_failed"
	errCodeIngestBadSource     = "bad_source"
	errCodeIngestFileUnknown   = "file_unknown"
	errCodeIngestNoChecksum    = "no_checksum"
	errCodeIngestSourceUnreach = "source_unreachable"
	errCodeIngestSourceForbid  = "source_forbidden"
	errCodeIngestSourceError   = "source_error"
	errCodeIngestStartFailed   = "ingest_start_failed"
	errCodeIngestUnavailable   = "ingest_unavailable"
	errCodeIngestNotAccepted   = "license_not_accepted"
	errCodeIngestGatedNoToken  = "gated_no_token"
	errCodeIngestIDExists      = "model_id_exists"

	// Registering the operator's Hugging Face token (ADR 0072 decision 6 as revised). The
	// write reaches two places — the sealed setting and the stack's secret — and they fail
	// for different reasons, so a single "could not save" would send the reader to the wrong
	// place: no secret means the stack predates P5, a refused write means IAM.
	errCodeHfTokenUnsupported = "hf_token_unsupported"
	errCodeHfTokenEmpty       = "hf_token_empty"
	errCodeHfTokenStoreFailed = "hf_token_store_failed"
	errCodeHfTokenPutFailed   = "hf_token_put_failed"
)
