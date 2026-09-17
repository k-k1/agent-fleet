// registeredGroups — how the 登録済み catalogue names a row and what it files it under
// (ADR 0088).
//
// Its own module and not part of adminEngineAdd.tsx because both answers are pure functions over
// a row, and they are the part of this screen that has to be right without a DOM: a title that
// falls back wrongly makes a card unidentifiable, and a grouping key that differs between the
// section headings and the filter chips draws a category nobody can open.
import type { EngineModel, EngineRow } from "./engineTypes.ts";

/** What to draw as a card's title, and what to keep underneath it.
 *
 * 🔴 The id is ALWAYS carried, never replaced. It is the key the launch menu, the active set,
 * every S3 path and every other admin screen are written in — a card that showed only
 * "AbyssOrangeMix2" would be one an operator could not match to the file they are looking at. */
export type RegisteredTitle = {
  /** The publisher's name when the row carries one, and the id when it does not. */
  title: string;
  /** The version's own name ("Hard", "v1.1"), drawn beside the title. Empty when the publisher
   *  gave none, or when it repeats the title — Civitai's default version name is the model's. */
  version: string;
  /** True when `title` IS the id, which is the state the metadata button exists to leave. */
  bare: boolean;
};

export function registeredTitle(model: EngineModel): RegisteredTitle {
  const name = (model.display_name || "").trim();
  const version = (model.version_name || "").trim();
  if (!name) return { title: model.id, version: "", bare: true };
  return { title: name, version: version && version !== name ? version : "", bare: false };
}

/** A row nobody has read a model page for. Both halves, because the two sources answer
 *  differently and either one is enough to identify a card: Civitai publishes a name and a
 *  picture, and Hugging Face publishes a repository id and (measured 2026-09-18, on four
 *  repositories including the two this deployment runs) no thumbnail at all. */
export function registeredNeedsMeta(model: EngineModel): boolean {
  return !(model.display_name || "").trim() && !(model.preview_url || "").trim();
}

export type RegisteredGroup = {
  /** The stored value rows were filed under — a family, a parent model, a publisher. "" is the
   *  group of rows that declare none, and it is drawn with its own label. */
  key: string;
  models: EngineModel[];
};

/** What a row belongs to, in the one vocabulary that means something for its role.
 *
 * Three rules because the three kinds of row answer three different questions, and one of them
 * was the complaint this ADR started from — the family was a tag in the middle of a card:
 *
 *   - an IMAGE row belongs to its family (`sdxl`, `flux1`), which is what the provider picks a
 *     workflow with and therefore what decides whether two rows are interchangeable;
 *   - an LLM ADAPTER belongs to the model it was trained against, which its `base_model` names;
 *   - a GGUF declares neither, so it is filed under its PUBLISHER — the first segment of the
 *     repository id, which is what `display_name` holds for a Hugging Face row. Quantisations of
 *     the same model by different people are genuinely different rows, and who made them is the
 *     fact an operator picks between them by.
 */
export function registeredGroupKey(model: EngineModel, image: boolean): string {
  if (image || model.kind === "lora") return (model.base_model || "").trim();
  const name = (model.display_name || "").trim();
  const slash = name.indexOf("/");
  return slash > 0 ? name.slice(0, slash) : "";
}

/** Group the rows, in the order the sections are drawn.
 *
 * 🔴 The unlabelled group is FIRST, like the ledger's orphans. A row with no family is not a
 * tidy leftover: the CP refuses to enable it (`base_model_missing`), so it is the one group on
 * the screen that somebody has to act on, and sorting it to the bottom is how it stays there.
 *
 * After it come the engine's OWN declared families in the engine's own order, so the sections
 * read the same way on every deployment and a family that holds nothing today still has a
 * predictable place. Anything else — a family an older row declares that this provider no longer
 * lists, a publisher — follows alphabetically. */
export function groupRegistered(models: EngineModel[], row: EngineRow, image: boolean): RegisteredGroup[] {
  const groups = new Map<string, EngineModel[]>();
  for (const model of models) {
    const key = registeredGroupKey(model, image);
    const bucket = groups.get(key);
    if (bucket) bucket.push(model); else groups.set(key, [model]);
  }
  const declared = image ? row.base_models || [] : [];
  const rest = [...groups.keys()]
    .filter((key) => key !== "" && !declared.includes(key))
    .sort((left, right) => left.localeCompare(right));
  const order = ["", ...declared, ...rest];
  return order.flatMap((key) => {
    const bucket = groups.get(key);
    return bucket?.length ? [{ key, models: bucket }] : [];
  });
}
