import { errDetail } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";
import { type ModelKind } from "./adminEngineModels.tsx";
import { type CatalogSource, type EngineApiError, type EngineModel, type EngineObjectRow, type EngineRow, type IngestHit } from "./engineTypes.ts";

export type CatalogProps = {
  row: EngineRow;
  kind: ModelKind;
  onKind: (kind: ModelKind) => void;
  /** Which pane this face belongs to — half of its memory's key (catalogMemory). */
  paneId?: string;
  readOnly: boolean;
  onChanged: () => void;
};

/** The search half of the pane also needs the search sources this DEPLOYMENT offers, as the
 *  engine list answered them. The registered half never searches, so it does not take them. */
export type BrowseProps = CatalogProps & { sources: CatalogSource[] };

/** The ledger route, shared by both tabs: the bucket is the only thing that knows what this
 * deployment holds, so "have I got this already" and "what is in there" read the same answer. */
export function objectsPath(engineKey: string): string {
  return `api/admin/engines/${encodeURIComponent(engineKey)}/objects`;
}

/** What else the model behind a row is published as, and how to ask for it — or "" when nothing
 * can be listed.
 *
 * Read off the row's recorded `source`, which is the only thing that says where the bytes came
 * from. The two roles ask different questions of different upstreams:
 *
 *   - an IMAGE row came from a Civitai VERSION (`civitai:<id>`), and the others are the model's
 *     other versions;
 *   - a GGUF row came from a Hugging Face FILE (`hf:<owner>/<repo>/<file>`), and the others are
 *     the repository's other quantisations — which is ADR 0089's ladder, now reachable from any
 *     such row instead of only from a group whose heading happens to be a repository name.
 *
 * 🔴 A LoRA is left out of the chat side on purpose: an adapter is filed under the model it was
 * trained against, not under a repository of sizes, and the ladder prices a KV cache it does not
 * have. A `url:` source and a row with none answer "" — there is no page behind either.
 */
export function otherOf(model: EngineModel, image: boolean): string {
  const source = (model.source || "").trim();
  if (image) return source.startsWith("civitai:") ? source.slice("civitai:".length) : "";
  if (model.kind === "lora" || !source.startsWith("hf:")) return "";
  const parts = source.slice("hf:".length).split("/");
  return parts.length >= 3 && parts[0] && parts[1] ? `${parts[0]}/${parts[1]}` : "";
}

export const CATALOG_HARD_RESTRICTIONS = new Set(["gated_auto", "gated_manual", "paid", "early_access", "private", "generate_only", "unscanned", "pickle"]);

/** A refusal, with who is holding the thing and the one button that clears it. Drawn even when
 * the CP sent neither — an error line without a next act is still the error line. */
export function EngineRefusal({ error, busy, onNext }: { error: EngineApiError | null; busy: boolean; onNext: () => void }) {
  const tr = useT();
  if (!error) return null;
  const holder = error.holder;
  return <p className="form-err engine-refusal">
    <span>{errDetail(error)}</span>
    {holder && <span className="muted engine-refusal-holder">{(tr("admin.catalog_holder" as never) as string)
      .replace("{k}", tr((`admin.catalog_holder_${holder.kind}`) as never) as string)
      .replace("{i}", holder.id || holder.key || "")}</span>}
    {error.next && <Button small variant="primary" className="engine-refusal-next" disabled={busy} onClick={onNext}>
      {tr((`admin.catalog_next_${error.next.act}`) as never)}
    </Button>}
  </p>;
}

/** Count concrete source objects, never repositories. A `missing` entry stays distinct from an
 * absent one: the ledger says a row points at nothing, which is not "nobody took this in". */
export function savedObjectsForHit(hit: IngestHit, objects: EngineObjectRow[]): EngineObjectRow[] {
  return sourceObjectsForHit(hit, objects).filter((object) => object.state === "present");
}

export function sourceObjectsForHit(hit: IngestHit, objects: EngineObjectRow[]): EngineObjectRow[] {
  const modelRef = hit.model_ref || (hit.source === "hf" ? hit.ref : "");
  if (!modelRef) return [];
  if (hit.source === "civitai") {
    return objects.filter((object) => !!object.source && (
      object.source === `civitai:${hit.ref}` || object.source.startsWith(`civitai:${hit.ref}/`)
    ));
  }
  return objects.filter((object) => !!object.source && (
    object.source.startsWith(`hf:${modelRef}/`) || object.source.startsWith(`hf:${modelRef}@`)
  ));
}

export function compactCount(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  if (value >= 1_000) return `${Math.round(value / 1_000)}k`;
  return String(Math.round(value));
}

export function formatBytes(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB`;
  if (value >= 1_000_000) return `${Math.round(value / 1_000_000)} MB`;
  if (value >= 1_000) return `${Math.round(value / 1_000)} kB`;
  return `${value} B`;
}
