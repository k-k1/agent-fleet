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
	// The membership a borrowing token was asked for is not active, so the engine gateway would
	// refuse the token this route would hand out (ADR 0079 decision 3). DECLARED rather than left
	// a literal like admin_stats.go's siblings: this one is read by a super_admin on a screen that
	// may be Japanese, and an untranslated developer sentence there is the defect the note at the
	// top of this block records.
	errCodeMembershipInactive = "membership_inactive"
	// The catalogue of a BORROWED engine is the far administrator's document, so it is not
	// editable from this panel (ADR 0079 decision 7). Its own code rather than bad_body because
	// nothing about the request was malformed — it arrived at the wrong deployment, and the
	// message says which one to go to.
	errCodeEngineNotOurs = "engine_not_ours"
	// The model wants more VRAM than the chosen instance class declares, and the request did
	// not say it knew that (ADR 0074 decision 6). A REFUSAL TO GUESS, not a refusal: repeating
	// the call with confirm_vram succeeds, because quantisation and offloading are real and the
	// panel points rather than decides.
	errCodeEngineVramConfirm = "engine_vram_confirm"
	// The row's declared family names a workflow that reads files the row does not have (ADR
	// 0072 P2 欠落 10). Unlike the VRAM one this has no confirm: it is not a risk, it is
	// `comfyBuildGraph` refusing before it dials anything, so enabling would only put an id in
	// generate_image's list that every request bounces off.
	errCodeEngineFilesMissing = "engine_files_missing"
	// The row's checkpoint was read and carries no VAE tensors, and the row declares no `--vae`
	// file either (ADR 0072 follow-up). Like files_missing and unlike the VRAM gate it has no
	// confirm: `generate_image` has no VAE argument and the template has no other source of one,
	// so switching the row on offers a model whose every request dies inside ComfyUI after the
	// checkpoint switch.
	errCodeEngineVaeMissing = "engine_vae_missing"
	// The question could not be ASKED: no source to re-read the header from, or an upstream that
	// refused. Its own code because the two answers send the reader to opposite places — this one
	// is "nobody knows", which is never a reason to distrust a row that generates today.
	errCodeEngineVaeUnreadable = "engine_vae_unreadable"
	// There is no model page behind this row to read a name and an example image off (ADR 0088):
	// nothing was recorded about where it came from, or what was recorded is a plain URL, which
	// addresses the weights themselves. Its own code because the answer is neither a mistake nor
	// something pressing again fixes — the row simply has no upstream, which is the normal state
	// of a seeded row and of one an operator registered from the bucket by hand.
	errCodeEngineNoSource = "engine_no_source"
	// The discovery button (ADR 0082 decisions 6 and 7): reading what an external ComfyUI's own
	// checkpoint/LoRA/VAE folders hold. Two codes because the two refusals send the reader to
	// opposite places — `unsupported` is a row this button was never for (a borrowed mirror, a
	// managed row, a non-comfy provider) and nothing about the network changes that; `unreachable`
	// is the right kind of row answering "that machine did not answer this time", which pressing
	// the button again may fix.
	errCodeEngineDiscoverUnsupported = "engine_discover_unsupported"
	errCodeEngineDiscoverUnreachable = "engine_discover_unreachable"
	errCodeIngestBadSource           = "bad_source"
	errCodeIngestFileUnknown         = "file_unknown"
	errCodeIngestNoChecksum          = "no_checksum"
	errCodeIngestSourceUnreach       = "source_unreachable"
	errCodeIngestSourceForbid        = "source_forbidden"
	errCodeIngestSourceError         = "source_error"
	errCodeIngestStartFailed         = "ingest_start_failed"
	errCodeIngestUnavailable         = "ingest_unavailable"
	errCodeIngestNotAccepted         = "license_not_accepted"
	errCodeIngestGatedNoToken        = "gated_no_token"
	// The token DID reach the ingest task and Hugging Face still refused (403): that account
	// has not accepted this repository's terms. Measured on af-sandbox (ADR 0072 P5 実機検証):
	// one token, FLUX.1-dev through and SD3.5 Medium refused, and accepting on the model page
	// fixed it. A different act from registering a token, so a different code — the two share
	// the word "gated" and nothing else.
	errCodeIngestGatedNotAccepted = "gated_not_accepted"
	errCodeIngestIDExists         = "model_id_exists"
	// A Civitai download that failed with 403: a token reached the task and this deployment's
	// Civitai account still cannot have the file (an uploader-restricted asset, or an account
	// with no entitlement for it) — the same "the token arrived, the account cannot" shape as
	// `gated_not_accepted`, spelled differently because the fix is on Civitai's site, not
	// Hugging Face's.
	errCodeIngestCivitaiLogin = "civitai_login_required"
	// A Civitai download that failed with 401: no token reached the task, either because none
	// is registered or because it did not carry into the secret. The counterpart of
	// `gated_no_token`.
	errCodeIngestCivitaiNoToken = "civitai_gated_no_token"
	// Forgetting a row of the job history (ADR 0072 P4, the delete the table never had). Two
	// codes because the two refusals send the reader to opposite places: `ingest_job_unknown`
	// is the id — and also the answer a granted tenant_admin gets for another tenant's job,
	// which is why it must not be spelled "forbidden"; `ingest_job_live` is a job that is still
	// downloading, where the answer is to wait, because forgetting the row does not stop the
	// ECS task and the task still writes its catalogue row afterwards.
	errCodeIngestJobUnknown = "ingest_job_unknown"
	errCodeIngestJobLive    = "ingest_job_live"
	// The plan the form was looking at is not what taking this model in would do now (ADR 0085
	// decision 4). Its own code because it is the one refusal that is not a mistake: the bucket,
	// the licence or the size changed between the resolve and the press, the caller did nothing
	// wrong, and the answer carries the fresh plan beside the error for it to look at again.
	errCodeEnginePlanStale = "engine_plan_stale"

	// 揃える was asked to put a file in a role the row already fills (ADR 0085 decision 3). Its own
	// code and not `bad_body`, because it is the one refusal in that area that is a QUESTION: the
	// same request with `replace` succeeds, and the Console draws the refusal's `next` as that
	// button. Swapping a part changes what an enabled row loads at the next cold start, which is
	// not something a mis-click may do.
	errCodeEngineSlotFilled = "engine_slot_filled"

	// Registering the operator's Hugging Face token (ADR 0072 decision 6 as revised). The
	// write reaches two places — the sealed setting and the stack's secret — and they fail
	// for different reasons, so a single "could not save" would send the reader to the wrong
	// place: no secret means the stack predates P5, a refused write means IAM.
	errCodeHfTokenUnsupported = "hf_token_unsupported"
	errCodeHfTokenEmpty       = "hf_token_empty"
	errCodeHfTokenStoreFailed = "hf_token_store_failed"
	errCodeHfTokenPutFailed   = "hf_token_put_failed"

	// Registering the operator's Civitai token, the same shape as the Hugging Face token above
	// and for the same reason: the write reaches a sealed setting and the stack's secret.
	errCodeCivitaiTokenUnsupported = "civitai_token_unsupported"
	errCodeCivitaiTokenEmpty       = "civitai_token_empty"
	errCodeCivitaiTokenStoreFailed = "civitai_token_store_failed"
	errCodeCivitaiTokenPutFailed   = "civitai_token_put_failed"
)
