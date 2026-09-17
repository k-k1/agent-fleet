// The repository card for a chat engine's catalogue (ADR 0089).
//
// A quantisation repository is one model published at a dozen sizes — unsloth/Qwen3.8-27B-GGUF is
// fourteen of them beside an importance matrix and two projectors (measured 2026-09-18) — and the
// choice between them is the whole decision: quality against what the card can hold. Before this
// the catalogue drew one card per quantisation with nothing relating them, and adding a second
// size meant finding the repository again in the 探す tab.
//
// So the card is the REPOSITORY, the rows under it stay one per quantisation (a member has to be
// able to choose between IQ2_XXS and IQ2_S, so they cannot be merged), and the ladder of sizes
// that are not here yet is one press away with a verdict on every line.
import { useCallback, useState } from "react";
import { apiJSON } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";
import { Icon } from "../../../ui/Icon.tsx";
import { modelFit, type Fit } from "./engineFit.ts";
import { quantLabel } from "./registeredGroups.ts";
import type { EngineApiError, EngineModel, EngineRow, IngestCandidate } from "./engineTypes.ts";

/** The verdict, as one pill. Shared by the ladder and the ingest form so that the same demand
 *  against the same card never reads two ways on two screens. */
export function FitTag({ fit }: { fit: Fit }) {
  const tr = useT();
  if (fit.state === "unknown") return null;
  const tone = fit.state === "fits" ? "on" : fit.state === "tight" ? "warn" : "bad";
  const label = fit.state === "over" && fit.nextClass
    ? (tr("admin.fit_over_next" as never) as string).replace("{c}", fit.nextClass.label || fit.nextClass.id)
    : (tr((`admin.fit_${fit.state}`) as never) as string).replace("{p}", String(Math.round(fit.used * 100)));
  return <span className={`engines-model-tag ${tone}`}>{label}</span>;
}

/** Which of a repository's files this deployment already holds.
 *
 * Matched on the row's `source`, which the ingest writes as `hf:<repo>/<file>` — not on the row
 * id, which is derived from the file name and mangled (`Qwen3.8-27B-UD-IQ2_S.gguf` becomes
 * `qwen3_8_27b_ud_iq2_s`). Matching on the mangled form would be re-deriving a transformation the
 * source already records exactly. */
export function heldFiles(rows: EngineModel[], repo: string): Map<string, EngineModel> {
  const prefix = `hf:${repo}/`;
  const out = new Map<string, EngineModel>();
  for (const model of rows) {
    const source = (model.source || "").trim();
    if (source.startsWith(prefix)) out.set(source.slice(prefix.length), model);
  }
  return out;
}

type LadderAnswer = {
  files?: IngestCandidate[];
  kv_mib_per_1k_tokens?: number;
  kv_from?: string;
  error?: EngineApiError;
};

export function RepoQuantLadder({ engine, repo, rows, readOnly, onTakeIn }: {
  engine: EngineRow;
  repo: string;
  /** The rows of THIS repository that are already in the catalogue. */
  rows: EngineModel[];
  readOnly: boolean;
  onTakeIn: (file: string) => void;
}) {
  const tr = useT();
  const [answer, setAnswer] = useState<LadderAnswer | null>(null);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  // The window this ladder is priced at. Taken from what the rows here are actually declared to
  // run at — they are the same model, so one of them answering 32768 is the deployment's own
  // decision about this repository — and editable, because "what would I have to shrink it to"
  // is the question a red line provokes. 🔴 Local only: changing it prices the table and writes
  // nothing, since a row's window is a property of that row and is edited on the row.
  const declared = Math.max(0, ...rows.map((model) => model.context_tokens || 0));
  const [ctx, setCtx] = useState(String(declared || 32768));
  const contextTokens = Number(ctx.trim().replace(/[_,]/g, "")) || 0;
  const cardMiB = engine.class?.vram_mib || 0;
  const held = heldFiles(rows, repo);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      // One press, one listing (and one 1 MiB header read behind it). NOT on mount: a catalogue
      // of eight repositories would open as eight upstream requests nobody asked for, at a host
      // that sheds load with a 503.
      const got = await apiJSON(`api/admin/engines/${encodeURIComponent(engine.key)}/ingest/files`, "POST",
        { source: { hf: { repo } } }) as LadderAnswer;
      setAnswer(got || {});
      setOpen(true);
    } finally { setBusy(false); }
  }, [engine.key, repo]);

  const kvPer1k = answer?.kv_mib_per_1k_tokens || 0;
  // A repository file that is not a model — the importance matrix, a vision projector — is not a
  // choice anybody makes here. Dropped from THIS table only: the manual picker still lists them,
  // because hiding a file from somebody who came looking for it is the older fault.
  const candidates = (answer?.files || []).filter((file) => (file.role || "model") === "model");

  return <div className="engine-repo-ladder">
    {!open && <Button variant="ghost" small icon="chevron-down" disabled={busy} onClick={() => void load()}>
      {busy ? <><Icon name="loading" spin /> {tr("admin.repo_ladder_loading" as never)}</> : tr("admin.repo_ladder_open" as never)}
    </Button>}
    {open && <>
      <div className="engine-repo-ladder-head">
        <label><span>{tr("admin.engines_model_window_context")}</span>
          <input inputMode="numeric" value={ctx} onChange={(event) => setCtx(event.currentTarget.value)} /></label>
        {!!cardMiB && <span className="muted">{(tr("admin.fit_card" as never) as string).replace("{c}", String(cardMiB))}</span>}
        <Button variant="ghost" small icon="chevron-up" onClick={() => setOpen(false)}>{tr("admin.repo_ladder_close" as never)}</Button>
      </div>
      {answer?.error && <p className="form-err">{answer.error.message}</p>}
      {/* 🔴 Said once, here, and not on every line: this counts the weights and the KV cache and
          cannot count llama.cpp's compute buffers or the CUDA context. ADR 0074 measured what
          that costs — an L4 took 17 GB of weights and then died allocating the cache. */}
      <p className="admin-hint">{tr("admin.fit_estimate_note" as never)}
        {!kvPer1k && <> {tr("admin.fit_no_kv" as never)}</>}
        {!!answer?.kv_from && <> {(tr("admin.fit_kv_from" as never) as string).replace("{f}", quantLabel(answer.kv_from, repo))}</>}</p>
      {!candidates.length && <p className="muted">{tr("admin.repo_ladder_empty" as never)}</p>}
      <ul className="engine-repo-quants">{candidates.map((file) => {
        const weightsMiB = file.bytes ? Math.round(file.bytes / 1048576) : 0;
        const fit = modelFit(weightsMiB, kvPer1k, contextTokens, cardMiB, engine.classes || []);
        const have = held.get(file.name);
        return <li key={file.name} className={have ? "held" : ""}>
          <span className="engine-repo-quant-mark" aria-hidden="true">{have ? "●" : "○"}</span>
          <span className="mono engine-repo-quant-name">{quantLabel(file.name, repo)}</span>
          <span className="muted">{formatMiB(weightsMiB)}</span>
          <span className="muted">{fit.kvMiB ? `+ ${formatMiB(fit.kvMiB)} = ${formatMiB(fit.needMiB)}` : ""}</span>
          <FitTag fit={fit} />
          {have
            ? <span className="engines-model-tag on">{tr("admin.repo_ladder_held" as never)}</span>
            : !readOnly && <Button small variant={fit.state === "fits" ? "primary" : undefined}
              aria-label={`${tr("admin.catalog_add" as never)}: ${file.name}`}
              onClick={() => onTakeIn(file.name)}>{tr("admin.catalog_add" as never)}</Button>}
        </li>;
      })}</ul>
    </>}
  </div>;
}

function formatMiB(value: number): string {
  if (value <= 0) return "—";
  return value >= 1024 ? `${(value / 1024).toFixed(1)} GiB` : `${value} MiB`;
}
