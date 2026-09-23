// families — what the Console still knows about a ComfyUI family on its own: the size list to
// fall back to when a row carries none (ADR 0081 decision 5).
//
// The family's PROSE facts — dialect, quality prefixes, recommended steps and cfg, trial steps —
// are the Agent's (ADR 0100 decision 7: `comfyFamilyRow` drives both the graph and
// `modelStatus`, so a new family is one commit on one side). `familyFacts` below only reads them
// off the model row. What stays here is the size fallback, because `buildRequest` needs it even
// when the status carried no list, and it mirrors the Agent's own `comfyDefaultSizes`.
import type { ImagegenModel } from "./wire.ts";

/** The Agent's own default list (`comfyMegapixelSizes`) — the fallback, never an override. */
const DEFAULT_SIZES = ["1024x1024", "1152x896", "896x1152", "1216x832", "832x1216"];

/** SD1.5's UNet was trained at 512. Asking it for 1024 does not fail, it returns a picture
 *  with the subject duplicated — so this family gets its own presets rather than the
 *  megapixel list. Mirrors `comfyDefaultSizes` in the Agent. */
const SD15_SIZES = ["512x512", "512x768", "768x512", "640x512", "512x640"];

/**
 * Fallback sizes per family, for a row whose `sizes` the status did not carry. An explicit empty
 * list means the family takes its size from the INPUT picture (ADR 0094 decision 4: the
 * instruction-edit families), so no candidate would reach the sampler and the field is not drawn.
 * `qwen-image-2.1` generates as well as edits, so it has a list — with the doubled sides ADR 0098
 * Open 3 measured whole on the dev deployment.
 */
const FAMILY_SIZES: Record<string, string[]> = {
  sd15: SD15_SIZES,
  "qwen-image-edit-2509": [],
  "qwen-image-edit-2511": [],
  "qwen-image-2.1": [...DEFAULT_SIZES, "2048x2048", "2304x1792", "1792x2304", "2432x1664", "1664x2432"],
};

const familySizes = (family: string | undefined | null): string[] | undefined =>
  family ? FAMILY_SIZES[family.trim().toLowerCase()] : undefined;

/** The family facts a card draws, as the Agent reported them on the model row. */
export interface FamilyFacts {
  dialect?: "tags" | "sentences";
  quality: string[];
  steps?: [number, number];
  cfg?: [number, number];
  trialSteps?: number;
}

/**
 * The card's facts for a model, or null when the Agent reported none — an Agent that predates
 * ADR 0100 decision 7, or a family it has no table row for. Null is a real answer: a card
 * invented in the browser would advise on a dialect nobody checked.
 */
export function familyFacts(model: ImagegenModel | null | undefined): FamilyFacts | null {
  if (!model) return null;
  const range = (v: unknown): [number, number] | undefined =>
    Array.isArray(v) && v.length === 2 && v.every((n) => typeof n === "number" && Number.isFinite(n))
      ? [v[0] as number, v[1] as number]
      : undefined;
  const dialect = model.dialect === "tags" || model.dialect === "sentences" ? model.dialect : undefined;
  const quality = Array.isArray(model.quality_prefixes) ? model.quality_prefixes.filter((q) => typeof q === "string" && q) : [];
  const steps = range(model.steps_range);
  const cfg = range(model.cfg_range);
  const trialSteps = typeof model.trial_steps === "number" && model.trial_steps > 0 ? model.trial_steps : undefined;
  if (!dialect && !quality.length && !steps && !cfg && !trialSteps) return null;
  return { dialect, quality, steps, cfg, trialSteps };
}

/** Size options for a model: the row's list when the status carried one, else the family's.
 *
 *  One family wins even over the ROW's own declaration (ADR 0094 decision 4): a family whose
 *  `sizes` is an explicit empty list (as opposed to one this file never populated) means the
 *  family decides the size from something other than a candidate list, so a row's own `sizes`
 *  would offer a control that silently does nothing. */
export function sizeOptions(rowSizes: string[] | undefined, family: string | undefined): string[] {
  const fam = familySizes(family);
  if (fam && fam.length === 0) return [];
  const rows = (rowSizes || []).filter((s) => typeof s === "string" && /^\d+x\d+$/.test(s));
  if (rows.length) return rows;
  return fam ?? DEFAULT_SIZES;
}

/** `"1216x832"` → `[1216, 832]`, or null. Used by the pixel-ceiling hint and the presets. */
export function parseSize(size: string | undefined): [number, number] | null {
  const m = /^(\d+)x(\d+)$/.exec((size || "").trim());
  if (!m) return null;
  const w = Number(m[1]);
  const h = Number(m[2]);
  return w > 0 && h > 0 ? [w, h] : null;
}
