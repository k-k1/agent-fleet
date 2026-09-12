// English カタログ / ドメイン: admin
// キー接頭辞: admin, tenant, clean
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。Record<keyof typeof ja...> で ja に無いキー / 足りないキーを tsc が落とす。
import type { admin as jaAdmin } from "../ja/admin.ts";

export const admin: Record<keyof typeof jaAdmin, string> = {
  // --- admin (AdminTab; super_admin / tenant_admin) ---
  "admin.title": "Admin",
  "admin.forbidden": "You don't have permission (super_admin only).",
  "admin.mode_sessions": "Sessions",
  "admin.mode_usage": "Running time",
  "admin.mode_audit": "Audit",
  "admin.mode_egress": "Egress",
  "admin.mode_mcp": "MCP",
  "admin.mode_tts": "Read aloud",
  "admin.mode_brand": "Appearance",
  "admin.brand_title": "This deployment's colour and name",
  "admin.brand_note": "Ship the same image to several environments and every tab — and every home-screen icon — looks identical. A colour and a label tell them apart through the favicon, the PWA icon and the app name. Cosmetic only: nothing about access or data depends on it.",
  "admin.brand_color": "Colour",
  "admin.brand_label": "Label",
  "admin.brand_label_ph": "dev, staging, …",
  "admin.brand_label_note": "Goes in front of the app name as \"[dev] \". A prefix because a tab strip and a phone launcher both truncate the end. Empty means no label.",
  "admin.brand_preview_none": "(none)",
  "admin.brand_reset": "Follow the environment",
  "admin.brand_src_admin": "This screen's setting is in force (it wins over AF_BRAND_*, which says {color} / {label}).",
  "admin.brand_src_env": "Currently taken from AF_BRAND_*. Saving here takes over from it.",
  "admin.brand_src_default": "Currently the default: the shipped teal, no label.",
  "admin.brand_pwa_note": "Saving applies to this tab at once. Other tabs pick it up on reload, and a PWA already installed keeps its icon and name until it is reinstalled.",
  "admin.mode_pool": "Slots",
  "admin.mode_engines": "Inference engines",
  // Two screens: the machine and what it loads. The rail follows that order. The same models
  // screen in tenant settings is tenant.tab_engines.
  "admin.mode_engine_models": "Inference engine models",
  "admin.engines_none": "This deployment runs no self-hosted inference engines.",
  "admin.engines_none_models_hint": "What there is to run can be browsed under \"Inference engine models\" — it needs no engine and no token.",
  "admin.engines_ops_super_only": "Starting and stopping engines and choosing the GPU are the deployment administrator's (super_admin). Taking models in and reading the catalogue is under \"Inference engine models\".",
  // The role tabs. A deployment with one engine gets its name and no tab strip.
  "admin.engines_role_llm": "Text",
  "admin.engines_role_image": "Image",
  "admin.engines_tab_models": "Models",
  "admin.engines_tab_loras": "LoRAs",
  // No LoRA is an ordinary state. In red it would look broken on every deployment that never
  // wanted one.
  "admin.engines_loras_empty": "This engine has no LoRAs.",
  "admin.engines_lora_no_family": "no family declared",
  "admin.engines_models_label": "Models",
  "admin.engines_always_on_note": "Always-on keeps the GPU instance up, at whatever the instance class costs per hour. Put it back on demand when you are done.",
  "admin.engines_tenant_scope":
    "Here you can take models in and see the rows that produced. Enabling a model, starting and stopping the engine, choosing the GPU and forgetting a row belong to the deployment administrator (super_admin), so they are not on this screen. The catalogue is one per deployment, and the id of a model you take in is visible from every tenant.",
  "admin.engines_note": "Disabled takes the engine out of the launch menu and out of generate_image, and requests are refused with 503. On demand buys an instance only when something asks, and it stops itself once idle.",
  // --- externally managed engines (ADR 0076) ---
  // 🔴 Do not reuse the TTS panel's "standalone docker, etc.". That is right for VOICEVOX and
  // wrong for a ComfyUI that may be running on somebody's desktop. The claim they share is the
  // one an operator has to read: this deployment neither starts nor stops it.
  "admin.engines_external": " (externally managed: this deployment neither starts nor stops it)",
  "admin.engines_url_label": "Endpoint",
  "admin.engines_note_external": "An externally managed engine is a URL this deployment points at. Disabled only closes the route — it does not stop the other side, which this deployment neither starts nor stops. Changing the URL means restarting the Control Plane.",
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
  "admin.engines_note_remote": "A borrowed engine is reached through another Agent Fleet's gateway. Starting and stopping it, choosing its GPU and editing its catalogue all happen on that deployment's admin panel. Disabled closes the route on this side only — it does not stop their box.",
  "admin.engines_remote_catalog": "This catalogue is a mirror of the lending deployment's. It cannot be changed from here — ingest, enable and forget all happen over there. Lending deployment:",
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
  "admin.engines_issue_autoclear": "This value clears itself from the screen after {m} minutes. Press \"Clear it from the screen\" when you are done with it — issuing again returns the same value.",
  "admin.engines_issue_env_label": "Where the borrower puts it",
  "admin.engines_issue_opens_label": "What this token opens",
  "admin.engines_issue_opens_only": "Those routes and nothing else. This token opens no git, no MCP, no memos and no API.",
  "admin.engines_issue_deterministic": "⚠️ This value is derived deterministically from the signing master, so every issue returns the same string. There is no way to invalidate one copy of it.",
  "admin.engines_issue_revoke_label": "How to revoke it",
  "admin.engines_issue_revoke": "Remove the membership {t}/{k}. It is resolved on every single request, so access stops at the next one.",
  "admin.engines_issue_revoke_only": "⚠️ The only other way to revoke it is rotating the signing master — and the git, memo and schedule tokens come from that same master, so rotating it logs out everyone on this deployment.",
  "admin.engines_issue_has_workspace_tag": "has a workspace",
  "admin.engines_issue_has_workspace": "⚠️ This membership has a workspace, which is what a person's membership looks like. Lend a person's issuing token and the only way to take it back is the signing-master rotation that logs out everyone on this deployment. Make a separate membership for borrowing and issue for that one.",
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
  "admin.engines_catalog_none_enabled": "No model is enabled. This engine will not start until one is.",
  "admin.engines_model_started": "loaded at start",
  // The state is said in a badge. The button's label says what pressing it would do, not
  // which state the row is in; dimming the row instead is what a disabled control looks like.
  "admin.engines_model_is_on": "enabled",
  "admin.engines_model_is_off": "disabled",
  "admin.engines_model_enable": "Enable",
  "admin.engines_model_disable": "Disable",
  "admin.engines_model_select": "Start with this",
  // 🔴 Choosing another one does NOT swap a running instance. It holds the one chosen at start, and
  // redeploying here would kill a generation in flight (ADR 0072 decision 4). Say so first.
  "admin.engines_model_next_start": "A new choice takes effect at the next start. A running engine is not swapped (that would kill a generation in flight).",
  "admin.engines_model_window": "context {c} / output {o}",
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
  "admin.engines_model_forget_go": "Forget it",
  // Not P4's ingest (which fetches from Hugging Face); just writing down what a file already in
  // the bucket IS. The seed creates one row per role, so without this there is no second
  // checkpoint to switch to without touching CloudFormation.
  "admin.engines_model_add": "Register a file from the bucket",
  "admin.engines_model_add_id": "id",
  "admin.engines_model_add_key": "key",
  "admin.engines_model_add_family": "family",
  // ADR 0072 follow-up, negative prompts. Three places get a say in what a picture keeps out —
  // this row, the request, and the deployment — and they are ADDED, so none of the labels may
  // read as "the" negative prompt.
  "admin.engines_model_negative": "never draw",
  "admin.engines_model_negative_placeholder": "what this checkpoint should keep out",
  "admin.engines_negative_label": "excluded from every image",
  "admin.engines_negative_placeholder": "keywords, separated by commas",
  "admin.engines_negative_save": "Save",
  // 🔴 The sentence that keeps this from being read as a content filter. It is a negative
  // prompt: a nudge to the sampler, absent altogether on the distilled checkpoint families,
  // and no guarantee about what comes out.
  "admin.engines_negative_note":
    "Added to the negative prompt of every image this engine makes, on top of the model's own and the request's. It is guidance, not a filter — two checkpoint families sample without a negative prompt at all, and those requests say so in their warnings.",
  "admin.engines_negative_too_long": "Too long — this is a keyword list, not a policy document.",
  // ADR 0072 decision 5, the llm half. A LoRA is not a model: it is pinned to one, travels in
  // that model's preset section, and is invisible to the member — who sees an ordinary model id
  // that happens to include the fine-tune.
  "admin.engines_model_add_kind": "this row is",
  "admin.engines_model_add_kind_model": "a model",
  "admin.engines_model_add_kind_lora": "a LoRA adapter",
  "admin.engines_model_add_lora_base": "applies to",
  "admin.engines_model_add_lora_base_pick": "choose the model it fine-tunes",
  "admin.engines_model_add_lora_scale": "strength (0-2, default 1)",
  "admin.engines_model_add_lora_note": "A LoRA is loaded with the model it names and with no other, every time that model is started. Nothing appears in the launch menu for it. Its file belongs under {p}.",
  "admin.engines_ingest_family_hint": "The repository calls this \"{n}\". That is a display name, so pick the family it corresponds to above.",
  "admin.engines_model_add_family_pick": "choose one",
  "admin.engines_model_add_part": "part",
  "admin.engines_model_add_part_whole": "checkpoint (single file)",
  "admin.engines_model_add_part_more": "Add a file",
  "admin.engines_model_add_part_drop": "Remove",
  "admin.engines_model_no_family": "No checkpoint family is declared. This engine picks a workflow from the family and will not guess one from a name, so this row can be enabled and will appear as a model — and then fail when something asks it to generate. Choose one below.",
  // 🔴 Declaring a family clears `base_model_missing`; whether the row holds the files that
  // family's template reads is a different question. On the real deployment `flux1-dev` was one
  // unflagged file in `image/checkpoints/` and flux1 reads four others — no answer in the
  // selector could work, and choosing one made the only mark disappear.
  "admin.engines_model_files_missing": "This row does not hold the files the \u201c{n}\u201d workflow reads (missing: {f}). It cannot be enabled until they are taken in and attached to it.",
  "admin.engines_model_add_desc": "description",
  // Optional. This route has no source to read a licence from, so it is the one place a person
  // types one. Left blank, the row says "licence not recorded" rather than showing a gap.
  "admin.engines_model_add_license": "licence (optional)",
  "admin.engines_model_add_license_url": "licence URL (optional)",
  // 🔴 Both halves of the window or neither: with a context and no output cap, opencode reads
  // the cap as 32,000 and a 32k model is left with 768 usable tokens (ADR 0072 decision 3).
  "admin.engines_model_add_ctx": "context window",
  "admin.engines_model_add_out": "output cap",
  // The control plane cannot look in S3, so a declared size is the only source for "sync +N s".
  "admin.engines_model_add_bytes": "size",
  "admin.engines_model_add_go": "Register",
  "admin.engines_model_add_note": "The id is what a member picks, the key is the path inside the bucket, the description is the one line an agent reads. The window and the output cap only count when BOTH are given (one alone is ignored — without a cap it is read as 32,000, which leaves a 32k model 768 usable tokens). The size in bytes is optional and only feeds the \"sync +N s\" estimate. The control plane does not look in S3 (it holds no permission to), so a mistyped key shows up in the fetch log at the next start. The row is created disabled.",
  // --- ingest (ADR 0072 decision 6, phase P4) ---
  // 🔴 Resolve, then accept. An "I agree" offered before the licence and the gating are on
  // screen is not an acceptance, and a gated repository with no token is refused here rather
  // than by a 401 nine minutes into a task.
  "admin.engines_ingest_open": "Take one in from Hugging Face",
  "admin.engines_ingest_search": "find",
  "admin.engines_ingest_search_go": "Search",
  "admin.engines_ingest_search_none": "Nothing found. Try other words, or type the repository name in directly.",
  "admin.engines_ingest_source_hf": "Hugging Face",
  "admin.engines_ingest_source_civitai": "Civitai",
  "admin.engines_ingest_browse_go": "Browse",
  "admin.engines_browse_kind_gguf": "LLM (GGUF)",
  "admin.engines_browse_kind_checkpoint": "Image (checkpoint)",
  "admin.engines_browse_note": "Browsing only. Taking one in needs 60-engines deployed and an engine to stage it into — what is shown here is Hugging Face's public metadata, read without a token and without touching any bucket.",
  "admin.engines_ingest_sort_downloads": "Downloads",
  "admin.engines_ingest_sort_trending": "Trending",
  "admin.engines_ingest_sort_likes": "Likes",
  "admin.engines_ingest_hit_downloads": " downloads",
  "admin.engines_ingest_hit_likes": " likes",
  "admin.engines_ingest_hit_trending": " trending",
  "admin.engines_ingest_hit_gated": "gated",
  // 🔴 What the source says you may not do, before anything is downloaded. The codes are a
  // closed set the CP decides and the words exist only here (engineRestrictLabel).
  // "Login required" is not in Civitai's metadata at all — the CP has to HEAD the download to
  // learn it — so "could not tell" is blank, and is not the same answer as "anyone may".
  "admin.engines_hit_login_required": "login required",
  "admin.engines_hit_login_note": "Civitai hands this file only to a logged-in account. This deployment holds no Civitai credentials, so the ingest would fail with 401.",
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
  // Fills the ingest form from a search card. Not "ingest": it fills, and resolve → accept →
  // ingest still runs from there unchanged.
  "admin.engines_ingest_hit_pick": "Use this",
  // Heads the card the form was filled from, kept after the results list is gone. The link to
  // the page, the trigger words and the licence are on that card and nowhere else, and the
  // repository field below is an id like `civitai:1759168` — so dropping it left no way to
  // check what is about to be taken in.
  "admin.engines_ingest_picked": "Chosen result",
  "admin.engines_ingest_repo": "repository",
  "admin.engines_ingest_file": "file name",
  // With a plain https URL pasted above, this field is the sha256 rather than a file name
  // (listable()). Keeping the label and offering "name.safetensors" asks for the one thing
  // that field must not be given, so the label is swapped with it.
  "admin.engines_ingest_sha256": "sha256",
  "admin.engines_ingest_sha256_ph": "64 hex characters",
  "admin.engines_ingest_resolve": "Look it up",
  // Not having the filename in hand is the normal state, so "look it up" starts by asking the
  // repository what it holds.
  "admin.engines_ingest_pick": "Choose one",
  "admin.engines_ingest_no_files": "This repository offers nothing this engine could load with a sha256.",
  // 🔴 The MODEL's ceiling, not the window this deployment can run. The 30B declares 262144 and
  // does not fit an L4, so it runs at 32768. Never shown without saying whose number it is.
  "admin.engines_ingest_ctx_max": "the model's maximum is {n}",
  // --- generation parameters, read out of the author's own description ---
  // 🔴 A regular expression's guess about somebody else's prose, which is why the sentence it
  // came from is always beside it and a person edits the field before pressing anything.
  "admin.engines_params": "Generation parameters",
  "admin.engines_params_note": "An empty field keeps the family's own recipe. Only what is filled in is replaced for this model.",
  "admin.engines_params_hint_found": "Read out of the author's description (unverified):",
  "admin.engines_params_hint_apply": "Use these",
  "admin.engines_params_none": "Generation parameters: none declared (the family's own recipe)",
  "admin.engines_params_steps": "Steps",
  "admin.engines_params_cfg": "CFG",
  "admin.engines_params_sampler": "Sampler",
  "admin.engines_params_scheduler": "Scheduler",
  "admin.engines_params_clip_skip": "Clip skip",
  "admin.engines_params_weight": "Default strength",
  // A display name ("DPM++ 2M Karras") is accepted: the CP translates it into ComfyUI's own
  // vocabulary and drops what it cannot translate.
  "admin.engines_params_name_ph": "dpmpp_2m",
  "admin.engines_params_clear": "Clear",
  "admin.engines_params_edit": "Parameters",
  "admin.engines_params_save": "Save",
  // 🔴 flux1 / klein fold guidance into the conditioning, so a card's "CFG" is a different knob.
  "admin.engines_params_cfg_ignored": "This family does not use CFG (guidance is a different input).",
  "admin.engines_params_clip_skip_note": "Clip skip is recorded only; no workflow here reads it yet.",
  // The family suggestion. Decision 2 keeps the declaration with the operator, so it is filled
  // in and can be changed.
  "admin.engines_family_suggested": "Guessed from \"{n}\". Pick another if that is wrong.",
  "admin.engines_ingest_go": "Take it in",
  // A split model is not one download (FLUX.1 is a unet, a clip_l, a t5 and a vae). The CP
  // refuses a plain ingest onto an id it already has — that would upsert the row's files,
  // licence and enabled flag away — so the other act is offered here instead.
  "admin.engines_ingest_attach": "Add it to “{id}” as a part (no new row)",
  "admin.engines_ingest_id_taken": "That id is taken. Choose another, or tick “add it as a part” above.",
  "admin.engines_ingest_accept": "I accept this model's licence (on behalf of everyone this deployment serves)",
  "admin.engines_ingest_gated": "A gated repository. It is fetched with the operator's token, which has accepted its terms.",
  "admin.engines_ingest_gated_no_token": "A gated repository, and this deployment has no Hugging Face token. Register the operator's token under \u201cHugging Face token\u201d below — it is read by the ingest task only.",
  // 🔴 A different wall from Hugging Face's gating, and there is no key to it: Civitai answers
  // its metadata 200 for everybody and only the DOWNLOAD is per uploader (five assets measured,
  // split 200/401/403). No token field is being added, so the sentence says what to do instead.
  "admin.engines_ingest_civitai_login": "The person who uploaded this asset only allows downloads from a logged-in account. This deployment ingests anonymously, so it cannot be fetched (a Hugging Face token does not help). Pick another asset, or stage the file in the bucket by hand and register it.",
  // 🔴 401 and 403 on a gated repository are one line of curl apart and need opposite screens:
  // 401 is a token that never reached the ingest task, 403 is a token that did and an account
  // that has not accepted THAT repository (measured: one token, FLUX.1-dev through, SD3.5 403).
  "admin.engines_ingest_job_not_accepted": "The token reached the task, and that account has not accepted this repository's terms yet. Accept them on the Hugging Face model page and take it in again.",
  "admin.engines_ingest_job_no_token": "No token reached the ingest task. Register the operator's token under \u201cHugging Face token\u201d below and take it in again.",
  "admin.engines_ingest_gated_accept_first": "A gated repository. A token is registered, but whether that account has accepted this repository's terms is something the Control Plane cannot check (it resolves anonymously). If it has not, the ingest fails with a 403 — so accept them on the Hugging Face model page first.",
  "admin.engines_hf_token": "Hugging Face token",
  "admin.engines_hf_token_field": "Token",
  "admin.engines_hf_token_save": "Register",
  "admin.engines_hf_token_remove": "Remove",
  "admin.engines_hf_token_unset": "Not registered. Only ungated repositories can be taken in.",
  "admin.engines_hf_token_set": "Registered ({who} / {when}). The value cannot be shown — the Control Plane can write it and has no permission to read it back.",
  "admin.engines_hf_token_stack": "This deployment's token comes from a CloudFormation parameter. It cannot be registered or removed from the Console, but gated repositories can be taken in.",
  "admin.engines_hf_token_unsupported": "This deployment's engine stack has nowhere to keep a token. Update 60-engines and it can be registered from here.",
  "admin.engines_hf_token_note": "One token for the whole deployment. It is stored encrypted and written into the deployment's secret before every ingest — read by the ingest task only, and never handed to an engine instance.",
  "admin.engines_ingest_noncommercial":
    "🔴 A non-commercial licence. Both commercial use of the model and commercial use of what it generates may be restricted — read the licence before enabling this.",
  "admin.engines_ingest_note": "A repository name (`owner/name`) or a pasted model-page URL both work. The sha256, the size and the licence are read from that source's own API by the control plane; the download is the ingest task's, which is also the only thing that touches S3 or the token. A row that arrives is created disabled.",
  // 🔴 A log of EVENTS, not the catalogue. A job stays after its model is gone ("this ingest
  // ran and finished" goes on being true), so it is headed and dated and reads as history.
  // Undated, a "done" beside a deleted model's id reads as that model's current state.
  "admin.engines_ingest_jobs_head": "Ingest history",
  "admin.engines_ingest_state_pending": "starting",
  "admin.engines_ingest_state_running": "fetching",
  "admin.engines_ingest_state_done": "done",
  "admin.engines_ingest_state_failed": "failed",
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
  "admin.engines_class_pending": "What is running is a {t} instance. The class you chose applies to the NEXT instance. Replacing it costs one cold start (about 9 minutes for llm, 3 for image), and the new instance does not start until the old one has left.",
  "admin.engines_class_replace": "Replace it now",
  // 🔴 The choice is SAVED before it is applied, so a failed apply leaves the picker showing a
  // class the capacity provider does not hold — and picking it again is no change at all. The
  // retry has to be a button of its own, or the only way out is a detour through another class.
  "admin.engines_class_apply_failed": "The class was saved, but writing it to the capacity provider failed, so the next instance is still bought on the previous one: {m}",
  "admin.engines_class_apply_retry": "Apply it again",
  "admin.engines_class_vram_ok": "The largest enabled model is {id} at {n} MiB ({src}); this class has {m} MiB.",
  "admin.engines_class_vram_over": "The largest enabled model is {id} at {n} MiB ({src}) and this class has {m} MiB. It may not fit.",
  "admin.engines_class_vram_unknown": "How much VRAM the enabled models need is not known — nobody measured it. That is not the same as saying they fit.",
  // ⚠️ The comparison above is a maximum, not a sum, because one model is in VRAM at a time.
  // That is exactly true of llm and sd-server and CONSERVATIVE of comfy, which picks a
  // checkpoint per request and keeps loaded ones cached — so this sentence is added there
  // rather than the number being turned into a sum nobody would read past.
  "admin.engines_class_vram_many": "This engine chooses a checkpoint per request and keeps loaded ones in VRAM, so several can be resident at once — the figure above is the largest ONE of them.",
  "admin.engines_vram_src_declared": "measured",
  "admin.engines_vram_src_floor": "a weights-only floor",
  "admin.engines_vram_src_unknown": "unknown",
  "admin.engines_vram_confirm": "{id} wants {n} MiB ({src}) and the class you have chosen has {m} MiB. Short VRAM does not slow CUDA down, it crashes it. Quantisation or offloading may still fit it — continue if you know that.",
  "admin.engines_vram_confirm_go": "Enable it anyway",
  "admin.engines_model_vram": "VRAM {n} MiB",
  "admin.engines_model_vram_floor": "VRAM at least {n} MiB (a weights-only floor)",
  // The licence facts a row can carry (ADR 0072 decision 10). "Not recorded" is stated rather
  // than left blank: a seeded row cannot know a licence and the hand-registration form does not
  // ask, and a gap where every ingested row names one reads as "no restrictions".
  "admin.engines_model_noncommercial": "non-commercial",
  "admin.engines_model_license_by": "accepted by {who} / {when}",
  "admin.engines_model_license_unknown": "licence not recorded",
  // An ESTIMATE, and it says so: S3 to the instance was measured at 104–147 MB/s and this
  // uses the slow end. For a router role every enabled model is synced, so this really is what
  // enabling it adds to the next cold start.
  "admin.engines_model_sync": "sync +{n} s (est.)",
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
    "Sampled about every {n} seconds. \"An instance existed\" includes the cold start, when it cannot answer yet (165-197 s measured), and the drain, when the task is gone but the instance is not (427-477 s measured) — both bill, neither answers a request. Hours from before recording began stay blank and cannot be filled in later. This is not money.",
  "admin.group_tenants": "Tenants",
  "admin.group_deployment": "Deployment",
  "admin.group_across": "Across tenants",
  "admin.all_tenants_back": "All tenants",
  "admin.tab_register": "Sign-in method register",
  "admin.destroy_ws": "Destroy workspace",
  "admin.destroy_title": "Destroy {key}'s workspace?",
  "admin.destroy_confirm": "Destroy",
  "admin.destroy_body": "This deletes their home and everything the runtime created for them — permanently. There is no undo, and re-inviting them gives them an empty workspace.",
  "admin.destroy_locks": "It also overrides any deletion locks they set: those live inside the home, which cannot be read while the workspace is stopped.",
  "admin.destroy_efs": "On the Fargate runtime the EFS directory behind the home survives and keeps billing; anything left over is listed afterwards and written to the audit log.",
  "admin.destroy_leftovers": "Destroyed, but these could not be deleted: {list}",
  "admin.remove_purge": "Also destroy their workspace and home (irreversible)",
  "admin.remove_purge_warn": "Their home and everything the runtime created for them will be deleted. Re-inviting will not bring it back.",
  // The third and last step of the clean-up (docs/log/61 §61.18); offered only once the
  // workspace has been destroyed.
  "admin.delete_member_row": "Delete this member",
  "admin.delete_member_row_title": "Delete {key} from the roster for good?",
  "admin.delete_member_row_confirm": "Delete for good",
  "admin.delete_member_row_body": "Deletes this person's row, including the record that they were removed. This cannot be undone; inviting them again starts a brand new member.",
  "admin.delete_member_row_gone": "Deleted: quotas, access tokens, SSM settings, schedules, memos, notifications and session shares.",
  "admin.delete_member_row_kept": "Kept: the audit log, cloud cost and occupancy. Past records and invoices are not rewritten.",
  // Deleting a tenant (super_admin; empty tenants only)
  "admin.delete_tenant": "Delete tenant",
  "admin.delete_tenant_title": "Delete this tenant",
  "admin.delete_tenant_hint": "Only an empty tenant can be deleted. It is refused while a member is still on the roster, a workspace row still exists, or an internal git repository is still there — the database row is the only handle left on a resource that lives in the cloud or on disk.",
  "admin.delete_tenant_repo_hint": "⚠️ Delete the internal git repositories while a member is still on the roster: once the last one is removed, nobody can reach the screen that deletes them.",
  "admin.delete_tenant_confirm_title": "Delete the tenant {slug}?",
  "admin.delete_tenant_confirm": "Delete",
  "admin.delete_tenant_body": "Deletes the tenant's settings (quotas, login rules, source-network restriction, sign-in methods, MCP distribution) and the rows of members already removed. This cannot be undone.",
  "admin.delete_tenant_kept": "The audit log, cloud cost and occupancy are kept (their tenant column will be blank).",
  // --- Tenant-distributed MCP servers (docs/log/48 P4, AdminTab's McpAdminView) ---
  "admin.mcp_intro":
    "MCP servers distributed to every member of the tenant. Only remote (Streamable HTTP) servers can be distributed — a stdio server cannot, because distributing a command is equivalent to running arbitrary code in every member's container.",
  "admin.mcp_distributed": "Distributed MCP servers",
  "admin.mcp_none": "No MCP servers are distributed.",
  "admin.mcp_add": "Distribute an MCP server",
  "admin.mcp_edit_title": "Edit distribution",
  "admin.mcp_save_add": "Distribute",
  "admin.mcp_disabled": "Disabled",
  "admin.mcp_user_secret_badge": "Member-supplied value",
  "admin.mcp_secret_policy": "Credentials",
  "admin.mcp_user_secret": "Each member enters the credential",
  "admin.mcp_headers_hint":
    "Values are stored encrypted and distributed to every member. Put a bearer token in the Authorization header.",
  "admin.mcp_headers_names_hint":
    "Header names only — each member fills in the values in their own workspace.",
  "admin.mcp_user_secret_hint":
    "Only the endpoint and the header names are distributed; each member enters the values in their own workspace. A value distributed here is readable in plaintext inside every member's container.",
  "admin.mcp_url_hint": "The MCP endpoint URL. Put credentials in a header, not in the URL.",
  "admin.mcp_enabled_hint": "Disabling keeps the definition but stops distributing it to anyone.",
  "admin.mcp_restart_note":
    "Member workspaces fetch this every 5 minutes, and it takes effect in sessions started after that.",
  "admin.mcp_del_title": "Remove distribution",
  "admin.mcp_del_body":
    "Removes the distribution of {name}. It disappears from each member's workspace at their next fetch.",
  "admin.crumb_tenants": "Tenants",
  "admin.tenant": "Tenant",
  "admin.all_tenants": "All tenants",
  "admin.search": "Search",
  "admin.refresh": "Refresh",
  "admin.unknown": "(unknown)",
  "admin.load_error": "Can't load.",
  "admin.search_ph_sessions": "User / label / repository",
  "admin.running_count": "{n} running",
  "admin.no_running_sessions": "No running sessions.",
  "admin.no_matching_sessions": "No matching sessions.",
  "admin.search_ph_audit": "Action / target / user",
  "admin.count_items": "{n} items",
  "admin.no_audit": "No audit log yet.",
  "admin.no_matching_audit": "No matching log entries.",
  "admin.mode_label": "Mode",
  "admin.egress_enforce_note": "enforce: blocks traffic not on the allowlist. Confirm reality in log-only first before switching.",
  "admin.egress_logonly_note": "log-only: observes only, blocks nothing. Firm up the allowlist before moving to enforce.",
  "admin.egress_proposed": "Proposed (needs approval)",
  "admin.approve": "Approve",
  "admin.reject": "Reject",
  "admin.egress_allowlist": "Allowlist (added)",
  "admin.egress_entry_ph": "host or .suffix.example.com",
  "admin.egress_reason_ph": "Reason (optional)",
  "admin.add": "Add",
  "admin.egress_no_entries": "No added allow entries (only the product defaults are active).",
  "admin.retire": "Retire",
  "admin.egress_observed": "Observed destinations",
  "admin.period": "Period",
  "admin.days_1": "1 day",
  "admin.days_7": "7 days",
  "admin.days_30": "30 days",
  "admin.egress_no_records": "No records (egress proxy not configured, or no traffic in the period).",
  "admin.egress_allowed": "{n} allowed",
  "admin.egress_blocked": "blocked",
  "admin.egress_blocked_candidate": "would block",
  "admin.tts_running": "Running",
  "admin.tts_starting": "Starting (getting ready)",
  "admin.tts_running_waiting": "Running (awaiting response)",
  "admin.tts_stopped": "Stopped",
  "admin.tts_stopped_or_off": "Stopped / not started",
  "admin.tts_stopping": "Stopping",
  "admin.tts_engine_label": "VOICEVOX engine (Zundamon)",
  "admin.tts_mode_off": "Disabled",
  "admin.tts_mode_ondemand": "On demand",
  "admin.tts_mode_on": "Always on",
  "admin.enable": "Enabled",
  "admin.disable": "Disabled",
  "admin.tts_engine_prefix": "Engine: ",
  "admin.tts_managed": " (ECS-managed)",
  "admin.tts_external": " (externally managed: standalone docker, etc.)",
  "admin.tts_polly_sep": " / Polly: ",
  "admin.tts_polly_ready": "Available",
  "admin.tts_polly_unset": "Not set",
  "admin.tts_starting_note": "Startup takes 1–2 minutes. Until it's ready, Japanese read-aloud is covered by Polly (silent if Polly isn't set).",
  "admin.tts_stopping_note":
    "Disabled. Read-aloud has already moved to Polly; the engine itself stops in about a minute. That grace is there so a mis-click — or turning it straight back on — doesn't pay for another 2 GB pull and 70–80 seconds of startup. Switch it back on within it and nothing stops or restarts at all.",
  "admin.tts_ondemand_note":
    "On demand: the engine starts once read-aloud demand builds up (2,000 characters in 5 minutes) and stops after 30 minutes with nobody listening. Japanese is read by Polly until it is up. Every automatic start and stop is recorded in the audit log.",
  "admin.tts_no_engine":
    "This deployment has no VOICEVOX engine, and it is not ECS-managed either, so there is nothing this screen could start. Enabling it would route nothing to Zundamon, so the toggle is held at disabled. Provide an engine and it becomes operable again on its own.",
  "admin.tts_disable_note":
    "Disabling sends read-aloud to Polly at once and, on AWS, sets the ECS desired count to 0 about a minute later to stop the engine (no cost while stopped). Read-aloud itself is turned on/off in the user setting (Read aloud).",
  "admin.tts_dict_title": "Tenant-wide reading dictionary",
  "admin.saving": "Saving…",
  "admin.tts_dict_ph": "spelling=reading (one per line)\ne.g. agent-fleet=エージェントフリート\n# comment line",
  "admin.tts_dict_note":
    "A shared dictionary applied to every user's read-aloud (one “spelling=reading” per line; lines starting with # are comments). If a user's own reading dictionary (Read-aloud tab) has the same spelling, that user's entry wins. After saving, other users pick it up on the next Console load.",
  "admin.usage_load_error": "Failed to load.",
  // --- Cloud cost (docs/log/67 + ADR 0048) ---
  // ⚠️ Deliberately NOT called "Usage". Three surfaces already carry that name (agent
  // tokens, and workspace running time in two places). This one is money, and it only
  // exists where there is an AWS bill.
  "admin.mode_cost": "Cloud cost",
  "tenant.tab_cost": "Cloud cost",
  "admin.usage_title": "Running time (workspace occupancy)",
  "admin.usage_intro":
    "Infrastructure occupancy = the total time workspaces were running (Claude usage fees are on each person's own subscription and not included here). Sampled about every 5 minutes, so there's some error.",
  "admin.from": "From",
  "admin.to": "To",
  "admin.apply": "Apply",
  "admin.total_running": "Total running",
  "admin.members": "Members",
  "admin.range": "{from} – {to}",
  "admin.usage_no_records": "No running records for this period.",
  "admin.tenants_list": "Tenants",
  "admin.new_tenant": "New tenant",
  "admin.no_tenants": "No tenants. Create one from “New tenant”.",
  "admin.member_count_title": "Number of members",
  "admin.person_count": "{n}",
  "admin.running_ws_title": "Running workspaces",
  "admin.running_ws": "{n} running",
  "admin.tenant_limits": "Limits — Workspace: {ws} / Session: {ss}",
  "admin.create_failed": "Failed to create: {msg}",
  "admin.slug_ph": "slug (alphanumeric)",
  "admin.display_name_ph": "Display name (optional)",
  "admin.create": "Create",
  "admin.limits": "Limits",
  "admin.zero_unlimited": "0 = unlimited",
  "admin.max_workspace": "Max workspaces",
  "admin.max_workspace_note": "running at once",
  "admin.max_session": "Max sessions",
  "admin.max_repos": "Max internal repositories",
  "admin.max_lfs": "Max LFS size",
  "admin.max_ws_mem": "Per-workspace memory cap",
  "admin.per_container": "= {hint} / container",
  "admin.zero_no_tenant_cap": "0 = no tenant cap",
  "admin.ws_mem_hint_1": "“Per-workspace memory cap” is the ceiling of memory assignable to one container (each user setting within the tenant is clamped to this range). 0 = no tenant cap (only the deploy default ",
  "admin.ws_mem_hint_2": " and, if set, the host ceiling ",
  "admin.ws_mem_hint_3": "). Set each allocation in the member detail, and it ",
  "admin.ws_mem_hint_bold": "takes effect on the next container start / recreate",
  "admin.ws_mem_hint_4": ".",
  "admin.idle_autostop": "Idle auto-stop",
  "admin.idle_stop_in": "Stops in {left}",
  "admin.idle_heading": "Idle-stop outlook",
  "admin.idle_stop_at_paren": " ({at})",
  "admin.idle_stopping_soon": "Stopping on the next sweep.",
  "admin.idle_held": "Held open by:",
  "admin.idle_hold_working_row": "session {session} is running a turn",
  "admin.idle_hold_background_row": "session {session} has background work (run_in_background / subagents)",
  "admin.idle_hold_repojob_row": "a repository import is running (clone / svn checkout)",
  "admin.idle_hold_pin_row": "session {session} has a keep-awake pin ({left} left)",
  "admin.idle_hold_watching_row": "someone is working here (recent typing or Console interaction)",
  "admin.idle_observed": "As observed at {at} (may lag by up to one sweep interval)",
  "admin.idle_stop_at": "Scheduled idle-stop: {at} (the reaper's latest read)",
  "admin.idle_off": "Idle-stop off",
  "admin.idle_off_hint": "Workspace idle-stop is disabled for this tenant (ws_idle_timeout = 0).",
  "admin.idle_hold_working": "Held: a turn is running",
  "admin.idle_hold_background": "Held: background work",
  "admin.idle_hold_repojob": "Held: repository import",
  "admin.idle_hold_pin": "Held: keep-awake pin",
  "admin.idle_hold_watching": "Held: someone is working here",
  "admin.idle_hold_more": " +{n} more",
  "admin.empty_deploy_default": "empty = follow the deploy default",
  "admin.session_halt": "Session halt after",
  "admin.ws_stop": "Workspace stop after",
  "admin.interaction_halt": "Halt when awaiting a decision",
  "admin.interaction_ph": "empty = same as session",
  "admin.interaction_hint": "\"Awaiting a decision\" = a question, a plan awaiting approval, a permission prompt, the usage-limit menu, an expired login. These hold a container up until someone answers, so they get their own clock. Nothing is lost when one is folded away — the answer is delivered on resume from the card in the mirror.",
  "admin.idle_ph_30m": "e.g. 30m (empty = the deploy default, 1h)",
  "admin.idle_ph_60m": "e.g. 60m (empty = the deploy default, 2h)",
  "admin.idle_hint_1": "Idle claude sessions are folded to stopped (resumable) after Session halt after, and workspaces with no connection or activity are docker-stopped after Workspace stop after. Format: ",
  "admin.idle_hint_2": ". Empty follows the deploy default (1h for a session, 2h for the workspace); ",
  "admin.idle_hint_3": " explicitly disables it.",
  // Home hibernation (AF_RUNTIME=ecs-ec2 only; ADR 0045 決定 13-2). This is the one
  // setting that moves a user's home off the disk it was on, so the copy has to say both
  // that it is reversible and that the return is slower.
  "admin.hibernate_title": "Hibernate unused homes",
  "admin.hibernate_after": "Hibernate after",
  "admin.hibernate_ph": "e.g. 720h = 30 days (empty = deploy default)",
  "admin.hibernate_hint":
    "A home nobody has opened for this long is captured as a snapshot and its disk released. The next start brings it back, so nothing is lost — but that start takes longer, and the disk is slower for a few hours afterwards.",
  "admin.hibernate_warn":
    "Only hibernation is automatic; nothing is ever destroyed this way. The unit goes up to hours, so write days as a multiple of 24h. Enter 0 to never hibernate this tenant's homes.",
  // Home backups (ADR 0045 決定 17). Losing a whole AZ is a story only this runtime has,
  // so lead with why it exists. Say "how far back" rather than "RPO".
  "admin.backup_title": "Keep a spare copy of each home",
  "admin.backup_every": "Copy every",
  "admin.backup_ph": "e.g. 24h (empty = deploy default)",
  "admin.backup_hint":
    "A home lives inside one Availability Zone, and losing that zone loses the home with it. A spare copy is kept outside the zone, so the home can be rebuilt from it. What you are choosing here is how far back the worst case may throw someone.",
  "admin.backup_warn":
    "The copy is taken while the home is in use, so it is the same picture a power cut would leave. It is never restored automatically — that is an operator decision. Enter 0 to take no copies for this tenant.",
  "admin.term_log_title": "Terminal-log retention",
  "admin.retention": "Retention",
  "admin.retention_off": "Disabled (standard short-lived history only)",
  "admin.term_log_hint":
    "When enabled, output shown in the terminal is saved to the workspace's persistent area. It doesn't record keystrokes themselves, but command output may contain sensitive information. Changes take effect from the next workspace start.",
  "admin.agent_cli_update": "Agent CLI updates",
  "admin.allow_self_update": "Allow members to update the agent CLIs and rtk themselves",
  "admin.allow_self_update_hint":
    "Covers claude / opencode / codex / Copilot / Antigravity (agy) / rtk. OFF (default) pins everyone to this deploy's image versions. ON lets each member choose “update to the latest on start” in their own settings (in-container in-place update; applied on Stop → Start, reversible).",
  "admin.engine_ingest_title": "Inference engine model ingest",
  "admin.allow_engine_ingest": "Allow this tenant's administrators to take models in",
  "admin.allow_engine_ingest_hint":
    "OFF (default) means only a super_admin can start an ingest. ON lets this tenant's tenant_admins take models in from Hugging Face / Civitai / a URL. Enabling a model, changing the selected checkpoint, forgetting a row and the deployment's Hugging Face token stay super_admin. The catalogue is one per deployment, so the id of a model taken in is visible from every tenant.",
  "admin.saved": "Saved",
  "admin.no_members": "No members. Add one from the form below.",
  "admin.add_failed": "Failed to add: {msg}",
  "admin.add_member": "Add member",
  "admin.or_user_key": "or user_key",
  "admin.checking": "Checking…",
  "admin.running_state": "Running",
  "admin.stopped_state": "Stopped",
  "admin.super_admin_deploy_title": "super_admin (whole deployment)",
  "admin.tenant_admin_paren": " (tenant admin)",
  "admin.ws_resources": "Workspace resources",
  "admin.ws_stopped": "The workspace is stopped{suffix}.",
  "admin.ws_stopped_disk_suffix": " (showing disk usage only)",
  "admin.res_memory": "Memory",
  "admin.res_disk": "Disk",
  "admin.disk_home_sub": "(home usage)",
  "admin.cpu_sub": "1 core = 100%",
  "admin.sessions_heading": "Sessions",
  "admin.no_sessions": "No sessions",
  "admin.permissions": "Permissions",
  "admin.super_admin_note_1": "This user is a deployment-wide super_admin (managed via env ",
  "admin.super_admin_note_2": ").",
  "admin.tenant_admin_role": "Tenant admin (tenant_admin)",
  "admin.revoke_admin": "Revoke admin",
  "admin.make_admin": "Make an admin of this tenant",
  "admin.tenant_admin_hint_1": "A tenant admin can manage members, view resources, force-stop workspaces, and set session limits within ",
  "admin.tenant_admin_hint_2": " — including cleaning a home, discarding a workspace, and setting a member's size and session limit (they can't create tenants, change the tenant-wide limits, or grant roles).",
  "admin.operations": "Operations",
  "admin.force_stop_ws": "Force-stop the workspace",
  "admin.clean_home": "Clean home",
  "admin.ws_cpu": "Workspace CPU",
  "admin.ws_disk": "Workspace working disk",
  "admin.ws_size_preset": "Size",
  "admin.ws_size_custom": "Custom",
  "admin.zero_deploy_default_cpu": "0 = deploy default",
  "admin.ws_disk_hint": "0 = default 20 GiB (free tier)",
  "admin.ws_disk_warn": "The working disk is wiped when the workspace stops. Only the home directory persists.",
  "admin.ws_cpu_vcpu": "= {n} vCPU",
  "admin.ws_mem_req": "Workspace memory (required)",
  "admin.ws_slot_lands": "→ {type} ({spec}, dedicated)",
  "admin.ws_slot_zero": "0 = smallest slot ({type})",
  "admin.ws_slot_usable": "{n} (of {box})",
  "admin.ws_slot_note": "The slot is used by one person and the task reserves nothing, so the whole instance is available — this number only chooses which instance.",
  "tenant.machine_title": "Default machine",
  "tenant.machine_note":
    "Which machine this tenant's members land on when they have no choice of their own. A per-member choice is made from the member's page and wins over this.",
  "tenant.machine_deploy_default": "Deployment default",
  "tenant.machine_member_note":
    "Members who chose a machine themselves are unaffected. The change reaches each member at their next workspace start.",
  "admin.roster_spec": "{n} vCPU / {mem}",
  "admin.roster_disk": "{n} GB disk",
  "admin.ws_machine": "Machine",
  "admin.ws_machine_tenant_default": "Tenant default",
  "admin.ws_machine_arch_warn":
    "This machine has a different CPU family. On the next start the home reinstalls the tools that were built for the old one (the agent CLIs, node, Chromium — a few minutes). Anything under ~/repos is left alone, so node_modules / target / .venv survive but will not run until you reinstall them yourself.",
  "admin.ws_cpu_na": "CPU is not selectable on this runtime: a workspace gets the whole instance.",
  "admin.ws_disk_home": "Workspace home (persistent)",
  "admin.ws_disk_home_hint": "0 = deployment default {n} GiB. Applied when the home volume is created, and it cannot be shrunk afterwards.",
  "admin.ws_disk_quota_hint": "0 = no quota. Reported for reference only — nothing enforces it.",
  "admin.ws_disk_work_hint": "0 = deployment default {n} GiB",
  // --- Size and limits (ADR 0045 addendum). Its own card, out of "Operations": what it
  // used to sit next to was cleaning a home and removing a member, which put changing a
  // setting in the same row as the actions that cannot be taken back. ---
  "admin.ws_size_heading": "Size and limits",
  "admin.ws_size_change": "Change",
  "admin.ws_size_unset": "All deployment defaults",
  "admin.ws_size_group": "Workspace size",
  "admin.session_limit_group": "Session limit",
  "admin.danger_zone": "Cannot be undone",
  // How to say it on a runtime whose home can grow (ecs-ec2). Only the raising direction
  // reaches the home that exists; lowering reaches the next one. EBS's rule, not a policy.
  "admin.ws_disk_home_grow_hint":
    "0 = deployment default {n} GiB. Raising this grows the home they already have. Lowering it leaves that home as it is — EBS cannot shrink.",
  "admin.home_resize_growing": "Growing the home from {from} to {to} GiB. The workspace keeps running.",
  "admin.home_resize_shrink":
    "The existing home stays at {from} GiB — EBS cannot shrink. {to} GiB is what the next home created for this member will be.",
  "admin.home_resize_no_home": "There is no home yet. The next one created will be {to} GiB.",
  "admin.home_resize_failed":
    "The home could not be grown. The setting is saved, so try saving again later — one volume can only be modified once every 6 hours: {detail}",
  "admin.max_sessions_label": "Max sessions",
  "admin.ws_memory": "Workspace memory",
  "admin.eq_hint": "= {hint}",
  "admin.zero_deploy_default": "0 = deploy default",
  "admin.mem_clamp_1": "Memory is clamped to the tenant cap and ",
  "admin.mem_clamp_2": " (running containers aren't updated immediately).",
  "admin.stop_ws_title": "Stop {key}'s workspace",
  "admin.stop_confirm": "Stop",
  "admin.stop_body": "This stops this member's {slug} workspace container.",
  "admin.clean_title": "Clean {key}'s home",
  "admin.clean_confirm": "Clean",
  "admin.clean_body": "This cleans this user's workspace home. The container is stopped.",
  "admin.clean_keep": "Kept: connections / git auth / Claude・Codex logins",
  "admin.clean_delete": "Deleted: repos (incl. uncommitted) / caches / everything else under home",
  "admin.grant_title": "Make {key} an admin of {slug}",
  "admin.grant_confirm": "Make admin",
  "admin.grant_body_1": "This grants this member tenant-admin rights for ",
  "admin.grant_body_2": ".",
  "admin.grant_note": "After granting, they can manage members, view resources, force-stop and discard workspaces, clean a home, and set a member's size and session limit within this tenant (other tenants are unaffected).",

  // --- per-tenant login (docs/log/61 §61.9 · P3). The three rules look alike and are
  // not: reading "invite domains" as "domains that may use this tenant" is the
  // mistake that breaks the operation. ---
  "admin.login_rules": "Login rules",
  "admin.login_rules_note": "empty = no restriction",
  "admin.auto_join_domains": "Auto-join domains",
  "admin.auto_join_domains_unit": "joins this tenant on first sign-in",
  "admin.invite_domains": "Invite domains",
  "admin.invite_domains_unit": "a guard on adding members only",
  "admin.login_rules_hint":
    "Invite domains apply only when adding a member. Someone already on the roster keeps working even from another domain (use \"Remove member\" below to take them off). " +
    "An auto-join domain can belong to only one tenant.",
  "admin.login_url": "Sign-in URL for this tenant:",

  // --- the deployment's methods = the default tenant's methods (docs/log/61 §61.17).
  // Since P7-0 they appear as "deployment-wide" rows in every tenant's sign-in
  // method list. The display name leads; the id is shown next to it in <code>. ---
  "admin.providers_none": "This deployment has no sign-in method configured (the login page shows no buttons).",
  // ★ "none" and "could not read" must never share a string. The 403 used to collapse
  // into an empty array, which told an unauthorized reader the deployment was
  // unconfigured (docs/log/61 §61.17.9 ②).
  "admin.providers_unreadable": "Could not load the list of sign-in methods — you may not have permission, or it is temporarily unavailable.",

  // --- tenant-defined sign-in methods (docs/log/61 §61.11 · P4), for a group whose
  // subsidiaries each have their own Entra tenant. The tenant admin writes the
  // definition, the deployment admin activates it (決定 30) — that asymmetry is the
  // feature. ---
  "admin.idp_title": "Sign-in methods this tenant can use",
  "admin.idp_note": "activating a method of your own needs a deployment administrator",
  "admin.idp_hint":
    "Every way into this tenant: the deployment-wide methods, plus any method registered for this tenant alone. " +
    "Register your own IdP (Entra ID / Okta / Keycloak …), or a GitHub organization, under \"Add a sign-in method\". " +
    "A new method starts as \"waiting for approval\": until a deployment administrator approves it, no button appears on the sign-in page and no one can sign in with it.",
  "admin.idp_none": "This tenant has no method of its own yet (the deployment-wide ones above still work).",
  "admin.idp_add": "Add a sign-in method",
  // --- the two per-row toggles (docs/log/61 §61.17.5). The DB still stores two CSV
  // columns; only the screen changed. ★ "Show" is subordinate to "Accept" — a
  // method that is not accepted never appears, however this is set. ---
  "admin.idp_accept": "Accept",
  "admin.idp_show": "Show button",
  "admin.idp_deployment_wide": "Deployment-wide",
  "admin.idp_accept_last":
    "The last one cannot be cleared. Clearing them all means \"no restriction — accept every method\", so you would open it up while meaning to narrow it.",
  "admin.idp_show_last":
    "The last one cannot be cleared. Hiding every button would leave a sign-in page with no buttons, so the setting is ignored instead.",
  "admin.idp_show_needs_accept": "Not accepted, so it never appears on the sign-in page.",
  "admin.idp_approve": "Approve and activate",
  "admin.idp_suspend": "Suspend",
  "admin.idp_reapply": "Request approval",
  "admin.idp_state_pending": "Waiting for approval",
  "admin.idp_state_active": "Active",
  "admin.idp_state_suspended": "Suspended",
  "admin.idp_state_broken": "Approved, but the settings are incomplete",
  "admin.idp_name": "Name",
  "admin.idp_name_hint": "Identifier within this tenant (a-z, 0-9, - _). For example: entra",
  "admin.idp_kind": "Kind of sign-in",
  "admin.idp_kind_hint":
    "Your own IdP (OIDC), or a GitHub organization. GitHub is one issuer shared by every tenant, so \"which organization are they a member of\" is what makes a sign-in yours.",
  "admin.idp_kind_oidc": "Our own IdP (Entra ID / Okta / Keycloak …)",
  "admin.idp_kind_github": "A GitHub organization",
  "admin.idp_orgs": "Allowed GitHub organizations",
  "admin.idp_orgs_hint":
    "Comma-separated (required). Active membership in one of them is what authorizes a sign-in. " +
    "If an organization restricts third-party OAuth apps, everyone is denied until an organization owner approves this OAuth app.",
  "admin.idp_github_app_hint":
    "Create an OAuth App for this tenant in GitHub, add {url} as its callback URL, then enter its client ID and secret here.",
  "admin.idp_github_domains_note":
    "GitHub hands over exactly one address it has verified. Someone whose primary GitHub address is outside your company domain should be stopped here — letting them through lands them in a NEW workspace rather than their existing one.",
  "admin.idp_issuer": "Issuer URL",
  "admin.idp_issuer_hint": "The IdP's issuer URL. For Entra ID, use the URL containing your own tenant GUID (common / organizations require tenant ids below).",
  "admin.idp_client_id": "Client ID",
  "admin.idp_client_secret": "Client secret",
  "admin.idp_secret_hint": "Encrypted when saved, and never shown again.",
  "admin.idp_secret_kept": "Leave empty to keep the stored value.",
  "admin.idp_trust": "How the email is trusted",
  "admin.idp_trust_hint": "Why the email this IdP asserts can be believed. Entra ID never sends email_verified, so pick the pinned-issuer rule.",
  "admin.idp_trust_issuer": "The issuer is pinned to our own tenant",
  "admin.idp_trust_email": "The IdP asserts email_verified",
  "admin.idp_domains": "Email domains to admit",
  "admin.idp_domains_hint":
    "The domains that may sign in with this method (required). It cannot be empty: this method does not fall back to the deployment-wide allowlist, so an empty list admits nobody. " +
    "One domain can belong to only one tenant.",
  "admin.idp_tids": "Allowed tenant ids (Entra tid, optional)",
  "admin.idp_tids_hint": "Comma-separated. Required when the issuer is common / organizations.",
  "admin.idp_link_claim": "How the same account is recognised",
  "admin.idp_link_claim_none": "Default (recognise by sub)",
  "admin.idp_link_claim_hint":
    "Use this when the same issuer has more than one app registration. Entra's sub differs per app registration, so one person pressing head office's button and this one looks like two accounts. Picking oid makes them one. Only values the IdP assigns can be picked — never one somebody can assert, such as an email address. Changing it sends the row back for approval.",
  "admin.idp_label_ja": "Button label (Japanese)",
  "admin.idp_label_en": "Button label (English)",
  "admin.idp_repend_hint":
    "Changing the issuer, the client ID, the trust rule, the kind or how the same account is recognised — or adding a domain, tenant id or GitHub organization — sends the method back for approval, " +
    "because the approval was given to that identity source for that scope.",
  // ★ P7-1 (docs/log/61 §61.17.6) removed the "has no effect on the plain /login"
  // workaround. What is left is the one misreading worth heading off: hidden ≠ gone.
  "admin.hidden_still_accepted_note":
    "★ A method without a button is still accepted. People signing in with it — someone who also belongs to another tenant, typically — keep getting in; it simply stops appearing on this tenant's sign-in page.",
  "admin.allowed_providers_shared_note":
    "★ Narrowing this to your own methods locks out people who also belong to another tenant and sign in there: an account at a different IdP is a different login, even with the same address. Leave the method those people use on \"Accept\" and just clear \"Show button\", so it stays usable without appearing here. Accepting a method does not widen who can enter — the roster decides that.",
  "admin.login_rules_methods_moved":
    "★ Which sign-in methods this tenant accepts, and which of them get a button on the sign-in page, are set per row under \"Sign-in methods\".",
  // ★ The suspend ordering guard (docs/log/61 §61.17.4). A confirmation, not a refusal —
  // suspending is also how a compromised IdP is stopped, and stopping is always
  // allowed to be faster than starting. The count comes from the CP's own message.
  "admin.idp_suspend_title": "Suspend {name}",
  "admin.idp_suspend_body":
    "Have those people link another sign-in method first (Settings → Personal → Account). " +
    "After you suspend it they cannot add one themselves — linking needs a session, and this is the method they sign in with.",
  "admin.idp_suspend_members":
    "{n} active member(s) have never used any other sign-in method. Suspending this one locks them out.",
  "admin.idp_delete_title": "Delete {name}",
  "admin.idp_delete_body":
    "This removes the sign-in method. People who used it can no longer sign in, but their workspaces, homes and stored credentials are kept.",
  "admin.idp_register": "Tenant-defined sign-in methods",
  "admin.idp_pending_count": "{n} waiting for approval",
  "admin.idp_register_none": "No tenant has defined a sign-in method yet.",
  "admin.idp_register_hint":
    "Every IdP registered by a tenant. Approval is a point-in-time check, but the IdP's own settings (self-sign-up, for one) can change afterwards. " +
    "Approved methods stay listed here so their issuers and domains can be reviewed periodically. Approve or suspend right here.",
  "admin.member_removed": "removed",
  "admin.remove_member": "Remove member",
  "admin.remove_title": "Remove {key} from {slug}",
  "admin.remove_confirm": "Remove",
  "admin.remove_body": "This takes the member off the {slug} roster. Access stops from the next request.",
  "admin.remove_keeps": "The workspace, its home and stored credentials are kept (use \"Clean home\" first to erase them).",
  "admin.remove_undo": "To restore access, add the same email address as a member again.",

  // --- tenant settings modal (the tenant administrator's surface). The admin modal is
  // the whole deployment, personal settings are yourself; this one is "the tenant you
  // administer". Panels moved here keep their admin.* keys (renaming is a separate
  // change from moving). ---
  "tenant.title": "Tenant settings",
  "tenant.back": "All tenant settings",
  "tenant.group_tenant": "Tenant",
  "tenant.tab_limits": "Limits & idle",
  "tenant.group_login": "Sign-in",
  "tenant.tab_signin": "Sign-in methods",
  "tenant.tab_rules": "Login rules",
  "tenant.tab_network": "Allowed networks",
  "tenant.net_title": "Source networks",
  "tenant.net_on": "restricted",
  "tenant.net_off": "no restriction",
  "tenant.net_allowed": "Allowed networks",
  "tenant.net_allowed_unit": "Comma-separated CIDR ranges or single addresses (IPv4/IPv6). Empty = no restriction.",
  "tenant.net_your_ip": "Your address, as this deployment sees it",
  "tenant.net_your_ip_unit": "This is what a rule is matched against — not what your browser thinks its address is.",
  "tenant.net_ip_unknown": "cannot be determined",
  "tenant.net_ip_unknown_hint": "The control plane cannot work out where this request came from, so a rule could not be enforced. Ask the operator to check AF_TRUSTED_PROXY_HOPS.",
  "tenant.net_proxy_not_configured": "A proxy sits in front of the control plane but the deployment has not declared it (AF_TRUSTED_PROXY_HOPS), so every request looks like it comes from that proxy. Saving a rule is blocked until an operator fixes it — otherwise the rule would let everyone in while appearing to restrict.",
  "tenant.net_scope_hint": "This restricts USE of the tenant, not reaching the site: the sign-in page still loads and signing in still works from anywhere, but nothing in this tenant can be opened from a network that is not listed.",
  "tenant.net_exempt_hint": "Not covered: MCP and the internal Git provider, which are called from inside a member's own workspace and say nothing about where the person is. Revoke those by deactivating the membership. Deployment administrators are exempt from this rule so a mistake here can always be undone.",
  "tenant.net_layers_hint": "This is an access rule, not a network defence — the request still reaches the control plane and is refused after the session is verified. To stop traffic before it arrives, an operator restricts it at the load balancer instead.",
  // Integrations (docs/log/71) — credentials the tenant created on the other service.
  "tenant.group_integrations": "Integrations",
  "tenant.tab_git_oauth": "Integration OAuth apps",
  "tenant.git_oauth_intro": "Decides which OAuth app the “Connect with OAuth” buttons use for your members (GitHub and Bitbucket under Connections › Git, Jira under Connections › Issue tracker). The app is created in your own GitHub org / Bitbucket workspace / Atlassian account, so a tenant administrator registers it here. It takes effect the moment you save — there is no approval step.",
  "tenant.git_oauth_optional": "Members can connect without this by pasting a token. Registering an app here is what makes “Connect with OAuth” appear for that provider.",
  "tenant.git_oauth_on": "registered",
  "tenant.git_oauth_off": "not registered",
  "tenant.git_oauth_client_id": "client_id (Bitbucket calls it Key)",
  "tenant.git_oauth_client_secret": "client_secret (Bitbucket calls it Secret)",
  "tenant.git_oauth_secret_kept": "stored — leave empty to keep it",
  "tenant.git_oauth_secret_unit": "Encrypted on save and never shown again. Fill this in only when you want to change it.",
  "tenant.git_oauth_redirect": "Register this callback URL with the provider's app:",
  "tenant.git_oauth_no_base_url": "This deployment has no PUBLIC_BASE_URL, so there is no callback URL to register with Bitbucket. Connecting via OAuth will fail even once you save this — the code grant has nowhere to come back to. Ask the operator to set PUBLIC_BASE_URL.",
  "tenant.git_oauth_jira_access": "Choose Resource-level as the Access type when creating the app — it limits the grant to the one site authorized. Account-level hands the app permanent access to every site in the account.",
  "tenant.git_oauth_bb_scopes": "Bitbucket puts no scope in the authorization URL — the consumer's Permissions are what members grant. Alongside Account: Read and Repositories: Read/Write (clone and push), tick Pull requests: Read if the issue tracker rail should list pull requests. Adding it later means members who are already connected have to connect again (their token carries the old permissions).",
  "tenant.git_oauth_jira_scopes": "Jira uses an Atlassian 3LO app, registered separately from the Bitbucket consumer. Add the Jira API under Permissions and grant read:jira-work, read:jira-user and write:jira-work (write is for “comment the work back”). offline_access is not in that list — it is an OAuth-level scope that af puts in the authorization URL, so there is nothing to configure for it.",
  "tenant.git_oauth_jira_sharing": "Turn Sharing on under the app's Distribution. A 3LO app is “in development” by default, which lets only its creator authorize it — every other member is stopped by Atlassian's “You don't have access to this app”, and since that is before the consent screen nothing comes back to af, so the connection just stays silently unmade. Enabling it asks for a Vendor name, Contact link and Privacy policy URL, which the members authorizing the app can see — use a company name and a support address, not a personal name or inbox. It does not put the app on the Marketplace.",
  "tenant.git_oauth_gh_device": "GitHub uses the device flow, so it needs neither a secret nor a callback — but the app must have “Enable Device Flow” ticked, or starting a connection fails.",
  "tenant.git_oauth_where": "Where to register the app:",
  "tenant.git_oauth_remove": "Remove registration",
  "tenant.summary_note": "a deployment administrator sets the tenant-wide caps",
  "tenant.group_manage": "Operations",
  "tenant.tab_members": "Members",
  "tenant.tab_sessions": "Sessions",
  "tenant.tab_usage": "Running time",
  "tenant.tab_audit": "Audit",
  "tenant.tab_mcp": "MCP distribution",
  "tenant.tab_engines": "Inference engine models",
  "tenant.picker": "Tenant",
  "tenant.none": "You don't administer any tenant.",
  "tenant.forbidden": "You don't have permission to view this tenant's settings.",
  "tenant.rules_readonly_note": "only a deployment administrator can change these",
  "tenant.rules_hint":
    "An auto-join domain can belong to only one tenant. " +
    "To change any of these rules, ask a deployment administrator.",
  "tenant.rules_unset": "not set (no restriction)",
  "tenant.rules_autojoin_note": "People with an email address in this domain join this tenant on their first sign-in.",
  "tenant.rules_invite_note": "A guard that applies only when adding a member. It does not affect people who are already members.",

  // === Cleanup panel (features/sessions/CleanupModal.tsx, docs/log/32) ===
  "clean.title": "Clean up",
  "clean.open": "Open cleanup (survey & tidy)",
  "clean.subtitle": "Survey and tidy up accumulated stopped sessions, unneeded worktrees and merged branches.",
  "clean.loading": "Surveying…",
  "clean.empty": "Nothing to clean up.",
  "clean.reload": "Re-survey",
  "clean.tab_candidates": "Candidates",
  "clean.tab_archives": "Trash (restore)",
  "clean.stage1_title": "① Tidy sessions (stopped → archive / shell·ssm → delete)",
  "clean.stage1_run": "Tidy all",
  "clean.stage1_run_title": "Tidy every stopped session (archived ones can be restored from the archive browser)",
  "clean.stage1_empty": "No stopped sessions to tidy.",
  "clean.stage1_confirm_title": "Tidy all stopped sessions",
  "clean.stage1_confirm_body": "Will {parts}. Archived sessions can be restored from the archive browser (the shelf).",
  "clean.stage2_title": "② Delete working copies & branches",
  "clean.stage2_run": "Delete all safe",
  "clean.stage2_run_title": "Delete only what is graded safe (merged & clean)",
  "clean.stage2_empty": "No working copies or branches can be deleted.",
  "clean.stage2_confirm_title": "Bulk-delete working copies & branches",
  "clean.stage2_confirm_body": "Delete the {count} items graded safe. Branches and sessions are moved to the trash first; deleting a worktree cannot be undone.",
  "clean.shelf_n": "{count} archived sessions are on the shelf (not part of cleanup).",
  "clean.open_shelf": "Open the archive browser",
  "clean.safety_safe": "Safe",
  "clean.safety_review": "Review",
  "clean.safety_keep": "Keep",
  "clean.type_session": "Session",
  "clean.type_worktree": "Worktree",
  "clean.type_branch": "Branch",
  "clean.col_target": "Target",
  "clean.col_reason": "Reason",
  "clean.select_all_safe": "Select all safe",
  "clean.clear_selection": "Clear selection",
  "clean.selected_n": "{count} selected",
  "clean.run_selected": "Clean up selected",
  "clean.keep_hint": "Keep (running, or uncommitted/unpushed). Stop or push it, or force-delete in the Console.",
  "clean.collapse_all": "Collapse",
  "clean.expand_all": "Expand",
  "clean.group_main": "(main clone)",
  "clean.group_other": "Other",
  "clean.group_safe_n": "{count} safe",
  "clean.action_archive_session": "Archive",
  "clean.action_delete_session": "Delete (with conversation, recoverable)",
  "clean.action_delete_worktree": "Delete worktree",
  "clean.action_delete_branch": "Delete branch",
  "clean.confirm_title": "Clean up the {count} selected item(s)?",
  "clean.confirm_body": "Deleted sessions and branches are moved to the trash for recovery. Deleting a worktree can't be undone (uncommitted/unpushed work is protected).",
  "clean.confirm_do": "Clean up {count}",
  "clean.run_done": "Cleaned up {done}. {failed} failed.",
  "clean.run_done_ok": "Cleaned up {done}.",
  "clean.archives_empty": "The trash is empty.",
  "clean.archive_reason_delete_session": "Session delete",
  "clean.archive_reason_delete_branch": "Branch delete",
  "clean.archive_sessions_n": "{count} session(s)",
  "clean.archive_branches_n": "{count} branch(es)",
  "clean.restore": "Restore",
  "clean.purge": "Delete permanently",
  "clean.restored": "Restored.",
  "clean.restore_failed": "Couldn't restore.",
  "clean.purge_title": "Permanently delete this trash archive?",
  "clean.purge_body": "This archive can no longer be restored (reclaims its space).",
  "clean.purge_do": "Delete permanently",
  // Candidate reasons — the Agent sends only the clean.reason.* key (ADR 0033).
  "clean.reason.locked": "Locked (delete-protected; not a cleanup target until unlocked)",
  "clean.reason.archived": "Archived (exempt from auto-prune; delete to reclaim, recoverable)",
  "clean.reason.stopped": "Stopped (resumable). Archive it to clear the list once it's finished",
  "clean.reason.ephemeral": "Stopped shell/ssm (no conversation to keep). Deleting tidies it up",
  "clean.reason.orphan_pane": "Orphan (running pane with no metadata). Attach or tidy it in the Console",
  "clean.reason.wt_live": "A session is still running here (stop it first)",
  "clean.reason.wt_locked_session": "A delete-locked session still lives here (unlock it first)",
  "clean.reason.wt_dirty": "Uncommitted/unpushed changes (push, or force-delete in the Console)",
  "clean.reason.wt_merged": "Merged and clean (already in the parent)",
  "clean.reason.wt_unmerged": "Clean but unmerged (has its own commits; the branch survives deletion, but check first)",
  "clean.reason.branch_merged": "Merged local branch (already in the parent; recoverable after deletion)",
  // The same reasons split into "state badge + hint" (row line 2; keys without a badge fall back to the sentence).
  "clean.reason_badge.locked": "Locked",
  "clean.reason_hint.locked": "Delete-protected; not a cleanup target until unlocked",
  "clean.reason_badge.archived": "Archived",
  "clean.reason_hint.archived": "Exempt from auto-prune; delete to reclaim, recoverable",
  "clean.reason_badge.stopped": "Stopped (resumable)",
  "clean.reason_hint.stopped": "Archive it to clear the list once it's finished",
  "clean.reason_badge.ephemeral": "shell/ssm",
  "clean.reason_hint.ephemeral": "No conversation to keep; deleting tidies it up",
  "clean.reason_badge.orphan_pane": "Orphan",
  "clean.reason_hint.orphan_pane": "Running pane with no metadata; attach or tidy in the Console",
  "clean.reason_badge.wt_live": "Running",
  "clean.reason_hint.wt_live": "A session is still running here — stop it first",
  "clean.reason_badge.wt_locked_session": "Locked session inside",
  "clean.reason_hint.wt_locked_session": "A delete-locked session still lives here; unlock it first",
  "clean.reason_badge.wt_dirty": "Uncommitted/unpushed",
  "clean.reason_hint.wt_dirty": "Push, or force-delete in the Console",
  "clean.reason_badge.wt_merged": "Merged",
  "clean.reason_hint.wt_merged": "Clean; already in the parent",
  "clean.reason_badge.wt_unmerged": "Unmerged",
  "clean.reason_hint.wt_unmerged": "Clean but has its own commits; the branch survives deletion — check first",
  "clean.reason_badge.branch_merged": "Merged branch",
  "clean.reason_hint.branch_merged": "Already in the parent; recoverable after deletion",
};
