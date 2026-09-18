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

export function registeredTitle(model: EngineModel, group = ""): RegisteredTitle {
  // Inside a repository group the repository is the HEADING, so repeating it as the title of
  // every card leaves two cards of the same model calling themselves the same thing — which is
  // what the screen did (seen on the rendered ladder). The quantisation is what tells them
  // apart, so in that one context it is the title.
  const quant = groupIsRepo(group) ? quantOfRow(model, group) : "";
  if (quant) return { title: quantLabel(quant, group), version: "", bare: false };
  const name = (model.display_name || "").trim();
  const version = (model.version_name || "").trim();
  if (!name) return { title: model.id, version: "", bare: true };
  return { title: name, version: version && version !== name ? version : "", bare: false };
}

/** The file of `repo` this row was built from, out of its recorded source (`hf:<repo>/<file>`),
 *  or "" when the row came from somewhere else. Read off the source and never off the id: the id
 *  is the file name MANGLED (`Qwen3.8-27B-UD-IQ2_S.gguf` becomes `qwen3_8_27b_ud_iq2_s`), and
 *  un-mangling it would be re-deriving a transformation the source already records exactly. */
export function quantOfRow(model: EngineModel, repo: string): string {
  const prefix = `hf:${repo}/`;
  const source = (model.source || "").trim();
  return source.startsWith(prefix) ? source.slice(prefix.length) : "";
}

/** What a repository's file is called in the catalogue's own vocabulary — `UD-IQ2_S` out of
 *  `Qwen3.8-27B-UD-IQ2_S.gguf`. The quantisation is the only part that differs between the files
 *  of one repository, and it is what the operator is choosing between.
 *
 *  Falls back to the whole base name when nothing can be stripped: a repository that names its
 *  files some other way gets a longer label, never a wrong one. */
export function quantLabel(fileName: string, repo: string): string {
  const base = (fileName.split("/").pop() || fileName).replace(/\.gguf$/i, "");
  const model = (repo.split("/").pop() || "").replace(/-GGUF$/i, "");
  if (model && base.toLowerCase().startsWith(model.toLowerCase())) {
    const rest = base.slice(model.length).replace(/^[-_.]/, "");
    if (rest) return rest;
  }
  return base;
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
 * was the complaint ADR 0088 started from — the family was a tag in the middle of a card:
 *
 *   - an IMAGE row belongs to its family (`sdxl`, `flux1`), which is what the provider picks a
 *     workflow with and therefore what decides whether two rows are interchangeable;
 *   - an LLM ADAPTER belongs to the model it was trained against, which its `base_model` names;
 *   - a GGUF declares neither, so it is filed under its REPOSITORY — the whole of
 *     `unsloth/Qwen3.8-27B-GGUF`, which is what `display_name` holds for a Hugging Face row.
 *
 * 🔴 The repository and not the publisher (which is what ADR 0088 filed them under first). A
 * quantisation repository is ONE model published at a dozen sizes — fourteen of them in that
 * repository, measured 2026-09-18 — and those sizes are alternatives to each other in a way that
 * two different models by the same publisher never are. Grouping by publisher put `Qwen3.8-27B`
 * and `Gemma` in one pile and split nothing that needed splitting.
 */
export function registeredGroupKey(model: EngineModel, image: boolean): string {
  if (image || model.kind === "lora") return (model.base_model || "").trim();
  return (model.display_name || "").trim();
}

/** Whether a group key is a Hugging Face repository — `owner/name`, exactly two segments.
 *
 * The ladder of other quantisations is offered on that and nothing else: it is a listing of that
 * repository, so a group keyed by anything else (a row registered by hand, a name somebody typed)
 * has nothing to list and must not be offered a button that answers 400. */
export function groupIsRepo(key: string): boolean {
  const parts = key.split("/");
  return parts.length === 2 && !!parts[0] && !!parts[1];
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
