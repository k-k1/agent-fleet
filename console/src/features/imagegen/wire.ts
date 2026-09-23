// features/imagegen/wire — the wire of ADR 0081, as TypeScript, and the pure readers of it.
//
// Split from `api.ts` (which holds the fetch calls) for one concrete reason: `api.ts` imports
// the shared client, which touches `localStorage` at module scope, and the node test project
// has no DOM. Every pure module here — jobs, draft, studioSync — needs these shapes and none
// of them needs a fetch, so the types live in a module with no runtime imports at all.
//
// **These names are the Agent's JSON tags** (`workspace/agent/internal/imagegen/{jobs.go,
// jobs_http.go,http.go,props.go}` on lane A), not the ADR's prose, wherever the two differ.
// The seven places they differ are listed in lane A's PR (#626) and each one is marked below.
// The general rule the Agent followed: a key the MCP path already ships keeps its existing
// camelCase spelling (`negativePrompt`, `aspectRatio`, a LoRA's `baseModel`), and only NEW
// keys are snake_case. Renaming an old one would be a wire break for the shared shape.
import type { ApiError } from "../../core/api/client.ts";

/** Where a job is. `waking` is the engine box starting, and is the reason the pane exists
 *  at all: a five-minute `running` with no explanation reads as a hang (decision 2). */
export type JobState =
  | "queued"
  | "waking"
  | "uploading"
  | "running"
  | "fetching"
  | "done"
  | "failed"
  | "cancelled";

/** A group is what one press of "enqueue N" submitted, and the unit every batch verb acts on. */
export type GroupState = "running" | "paused" | "done" | "cancelled";

export type SeedPolicy = "random" | "fixed" | "sequence";

/** The knobs a family actually reads. The Agent computes this from the same table the graph
 *  templates use; the Console must never keep a second copy (decision 4). `strength` is ADR
 *  0094 decision 12's addition — the first knob a family can answer false for (Qwen-Image-Edit
 *  fixes its denoise at 1). */
export type Knob = "steps" | "cfg" | "sampler" | "scheduler" | "negative" | "strength";

/** The ComfyUI families the Agent has templates for. A row whose `base_model` is
 *  something else still renders — the family card is the only thing that goes missing. */
export type Family =
  | "sd15"
  | "sdxl"
  | "sd35"
  | "flux1"
  | "flux2-klein"
  | "zimage"
  | "anima"
  | "krea2"
  | "qwen-image-edit-2509"
  | "qwen-image-edit-2511"
  | "qwen-image-2.1";

/** The `params` overlay of decision 4, in the shape the catalogue row already uses.
 *  `clip_skip` and `weight` ride along on the catalogue's side; the form sends neither. */
export interface EngineParams {
  steps?: number;
  cfg?: number;
  sampler?: string;
  scheduler?: string;
  clip_skip?: number;
  weight?: number;
}

/** `weight` 0 means "not stated" on this wire, and the Agent then uses 1 (decision 5). */
export interface LoraRef {
  name: string;
  weight?: number;
}

/** One checkpoint as the member-facing catalogue reports it (decision 5). Everything past
 *  `warm` is new in this ADR; an Agent that predates it sends the first three and the form
 *  degrades to "no knobs declared", which is what `knobs` being absent has to mean. */
export interface ImagegenModel {
  id: string;
  /** What to SHOW instead of the id (ADR 0090) — the publisher's name and the part that tells
   *  two sizes of one model apart, composed by the Control Plane.
   *
   *  🔴 A name to draw, never a value to send: `id` is what a generation names. Absent on an
   *  Agent or a Control Plane that composes none, and the form then draws the id as it always
   *  did. */
  label?: string;
  description?: string;
  warm?: boolean;
  family?: string;
  sizes?: string[];
  /** EFFECTIVE defaults (recipe ← row), so a placeholder in the form is what will run. */
  params?: EngineParams;
  /** The administrator's negative for this row. Shown as a chip the member cannot remove. */
  negative?: string;
  knobs?: Knob[];
  /** This MODEL's own op list (ADR 0094 decision 12) — unlike ImagegenProvider.ops, which is a
   *  UNION over every model on the route (decision 11). Absent on an Agent that predates the
   *  ADR, in which case the form falls back to the full OPS list — the same "no signal, assume
   *  the old permissive shape" rule `knobs` already follows. */
  ops?: string[];
  /** How many reference pictures THIS model reads (ADR 0094 decision 5, P3). Absent on an Agent
   *  that predates it, and the form then draws the single slot it always did — the same "no
   *  signal, assume the old shape" rule `knobs` and `ops` follow. */
  max_inputs?: number;
  license_name?: string;
  license_url?: string;
  source_url?: string;
  typical_ms?: number;
  /** ADR 0100 decision 7: the family's facts, from the Agent's family table rather than a
   *  second copy in the Console. Absent on an Agent that predates it; the card then says it has
   *  no guide for the family instead of guessing. */
  dialect?: "tags" | "sentences";
  quality_prefixes?: string[];
  steps_range?: [number, number];
  /** Absent for a family that does not read cfg. */
  cfg_range?: [number, number];
  /** The steps a trial runs at (ADR 0081 decision 11). */
  trial_steps?: number;
}

export interface ImagegenLora {
  name: string;
  description?: string;
  /** camelCase, deliberately: the same fact already rides this route under this key, and lane
   *  A refused to add a second spelling of it (#626, deviation 2). `trained_words` is new and
   *  therefore snake_case. */
  baseModel?: string;
  trained_words?: string[];
  /** The strength the catalogue declares for this adapter. 0 / absent = not declared, and the
   *  Agent then uses 1. */
  weight?: number;
}

/**
 * One ready provider. The engine-level fields of decision 5 (`samplers`, `schedulers`,
 * `typical_ms`, `wake_ms`, `lora_weight_max`, `negative_always`) live HERE, not on the status
 * root — they are per provider, and a fleet with both comfy and openai-compat has two answers.
 */
export interface ImagegenProvider {
  id: string;
  /**
   * Whether the fleet's own hardware — or another fleet's, borrowed (ADR 0079 decision 9) —
   * serves this row (ADR 0082 decision 4, the Agent's own providerIsFleet).
   *
   * This is the ONLY thing that tells a fleet row apart from a CLI-driven one: since ADR 0082
   * P0 `id` is the images ROW's own key ("image", "comfy-lan", …), not one of a fixed set of
   * kind names, so matching it against a hardcoded `["comfy","openai-compat"]` stops finding
   * anything the moment a deployment's row is keyed anything else — which every real
   * deployment's default row already is (`AF_COMFY_URL` and the images role both compose the
   * fixed key "image"). Read `fleet` instead of guessing from the id's spelling.
   */
  fleet?: boolean;
  /**
   * The CLIENT implementation behind this row (ADR 0082 decision 1): `comfy` / `openai-compat`
   * for a fleet row, this route's own id for a vendor route (whose id already IS its kind).
   *
   * Used only by the settings screen's ordering list (`console/src/lib/settings.ts`,
   * `ImageFleetRow`) to expand a legacy stored alias into today's row(s) of that kind, and to
   * label a row with what kind of machine it is — never to decide fleet-ness, which `fleet`
   * itself already answers.
   */
  kind?: string;
  service?: string;
  model?: string;
  ops?: string[];
  aspectRatios?: string[];
  models?: ImagegenModel[];
  loras?: ImagegenLora[];
  seed?: boolean;
  negative?: boolean;
  strength?: boolean;
  samplers?: string[];
  schedulers?: string[];
  negative_always?: string;
  lora_weight_max?: number;
  typical_ms?: number;
  /** The observed cold start, as a moving average. 0 / absent = nothing measured yet, which
   *  is a different statement from "the engine starts instantly" (#626, deviation 3). */
  wake_ms?: number;
}

export interface ImagegenStatus {
  enabled: boolean;
  ready: boolean;
  provider?: string;
  kind?: string;
  ops?: string[];
  providers?: ImagegenProvider[];
  error?: ApiError;
}

/**
 * The body of `POST /imagegen/jobs` — decision 2's one list: the existing generateRequest
 * minus `session`, plus `params`, `label`, `out_dir`, `jobs`, `seed_policy`, `trial` and
 * `full_steps`. Inherited keys keep their camelCase; the new ones are snake_case.
 */
export interface EnqueueRequest {
  provider?: string;
  op?: string;
  prompt: string;
  negativePrompt?: string;
  size?: string;
  aspectRatio?: string;
  background?: string;
  /** ComfyUI `batch_size`. Above 4 the Agent answers 400 `bad_count` (#626, deviation 4). */
  count?: number;
  inputs?: string[];
  mask?: string;
  model?: string;
  loras?: LoraRef[];
  seed?: number;
  strength?: number;
  params?: EngineParams;
  label?: string;
  /** Ignored on a trial: those always go to `generated/console/trial/` (#626, deviation 5). */
  out_dir?: string;
  /** N ≥ 1: how many jobs the group expands into (decision 8). */
  jobs?: number;
  seed_policy?: SeedPolicy;
  /** Head of the queue, family trial steps, `generated/console/trial/` (decision 11). */
  trial?: boolean;
  /** Keep the trial's position and folder but run the form's steps (decision 11). */
  full_steps?: boolean;
}

export interface EnqueueResult {
  group?: string;
  jobs?: { id: string; position: number }[];
  error?: ApiError;
}

/** A picture on disk. `seed` is per image: the base seed at batch index 0, `seed+i` after. */
export interface StoredFile {
  path: string;
  name?: string;
  mime?: string;
  bytes?: number;
  width?: number;
  height?: number;
  seed?: number;
}

export interface Job {
  id: string;
  group?: string;
  label?: string;
  state: JobState;
  /** 1-based, and only while queued. */
  position?: number;
  trial?: boolean;
  provider?: string;
  model?: string;
  family?: string;
  op?: string;
  prompt?: string;
  /** `negative`, not `negative_prompt`: the job wire is the sidecar's spelling. */
  negative?: string;
  /** What was PINNED, absent when the provider drew one — the seed a picture actually came
   *  out at is `files[].seed`, because a batch of four is four seeds. */
  seed?: number;
  size?: string;
  count?: number;
  params?: EngineParams;
  loras?: LoraRef[];
  strength?: number;
  inputs?: string[];
  /** What the BATCH would run at, on a trial whose steps were reduced (decision 11). */
  full_steps?: number;
  out_dir?: string;
  created_at?: string;
  started_at?: string;
  /**
   * FINISHED jobs only, both of them. A live `elapsed_ms` would move on every poll and break
   * the control plane's ETag for the life of the batch — the mirror measured what that costs
   * a phone — so a running job's elapsed time is `now − started_at`, computed here (#626,
   * deviation 1). `jobElapsedMs` is the one place that does it.
   */
  finished_at?: string;
  elapsed_ms?: number;
  typical_ms?: number;
  files?: StoredFile[];
  warnings?: string[];
  error?: string;
}

export interface JobGroup {
  id: string;
  label?: string;
  state: GroupState;
  total: number;
  done: number;
  /** Separate counts: "12 of 40 made" and "12 of 40, 3 refused" are different things. */
  failed: number;
  cancelled?: number;
  /** The id of the job in flight, or absent. A STRING, not a count — the queue is serial. */
  running?: string;
  trial?: boolean;
  eta_ms?: number;
  paused_at?: string;
}

export interface JobsResponse {
  jobs?: Job[];
  groups?: JobGroup[];
  /** The queue-wide pause, not a group's. */
  paused?: boolean;
  /** The caps, so the form can say "full" before the Agent has to answer 429 (#626, 3). */
  queued?: number;
  queue_max?: number;
  trial_pending?: number;
  trial_max?: number;
  wake_ms?: number;
  error?: ApiError;
}

export type PropsSource = "sidecar" | "png" | "none";

/** What `GET /imagegen/props` recovers for one picture (decision 3). `source: "none"` means
 *  the UI says so rather than drawing a table of blanks. The sampler knobs are nested in
 *  `params`, the same shape the request carries them in — not flattened. */
export interface ImageProperties {
  source: PropsSource;
  provider?: string;
  model?: string;
  family?: string;
  op?: string;
  prompt?: string;
  negative?: string;
  seed?: number;
  size?: string;
  params?: EngineParams;
  full_steps?: number;
  loras?: LoraRef[];
  strength?: number;
  inputs?: string[];
  mask?: string;
  job?: string;
  group?: string;
  label?: string;
  trial?: boolean;
  elapsed_ms?: number;
  warnings?: string[];
  agent?: string;
  created_at?: string;
  error?: ApiError;
}

/** The four batch verbs of decision 12. All of them act on a GROUP — the thing the person
 *  submitted — and never on one picture; the per-job `DELETE` is the single-picture door. */
export type GroupOp = "pause" | "resume" | "skip" | "cancel";

export type QueueOp = "pause" | "resume";

/**
 * How long a job has been going, in ms, or null.
 *
 * A finished job carries `elapsed_ms`; a running one carries only `started_at`, on purpose
 * (see `Job.finished_at`). Every reader goes through here so the two cases cannot drift, and
 * so no view invents a duration for a job that has not started.
 */
export function jobElapsedMs(j: Job | null | undefined, now: number): number | null {
  if (!j) return null;
  if (j.elapsed_ms != null) return j.elapsed_ms;
  if (!j.started_at) return null;
  const t = Date.parse(j.started_at);
  return Number.isFinite(t) ? Math.max(0, now - t) : null;
}

/** A LoRA's trigger words. */
export const loraTriggers = (l: ImagegenLora): string[] => l.trained_words || [];

/** A LoRA's declared weight, or the Agent's default of 1 when the row declares none. */
export const loraWeight = (l: ImagegenLora): number => (l.weight && l.weight > 0 ? l.weight : 1);

/**
 * Whether a `providers` list came from an Agent build old enough to predate `kind` (ADR 0082
 * P0/P1) — the only reliable signal a CURRENT Agent stamps `kind` on EVERY entry, fleet or
 * vendor (the Agent's providerKindOf falls back to the route's own id for codex/agy), while an
 * old one sends none at all.
 *
 * `fleet` cannot be used for this the same way: a CURRENT Agent with zero declared fleet rows
 * also has every entry's `fleet` omitted (Go's `json:",omitempty"` drops `false`), which is
 * indistinguishable from the pre-ADR shape by that field alone.
 *
 * Vacuously false for an empty list: "nothing is ready right now" on a current Agent is a real,
 * different answer from "this build cannot say" and must not be read as the same thing — an
 * earlier version of the caller conflated them and made the fleet's own engine vanish from the
 * settings screen's ordering list on any pre-P1 Agent, the same class of bug ADR 0082 P0 itself
 * shipped once already (fleetProvider matching the id's spelling instead of reading `fleet`).
 */
export function isPreAdr0082Status(providers: ImagegenProvider[]): boolean {
  return providers.length > 0 && providers.every((p) => !p.kind);
}

// The provider ids a pre-ADR-0082 Agent could ever have sent for its fleet row — the fixed
// vocabulary of imagegen.providerRanks before P0 made the id the row's own key. `sdcpp` is ADR
// 0083's retired id (an Agent that old predates `fleet`/`kind` too, by construction). This list
// is closed and never grows: a CURRENT Agent's row keys are free text an operator chose, and
// matching THOSE by name would be exactly the ADR 0082 P0 regression (id-spelling routing) all
// over again — which is why every reader below reaches this list ONLY behind
// isPreAdr0082Status, never unconditionally.
const PRE_ADR_0082_FLEET_IDS = ["comfy", "openai-compat", "sdcpp"];

/**
 * Every ready fleet row (ADR 0082 P1) — the universe the studio's own provider picker offers, as
 * opposed to `fleetProvider`'s single "the first one", which is what availability and
 * `engineState` still only need.
 *
 * Falls back to matching the old, fixed kind-name ids ONLY when isPreAdr0082Status confirms
 * this status really is that old shape (ADR 0082 P1 follow-up, PR #658 review): without this,
 * the CP/Agent version gap that already required a guard for the settings screen's ordering
 * list also makes the whole image generation pane — and the button that opens it (available.ts)
 * — disappear for as long as the Workspace's Agent build lags the Control Plane's, which is
 * exactly the silent-disappearance experience ADR 0083 decision 5 was written to stop happening
 * again. The gate is what keeps this from being the id-matching regression ADR 0082 P0 itself
 * shipped: a CURRENT Agent's row keys are free text, and matching them by name would silently
 * pick up an unrelated row that happens to be keyed "comfy".
 */
export function fleetProviders(st: ImagegenStatus | null): ImagegenProvider[] {
  const list = st?.providers || [];
  const byFlag = list.filter((p) => p.fleet);
  if (byFlag.length > 0) return byFlag;
  if (isPreAdr0082Status(list)) return list.filter((p) => PRE_ADR_0082_FLEET_IDS.includes(p.id));
  return [];
}

/**
 * The provider this pane drives: the first entry of fleetProviders (ADR 0082 decision 4, and —
 * on an Agent old enough to predate it — the compat fallback described there).
 */
export function fleetProvider(st: ImagegenStatus | null): ImagegenProvider | null {
  return fleetProviders(st)[0] || null;
}

/**
 * Which fleet row drives the studio pane: the member's own pick (`providerId`, from
 * `ImagegenDraft`) if it is still among the ready ones, else the first — the same fallback
 * `model` already follows one field up. A stale or unset id (the row was removed, or this is a
 * different deployment's draft) is read as "no choice", never as an error (ADR 0082 unresolved
 * question 2: with N fleet rows, each carries its OWN Studio answer — overlapping model ids
 * across two comfy rows are a real possibility — so silently driving the pane off "whichever is
 * first" is only right while there is nothing else to pick).
 */
export function resolveFleetProvider(providers: ImagegenProvider[], providerId: string): ImagegenProvider | null {
  return providers.find((p) => p.id === providerId) || providers[0] || null;
}

// --- ADR 0100: the image studio -------------------------------------------------------------
//
// The Agent's copy is `workspace/agent/internal/imagegen/studio.go` (ImageStudio*, DraftLog*,
// Knowledge*); the JSON keys below are its tags. A draft uses POST /imagegen/jobs' own keys, so
// a press turns it into a job with no translation — camelCase where that route inherited it.

/** The draft fields the agent may write (decision 4). Every other field is the member's. */
export const STUDIO_AGENT_FIELDS = [
  "prompt",
  "negativePrompt",
  "params",
  "size",
  "loras",
  "strength",
  "op",
  "inputs",
  "suggest_model",
] as const;

/** The studio's draft. Every field may be missing: an unfinished draft is allowed (decision 3). */
export interface StudioDraft {
  /** The ready provider row the pane resolved; a trial never falls back to another one. */
  provider?: string;
  model?: string;
  op?: string;
  prompt?: string;
  negativePrompt?: string;
  size?: string;
  aspectRatio?: string;
  count?: number;
  inputs?: string[];
  mask?: string;
  loras?: LoraRef[];
  seed?: number;
  strength?: number;
  params?: EngineParams;
  label?: string;
  out_dir?: string;
  jobs?: number;
  seed_policy?: SeedPolicy;
  /** The pane's "trial at full steps" (ADR 0081 decision 11). The member's field. */
  full_steps?: boolean;
  /** The agent's proposal for `model`, which only the member sets; shown as a card. */
  suggest_model?: string;
}

/** One studio as stored (decision 2). */
export interface Studio {
  id: string;
  title: string;
  draft: StudioDraft;
  /** Draft fields the agent may not change, by key. */
  locks?: string[];
  /** The bound session, the truth of the binding. Absent when none. */
  session?: string;
  /** "Let the agent run a trial" (decision 3, on by default). */
  agent_trial: boolean;
  /** Canvas strokes (decision 11, P1), opaque to the Agent. */
  mask_strokes?: unknown;
  created_at: string;
  /** Also the version a PUT sends as If-Match. */
  updated_at: string;
}

/** GET /imagegen/studios/{id}: the stored studio plus what is derived from it. */
export interface StudioWire extends Studio {
  /** op=inpaint with no mask — derived on every read, never stored (decision 4). */
  needs_mask?: boolean;
  /** The newest edit-log entries (at most 20), newest last; the rest via draft-log. */
  recent_log?: DraftLogEntry[];
  error?: ApiError;
}

export interface StudioSummary {
  id: string;
  title: string;
  session?: string;
  updated_at: string;
}

export interface StudioList {
  studios: StudioSummary[];
  error?: ApiError;
}

/** POST /imagegen/studios: the pane moves its localStorage draft in. */
export interface StudioCreate {
  title?: string;
  draft: StudioDraft;
}

/** PUT /imagegen/studios/{id}: a merge patch — an absent key is unchanged, null clears it. */
export interface StudioPatch {
  draft?: { [K in keyof StudioDraft]?: StudioDraft[K] | null };
  author: "human" | "agent";
  session?: string;
  title?: string;
  locks?: string[];
  agent_trial?: boolean;
  mask_strokes?: unknown;
}

/** Why a patch left a field alone: kept apart so "locked" and "invalid" read differently. */
export interface DroppedField {
  field: string;
  reason: "locked" | "human_only" | "invalid";
  detail?: string;
}

export interface StudioPatchResult {
  studio: StudioWire;
  dropped?: DroppedField[];
  error?: ApiError;
}

export type DraftLogKind = "edit" | "press" | "press_result" | "rewind";

/** Which button made a version. */
export type PressMode = "trial" | "enqueue" | "agent_trial";

/** One line of the studio's edit log (decision 9). A reader takes the FIRST press_result per
 *  version and ignores later ones. */
export interface DraftLogEntry {
  seq: number;
  kind: DraftLogKind;
  at: string;
  author?: "agent" | "human" | "rewind";
  session?: string;
  changes?: DraftChange[];
  draft?: StudioDraft;
  /** The seq a rewind restored. */
  rewind_to?: number;
  /** The press's id; a picture's sidecar and history row carry it. */
  version?: string;
  mode?: PressMode;
  group?: string;
  jobs?: string[];
  state?: "ok" | "failed" | "recovered" | "lost";
  error?: string;
}

export interface DraftChange {
  field: string;
  before?: unknown;
  after?: unknown;
}

export interface DraftLogPage {
  entries: DraftLogEntry[];
  /** The seq to ask for next; absent when there is nothing older. */
  before?: number;
  error?: ApiError;
}

export interface StudioPressResult {
  version: string;
  group?: string;
  jobs?: { id: string; position: number }[];
  /** false: the job runs but its press_result line did not append — show "record pending". */
  recorded: boolean;
  error?: ApiError;
}

/** The first turn a new session is sent, in the member's language (decision 5). */
export interface StudioPersona {
  prompt: string;
  lang: string;
  error?: ApiError;
}

export interface HistoryItem {
  path: string;
  studio?: string;
  version?: string;
  created_at: string;
  trial?: boolean;
}

export interface HistoryPage {
  items: HistoryItem[];
  before?: string;
  error?: ApiError;
}

export type KnowledgeScope = "family" | "model";

/** One document of ~/imagegen-knowledge in its four sections (decision 12). */
export interface Knowledge {
  scope: KnowledgeScope;
  key: string;
  path: string;
  summary: string;
  settings: string;
  prompts: string;
  records: string;
  summary_truncated?: boolean;
  /** `path` relative to the browse root — what the Files pane opens. Absent when the browse
   *  root is not home: the pane then edits the four sections itself (decision 12). */
  files_path?: string;
  /** The content as read; a KnowledgeEdit names it. Absent while there is no file yet. */
  version?: string;
  error?: ApiError;
}

/** PUT /imagegen/knowledge — the pane's editor, all four sections at once. Refused with 412
 *  `knowledge_changed` when the file moved since `version` was read. */
export interface KnowledgeEdit {
  scope: KnowledgeScope;
  key: string;
  version: string;
  summary: string;
  settings: string;
  prompts: string;
  records: string;
}

/** POST /imagegen/knowledge — appends to "records" only. */
export interface KnowledgeAdd {
  scope: KnowledgeScope;
  key: string;
  note: string;
  evidence?: string;
}

/** needs_mask as the Agent derives it, for a pane that has edited the draft locally. */
export const studioNeedsMask = (d: StudioDraft): boolean => d.op === "inpaint" && !(d.mask ?? "").trim();
