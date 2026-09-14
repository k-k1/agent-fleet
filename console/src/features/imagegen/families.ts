// families — layer A of the prompt help (ADR 0081 decision 7): what the ComfyUI families
// expect, with no model call and no network.
//
// The PROSE of a card is i18n (`imggen.fam.*`); this file holds only what a card is made of,
// plus the two things a form needs when the Agent has not told it: a size list and a
// recommended range. It deliberately does NOT hold "which knobs this family reads" — that
// comes from the status's `knobs`, computed in the Agent from the same table the graph
// templates use, because the ADR's decision 4 forbids a second copy that can disagree.
//
// The quality prefixes are prompt TEXT, so they are not translated: they are the literal
// tokens the dialect was trained on. They are offered as a chip and never inserted on their
// own (decision 7 / the rejected "auto-insert" option).
import type { Family } from "./wire.ts";

export interface FamilyCard {
  id: Family;
  /** Tag list (`1girl, solo, …`) or natural-language sentences. */
  dialect: "tags" | "sentences";
  /** Prompt fragments offered as chips. Empty for a family with no convention. */
  quality: string[];
  /** Recommended steps and cfg, for the placeholder text next to the field. A family that
   *  does not read cfg has none, and the field is disabled from `knobs` anyway. */
  steps: [number, number];
  cfg?: [number, number];
  /** Steps a trial actually runs at, for the trial button's tooltip (decision 11). The
   *  Agent applies them; this is display only, so the two must be kept in step by the ADR,
   *  not by either side reading the other. */
  trialSteps: number;
  /** Fallback presets for a row whose `sizes` the status did not carry. The same list the
   *  Agent's comfy provider falls back to FOR THIS FAMILY (comfyDefaultSizes). */
  sizes: string[];
}

/** The Agent's own default list (`comfyMegapixelSizes`) — the fallback, never an override. */
const DEFAULT_SIZES = ["1024x1024", "1152x896", "896x1152", "1216x832", "832x1216"];

/** SD1.5's UNet was trained at 512. Asking it for 1024 does not fail, it returns a picture
 *  with the subject duplicated — so this family gets its own presets rather than the
 *  megapixel list. Mirrors `comfyDefaultSizes` in the Agent. */
const SD15_SIZES = ["512x512", "512x768", "768x512", "640x512", "512x640"];

export const FAMILY_CARDS: FamilyCard[] = [
  {
    id: "sd15",
    dialect: "tags",
    // Same tag dialect as SDXL and the same convention: SD1.5 fine-tunes are overwhelmingly
    // booru-tagged, and `masterpiece, best quality` is the prefix their cards print.
    quality: ["masterpiece, best quality"],
    steps: [20, 30],
    cfg: [6, 9],
    trialSteps: 10,
    sizes: SD15_SIZES,
  },
  {
    id: "sdxl",
    dialect: "tags",
    // Two dialects share the family: base/Illustrious/NooBAI take `masterpiece, best
    // quality`, Pony takes the score tags. Both are offered; the card says which is which.
    quality: ["masterpiece, best quality", "score_9, score_8_up, score_7_up"],
    steps: [20, 40],
    cfg: [5, 9],
    trialSteps: 10,
    sizes: DEFAULT_SIZES,
  },
  {
    id: "sd35",
    dialect: "sentences",
    quality: [],
    steps: [24, 40],
    cfg: [3.5, 6],
    trialSteps: 12,
    sizes: DEFAULT_SIZES,
  },
  {
    id: "flux1",
    dialect: "sentences",
    quality: [],
    steps: [16, 32],
    trialSteps: 8,
    sizes: DEFAULT_SIZES,
  },
  {
    id: "flux2-klein",
    dialect: "sentences",
    quality: [],
    steps: [4, 8],
    trialSteps: 4,
    sizes: DEFAULT_SIZES,
  },
  {
    id: "zimage",
    dialect: "sentences",
    quality: [],
    steps: [6, 12],
    cfg: [1, 2],
    trialSteps: 4,
    sizes: DEFAULT_SIZES,
  },
  {
    id: "anima",
    // Danbooru tags, natural-language captions, or the two mixed — the model card documents all
    // three. `tags` is the dialect the chips below belong to and the one a prompt is most likely
    // to be wrong in (lowercase, spaces not underscores, `@` before an artist name).
    dialect: "tags",
    // The model card's own recommended prefix, and the shorter one it tells Anima-Aesthetic
    // users to prefer — the card says NOT to use score_* tags with that version, so offering
    // both as separate chips is what keeps the advice honest for both checkpoints.
    quality: ["masterpiece, best quality, score_7, safe", "masterpiece, best quality"],
    steps: [30, 50],
    cfg: [4, 5],
    trialSteps: 12,
    sizes: DEFAULT_SIZES,
  },
];

const BY_ID = new Map(FAMILY_CARDS.map((c) => [c.id, c] as const));

/**
 * The card for a row's `base_model`, or null.
 *
 * Null is a real answer, not a failure: a deployment may hold a checkpoint whose family the
 * Agent has no template for, and a card invented for it would advise on a dialect nobody
 * checked. The pane then shows the model's own description alone.
 */
export function familyCard(family: string | undefined | null): FamilyCard | null {
  if (!family) return null;
  return BY_ID.get(family.trim().toLowerCase() as Family) ?? null;
}

/** Size options for a model: the row's list when the status carried one, else the family's. */
export function sizeOptions(rowSizes: string[] | undefined, family: string | undefined): string[] {
  const rows = (rowSizes || []).filter((s) => typeof s === "string" && /^\d+x\d+$/.test(s));
  if (rows.length) return rows;
  return familyCard(family)?.sizes ?? DEFAULT_SIZES;
}

/** `"1216x832"` → `[1216, 832]`, or null. Used by the pixel-ceiling hint and the presets. */
export function parseSize(size: string | undefined): [number, number] | null {
  const m = /^(\d+)x(\d+)$/.exec((size || "").trim());
  if (!m) return null;
  const w = Number(m[1]);
  const h = Number(m[2]);
  return w > 0 && h > 0 ? [w, h] : null;
}
