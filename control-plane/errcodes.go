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
	errCodeEngineClassUnknown  = "engine_class_unknown"
	// The model wants more VRAM than the chosen instance class declares, and the request did
	// not say it knew that (ADR 0074 decision 6). A REFUSAL TO GUESS, not a refusal: repeating
	// the call with confirm_vram succeeds, because quantisation and offloading are real and the
	// panel points rather than decides.
	errCodeEngineVramConfirm = "engine_vram_confirm"
	// The row's declared family names a workflow that reads files the row does not have (ADR
	// 0072 P2 欠落 10). Unlike the VRAM one this has no confirm: it is not a risk, it is
	// `comfyBuildGraph` refusing before it dials anything, so enabling would only put an id in
	// generate_image's list that every request bounces off.
	errCodeEngineFilesMissing  = "engine_files_missing"
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
	// The token DID reach the ingest task and Hugging Face still refused (403): that account
	// has not accepted this repository's terms. Measured on af-sandbox (ADR 0072 P5 実機検証):
	// one token, FLUX.1-dev through and SD3.5 Medium refused, and accepting on the model page
	// fixed it. A different act from registering a token, so a different code — the two share
	// the word "gated" and nothing else.
	errCodeIngestGatedNotAccepted = "gated_not_accepted"
	errCodeIngestIDExists         = "model_id_exists"
	// A Civitai asset whose uploader requires a logged-in account (ADR 0072 P2 欠落 5). The
	// counterpart of `gated_no_token`, and deliberately not the same code: gating is the
	// repository's terms and a registered token satisfies them, while this deployment has no
	// Civitai account at all and no field in which to put one.
	errCodeIngestCivitaiLogin = "civitai_login_required"

	// Registering the operator's Hugging Face token (ADR 0072 decision 6 as revised). The
	// write reaches two places — the sealed setting and the stack's secret — and they fail
	// for different reasons, so a single "could not save" would send the reader to the wrong
	// place: no secret means the stack predates P5, a refused write means IAM.
	errCodeHfTokenUnsupported = "hf_token_unsupported"
	errCodeHfTokenEmpty       = "hf_token_empty"
	errCodeHfTokenStoreFailed = "hf_token_store_failed"
	errCodeHfTokenPutFailed   = "hf_token_put_failed"
)
