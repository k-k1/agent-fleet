// features/imagegen/wire — the wire of ADR 0081, as TypeScript, and the pure readers of it.
//
// Split from `api.ts` (which holds the fetch calls) for one concrete reason: `api.ts`
// imports the shared client, which touches `localStorage` at module scope, and the node test
// project has no DOM. Every pure module here — jobs, draft, prompthelp — needs these shapes
// and none of them needs a fetch, so the types and the two spelling-tolerant readers live in
// a module with no imports at all.
//
// Every shape is the ADR's (decisions 2, 3, 5, 11, 12), not the Agent's source: this lane was
// built alongside the Agent lane, so the ADR is the contract both sides read. Where the
// Agent's EXISTING `/imagegen/status` already names a field in camelCase (`aspectRatios`, a
// LoRA's `baseModel`) and the ADR's new text names it in snake_case, both spellings are
// accepted on the way in — one relay renaming a field it already ships would be a silent
// regression for the MCP path, and guessing which way it lands would make this file wrong
// half the time. `loraBaseModel` / `loraTriggers` are the only place that ambiguity lives.
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
 *  templates use; the Console must never keep a second copy (decision 4). */
export type Knob = "steps" | "cfg" | "sampler" | "scheduler" | "negative";

/** The five ComfyUI families the Agent has templates for. A row whose `base_model` is
 *  something else still renders — the family card is the only thing that goes missing. */
export type Family = "sdxl" | "sd35" | "flux1" | "flux2-klein" | "zimage";

/** The `params` overlay of decision 4, in the shape the catalogue row already uses. */
export interface EngineParams {
  steps?: number;
  cfg?: number;
  sampler?: string;
  scheduler?: string;
}

export interface LoraRef {
  name: string;
  weight?: number;
}

/** One checkpoint as the member-facing catalogue reports it (decision 5). Everything past
 *  `warm` is new in this ADR; an Agent that predates it sends the first three and the form
 *  degrades to "no knobs declared", which is what `knobs` being absent has to mean. */
export interface ImagegenModel {
  id: string;
  description?: string;
  warm?: boolean;
  family?: string;
  sizes?: string[];
  /** EFFECTIVE defaults (recipe ← row), so a placeholder in the form is what will run. */
  params?: EngineParams;
  /** The administrator's negative for this row. Shown as a chip the member cannot remove. */
  negative?: string;
  knobs?: Knob[];
  license_name?: string;
  license_url?: string;
  source_url?: string;
}

export interface ImagegenLora {
  name: string;
  description?: string;
  /** snake_case is the ADR's; camelCase is what `loraStatus` ships today. */
  base_model?: string;
  baseModel?: string;
  trained_words?: string[];
  trainedWords?: string[];
}

export interface ImagegenProvider {
  id: string;
  service?: string;
  model?: string;
  ops?: string[];
  aspectRatios?: string[];
  models?: ImagegenModel[];
  loras?: ImagegenLora[];
  seed?: boolean;
  negative?: boolean;
  strength?: boolean;
  /** Engine-level allow-lists (decision 5). Per-provider is the fallback reading; the
   *  top-level one on the status is what the ADR's text describes. */
  samplers?: string[];
  schedulers?: string[];
  typical_ms?: number;
  lora_weight_max?: number;
}

export interface ImagegenStatus {
  enabled: boolean;
  ready: boolean;
  provider?: string;
  kind?: string;
  ops?: string[];
  providers?: ImagegenProvider[];
  /** The Agent's allow-lists: the form must not be able to offer a name the Agent refuses. */
  samplers?: string[];
  schedulers?: string[];
  /** Rolling average of a finished job, per (provider, model, size bucket, steps). */
  typical_ms?: number;
  /** `comfyMaxLoraWeight`. No column holds a DEFAULT weight — the Agent uses 1 (decision 5). */
  lora_weight_max?: number;
  /** The deployment-wide negative, shown next to the row's and equally unremovable. */
  negative_always?: string;
  /** Last observed wake, so "cold" can say how many minutes the first picture costs. */
  cold_ms?: number;
  error?: ApiError;
}

/** The body of `POST /imagegen/jobs` — decision 2's one list: the existing generateRequest
 *  minus `session`, plus `params`, `label`, `out_dir`, `jobs`, `seed_policy` and `trial`. */
export interface EnqueueRequest {
  op?: string;
  prompt: string;
  negative_prompt?: string;
  size?: string;
  aspect_ratio?: string;
  background?: string;
  /** ComfyUI `batch_size`, ceiling 4. NOT the number of pictures — that is `jobs`. */
  count?: number;
  inputs?: string[];
  mask?: string;
  model?: string;
  provider?: string;
  seed?: number;
  strength?: number;
  loras?: LoraRef[];
  params?: EngineParams;
  label?: string;
  out_dir?: string;
  /** N ≥ 1: how many jobs the group expands into (decision 8). */
  jobs?: number;
  seed_policy?: SeedPolicy;
  /** Head of the queue, family trial steps, `generated/console/trial/` (decision 11). */
  trial?: boolean;
  /**
   * Decision 11's "full steps" checkbox: keep the trial's head-of-queue position and its
   * folder, but run the form's steps instead of the family's reduced ones — the case where
   * the trial IS the picture.
   *
   * NOT in the ADR's field list for this body (decision 2 enumerates the other seven). The
   * checkbox is in decision 11's text with no wire named for it, and every alternative the
   * existing fields allow changes something else as well: dropping `trial` moves the job to
   * the tail and to the main folder. Lane A owes this one field, or the ADR owes a sentence
   * saying how else the checkbox reaches the Agent.
   */
  trial_full_steps?: boolean;
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
  size?: number;
  width?: number;
  height?: number;
  seed?: number;
}

export interface Job {
  id: string;
  group?: string;
  state: JobState;
  /** Only while queued. */
  position?: number;
  trial?: boolean;
  label?: string;
  started_at?: string;
  finished_at?: string;
  elapsed_ms?: number;
  /** The estimate for THIS job's (model, size bucket, steps), so a trial's fast average
   *  never colours a batch's bar (decision 11). */
  typical_ms?: number;
  model?: string;
  family?: string;
  seed?: number;
  size?: string;
  params?: EngineParams;
  loras?: LoraRef[];
  prompt?: string;
  negative_prompt?: string;
  op?: string;
  out_dir?: string;
  files?: StoredFile[];
  warnings?: string[];
  error?: string;
}

export interface JobGroup {
  id: string;
  label?: string;
  state: GroupState;
  done: number;
  failed: number;
  total: number;
  /** Jobs of this group in flight — serial, so 0 or 1 (decision 2). */
  running?: number;
  eta_ms?: number;
  paused_at?: string;
}

export interface JobsResponse {
  jobs?: Job[];
  groups?: JobGroup[];
  queue_paused?: boolean;
  error?: ApiError;
}

export type PropsSource = "sidecar" | "png" | "none";

/** What `GET /imagegen/props` recovers for one picture (decision 3). `source: "none"` means
 *  the UI says so rather than drawing a table of blanks. */
export interface ImageProperties {
  source: PropsSource;
  model?: string;
  family?: string;
  seed?: number;
  size?: string;
  steps?: number;
  cfg?: number;
  sampler?: string;
  scheduler?: string;
  loras?: LoraRef[];
  prompt?: string;
  negative_prompt?: string;
  op?: string;
  strength?: number;
  provider?: string;
  job?: string;
  label?: string;
  elapsed_ms?: number;
  warnings?: string[];
  error?: ApiError;
}

/** The four batch verbs of decision 12. All of them act on a GROUP — the thing the person
 *  submitted — and never on one picture; the per-job `DELETE` is the single-picture door. */
export type GroupOp = "pause" | "resume" | "skip" | "cancel";

export type QueueOp = "pause" | "resume";

/** A LoRA's family, whichever spelling the Agent used. */
export const loraBaseModel = (l: ImagegenLora): string => l.base_model || l.baseModel || "";

/** A LoRA's trigger words, whichever spelling the Agent used. */
export const loraTriggers = (l: ImagegenLora): string[] => l.trained_words || l.trainedWords || [];

/**
 * The provider this pane drives: the first FLEET provider (comfy / sdcpp) that is ready.
 * The CLI-driven providers are agents by construction and own none of these knobs, so the
 * pane must not offer them even when the status lists them first (decision 1).
 */
export const FLEET_PROVIDERS = ["comfy", "sdcpp"];

export function fleetProvider(st: ImagegenStatus | null): ImagegenProvider | null {
  const list = st?.providers || [];
  return list.find((p) => FLEET_PROVIDERS.includes(p.id)) || null;
}
