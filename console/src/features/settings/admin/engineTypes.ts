// The engine panel's shared vocabulary: what a row of `GET /api/admin/engines` holds, and the
// handful of questions both halves of the panel ask of it.
//
// The panel is two screens: the MACHINE — mode, ECS state, which box, which GPU rung, the uptime
// history — and what it LOADS — the catalogue, the ingest and the upstream search. One rail item
// held both and grew to 3,000 lines, which is also why the two questions were interleaved on one
// page: "is the GPU costing me money right now" and "which checkpoint should this run" are asked
// by the same person at different times.
//
// They read the same answer and almost nothing else, which is what this file is: the types, the
// loader, and four predicates. A helper only one screen uses belongs in that screen's own file.

import { useCallback, useEffect, useState } from "react";
import { api, errDetail } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { loadEngines, saveEngines } from "./catalogMemory.ts";

export type EngineBox = {
  id?: string;
  status?: string;
  /** The EC2 type this box actually is. After a class change it is NOT what the capacity
   *  provider says: the change reaches the next box only (ADR 0074 decision 4). */
  instance_type?: string;
  /** registeredAt: when the EC2 INSTANCE joined the cluster, which is a different fact from
   *  when the service last changed. See engineBox in control-plane/engine_ecs.go. */
  since?: string;
};

/** One entry of the model catalogue (ADR 0072). What the engine may load is a declaration an
 *  administrator edits here, not a CloudFormation parameter — which is the whole point of the
 *  ADR: swapping a checkpoint is an operational act, performed while the GPU is asleep. */
export type EngineModel = {
  id: string;
  kind?: string;
  enabled: boolean;
  /** The image role's ONE checkpoint (sd-server holds one, chosen at startup) and the llm
   *  role's answer to a request that named no model. Exclusive within an engine. */
  selected?: boolean;
  default?: boolean;
  description?: string;
  context_tokens?: number;
  max_output_tokens?: number;
  vram_mib?: number;
  /** What this model would want on the card, and how well that is known (ADR 0074 decision 6):
   *  "declared" = the operator measured it, "floor" = the weight files' size and nothing else
   *  (no KV cache, no context), "weights_kv" = that plus the KV cache this row's context window
   *  needs, "unknown" = nobody said. 🔴 `unknown` must never be drawn as a comfortable zero — it
   *  means the question was not answered.
   *
   *  🔴 `weights_kv` was missing from this union while the CP was already answering it, so it
   *  arrived typed as the two the panel knew and was drawn with the MEASURED wording. Both
   *  floors have to stay distinguishable from a measurement here. */
  vram_need_mib?: number;
  vram_need_source?: "declared" | "floor" | "weights_kv" | "unknown";
  /** What the KV cache costs per 1024 tokens of window, off this row's stored GGUF geometry.
   *  MULTIPLY it (kvCacheMiB) — the cache is linear in the window, so one number prices every
   *  value somebody could type, and it is what lets a registered row be re-fitted in place.
   *  🔴 ABSENT, never 0, when the row has no geometry: "nobody could read it" is not
   *  "it costs nothing". */
  kv_mib_per_1k_tokens?: number;
  /** The model's OWN maximum window (`<arch>.context_length`), stored at ingest. 🔴 A ceiling,
   *  not a setting — the same fact and the same name the ingest form receives it by. It is what
   *  bounds a re-fit: without it the panel would propose windows the model was never trained
   *  for. Absent on a row registered before it was stored, and the re-fit is then not offered. */
  context_length?: number;
  /** BOTH are kept and both are shown: Hugging Face reports `other` for the two
   *  non-commercial models in ADR 0072's table, with the real terms in license_name. */
  license?: string;
  license_name?: string;
  license_url?: string;
  /** "yes" | "no" | "unknown", resolved from the licence at ingest (ADR 0072 decision 10). The
   *  row says what may be RESTRICTED and points at the licence; it does not decide, because
   *  which of "running the model" and "selling what it makes" a non-commercial licence forbids
   *  differs between them. */
  commercial_use?: string;
  /** Who took the licence on for every member of this deployment, and when (ADR 0072 decision
   *  10). A record of a HUMAN act, which is the question an audit asks and the model card
   *  cannot answer — so it belongs on the row and not only in the ingest form that recorded it.
   *  Absent for a seeded row and for one registered by hand. */
  license_accepted_by?: string;
  license_accepted_at?: string;
  base_model?: string;
  /** What this checkpoint should keep OUT of every picture, as its publisher recommends it
   *  (ADR 0072 follow-up, negative prompts). A DEFAULT for the model: a request's own negative
   *  prompt is added to it, and the engine's exclusion list is added to both. Empty means
   *  undeclared, which the Agent answers with its own measured default. */
  negative_prompt?: string;
  /** The words an ADAPTER answers to, as its publisher records them (ADR 0081 decision 5).
   *  Absent on a checkpoint, which has no triggers at all. The ingest reads them from Civitai
   *  and stores them; this is where a wrong or missing one is corrected, and a LoRA loaded
   *  without its trigger changes nothing visible. */
  trained_words?: string[];
  /** The provider dispatches on base_model and THIS row's is missing or names no workflow
   *  template (ADR 0072 decision 2). Stated by the CP, because the panel cannot know the
   *  vocabulary — and because the row looks complete without it and fails only at generation,
   *  after a cold start somebody waited through. */
  base_model_missing?: boolean;
  /** The row HAS a family, and the workflow that family names reads files this row does not
   *  declare (ADR 0072 P2 欠落 10) — listed as the roles that are missing. It is the mark that
   *  used to disappear the moment a family was chosen: `flux1-dev` was one unflagged 22.2 GiB
   *  checkpoint, the flux1 template reads four other files, and choosing any family at all
   *  made the panel go quiet about a row that still could not generate. The CP refuses to
   *  enable one of these. */
  files_missing?: string[];
  /** The row's checkpoint was READ and carries no VAE tensors, and the row declares no `--vae`
   *  file either (ADR 0072 follow-up). The one fault no declaration can express: an SDXL
   *  checkpoint published without a VAE holds every file its family needs, validates, loads —
   *  and then fails every request inside ComfyUI, after the box has paid a 1-2.5 minute
   *  checkpoint switch. Measured twice on this deployment. The CP refuses to enable one. */
  vae_missing?: boolean;
  /** Which file would fix it, as `<repo>/<file>`. Absent for a family this deployment holds no
   *  default VAE for, where the fix is a declaration somebody makes by hand. */
  vae_fix?: string;
  /** 🔴 Nobody has read this row's header yet, which is NOT the same as "it has no VAE" — every
   *  row taken in before the question existed is in this state. The panel answers it with one
   *  scan call rather than drawing a mark, because a mark on all of them would be worth
   *  nothing. */
  vae_unread?: boolean;
  /** Where the bytes came from (`hf:<repo>/<file>`, `civitai:<id>`, a URL). The id is short and
   *  unique only inside this deployment, so this is the only thing that says WHICH vendor's
   *  model of that name this row is. Absent for a seeded row.
   *
   *  🔴 For a SPLIT model this describes the file that created the row and nothing else — the
   *  parts carry their own (`file_rows[].source`). */
  source?: string;
  /** What the publisher CALLS this, and which version of it was taken in (ADR 0088).
   *
   *  The `id` above is derived from the FILE name, because it is the key the launch menu, the
   *  active set and the S3 layout are written in — so it is not a name anybody chose, and a
   *  catalogue drawn from ids alone (`abyssorangemix2_hard_8832`) says nothing about which
   *  model is which. The panel draws these as the card's title and keeps the id underneath: the
   *  id is what every other screen and every S3 key says, so it is never replaced.
   *
   *  🔴 Absent, not empty, when nobody recorded one — a seeded row, a row registered from the
   *  bucket by hand, and every row taken in before the columns existed. That absence is the
   *  predicate the メタデータ button is offered on, so a fallback to `""` here would hide the
   *  one control that fills them in. */
  display_name?: string;
  version_name?: string;
  /** One example image the publisher published, at the lightbox size and the card size. URLs
   *  into the publisher's own CDN — this deployment mirrors nothing — so an image the publisher
   *  deletes is an empty box, which the same button re-reads. `thumb_url` is absent where the
   *  source offers no resizing (Hugging Face), and the card falls back to `preview_url`, which
   *  is the convention the 探す tab already draws hits with. */
  preview_url?: string;
  thumb_url?: string;
  /** The page `source` names, composed by the CP (engineSourceURL). Absent when it could not be
   *  composed, and the panel branches on THAT rather than parsing the string a second time:
   *  `civitai:<id>` is a model VERSION id and `/models/<id>` opens a different model, and a
   *  `url:` source is the direct download of the weights rather than a page. */
  source_url?: string;
  precision?: string;
  sizes?: string[];
  files?: string[];
  /** The row as it was DECLARED — the S3 keys, the flags and the sizes, in the shape
   *  `POST …/models` reads back (ADR 0072 P6 R2). `files` above is base names to read; these
   *  are what a forgotten row is rebuilt from, and since P6 the catalogue is the only place
   *  the declaration exists at all. Super-admin only, like the rest of this row. */
  file_rows?: {
    s3Key: string;
    flag?: string;
    bytes?: number;
    /** Where THIS part came from, and its page. Absent on a file staged by hand and on every
     *  file taken in before the field existed — which stays "nobody recorded", never "unknown". */
    source?: string;
    source_url?: string;
  }[];
  args?: string[];
  /** What enabling this model adds to the next cold start, in seconds, from the file sizes
   *  whoever staged them declared. Absent when nobody declared one — the CP cannot look in S3
   *  (ADR 0072 review R3), so this is an estimate and is labelled as one. */
  sync_secs?: number;
  /** What this row asks to be RUN at, over its family's own recipe. Absent for a row that
   *  declares nothing, which is every row until somebody says otherwise — and an object of
   *  zeros would read as "0 steps" rather than "not declared", which is why the field is
   *  optional rather than always present. */
  params?: EngineParams;
};

/** The generation defaults one catalogue row declares (store.EngineParams).
 *
 * Every field is optional and 0 means UNDECLARED: the provider merges them over the family's
 * template one field at a time, so a row that names only `steps` keeps the template's sampler.
 *
 * `clip_skip` is stored and shown but applied by no template today (none of the five ComfyUI
 * graphs has a CLIPSetLastLayer node) — it is kept because it is published alongside the others
 * and dropping it at the form means reading the model page again to get it back. */
export type EngineParams = {
  steps?: number;
  cfg?: number;
  sampler?: string;
  scheduler?: string;
  clip_skip?: number;
  /** A LoRA row's recommended strength, and the only field here about an adapter rather than a
   *  checkpoint. The provider uses it when a caller names the LoRA without a number. */
  weight?: number;
};

/** What POST …/ingest/resolve answered: what the file IS, before anything is started. */
export type ResolvedSource = {
  sha256?: string;
  /** Immutable identity of this exact resolved source file. It is the only value which may
   * match a stored object for reuse; the human-readable source is display provenance only. */
  artifact_identity?: string;
  bytes?: number;
  gated?: boolean;
  license?: string;
  license_name?: string;
  license_url?: string;
  base_model?: string;
  commercial_use?: string;
  /** false when the repository is gated and this deployment has no Hugging Face token, or when
   *  Civitai's uploader requires an account. The button is disabled on it rather than letting a
   *  task run nine minutes into a 401. */
  can_ingest?: boolean;
  /** The Civitai uploader requires a logged-in account to download this asset (ADR 0072 P2
   *  欠落 5). Its own field rather than `gated`, because the two have different answers: a
   *  registered Hugging Face token satisfies gating and cannot touch this one, so folding them
   *  would send somebody to the token field to fix what a token does not fix.
   *
   *  ⚠️ On its own it no longer means "cannot be taken in" — see `civitai_needs_account`. Read
   *  `can_ingest` for that, as the button does. */
  login_required?: boolean;
  /** Login-walled, and a Civitai account IS registered — the same shape as
   *  `gated_needs_acceptance`: the download may start, and the CP cannot say in advance whether
   *  that account satisfies this uploader (early access is bought per creator). A warning before
   *  the press, not a verdict. */
  civitai_needs_account?: boolean;
  /** Whether a Civitai account is registered at all, so the panel can tell "nobody can fetch
   *  this" from "we will try as our account". */
  deployment_civitai_token?: boolean;
  /** Gated, and a token IS registered — which is still not a yes. The CP resolves anonymously
   *  and cannot ask whether that account accepted THIS repository's terms; when it has not,
   *  the answer is a 403 on the download minutes later. So this is a warning before the press,
   *  not a verdict (ADR 0072 P5 実機検証). */
  gated_needs_acceptance?: boolean;
  /** The model's OWN maximum, off the GGUF header. 🔴 A ceiling, not a setting: the 30B in
   *  this deployment publishes 262144 and is run at 32768, because what the architecture
   *  allows and what fits in the GPU are different questions. Offered, never applied. */
  context_length?: number;
  /** What the KV cache costs per 1024 tokens of window, off this file's own GGUF header. The
   *  cache is LINEAR in the context length, so the panel multiplies this by the window in the
   *  form — 🔴 the formula itself stays in the CP (engineKVCacheMiB); a second copy here would
   *  be a second thing to correct the day a model declares different key and value widths.
   *
   *  🔴 Absent, NEVER 0, when the header could not be read: the CP's read is best-effort and
   *  silent, and "nobody measured it" is not "it costs nothing". The element type is assumed to
   *  be f16 and cannot be read at all (`-ctk`/`-ctv` are CloudFormation parameters that never
   *  reach the engine table), which is why the sentence that shows it says so. */
  kv_mib_per_1k_tokens?: number;
  /** `base_model` translated into the family vocabulary this provider dispatches on, or absent
   *  when the CP would not name one. The picker's initial value — never the stored family, and
   *  never silently: ADR 0072 decision 2 keeps the declaration with the operator, because an
   *  upstream display name stored as a family made rows that looked complete and would not
   *  generate. */
  base_model_suggest?: string;
  /** What this source says may not be done with the file, as codes (engineRestrictLabel). The
   *  resolve sees more than the search list did — `usageControl` is on the version document
   *  only — so a row can pick up a mark here that the card it was chosen from could not show. */
  restrictions?: string[];
  /** A LoRA's trigger words. Published structured, unlike everything in `params_hint`, and an
   *  adapter used without its trigger silently does nothing. */
  trained_words?: string[];
  /** How the author says to run this, read out of their own prose by the CP, and the sentence
   *  it was read from. 🔴 Both halves or neither: the numbers are a regular expression's guess
   *  about somebody else's paragraph, and the quote is what lets a person judge them instead of
   *  trusting them. Fills the form; nothing is stored until the form is submitted. */
  params_hint?: EngineParams;
  params_hint_quote?: string;
  /** Whether THIS file carries the VAE its family decodes with, read from the safetensors
   *  header before anything is downloaded: "yes" | "no". 🔴 Absent means the header could not be
   *  read (a `.ckpt`, a source that refused the Range) — which is not "no" and must not be drawn
   *  as one. Asked of a whole checkpoint on an image engine only. */
  vae_bundled?: string;
  /** What one press would DO, decided by the CP (ADR 0085 decision 4). Everything above it is
   *  facts the card still draws; this is the only thing the ingest request is built from. */
  plan?: IngestPlan;
};

/** One file of a plan, with what it costs. `download` spends money and `reuse` / `move` do not —
 *  the bucket already holds those bytes, and a move is server-side. */
export type IngestPlanFile = {
  /** The role it is taken in as (`--vae`, `--t5xxl`); empty for a whole checkpoint. */
  flag?: string;
  name: string;
  bytes?: number;
  action: "download" | "reuse" | "move" | "unknown";
  /** 🔴 TWO meanings, told apart by `action`: the upstream label (`hf:owner/repo/file`) for a
   *  download, and the S3 key the bytes occupy TODAY for a reuse or a move. The CP keeps it one
   *  field on purpose — a move's origin then rides inside the plan's hash, so an object that
   *  wanders between the card and the press makes the token stale instead of being moved from a
   *  key it has left. The card must branch on `action` before drawing it as either. */
  source?: string;
  /** Where it lands. Composed by the CP alone since ADR 0085 decision 1 — the Console neither
   *  builds nor sends a key, it only shows the one it is told. */
  key?: string;
};

/** What `POST …/ingest/resolve` answers beside the facts: the whole act, priced.
 *
 * 🔴 `plan_token` is what `POST …/ingest` carries instead of the fields a form used to fill. The
 * CP re-plans at the press and refuses a token that no longer matches (`engine_plan_stale`), so a
 * card left open for ten minutes cannot spend money on last decade's numbers. */
export type IngestPlan = {
  plan_token: string;
  id: string;
  base_model?: string;
  /** Offered ONLY when the CP could not read a family. The family selector is the one field the
   *  card shows, and it is never a free string: an upstream display name stored as a family makes
   *  a row that looks complete and will not generate (ADR 0072 decision 2). */
  base_model_candidates?: string[];
  main_flag?: string;
  files: IngestPlanFile[];
  bytes_to_download?: number;
  warnings?: string[];
};

/** What `POST …/models/{id}/complete` answers (ADR 0085 decision 3). The row is the subject: the
 *  CP reads what the family needs, what the row has and what the ledger holds, and closes the gap.
 *
 *  `action: "choose"` is the ONLY case a person is asked anything, and they are asked it for a
 *  checkpoint — never "which model does this part belong to". */
export type CompleteAnswer = {
  action: "none" | "attached" | "moving" | "job_started" | "choose" | "unknown";
  files?: CompleteFile[];
  bytes_to_download?: number;
  jobs?: IngestJob[];
};

export type CompleteFile = {
  flag: string;
  action: "declare" | "move" | "download" | "choose" | "unknown";
  key?: string;
  bytes?: number;
  source?: string;
  /** The ledger's objects that fit this role, ranked by the CP. With one candidate the press just
   *  uses it; several is what opens the dialog. A FILLED slot with candidates is how a part is
   *  swapped — the same picker, on a frame that is not empty. */
  candidates?: { key: string; source?: string; bytes?: number }[];
};

/** One object in the bucket, as the ledger lists it (ADR 0085 decision 2).
 *
 * 🔴 The first thing here that does not come from a row. Forget a row and the bytes went on
 * costing money while no longer existing to the Console — about 24 GB on af-sandbox, 2026-09-15,
 * with no screen that could show them and no button that could touch them. */
export type EngineObjectRow = {
  key: string;
  bytes?: number;
  last_modified?: string;
  role_dir: "checkpoints" | "diffusion_models" | "text_encoders" | "vae" | "loras" | "other";
  /** `misplaced` = the key is not `<role dir>/<base name>`, so no ComfyUI loader enumerates it. */
  placement: "ok" | "misplaced";
  /** `missing` is a row pointing at nothing, which is a ledger fact too. `uploading` / `failed`
   *  are a live job's state on this destination — jobs have no list of their own any more. */
  state: "present" | "uploading" | "failed" | "missing";
  declared_by?: { model_id: string; flag?: string }[];
  source?: string;
  artifact_identity?: string;
  license?: string;
  job?: { id: string; state: string; message?: string; created_at?: string };
};

export type EngineObjectsAnswer = {
  objects: EngineObjectRow[];
  checked_at?: string;
};

/** A refusal in this area, with the two fields ADR 0085 decision 5 requires of every 409: WHO is
 *  holding the thing, and WHAT to press about it. The Console draws `next` as the button on the
 *  error line, which is the whole point — "the key is already recorded" was true and useless. */
export type EngineApiError = {
  code?: string;
  message?: string;
  holder?: { kind: "row" | "job" | "object" | "task"; id?: string; key?: string };
  next?: { act: "register" | "complete" | "replace" | "forget_row" | "dismiss_job" | "wait"; target?: string };
  /** `engine_plan_stale` carries the plan that replaced the stale one, so the card is redrawn
   *  from the answer rather than from a second resolve. */
  plan?: IngestPlan;
};

/** One search result (POST …/ingest/search, ADR 0072 decision 11).
 *
 * A DESTINATION, not an ingest: picking one fills the repository field and the existing
 * resolve → accept → ingest road runs unchanged. The numbers here are the listing's, i.e. a
 * draft — the licence and the sha256 of record are what the resolve of the chosen FILE reads,
 * because a Hugging Face card can move between the two calls. */
export type IngestHit = {
  /** "hf" or "civitai" — decides how `ref` is turned into a repository field. */
  source: string;
  /** The repository for HF; the VERSION id for Civitai (not the model id on the page's URL). */
  ref: string;
  /** Stable upstream model identity. Hugging Face uses the repository and Civitai uses the
   *  model id; `ref` keeps naming the selectable revision/version for old clients. */
  model_ref?: string;
  name: string;
  /** Upstream example image at lightbox size. It is presentation only and is never used as an
   *  ingest source; absence means the card has no image area at all. */
  preview_url?: string;
  /** The same example at card size, when the source can resize (Civitai can, Hugging Face
   *  cannot). The card must use this: the URL Civitai publishes asks for the ORIGINAL, which is
   *  megabytes per row for a 92x108 box, and on a phone those loads are what fails. */
  thumb_url?: string;
  /** The three numbers a ranking is built on. All three ride on every row, whichever one the
   *  list was ordered by — sorting by one and showing only that one leaves "why is this here"
   *  unanswerable. `trending` is Hugging Face's own score; Civitai publishes none. */
  downloads?: number;
  likes?: number;
  trending?: number;
  gated?: boolean;
  /** WHICH gate: "auto" is satisfied by accepting the terms once with the account the
   *  deployment's token belongs to, "manual" waits on the author approving that account by
   *  hand. Two different amounts of work, and the reason the boolean above is not enough. */
  gated_kind?: string;
  /** "yes" | "no" | absent. 🔴 Three states, because Civitai publishes nothing that predicts
   *  this and the CP has to HEAD the download to find out — so "nobody could tell" is a real
   *  answer and must not be drawn as "anyone may download this". Measured 2026-09-12: 13 of
   *  the top 20 monthly checkpoints answer 401, and all 20 look identical in the metadata. */
  login_required?: string;
  /** Licence-level commercial-use verdict when the listing exposes it. */
  commercial_use?: string;
  /** A token exists, but its account may still need to accept this repository's terms. */
  gated_needs_acceptance?: boolean;
  /** What the source says may not be done with it, as codes — see engineRestrictLabel. */
  restrictions?: string[];
  /** A LoRA's trigger words, straight off the search answer. */
  trained_words?: string[];
  license?: string;
  license_name?: string;
  base_model?: string;
  /** The family this deployment's provider would call that, when it recognises it. Beside
   *  `base_model` rather than replacing it: one is what the upstream published and the other is
   *  a suggestion for the picker. */
  base_model_suggest?: string;
  /** When it first appeared, beside `updated_at`'s "when it last changed". Both, because for a
   *  quantisation repository they are a year apart and only the pair answers "is this
   *  maintained". 🔴 Display only — there is deliberately no "newest" ranking to sort by (the
   *  CP's engineSortHF says why: every date-ordered page is bulk automated re-quantisations). */
  published_at?: string;
  updated_at?: string;
  /** The upstream page, composed by the CP and used here VERBATIM. Not built in the panel: the
   *  two sources spell it differently and Civitai's needs the model id, which `ref` is not. */
  url?: string;
  bytes?: number;
  context_length?: number;
  /** Civitai's own content-rating number (0 = safe, higher = more explicit). Rides on every
   *  Civitai hit regardless of which search tab found it — a nonzero level shows up under the
   *  plain "civitai" tab's own default query too (measured 2026-09-14), so the card must not
   *  assume "safe" just because it did not come from the civitai-red tab. Absent for HF. */
  nsfw_level?: number;
};

/** One file a repository offers (POST …/ingest/files), already filtered to the ones this
 *  engine could load and that carry a sha256. */
export type IngestCandidate = {
  name: string;
  bytes?: number;
  sha256?: string;
  /** Optional source-side file identity when a provider exposes more than a filename. */
  ref?: string;
  /** What this file IS within the repository (ADR 0089): `model` — a quantisation somebody takes
   *  in on its own — or `projector` / `imatrix`, the two things a quantisation repository keeps
   *  beside its models. Absent on an older control plane, which every reader treats as `model`.
   *
   *  🔴 Classified by the CP and never filtered by it: this same list is the manual file picker,
   *  where a hidden file is how somebody ends up comparing two strings across two windows. The
   *  repository ladder folds what it does not need; the picker still shows everything. */
  role?: string;
};

export type IngestVersion = {
  ref: string;
  name: string;
  published_at?: string;
  updated_at?: string;
  /** The version's own model file, when the listing carried one. Absent means the upstream did
   *  not say — drawn as "size unknown", never as a comfortable zero, and no fit verdict is
   *  offered for it (a verdict computed from a missing size would be a lie about the card). */
  bytes?: number;
};

export type IngestVersionsAnswer = {
  versions: IngestVersion[];
  /** The model the versions belong to, as the CP resolved it. A registered row records the
   *  VERSION id alone (`civitai:<id>`), so this is how the panel learns the model id it needs to
   *  open the ingest form on one of the other versions. */
  model_ref?: string;
};

export type IngestSearchRequest = {
  q: string;
  source: string;
  sort: string;
  lora?: boolean;
  cursor?: string;
  /** One of the engine's own `base_models`, or absent for every family. The CP translates it
   *  into each upstream's own spelling — the Console must never send an upstream name here. */
  family?: string;
};

export type IngestSearchAnswer = {
  hits: IngestHit[];
  next_cursor?: string;
};

export type IngestJob = {
  id: string;
  model_id: string;
  s3_key?: string;
  source?: string;
  /** Which of the three the press turned out to be (ADR 0085 decision 1). Only on the answer to
   *  `POST …/ingest`: the job row itself cannot say it, because a move and a download are the
   *  same task to the reconciler — and it is the one thing the person who just pressed wants to
   *  know before any progress appears. */
  action?: "download" | "reuse" | "move";
  state: string;
  message?: string;
  /** What the operator has to DO about a failure, read by the CP out of the status in the
   *  message. 🔴 `gated_no_token` and `gated_not_accepted` are one line of curl apart and need
   *  opposite screens: 401 is a token that never reached the task, 403 is a token that did and
   *  an account that has not accepted THAT repository (measured, ADR 0072 P5 実機検証 — one
   *  token, FLUX.1-dev through and SD3.5 Medium refused). */
  code?: string;
  bytes?: number;
  created_at?: string;
  /** What the file was taken in AS — `checkpoint`, `gguf`, `lora` — and what it is within the
   *  model (`--vae`, `--t5xxl`; absent for a whole checkpoint). Read by the CP out of the job's
   *  own spec, and here for one reason: registering this key again is a `POST /models`, and a
   *  form that guessed either one would produce a row that loads nothing and says nothing about
   *  it until the next cold start. Absent on a job taken in before the field existed. */
  kind?: string;
  file_flag?: string;
  /** The catalogue row that already points at this job's `s3_key`, as `role/id`, or absent when
   *  none does.
   *
   *  🔴 It is NOT "the file is still in the bucket": the CP has no S3 permission at all (ADR
   *  0072 review R3) and `deleteModel?purge=1` deletes bytes while leaving the job `done` for
   *  ever. What it answers is who would still be broken by losing the file — which is why a
   *  job with nothing pointing at it is the one that is HARDER to forget: that row is then the
   *  last written record of the key. */
  key_used_by?: string;
};

/** One rung of the GPU ladder the operator declared (ADR 0074 decision 1). The CP asks neither
 *  EC2 nor the Pricing API: every number here was written by whoever wrote the ladder, and
 *  `usd_per_hour` is absent — not zero — when they left it out. */
export type EngineClass = {
  id: string;
  label: string;
  vram_mib: number;
  types: string[];
  usd_per_hour?: number;
};

/** One rung with its purchase form attached — the ladder as ADR 0075 decision 1 redraws it. The
 *  shape is EngineClass plus `buy`, so a control plane that sends no offers at all leaves every
 *  reader of `classes` untouched: the offers contract is additive on purpose.
 *
 *  🔴 `usd_per_hour` is the DEAREST type this offer can buy, not what it will cost: the machine
 *  image has no allocation strategy and buys the cheapest type that fits, so a row widened to
 *  three types has a price RANGE and only one number to say it with (decision 1). */
export type EngineOffer = EngineClass & { buy?: string };

/** The offer the service's capacity-provider strategy points at right now (decision 11). Read by
 *  the CP out of DescribeServices rather than remembered from its own choice — a remembered one
 *  starts lying the moment CloudFormation rewrites the service. */
export type EngineOfferRef = { id: string; buy?: string };

/** One attempt in this demand's walk down the list, and how it ended. `result` is one of
 *  active / unfulfillable / insufficient / quota / budget; an unknown value is printed verbatim
 *  rather than dropped, because the codes are read out of ECS service-event STRINGS and a new
 *  one arriving is exactly what nobody would otherwise see (decision 5). */
export type EngineOfferTry = EngineOfferRef & { result?: string };

export type EngineRow = {
  key: string;
  api?: string;
  provider?: string;
  /** This build's images-provider vocabulary is {comfy, openai-compat} (ADR 0083 decision 5).
   *  True when this row names anything else — most likely `sdcpp`, retired the same ADR — and
   *  no client here can generate from it no matter what its mode or lifecycle say. Absent, not
   *  false, for every row this build CAN serve. */
  provider_unserved?: boolean;
  models?: string[];
  /** Every row of the catalogue, enabled or not — this panel is where one is turned ON, so a
   *  list filtered to the enabled ones would have no way to reach the others. */
  model_rows?: EngineModel[];
  /** Whether anything is enabled at all. Stated by the CP rather than inferred from the list,
   *  because it is the reason the controller refuses to start the engine. */
  has_models?: boolean;
  /** The checkpoint families this provider picks a workflow graph from, and the labels a split
   *  model's files may carry. BOTH absent for a provider with no opinion (sd.cpp holds one
   *  unlabelled checkpoint and never reads a family), which is what the panel reads as "do not
   *  ask" — offering a choice that changes nothing is worse than offering none. */
  base_models?: string[];
  file_flags?: string[];
  /** What this deployment excludes from every image this engine makes (ADR 0072 follow-up,
   *  negative prompts), and how long that list may be. Only ever present on an image engine —
   *  a chat engine has nothing to exclude — so the section is drawn on the field arriving. */
  negative_always?: string;
  negative_max?: number;
  /** 🔴 Optional because the row a granted tenant_admin receives DOES NOT CARRY THEM (ADR 0072
   *  open question 11). That row is a strict subset of the operator's, so everything the
   *  reduced panel does not draw is simply absent — which is why the panel branches on the
   *  answer's `super_admin` flag and never on "did this field arrive". */
  mode?: string;
  enabled?: boolean;
  managed?: boolean;
  /** Why this deployment does not own the engine's lifecycle: `external` is a URL an operator
   *  pointed the control plane at — a ComfyUI on the LAN (ADR 0076 decision 1) — and `remote`
   *  is another Agent Fleet's gateway, which will start the engine for us (ADR 0079 decision 1).
   *  DECLARED by whoever wrote the engine table, never derived from "the ECS fields are
   *  missing". */
  lifecycle?: string;
  /** Where a row this deployment does not own points. Shown because it is the only answer to
   *  "which box is this": on a LAN nothing else on this screen names the machine, and for a
   *  borrowed row it is the only answer to "whose GPU is this model running on". Absent on a
   *  managed row: the upstream there is an ECS service the operator never typed. */
  url?: string;
  state?: string;
  desired?: number;
  /** The engine answered a real request since it came up. Different from `state:"running"`:
   *  llama-server binds its port 267 seconds before the weights are in VRAM (measured), so a
   *  panel showing only the ECS state reports an engine as up through its whole cold start. */
  warm?: boolean;
  /** The model whose weights are actually in VRAM, and how many times that changed. Both are
   *  in-memory facts of the CURRENT control-plane process (as window_units is), and the swap
   *  count is the visible price of `--models-max 1`: every change cost an unload plus a load. */
  warm_model?: string;
  model_swaps?: number;
  /** Service events — the only place ECS writes down why a start failed. */
  events?: string[];
  service_since?: string;
  box?: EngineBox;
  /** The GPU ladder, and where this role sits on it. All absent on a deployment that declares
   *  no ladder, which is what the panel reads as "this deployment does not choose its box". */
  classes?: EngineClass[];
  class?: EngineClass;
  class_default?: string;
  /** 🔴 Stated by the CP, not computed here: "you are not on the default" is the sentence that
   *  keeps a temporary experiment from becoming a permanent hourly bill (ADR 0074 decision 7).
   *
   *  ⚠️ ONE meaning moved under ADR 0075 decision 8 and the field did not: where `offers` arrive,
   *  false means PINNED — the stored choice is an offer id, so this role does not fall through to
   *  the next offer. Absent `offers` it still means "not the deployment's default rung". */
  class_is_default?: boolean;
  /** The ladder as offers (ADR 0075). Absent from a control plane too old to send it, and the
   *  whole offers half of this panel is drawn off its presence — an old CP must get the ADR 0074
   *  screen back, pixel for pixel. */
  offers?: EngineOffer[];
  offer?: EngineOfferRef;
  /** Whether an administrator has accepted interruption for this role, which is what lets a
   *  `spot` offer be bought at all. 🔴 Not derivable from `offers`: those say what the OPERATOR
   *  declared and this says what the ADMINISTRATOR accepted, so a declared Spot row with this
   *  false is an offer that exists and will not be bought — which is exactly what the picker has
   *  to draw. Absent from a control plane too old to send it, and the tick box is drawn off its
   *  presence: an old CP buys Spot the moment it is declared, and offering a control that would
   *  not reach it would be worse than not offering one. */
  spot_allowed?: boolean;
  /** What this demand tried, in the order it tried them. Omitted while nothing has been tried. */
  offer_trail?: EngineOfferTry[];
  /** A box of another rung is still up, so the saved choice has reached nothing yet. */
  class_replace_pending?: boolean;
  /** 🔴 Why the capacity provider does not hold the rung above. The CP saves the choice BEFORE
   *  it applies it, so a failed apply leaves this picker showing a rung nothing was written for
   *  — and picking that same rung again is no change, so nothing is sent. This field is what
   *  the retry hangs off. In-memory at the CP: absent means "no claim", never "it was applied". */
  class_apply_error?: string;
  /** The largest demand among the ENABLED models — a maximum, not a sum: one model is in VRAM
   *  at a time (`--models-max 1`, one checkpoint). */
  vram_need_mib?: number;
  vram_need_source?: "declared" | "floor" | "unknown";
  vram_need_model?: string;
  vram_fits?: boolean;
  /** When the controller will stop it by itself. ABSENT is meaningful: pinned on, switched
   *  off, already stopped, or no demand mark yet — see engineStopETA. Never render a fallback. */
  stop_eta?: string;
  idle_secs?: number;
  idle_min_secs?: number;
  window_secs?: number;
  window_units?: number;
  window_counted_secs?: number;
  last_demand?: string;
  error?: string;
};

/** A row this deployment does not start or stop — the URL half of ADR 0076 decision 1.
 *
 * 🔴 `managed === false`, never `!managed`. A granted tenant_admin's row carries no `managed` at
 * all (ADR 0072 open question 11), and reading its absence as "external" would strip the ECS half
 * off the operator's own panel the moment a field is renamed — the same failure `isSuper` is
 * taken from an explicit flag to avoid.
 *
 * What hangs off this: the mode segment drops `ondemand` (the API answers 400 — there is no box
 * to stop, decision 5), and everything ECS wrote about a box — uptime, stop countdown, idle
 * policy, demand window, the GPU ladder, the heatmap — is not drawn at all. The control plane
 * omits those fields rather than zeroing them, so this is belt and braces: an invented "0
 * requests" or "no ladder" reads as a measurement of a machine nobody here owns. */
export function engineIsExternal(e: EngineRow): boolean {
  return e.managed === false;
}

/** A row borrowed from another Agent Fleet: its gateway relays, and ITS control plane buys and
 *  stops the box (ADR 0079 decision 1). A strict subset of `engineIsExternal` — everything that
 *  hangs off "this deployment owns no box" is already right for it — and this predicate is only
 *  for the three places where "somebody is on the other end" changes the answer:
 *
 *    - the badge. "Externally managed" is true and useless when the far end is a fleet with an
 *      admin panel of its own; the operator's next act is over THERE (decision 10).
 *    - the URL beside it, which is the only thing on this screen that names which deployment.
 *    - the catalogue, which is the far administrator's document mirrored read-only here
 *      (decision 7). Every write route answers 400 `engine_not_ours`, so the controls are not
 *      drawn at all — the same reason the reduced tenant_admin panel omits rather than disables.
 *
 * 🔴 DECLARED: `lifecycle` is the row's own field, which the CP emits verbatim (`engine_admin.go`
 * `row()`). Never inferred from the URL's shape — a URL that happens to contain `/engine/` must
 * not silently change what a row means (ADR 0053). `managed === false` is required alongside it
 * for the same reason `engineIsExternal` reads the explicit flag: a row that claims to be
 * borrowed while this deployment holds its service is a contradiction, and the half that decides
 * whether ECS controls are drawn must not be the guessed half. */
export function engineIsRemote(e: EngineRow): boolean {
  return e.managed === false && e.lifecycle === "remote";
}

/** The modes this row can actually be put in. Two for an external engine, three for a managed
 *  one — and drawing a button the API answers with 400 is worse than drawing none. */
export function engineModes(e: EngineRow): readonly string[] {
  return engineIsExternal(e) ? ["off", "on"] : ["off", "ondemand", "on"];
}

/** A search source in the model catalogue. `civitai-red` is Civitai's NSFW sister domain and is
 *  the only one a deployment can decline to offer — see `catalogSources` below. */
export type CatalogSource = "hf" | "civitai" | "civitai-red";

/** What a deployment offers when the answer does not say. The safe half, for the same reason
 *  `isSuper` defaults to false: a Control Plane too old to send the list has no way to mean
 *  "and the NSFW one", and assuming it would draw the tab exactly where it is not wanted. */
export const CATALOG_SOURCES_DEFAULT: readonly CatalogSource[] = ["hf", "civitai"];

function catalogSources(value: unknown): CatalogSource[] {
  if (!Array.isArray(value)) return [...CATALOG_SOURCES_DEFAULT];
  const known: CatalogSource[] = ["hf", "civitai", "civitai-red"];
  return known.filter((source) => value.includes(source));
}

/** Everything both screens need out of `GET /api/admin/engines`, read once per screen.
 *
 * 🔴 `isSuper` comes from the answer's own flag and is never inferred from which fields arrived:
 * the tenant_admin row is a strict SUBSET of the operator's (ADR 0072 open question 11), so
 * "mode is undefined" would work today and start drawing 403-ing buttons the day a field is
 * renamed. It defaults to false, so a Control Plane too old to send the flag shows the safe half
 * rather than controls that cannot work.
 *
 * Two screens asking separately costs one extra GET on a screen change and buys the thing that
 * matters: neither of them has to be mounted for the other to work. */
export function useEngineRows() {
  const tr = useT();
  // The last answer, so a pane that is mounted again — the return from another tab — draws the
  // catalogue it had instead of a page of nothing for the length of one round trip. It is still
  // re-read below; this decides only what is on screen while that happens.
  const [held] = useState(() => loadEngines());
  const [rows, setRows] = useState<EngineRow[] | null>(held?.rows ?? null);
  const [isSuper, setIsSuper] = useState(held?.isSuper ?? false);
  // The source tab strips are drawn from this and never from a literal: three strips each
  // keeping their own list is three places for a deployment's gate to be forgotten.
  const [sources, setSources] = useState<CatalogSource[]>(held?.sources ?? [...CATALOG_SOURCES_DEFAULT]);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/engines");
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      const answered = Array.isArray(d?.engines) ? d.engines : [];
      const superAdmin = !!d?.super_admin;
      const offered = catalogSources(d?.catalog_sources);
      setIsSuper(superAdmin);
      setSources(offered);
      setRows(answered);
      saveEngines({ rows: answered, isSuper: superAdmin, sources: offered });
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);

  return { rows, isSuper, sources, err, setErr, setRows, load };
}

// The heading names the engine by what it does, not by its key: "llm" and "image" are the
// stack's words, and `provider` is what a session sees in its launch menu.
export function engineTitle(e: EngineRow): string {
  const provider = e.provider || e.key;
  return e.api === "images" ? `${provider} (${e.key}) — image` : `${provider} (${e.key})`;
}

/** The role this engine plays, as the models screen's own tab key. `images` is the CP's word for
 *  the API a row speaks, and it is the only thing that tells a checkpoint catalogue from a GGUF
 *  one — the two have different files, different families and different forms. */
export function engineIsImage(e: EngineRow): boolean {
  return e.api === "images";
}
