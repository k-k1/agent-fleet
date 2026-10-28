// English catalog / domain: engineadmin (the admin Inference engines pane and its model catalog,
// features/settings/admin/)
// Key prefixes: admin.engines_, admin.catalog_, admin.fit_, admin.repo_
// (every other admin.* key stays in admin.ts; engines.ts is the topbar indicator's engine.*)
//
// Add keys only to your own domain's file (ADR 0067 decision 4). ja is the canonical catalog;
// Record<keyof typeof (the ja file), string> makes tsc fail on a missing or an extra key.
import type { engineadmin as jaEngineadmin } from "../ja/engineadmin.ts";

export const engineadmin: Record<keyof typeof jaEngineadmin, string> = {
  "admin.engines_none": "This deployment runs no self-hosted inference engines.",
  // The button that opens one engine's model catalogue as its own pane (the same models screen
  // tenant settings' tenant.tab_engines opens for a granted tenant_admin).
  "admin.engines_open_catalog": "Model catalogue",
  "admin.engines_ops_super_only": "Starting and stopping engines and choosing the GPU are the deployment administrator's (super_admin). Taking models in and reading the catalogue is behind the \"Model catalogue\" button on each row.",
  // The role tabs. A deployment with one engine gets its name and no tab strip.
  "admin.engines_role_llm": "Text",
  "admin.engines_role_image": "Image",
  "admin.engines_tab_models": "Models",
  "admin.engines_tab_loras": "LoRAs",
  // No LoRA is an ordinary state. In red it would look broken on every deployment that never
  // wanted one.
  "admin.engines_loras_empty": "This engine has no LoRAs.",
  "admin.engines_models_label": "Models",
  "admin.engines_always_on_note": "Always-on keeps the GPU instance up, at whatever the instance class costs per hour. Put it back on demand when you are done.",
  "admin.engines_provider_unserved": "This row declares images provider \"{p}\", which this build does not implement a client for. generate_image will not work here until the row is repointed at comfy or openai-compat.",
  "admin.engines_tenant_scope":
    "Here you can take models in and see the rows that produced. Enabling a model, starting and stopping the engine, choosing the GPU and forgetting a row belong to the deployment administrator (super_admin), so they are not on this screen. The catalogue is one per deployment, and the id of a model you take in is visible from every tenant.",
  "admin.engines_note": "Disabled takes the engine out of the launch menu and out of generate_image, and requests are refused with 503. On demand buys an instance only when something asks, and it stops itself once idle.",
  // --- externally managed engines (ADR 0076) ---
  // 🔴 Do not reuse the TTS panel's "standalone docker, etc.". That is right for VOICEVOX and
  // wrong for a ComfyUI that may be running on somebody's desktop. The claim they share is the
  // one an operator has to read: this deployment neither starts nor stops it.
  "admin.engines_external": " (externally managed: this deployment neither starts nor stops it)",
  "admin.engines_url_label": "Endpoint",
  "admin.engines_note_external": "An externally managed engine is a URL this deployment points at. Disabled only closes the route. It does not stop the other side, which this deployment neither starts nor stops. Changing the URL means restarting the Control Plane.",
  // --- borrowed engines (ADR 0079) ---
  // 🔴 Do not reuse "externally managed". For a LAN box "nobody starts it" is right; on the other
  // end of a borrowed row is another Agent Fleet with an admin panel of its own, and starting it,
  // stopping it and editing its catalogue are all its job (decision 10). "Externally managed" is
  // true of it and tells the operator nothing about what to do next.
  "admin.engines_remote": " (borrowed: another fleet starts it)",
  // "Borrowed from", not "Endpoint": the value is the far fleet's base URL, and it is the only
  // thing on this screen that answers "whose GPU is this model running on".
  "admin.engines_remote_url_label": "Borrowed from",
  // 🔴 A borrowed row's warm is not this deployment's observation but a mirror of the one the far
  // catalogue publishes (decision 10). No state such as RUNNING arrives for such a row, so an
  // unqualified "loaded" would be this panel asserting something about a box it does not hold.
  "admin.engines_remote_model_loaded": "(loaded, as the lending deployment last saw it)",
  "admin.engines_remote_model_declared": "(declared; the lending deployment has not seen it loaded)",
  "admin.engines_note_remote": "A borrowed engine is reached through another Agent Fleet's gateway. Starting and stopping it, choosing its GPU and editing its catalogue all happen on that deployment's admin panel. Disabled closes the route on this side only; it does not stop their instance.",
  "admin.engines_remote_catalog": "This catalogue is a mirror of the lending deployment's. It cannot be changed from here: ingest, enable and forget all happen over there. Lending deployment:",
  // --- issuing a borrowing token (ADR 0079 decision 3, the LENDING side) ---
  // The super_admin of the deployment that owns the engines mints the one issuing token a
  // borrowing deployment needs. Without this panel the procedure was four steps — invite a
  // dedicated account to the far IdP, sign in as it once, read an environment variable out of
  // its workspace, stop that workspace — which is what open question 6 was about.
  //
  // 🔴 None of this wording is decoration. The value is derived deterministically from the
  // signing master, so "invalidate this one token" is not an operation that exists, and the CP
  // cannot refuse the request either ("is this membership a person?" has no truthful column).
  // Drop the warning or the revocation instruction and an operator lends their own token and
  // holds the whole fleet hostage with it.
  //
  // ⚠️ The server also answers with `warning` and `revoke` as English prose. This screen does
  // not print them: it draws from `deterministic`, `has_workspace`, `env_var` and `opens`
  // instead, so that the two sentences that must be read are never an English paragraph in the
  // middle of a Japanese screen.
  "admin.engines_issue_head": "Issue a borrowing token and show it",
  "admin.engines_issue_intro": "A deployment borrowing this one's engines needs exactly one issuing token (afei_…). Name the tenant and the user_key of the membership kept for borrowing, and this screen shows its token.",
  "admin.engines_issue_before": "What appears is that membership's own engine credential. Nothing is created here: the value is derived deterministically from this deployment's signing master, so every press returns the same string. There is no way to give each borrower a different one.",
  "admin.engines_issue_before_only": "⚠️ Issue this only for a membership that is used for nothing else. Never lend a person's token, including your own.",
  "admin.engines_issue_tenant": "Tenant",
  "admin.engines_issue_tenant_ph": "tenant slug",
  "admin.engines_issue_user_key": "user_key",
  "admin.engines_issue_user_key_ph": "user_key of the borrowing membership",
  "admin.engines_issue_submit": "Issue and show",
  "admin.engines_issue_working": "Issuing…",
  "admin.engines_issue_for": "For {t}/{k} (role {role}, membership {id})",
  "admin.engines_issue_reveal": "Show",
  "admin.engines_issue_hide": "Hide",
  "admin.engines_issue_copy": "Copy the token",
  "admin.engines_issue_copy_env": "Copy as {v}=",
  "admin.engines_issue_copied": "Copied",
  "admin.engines_issue_clear": "Clear it from the screen",
  "admin.engines_issue_autoclear": "This value clears itself from the screen after {m} minutes. Press \"Clear it from the screen\" when you are done with it. Issuing again returns the same value.",
  "admin.engines_issue_env_label": "Where the borrower puts it",
  "admin.engines_issue_opens_label": "What this token opens",
  "admin.engines_issue_opens_only": "Those routes and nothing else. This token opens no git, no MCP, no memos and no API.",
  "admin.engines_issue_deterministic": "⚠️ This value is derived deterministically from the signing master, so every issue returns the same string. There is no way to invalidate one copy of it.",
  "admin.engines_issue_revoke_label": "How to revoke it",
  "admin.engines_issue_revoke": "Remove the membership {t}/{k}. It is resolved on every request, so the next request gets a 401.",
  "admin.engines_issue_revoke_only": "⚠️ The only other way to revoke it is rotating the signing master. The git, memo and schedule tokens come from that same master, so rotating it logs out everyone on this deployment.",
  "admin.engines_issue_has_workspace_tag": "has a workspace",
  "admin.engines_issue_has_workspace": "⚠️ This membership has a workspace, which is what a person's membership looks like. Lend a person's issuing token and taking it back means either removing that person's membership, which locks them out too, or rotating the signing master, which logs out everyone on this deployment. Make a separate membership for borrowing and issue for that one.",
  "admin.engines_issue_no_workspace": "This membership has no workspace, which is what a membership kept only for borrowing should look like.",
  // --- engine status (adminEngines.tsx, EngineStatus) ---
  // ⚠️ Every line here follows "do not write down what you do not know". A line the CP has no
  // answer for is omitted entirely, so do not add filler like "unknown", "0" or "not
  // scheduled": a blank reads as unknown, and filler reads as fact and gets acted on.
  "admin.engines_model_loaded": "(loaded)",
  "admin.engines_model_declared": "(declared, not loaded yet)",
  // --- the model catalogue (ADR 0072) ---
  // "enabled" and "started with" are different questions: the first is whether this deployment
  // may use it, the second is the one checkpoint sd-server holds (or, for llm, the model a
  // request that named none gets).
  "admin.engines_catalog_empty": "This engine's catalogue is empty. Until a model is ingested, requests are refused with 503 and no instance is started.",
  "admin.engines_model_started": "loaded at start",
  // The state is said in a badge. The button's label says what pressing it would do, not
  // which state the row is in; dimming the row instead is what a disabled control looks like.
  "admin.engines_model_is_on": "enabled",
  "admin.engines_model_is_off": "disabled",
  "admin.engines_model_enable": "Enable",
  "admin.engines_model_disable": "Disable",
  "admin.engines_model_select": "Start with this",
  // 🔴 "Forget", not "Delete": the CP has no s3:DeleteObject and is not getting one (ADR 0072
  // decision 7 — deleting the file is the ingest task's job, phase P4). The file stays.
  "admin.engines_model_forget": "Forget",
  // 🔴 Forgetting the row and deleting the file are different acts. The first alone leaves
  // bytes in the bucket that nothing can reach and that keep being paid for (measured
  // 2026-09-09: a 491 MB file outlived its row). The second is the ingest task's MODE=delete —
  // the CP has no s3:DeleteObject (decision 7). Ask before the press, not after.
  "admin.engines_model_forget_purge": "Delete the file from the bucket too (cannot be undone)",
  "admin.engines_model_forget_note": "Forgets the catalogue row only. The file stays in the bucket and keeps costing storage.",
  "admin.engines_model_forget_purge_note": "Forgets the row and starts a task that deletes the bytes. The deletion is the task's, so it takes a moment.",
  "admin.engines_model_add_id": "id",
  "admin.engines_model_add_family": "family",
  // ADR 0072 follow-up, negative prompts. Three places get a say in what a picture keeps out —
  // this row, the request, and the deployment — and they are ADDED, so none of the labels may
  // read as "the" negative prompt.
  "admin.engines_model_negative": "never draw",
  // ADR 0081 decision 5. The adapter's counterpart to the checkpoint's "never draw" above: a
  // LoRA loaded without its trigger changes nothing visible, which reads as a failed ingest.
  "admin.engines_model_trigger": "trigger words",
  "admin.engines_negative_label": "excluded from every image",
  "admin.engines_negative_placeholder": "keywords, separated by commas",
  "admin.engines_negative_save": "Save",
  // 🔴 The sentence that keeps this from being read as a content filter. It is a negative
  // prompt: a nudge to the sampler, absent altogether on the distilled checkpoint families,
  // and no guarantee about what comes out.
  "admin.engines_negative_note":
    "Added to the negative prompt of every image this engine makes, on top of the model's own and the request's. It is guidance, not a filter. Two checkpoint families sample without a negative prompt at all, and requests for them say so in their warnings.",
  "admin.engines_negative_too_long": "Too long. This is a keyword list, not a policy document.",
  "admin.engines_model_add_lora_base": "applies to",
  "admin.engines_model_add_lora_base_pick": "choose the model it fine-tunes",
  "admin.engines_model_add_family_pick": "choose one",
  "admin.engines_model_add_part_whole": "checkpoint (single file)",
  // --- the discovery button (ADR 0082 decisions 6 and 7) ---------------------
  // Reads the filenames the LAN ComfyUI actually has and offers them as candidates. The family
  // is only ever a SUGGESTION read off the filename; a row is only ever written by a person's
  // own press of "add".
  "admin.engines_discover_button": "Look at this engine's files",
  "admin.engines_discover_busy": "Looking…",
  "admin.engines_discover_checkpoints": "Checkpoints",
  "admin.engines_discover_loras": "LoRAs",
  "admin.engines_discover_vaes": "VAEs",
  "admin.engines_discover_vae_hint": "These are not rows of their own; they are filenames to use as `--vae` when building a checkpoint row.",
  "admin.engines_discover_add": "Add a row with this name",
  "admin.engines_discover_added": "Added",
  "admin.engines_discover_empty": "No checkpoint, LoRA or VAE files were found on this engine.",
  "admin.engines_wizard_cannot": "This deployment cannot take that file in (the reason is above).",
  "admin.engines_wizard_need_id": "Give it an id.",
  "admin.engines_wizard_need_family": "Choose the family. This engine picks a workflow from it and will not guess one.",
  "admin.engines_model_files_missing_tag": "missing: {f}",
  "admin.engines_model_add_desc": "description",
  // 🔴 Both halves of the window or neither: with a context and no output cap, opencode reads
  // the cap as 32,000 and a 32k model is left with 768 usable tokens (ADR 0072 decision 3).
  "admin.engines_model_add_ctx": "context window",
  "admin.engines_model_add_out": "output cap",
  "admin.engines_ingest_search": "find",
  "admin.engines_ingest_search_go": "Search",
  "admin.engines_ingest_searching": "Searching…",
  "admin.engines_ingest_search_none": "Nothing found. Try other words, or type the repository name in directly.",
  "admin.engines_ingest_source_hf": "Hugging Face",
  "admin.engines_ingest_source_civitai": "Civitai",
  "admin.engines_ingest_source_civitai-red": "Civitai Red",
  "admin.engines_ingest_browse_go": "Browse",
  "admin.engines_browse_kind_gguf": "LLM (GGUF)",
  "admin.engines_browse_kind_checkpoint": "Image (checkpoint)",
  "admin.engines_browse_note": "Browsing only. Taking one in needs 60-engines deployed and an engine to stage it into. What is shown here is Hugging Face's public metadata, read without a token and without touching any bucket.",
  "admin.engines_ingest_sort_downloads": "Downloads",
  "admin.engines_ingest_sort_trending": "Trending",
  "admin.engines_ingest_sort_likes": "Likes",
  "admin.engines_ingest_sort_updated": "Recently updated",
  "admin.engines_ingest_sort_newest": "New arrivals",
  "admin.catalog_title": "Model catalogue",
  "admin.catalog_image_title": "Image model catalogue",
  "admin.catalog_llm_title": "Language model catalogue",
  "admin.catalog_opening": "Opening the model catalogue…",
  "admin.catalog_role_label": "Engine role",
  "admin.catalog_view_label": "Catalogue view",
  "admin.catalog_view_search": "Browse",
  "admin.catalog_view_registered": "Registered",
  "admin.catalog_family_all": "Every family",
  "admin.catalog_sort": "Sort",
  "admin.catalog_checkpoint": "Checkpoint",
  "admin.catalog_family": "Family",
  "admin.catalog_context": "Context",
  "admin.catalog_output": "Output",
  "admin.catalog_registered_title": "Registered models",
  "admin.catalog_registered_search": "Filter registered models",
  "admin.catalog_sort_name": "Name",
  "admin.catalog_sort_enabled": "Enabled first",
  "admin.catalog_sort_default": "Default first",
  "admin.catalog_edit": "Edit",
  "admin.catalog_parts_unknown": "No file keys are recorded for this model.",
  "admin.catalog_no_description": "No description",
  "admin.catalog_file_present": "present",
  "admin.catalog_file_missing": "missing",
  "admin.catalog_file_unknown": "not checked",
  "admin.catalog_file_uploading": "taking in",
  "admin.catalog_file_failed": "ingest failed",
  "admin.catalog_edit_window_invalid": "Context and output must be whole numbers and must both be set or both be zero.",
  "admin.catalog_edit_vram_invalid": "Measured VRAM must be a whole number.",
  "admin.catalog_gate_accept": "terms must be accepted",
  "admin.catalog_civitai_newest_note": "New arrivals on Civitai; this is not a strict last-updated order.",
  "admin.catalog_storage_checking": "Checking the known model files in storage…",
  "admin.catalog_storage_unavailable": "Storage status could not be loaded. Refresh to try again.",
  "admin.catalog_saved_none": "No saved files from this source",
  "admin.catalog_saved_unknown": "Storage status for this source is not known",
  "admin.catalog_manual": "URL or repository",
  "admin.catalog_more": "Load more",
  "admin.catalog_preview": "Enlarge example image",
  "admin.catalog_source_page": "Source page",
  "admin.catalog_saved_count": "{count} saved file(s) from this source",
  "admin.catalog_add": "Add",
  "admin.catalog_inspect": "Inspect",
  "admin.catalog_version": "Version",
  "admin.catalog_file": "File",
  "admin.catalog_checksum": "SHA-256",
  "admin.catalog_pick_file": "Choose a file",
  "admin.catalog_size_unknown": "Size not reported",
  "admin.catalog_advanced": "Advanced settings",
  "admin.catalog_need_source": "Enter a source.",
  "admin.catalog_need_version": "Choose a version.",
  "admin.catalog_need_file": "Choose a file.",
  "admin.catalog_need_checksum": "Enter the file's SHA-256.",
  "admin.catalog_need_license": "Read and accept the source licence to continue.",
  "admin.catalog_registered_present": "Storage: {present}/{total} files present",
  "admin.catalog_registered_partial": "Storage: {present}/{total} files present (partial)",
  "admin.catalog_registered_missing": "Storage: no registered files are present",
  "admin.catalog_registered_unknown": "Storage: existence not confirmed",
  // --- Names, example images and categories (ADR 0088). An id is a key, not a name. ---
  // 🔴 "Uncategorised", never "unknown": for an image row it means no family, which is a row the
  // CP refuses to enable — the group somebody has to act on, which is why it sorts first.
  "admin.catalog_family_none": "Uncategorised",
  "admin.catalog_meta": "Read the source page",
  "admin.catalog_meta_all": "Fetch names and example images ({n})",
  "admin.catalog_meta_busy": "Reading the source pages… {n}/{m}",
  "admin.catalog_meta_done": "{n} rows now carry a name ({f} could not be read).",
  // --- Does it fit (ADR 0089). The verdict counts the weights and the KV cache, and cannot count
  // the compute buffers or the CUDA context — which is why 99% is not called a fit.
  "admin.fit_fits": "Fits",
  "admin.fit_tight": "Tight ({p}%)",
  "admin.fit_over": "Will not fit this class",
  "admin.fit_over_next": "Will not fit this class ({c} would hold it)",
  "admin.fit_card": "This class has {c} MiB of VRAM",
  "admin.fit_estimate_note": "An estimate of the weights plus the KV cache. It does not include the compute buffers or the CUDA context.",
  "admin.fit_no_kv": "The KV cache could not be read, so this compares the weights alone.",
  // A checkpoint has no KV cache; saying the LLM sentence here would estimate something that
  // does not exist.
  "admin.fit_estimate_note_weights": "An estimate of the weights alone. It does not include the working memory generation needs on top of them.",
  "admin.engines_refit_done": "Windows moved onto this class:",
  "admin.engines_refit_blocked_header": "its header has not been read yet (enabling it reads it)",
  "admin.engines_refit_blocked_weights": "its weights cannot be sized (the files declare no bytes), or they alone do not fit this class",
  "admin.engines_refit_blocked_measured": "its VRAM was declared by hand, so this does not move it",
  "admin.engines_refit_stored_unknown": "the connection dropped, so whether it was stored cannot be said:",
  "admin.engines_refit_failed": "Models whose re-fit did not complete:",
  "admin.engines_refit_stored_unpublished": "the window was stored, but telling the engine failed:",
  "admin.engines_refit_unchanged": "its setting is unchanged:",
  "admin.engines_refit_next_start": "It takes effect the next time the engine starts; a running box keeps the window it was started with.",
  "admin.engines_refit_stuck": "Models this class could not be fitted to:",
  "admin.catalog_edit_window_fit": "Use the largest this class holds ({n})",
  "admin.fit_kv_from": "The KV figure was read from {f}'s header (builds in one repository differ a little).",
  "admin.engines_ingest_ctx_ceiling": "Ceiling {n}",
  // --- The repository card (ADR 0089). The ladder opens on a press, because opening it reads
  // upstream.
  "admin.repo_held_count": "{n} taken in",
  "admin.repo_ladder_open": "Show the other quantisations in this repository",
  "admin.repo_ladder_close": "Close",
  "admin.repo_ladder_loading": "Reading the repository…",
  "admin.repo_ladder_empty": "This repository offers no file that can be taken in.",
  "admin.repo_ladder_held": "Taken in",
  // --- What else the model behind a registered row is published as: versions for image,
  // sizes for chat.
  "admin.catalog_other_versions": "Other versions…",
  "admin.catalog_other_sizes": "Other sizes…",
  "admin.catalog_other_note": "Taking one in creates a new row. To swap, press \"Start with this\" on the new row and then forget the old one.",
  "admin.catalog_versions_empty": "This publisher offers no other version.",
  // --- Complete (ADR 0085 decision 3). The subject is the row; a part never is. ---
  "admin.catalog_complete": "Complete",
  "admin.catalog_complete_busy": "Completing…",
  "admin.catalog_complete_none": "Nothing is missing from this row.",
  "admin.catalog_complete_attached": "Declared from files this deployment already held. Nothing was downloaded.",
  // 🔴 Said apart from a download: this is a server-side copy inside the bucket, so nothing
  // crosses the internet and "taking it in" would have somebody watching for a transfer.
  "admin.catalog_complete_moving": "Moving this row's files inside the bucket (no download). The loader can read them when it lands.",
  "admin.catalog_complete_started": "Taking the missing files in. They attach themselves when they land.",
  "admin.catalog_complete_unknown": "This deployment has no part list for the rest of what this family reads. Take those files in to attach them.",
  "admin.catalog_complete_note": "Fills what this row reads from what the bucket already holds, and takes in only what is left.",
  "admin.catalog_complete_pick": "Choose the file to use",
  "admin.catalog_complete_keep": "Keep the current one",
  "admin.catalog_complete_file_declare": "Held in the bucket — declared",
  "admin.catalog_complete_file_move": "Moved inside the bucket (no download)",
  "admin.catalog_complete_file_download": "Taken in",
  "admin.catalog_complete_file_choose": "Several candidates",
  "admin.catalog_complete_file_unknown": "No file is known for this role",
  "admin.catalog_files": "Files",
  // --- The plan card (ADR 0085 decision 4). No role selector, no key field, no parts box. ---
  "admin.catalog_plan_title": "Take in",
  "admin.catalog_plan_building": "Working out what this press would do…",
  "admin.catalog_plan_files": "Files to take in",
  "admin.catalog_plan_whole": "main file",
  "admin.catalog_plan_action_reuse": "no download (already held)",
  "admin.catalog_plan_action_move": "no download (moved inside the bucket)",
  "admin.catalog_plan_action_unknown": "no part list in this deployment",
  "admin.catalog_plan_at": "currently at {k}",
  "admin.catalog_plan_total": "{n} to download",
  "admin.catalog_plan_total_none": "Nothing is downloaded.",
  // The plan is a quote and the press is the purchase. When anything material moved the card is
  // redrawn — including the licence tick, because what was accepted is not what would be taken in.
  "admin.catalog_plan_stale": "The source changed, so the plan was worked out again. Read it once more before pressing.",
  "admin.catalog_commercial_yes": "commercial use allowed",
  "admin.catalog_commercial_unknown": "commercial use unknown",
  "admin.catalog_started": "Taking it in. Progress is on the bucket under the Registered tab.",
  // A reuse and a move cross no network: "downloading" would have somebody watch for nothing.
  "admin.catalog_started_no_download": "Built from bytes this deployment already held (nothing was downloaded). It is on the bucket under the Registered tab.",
  // --- The bucket (ADR 0085 decisions 2 and 7): what S3 holds, as it holds it. ---
  "admin.catalog_ledger_title": "Bucket",
  "admin.catalog_ledger_note": "The objects under this engine's prefix. Ones no row declares (orphans) and ones no loader can list (misplaced) sort first.",
  "admin.catalog_ledger_note_acts": "A part has no button of its own; the Complete of the model that reads it attaches it.",
  "admin.catalog_ledger_checked": "checked {t}",
  "admin.catalog_ledger_empty": "There are no objects under this engine's prefix.",
  "admin.catalog_ledger_unavailable": "The bucket listing could not be read. Reload to try again.",
  "admin.catalog_ledger_misplaced": "misplaced",
  "admin.catalog_ledger_state_present": "present",
  "admin.catalog_ledger_state_uploading": "taking in",
  "admin.catalog_ledger_state_failed": "failed",
  "admin.catalog_ledger_state_missing": "bytes absent",
  // 🔴 The CP holds no s3:DeleteObject (ADR 0072 decision 7): "delete" starts a task, and the
  // bucket keeps returning the object until it finishes. Without this wording the press looks
  // like it did nothing, which is how it read on af-sandbox.
  "admin.catalog_ledger_state_deleting": "deleting",
  // 🔴 Taking a key in over bytes that are already there leaves the ledger state `present`
  // with only the job `uploading`. Deciding the row's acts from the state alone put 登録 on a
  // key an ingest was writing, which answers 409 `already declared by` (measured on af-sandbox).
  "admin.catalog_ledger_uploading_note": "An ingest task is running. The declaration appears on this line when it lands.",
  "admin.catalog_ledger_deleting_note": "A delete task is running. This line leaves the list when it finishes (it can take minutes).",
  "admin.catalog_ledger_missing_note": "{m} points at this key and the bucket holds no bytes for it. Complete that row to fetch them again.",
  "admin.catalog_ledger_orphan": "no row declares this",
  "admin.catalog_ledger_declared": "declared by: {m}",
  "admin.catalog_ledger_register": "Register",
  "admin.catalog_ledger_delete": "Delete",
  "admin.catalog_ledger_delete_note": "Deletes this object from the bucket. It cannot be undone, and it is refused while any row declares the key.",
  "admin.catalog_ledger_registered": "Registered as {id}.",
  // --- Refusals (ADR 0085 decision 5): who holds it, and the one button that clears it. ---
  "admin.catalog_holder": "held by {k}: {i}",
  "admin.catalog_holder_row": "the registered row",
  "admin.catalog_holder_job": "the ingest job",
  "admin.catalog_holder_object": "the bucket object",
  "admin.catalog_holder_task": "the running task",
  "admin.catalog_next_register": "Register it",
  "admin.catalog_next_complete": "Complete it",
  "admin.catalog_next_replace": "Replace it",
  "admin.catalog_next_forget_row": "Forget the row",
  "admin.catalog_next_dismiss_job": "Dismiss the job",
  "admin.catalog_next_wait": "Reload",
  "admin.engines_ingest_hit_downloads": " downloads",
  "admin.engines_ingest_hit_likes": " likes",
  "admin.engines_ingest_hit_trending": " trending",
  "admin.engines_ingest_hit_gated": "gated",
  // 🔴 What the source says you may not do, before anything is downloaded. The codes are a
  // closed set the CP decides and the words exist only here (engineRestrictLabel).
  // "Login required" is not in Civitai's metadata at all — the CP has to HEAD the download to
  // learn it — so "could not tell" is blank, and is not the same answer as "anyone may".
  "admin.engines_hit_login_required": "login required",
  "admin.engines_hit_login_note": "Civitai hands this file only to a logged-in account. The ingest runs as the registered Civitai account: with none registered it fails with 401, and with one registered, whether it meets the uploader's condition is unknown at search time.",
  "admin.engines_limit_gated_auto": "gated (accept the terms)",
  "admin.engines_limit_gated_manual": "gated (the author approves)",
  "admin.engines_limit_noncommercial": "non-commercial",
  "admin.engines_limit_credit": "credit required",
  "admin.engines_limit_no_derivatives": "no derivatives",
  "admin.engines_limit_same_license": "same licence only",
  "admin.engines_limit_paid": "paid",
  "admin.engines_limit_early_access": "early access (paid until a date)",
  "admin.engines_limit_private": "not public",
  "admin.engines_limit_generate_only": "on-site generation only (no download)",
  "admin.engines_limit_nsfw": "NSFW",
  "admin.engines_limit_poi": "real person",
  "admin.engines_limit_minor": "minor",
  "admin.engines_limit_unscanned": "virus scan not clean",
  "admin.engines_limit_pickle": "pickle warning",
  // A LoRA's trigger words. Used without them, the adapter loads and nothing changes.
  "admin.engines_hit_trigger": "trigger",
  // Published and updated answer different questions ("is this new" and "is it still being
  // worked on"), so both ride, each labelled. One alone cannot tell a model published a year
  // ago and touched last week from one published last week.
  "admin.engines_ingest_hit_published": "Published",
  "admin.engines_ingest_hit_updated": "Updated",
  // Back to the page it came from, opening the URL the CP composed (never one built here).
  "admin.engines_ingest_hit_open_hf": "Open on HF",
  "admin.engines_ingest_hit_open_civitai": "Open on CivitAI",
  // Civitai's own content-rating number, shown on EVERY Civitai hit regardless of which tab
  // found it — a nonzero level shows up under the plain "Civitai" tab's own default query too.
  "admin.engines_ingest_hit_nsfw_level": "NSFW {n}",
  // 🔴 The MODEL's ceiling, not the window this deployment can run. The 30B declares 262144 and
  // does not fit an L4, so it runs at 32768. Never shown without saying whose number it is.
  "admin.engines_ingest_ctx_max": "the model's maximum is {n}",
  // --- Recommended parameters, read out of the author's own prose ---
  // 🔴 A regular expression's guess about somebody else's paragraph, which is why the sentence
  // it was read from is always drawn beside it. Nothing is stored until the press.
  "admin.engines_params_note": "An empty field keeps the family's own recipe. Only what is filled in is replaced for this model.",
  "admin.engines_params_hint_found": "Read out of the author's description (unverified):",
  "admin.engines_params_hint_apply": "Use these",
  "admin.engines_params_none": "Generation parameters: none declared (the family's own recipe)",
  "admin.engines_params_steps": "Steps",
  "admin.engines_params_cfg": "CFG",
  "admin.engines_params_sampler": "Sampler",
  "admin.engines_params_scheduler": "Scheduler",
  "admin.engines_params_clip_skip": "Clip skip",
  "admin.engines_params_guidance": "Guidance (FLUX.1)",
  "admin.engines_params_shift": "Shift (Anima)",
  "admin.engines_params_weight": "Default strength",
  // A display name ("DPM++ 2M Karras") is accepted: the CP translates it into ComfyUI's own
  // vocabulary and drops what it cannot translate.
  "admin.engines_params_name_ph": "dpmpp_2m",
  "admin.engines_params_clear": "Clear",
  "admin.engines_params_edit": "Parameters",
  "admin.engines_params_save": "Save",
  // 🔴 flux1 / klein fold guidance into the conditioning, so a card's "CFG" is a different knob.
  "admin.engines_params_cfg_ignored": "This family does not use CFG (guidance is a different input).",
  "admin.engines_params_clip_skip_note": "Clip skip applies to SD1.5 and SDXL only. SDXL already runs at 2, so only 3 and up change its picture.",
  // The family suggestion. Decision 2 keeps the declaration with the operator, so it is filled
  // in and can be changed.
  "admin.engines_ingest_go": "Take it in",
  // 🔴 Whether it fits, said before the press. Measured on a borrowed llm engine: an L4 (24 GB)
  // took 17 GB of weights and then died on `cudaMalloc failed: out of memory … failed to
  // allocate buffer for kv cache` for the 16 GB the window wanted — four minutes and one
  // purchased GPU after the button, and the panel had shown a filename and a size.
  // A candidate over the card on WEIGHTS ALONE is marked in the list; the cache is not known
  // until the file is resolved, so an unmarked one is "not ruled out here", never "it fits".
  "admin.engines_ingest_fit_weights": "weights {n} MiB",
  // 🔴 The cache's element type cannot be read at all: -ctk/-ctv are CloudFormation parameters
  // that never reach the engine table. f16 is assumed and said so — a deployment running a
  // quantised cache is over-estimated, which is the safe direction for "will this fit".
  "admin.engines_ingest_fit_kv": "KV cache {n} MiB (at {c} tokens, assuming f16)",
  "admin.engines_ingest_fit_kv_unread": "the KV cache could not be read, so this figure is the weights alone",
  "admin.engines_ingest_fit_card": "{n} MiB in total, against a {c} MiB card",
  // 🔴 The total above is the files added up, which overstates a split family — its text encoder
  // is evicted once it has encoded. A family somebody read /system_stats for shows that number
  // instead, and the OPERATOR enters it; the machine never writes it (ADR 0094 decision 8).
  // Left blank, the ladder picks a rung from the total, which buys a larger box than the run needs.
  "admin.engines_vram_measured": "measured for this family: {n} MiB on a {c} ({s}, batch {b}, {i} reference picture(s), with {f})",
  "admin.engines_vram_measured_use": "Use the measured {n}",
  "admin.engines_vram_measured_after": "Enter it under Measured VRAM from the row's Edit once the ingest finishes.",
  "admin.engines_ingest_accept": "I accept this model's licence (on behalf of everyone this deployment serves)",
  "admin.engines_ingest_gated_no_token": "A gated repository, and this deployment has no Hugging Face token. Register the operator's token under \u201cHugging Face token\u201d below. It is read by the ingest task only.",
  // 🔴 A different wall from Hugging Face's gating, and there is no key to it: Civitai answers
  // its metadata 200 for everybody and only the DOWNLOAD is per uploader (five assets measured,
  // split 200/401/403). No token field is being added, so the sentence says what to do instead.
  "admin.engines_ingest_civitai_account_first": "This asset is download-restricted by its uploader. Looking it up here is anonymous, but the ingest runs as the registered Civitai account. Whether that account meets the uploader's condition cannot be checked here; if it does not, the download answers 401 and the job says so.",
  "admin.engines_ingest_civitai_login": "The uploader of this asset only allows downloads from a logged-in account. This deployment has no Civitai token registered, so it cannot ingest this asset. Register one under “API tokens”, or pick another asset.",
  "admin.engines_ingest_gated_accept_first": "A gated repository. A token is registered, but whether that account has accepted this repository's terms is something the Control Plane cannot check (it resolves anonymously). If it has not, the ingest fails with a 403, so accept them first on the Hugging Face model page.",
  "admin.engines_comfy_lan": "LAN ComfyUI (image engine)",
  "admin.engines_comfy_lan_source_panel": "In effect: {url} (set in this panel).",
  "admin.engines_comfy_lan_source_env": "In effect: {url} (from AF_COMFY_URL in the Control Plane's environment).",
  "admin.engines_comfy_lan_source_table": "In effect: {url} (from this deployment's engine table).",
  "admin.engines_comfy_lan_source_remote": "In effect: {url} (borrowed from another Agent Fleet).",
  "admin.engines_comfy_lan_source_none": "No image engine is configured.",
  "admin.engines_comfy_lan_unavailable": "The image engine is managed by this deployment's engine stack, which takes precedence over this panel. Take the role out of the stack to point it at a ComfyUI on your network.",
  "admin.engines_comfy_lan_url": "URL",
  "admin.engines_comfy_lan_key": "Bearer key (optional)",
  "admin.engines_comfy_lan_key_keep": "Leave empty to keep the stored key",
  "admin.engines_comfy_lan_key_set": "Saved with a key ({who} / {when}). The key cannot be shown.",
  "admin.engines_comfy_lan_key_unset": "Saved without a key ({who} / {when}): no Authorization header is sent.",
  "admin.engines_comfy_lan_not_saved": "Nothing is saved in this panel.",
  "admin.engines_comfy_lan_save": "Save connection",
  "admin.engines_comfy_lan_clear_key": "Remove key",
  "admin.engines_comfy_lan_remove": "Remove panel setting",
  "admin.engines_comfy_lan_fallback_env": "A URL saved here takes precedence over AF_COMFY_URL. Removing it returns images to {url} (AF_COMFY_URL).",
  "admin.engines_comfy_lan_fallback_table": "Removing the URL saved here returns images to {url}, an external row of the engine table.",
  "admin.engines_comfy_lan_fallback_remote": "Removing the URL saved here returns images to {url}, borrowed from another Agent Fleet.",
  "admin.engines_comfy_lan_fallback_borrow": "Removing the URL saved here can hand images to the engine borrowed from AF_REMOTE_ENGINE_URL, if that fleet offers one; image generation does not necessarily stop.",
  "admin.engines_comfy_lan_fallback_none": "Nothing else is configured for images, so removing the URL saved here takes the image engine away.",
  "admin.engines_comfy_lan_note": "Changes apply immediately, without restarting the Control Plane. The key is stored encrypted and is sent only to the URL saved here, never the AF_COMFY_API_KEY of the environment.",
  "admin.engines_hf_token": "Hugging Face token",
  "admin.engines_hf_token_field": "Token",
  "admin.engines_hf_token_save": "Register",
  "admin.engines_hf_token_remove": "Remove",
  "admin.engines_hf_token_unset": "Not registered. Only ungated repositories can be taken in.",
  "admin.engines_hf_token_set": "Registered ({who} / {when}). The value cannot be shown: the Control Plane can write it but has no permission to read it back.",
  "admin.engines_hf_token_stack": "This deployment's token comes from a CloudFormation parameter. It cannot be registered or removed from the Console, but gated repositories can be taken in.",
  "admin.engines_hf_token_unsupported": "This deployment's engine stack has nowhere to keep a token. Update 60-engines and it can be registered from here.",
  "admin.engines_hf_token_note": "One token for the whole deployment. It is stored encrypted and written into the deployment's secret before every ingest. Only the ingest task reads it, and it is never handed to an engine instance.",
  "admin.engines_civitai_token": "Civitai token",
  "admin.engines_civitai_token_field": "Token",
  "admin.engines_civitai_token_save": "Register",
  "admin.engines_civitai_token_remove": "Remove",
  "admin.engines_civitai_token_unset": "Not registered. Assets that require a logged-in account cannot be taken in.",
  "admin.engines_civitai_token_set": "Registered ({who} / {when}). The value cannot be shown: the Control Plane can write it but has no permission to read it back.",
  "admin.engines_civitai_token_unsupported": "This deployment's engine stack has nowhere to keep a token. Update 60-engines and it can be registered from here.",
  "admin.engines_civitai_token_note": "One token for the whole deployment, separate from the Hugging Face one above. It is stored encrypted and written into the deployment's secret before every ingest. Only the ingest task reads it, and it is never handed to an engine instance.",
  "admin.engines_civitai_red": "Civitai Red (the NSFW sister domain)",
  "admin.engines_civitai_red_show": "Offer the Civitai Red tab in the model search",
  "admin.engines_civitai_red_note":
    "civitai.red is the sister domain Civitai split off for NSFW browsing, and this tab is the only search that sends nsfw=true. Example images are drawn unblurred. It is not a content filter: the plain Civitai tab also answers models with a non-zero nsfwLevel. Pasting a civitai.red URL into the ingest form works either way.",
  "admin.engines_civitai_red_unavailable": "This deployment does not have Civitai Red. Set AF_ENGINE_CIVITAI_RED on the Control Plane and the switch appears here.",
  "admin.engines_ingest_noncommercial":
    "🔴 A non-commercial licence. Both commercial use of the model and commercial use of what it generates may be restricted. Read the licence before enabling this.",
  // The GPU rung this role buys (ADR 0074). The hourly figure comes from the ladder the
  // operator declared, never from a number written here: the instance is selectable now.
  "admin.engines_class": "Instance class: ",
  "admin.engines_class_not_default": "not the default",
  "admin.engines_class_reset": "Back to the default",
  // Offers (ADR 0075 decisions 1 and 8): the ladder with a purchase form on each rung, tried in
  // the order it was DECLARED — the control plane does not sort by price, so neither does this
  // panel. Choosing one is now a PIN rather than "a rung other than the default": not choosing is
  // automatic and falls through to the next offer, a pinned one never does.
  "admin.engines_class_auto": "Automatic (the default)",
  "admin.engines_class_pinned": "pinned, not automatic",
  "admin.engines_class_unpin": "Back to automatic",
  "admin.engines_offers_head": "Offers, tried from the top in the order they were declared",
  "admin.engines_offer_buy_od": "On-demand",
  "admin.engines_offer_buy_spot": "Spot",
  "admin.engines_offer_now": "Current offer: ",
  "admin.engines_offer_trail": "Tried, in order: ",
  // The failure codes of decision 5. "Cannot be bought as declared" and "no capacity" are waited
  // on differently: the first is not a shape that waiting fixes, so the next offer is tried at
  // once, while the second gets whatever is left of the time budget.
  "admin.engines_offer_result_active": "got it",
  "admin.engines_offer_result_unfulfillable": "cannot be bought as declared",
  "admin.engines_offer_result_insufficient": "no capacity",
  "admin.engines_offer_result_quota": "quota",
  "admin.engines_offer_result_budget": "budget spent",
  "admin.engines_offer_result_unusable": "cannot be asked for",
  "admin.engines_offer_result_interrupted": "taken away",
  // Accepting interruption (ADR 0077 decision 9, amended). The operator declares the offers; the
  // administrator says here that a box may be taken away. 🔴 The sentence does not stop at "the
  // instance may stop", because two things are actually being agreed to — the answer in flight is
  // lost, and the next request waits out a cold start. Only somebody who has read both should be
  // able to tick it.
  "admin.engines_spot_allow": "Accept interruption (this role may buy Spot instances)",
  "admin.engines_spot_note":
    "AWS can take a Spot instance away. When it does, whatever was being generated at that moment is lost and the next request waits out a cold start (about 9 minutes for llm, 3 for image). An offer taken away twice in a row is skipped for the rest of that start, so the engine ends up on an on-demand offer whose VRAM fits.",
  "admin.engines_spot_blocked": "needs interruption accepted",
  "admin.engines_spot_pin_ignored":
    "The pinned offer is Spot, so it is not being used (the role has fallen back to choosing automatically). Accept interruption, or pin a different offer.",
  "admin.engines_spot_running":
    "The instance running right now is a Spot one. Un-ticking this does not make it safe from being taken away; it applies to the NEXT instance.",
  "admin.engines_class_pending": "What is running is a {t} instance. The class you chose applies to the NEXT instance. Replacing it costs one cold start (about 9 minutes for llm, 3 for image), and the new instance does not start until the old one has left.",
  "admin.engines_class_replace": "Replace it now",
  // 🔴 The choice is SAVED before it is applied, so a failed apply leaves the picker showing a
  // class the capacity provider does not hold — and picking it again is no change at all. The
  // retry has to be a button of its own, or the only way out is a detour through another class.
  "admin.engines_class_apply_failed": "The class was saved, but writing it to the capacity provider failed, so the next instance is still bought on the previous one: {m}",
  "admin.engines_class_apply_retry": "Apply it again",
  "admin.engines_class_vram_ok": "The largest enabled model is {id} at {n} MiB ({src}); this class has {m} MiB.",
  "admin.engines_class_vram_over": "The largest enabled model is {id} at {n} MiB ({src}) and this class has {m} MiB. It may not fit.",
  "admin.engines_class_vram_unknown": "How much VRAM the enabled models need is not known: nobody measured it. That is not the same as saying they fit.",
  // ⚠️ The comparison above is a maximum, not a sum, because one model is in VRAM at a time.
  // That is exactly true of llm and sd-server and CONSERVATIVE of comfy, which picks a
  // checkpoint per request and keeps loaded ones cached — so this sentence is added there
  // rather than the number being turned into a sum nobody would read past.
  "admin.engines_class_vram_many": "This engine chooses a checkpoint per request and keeps loaded ones in VRAM, so several can be resident at once; the figure above is the largest ONE of them.",
  "admin.engines_vram_src_declared": "measured",
  "admin.engines_vram_src_floor": "a weights-only floor",
  // The CP answers this source too (engine_class.go), and until now neither catalogue had a word
  // for it — the confirmation dialog built its key by concatenation and drew a missing one.
  "admin.engines_vram_src_weights_kv": "a floor of the weights plus the KV cache",
  "admin.engines_vram_src_unknown": "unknown",
  "admin.engines_vram_confirm": "{id} wants {n} MiB ({src}) and the class you have chosen has {m} MiB. Short VRAM does not slow CUDA down; it crashes it. Quantisation or offloading may still fit it, so continue if you know that.",
  "admin.engines_vram_confirm_go": "Enable it anyway",
  "admin.engines_model_vram": "VRAM {n} MiB",
  "admin.engines_model_window_context": "Context",
  "admin.engines_model_window_output": "Max output",
  "admin.engines_model_vram_edit": "Measured VRAM (MiB)",
  // The licence facts a row can carry (ADR 0072 decision 10). "Not recorded" is stated rather
  // than left blank: a seeded row cannot know a licence and the hand-registration form does not
  // ask, and a gap where every ingested row names one reads as "no restrictions".
  "admin.engines_model_noncommercial": "non-commercial",
  // Which model has weights in VRAM, and how often that changed — the price of holding one
  // model at a time, rather than a uniformly "warm" engine.
  "admin.engines_warm_model": "In VRAM: ",
  "admin.engines_model_swaps": "model changes since this control plane started: {n}",
  // The instance's clock and the service's clock are different facts: the first is when the
  // EC2 instance registered, the second when the deployment last changed — which moves without
  // any new instance being bought.
  "admin.engines_since_box": "Instance started ",
  "admin.engines_since_service": "Service updated ",
  "admin.engines_up_for": "{d} ago",
  "admin.engines_stops_at": "Stops by itself at ",
  "admin.engines_stops_in": "in {d}",
  "admin.engines_stops_due": "Past its stop time (it goes on the controller's next pass)",
  // Shown when there is no countdown to give (it is stopped, so there is nothing to stop).
  // A different claim from a time, and without it a stopped engine shows no window at all.
  "admin.engines_idle_policy": "Stops itself {d} after nobody is using it",
  "admin.engines_idle_label": "Stop after",
  "admin.engines_idle_minutes": "minutes",
  "admin.engines_idle_hint": "Time from the last use until shutdown. Use at least {m} minutes so the engine is not stopped while starting.",
  "admin.engines_recent": "Requests in the last {m} min: {n}",
  // 🔴 The count lives in this control plane's memory and nowhere else, so it resets to 0 when
  // the CP is replaced. Say so whenever less than a full window has been counted; the
  // last-request time next to it is persisted and stays true.
  "admin.engines_recent_partial": "(this control plane has only been counting for {d})",
  "admin.engines_last_demand": "Last request ",
  "admin.engines_history": "History (14 days)",
  "admin.engines_metric_label": "What the shade means",
  "admin.engines_metric_running": "Able to answer",
  "admin.engines_metric_up": "An instance existed",
  "admin.engines_state_down": "Down",
  "admin.engines_ro_detail": "answering {run} · starting {start} · draining {drain}",
  "admin.engines_col_running": "Answering",
  "admin.engines_col_starting": "Starting",
  "admin.engines_col_draining": "Draining",
  "admin.engines_dur_hm": "{h}h {m}m",
  "admin.engines_dur_m": "{m}m",
  "admin.engines_uptime_none": "Nothing was recorded as running in this period.",
  "admin.engines_uptime_error": "Could not load the history.",
  "admin.engines_uptime_note":
    "Sampled about every {n} seconds. \"An instance existed\" includes the cold start, when it cannot answer yet (165-197 s measured), and the drain, when the task is gone but the instance is not (427-477 s measured). Both bill, but neither answers a request. Hours from before recording began stay blank and cannot be filled in later. This is not money.",
};
