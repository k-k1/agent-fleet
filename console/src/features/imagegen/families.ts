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
  {
    id: "krea2",
    dialect: "sentences",
    // Krea 2 was trained for aesthetics rather than tag adherence, and its own prompt-enhancer
    // node rewrites a short prompt into a paragraph. There is no quality-tag convention to offer.
    quality: [],
    // 🔴 The range spans the family's two modes: Turbo is 8 steps at cfg 1 (the family recipe and
    // the only mode ComfyUI ships a template for), Raw is 52 steps with real guidance. A row
    // declares which one it is; the card cannot, so it shows both ends rather than picking.
    steps: [8, 52],
    cfg: [1, 4.5],
    trialSteps: 8,
    sizes: DEFAULT_SIZES,
  },
  {
    id: "qwen-image-edit-2509",
    // Instruction editing: the prompt is a sentence describing the change, not a tag list.
    dialect: "sentences",
    quality: [],
    // The family's own recipe (ADR 0094 実測 A): steps 20, cfg 4, fixed — there is no published
    // range to span, unlike the other cards.
    steps: [20, 20],
    cfg: [4, 4],
    trialSteps: 8,
    // Empty means the size field does not draw at all (sizeOptions), not "use the megapixel
    // list": the output size follows the INPUT picture's own size (the Agent's comfyQwenEditSize),
    // so no candidate here would reach the sampler (decision 4).
    sizes: [],
  },
  {
    id: "qwen-image-edit-2511",
    // The same instruction-edit card as 2509 in everything a member types; the two are separate
    // families because the GRAPH differs (decision 6), and a prompt is written the same way for
    // both. Only the step count moved, and upstream doubled it.
    dialect: "sentences",
    quality: [],
    // The family's own recipe (ADR 0094 実測 E): steps 40, cfg 4. 実測 E took 393.8 s for one
    // 1024² edit at these, which is what the range not spanning anything is hiding.
    steps: [40, 40],
    cfg: [4, 4],
    trialSteps: 8,
    sizes: [],
  },
  {
    id: "qwen-image-2.1",
    // Sentences, and for one reason more than the two cards above: the same prompt box drives
    // both of this family's ops, and its edit instructions name their reference pictures inline
    // as `<image1>`, `<image2>` — the official template's own note.
    dialect: "sentences",
    quality: [],
    // 25 is what both shipped templates start at; 50 is the top of the range their note gives for
    // the official pipeline ("about 40-50 with euler"). Unlike the two cards above there IS a
    // range to span here, and it is upstream's own.
    steps: [25, 50],
    // cfg 1 is the published path, and the note says to raise it only alongside a negative
    // prompt — which is also when the negative field stops being greyed out (`knobs`).
    cfg: [1, 4],
    trialSteps: 8,
    // Not empty, unlike its two neighbours: this family generates as well as edits, and the
    // generate path fills an EmptyLatentImage from whatever size is picked. On an EDIT the size is
    // ignored the same way it is for them — the canvas follows the first reference picture.
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

/** Size options for a model: the row's list when the status carried one, else the family's.
 *
 *  🔴 One family wins even over the ROW's own declaration (ADR 0094 decision 4): a card whose
 *  `sizes` is an explicit empty list (as opposed to one this file never populated) means the
 *  family decides the size from something other than a candidate list, so a row's own `sizes`
 *  would offer a control that silently does nothing. */
export function sizeOptions(rowSizes: string[] | undefined, family: string | undefined): string[] {
  const card = familyCard(family);
  if (card && card.sizes.length === 0) return [];
  const rows = (rowSizes || []).filter((s) => typeof s === "string" && /^\d+x\d+$/.test(s));
  if (rows.length) return rows;
  return card?.sizes ?? DEFAULT_SIZES;
}

/** `"1216x832"` → `[1216, 832]`, or null. Used by the pixel-ceiling hint and the presets. */
export function parseSize(size: string | undefined): [number, number] | null {
  const m = /^(\d+)x(\d+)$/.exec((size || "").trim());
  if (!m) return null;
  const w = Number(m[1]);
  const h = Number(m[2]);
  return w > 0 && h > 0 ? [w, h] : null;
}
