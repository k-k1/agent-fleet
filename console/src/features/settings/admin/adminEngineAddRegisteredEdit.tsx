import { useState } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";
import { Modal } from "../../../ui/Modal.tsx";
import { kvKnown, kvPriceOf, modelFit, NO_KV, windowThatFits } from "./engineFit.ts";
import { familyVramMeasurement } from "./engineFamilyVram.ts";
import { FitTag } from "./adminEngineRepo.tsx";
import { engineIsImage, type EngineModel, type EngineParams, type EngineRow } from "./engineTypes.ts";

export function RegisteredEditDialog({ row, model, error, onClose, onSave }: {
  row: EngineRow;
  model: EngineModel;
  error: string;
  onClose: () => void;
  onSave: (body: Record<string, unknown>) => Promise<void>;
}) {
  const tr = useT();
  const image = engineIsImage(row);
  const lora = model.kind === "lora";
  const [description, setDescription] = useState(model.description || "");
  const [baseModel, setBaseModel] = useState(model.base_model || "");
  const [context, setContext] = useState(String(model.context_tokens || 0));
  const [output, setOutput] = useState(String(model.max_output_tokens || 0));
  const [vram, setVram] = useState(String(model.vram_mib || 0));
  const [negative, setNegative] = useState(model.negative_prompt || "");
  const [trainedWords, setTrainedWords] = useState((model.trained_words || []).join(", "));
  const [params, setParams] = useState<Record<keyof EngineParams, string>>({
    steps: String(model.params?.steps || ""), cfg: String(model.params?.cfg || ""), sampler: model.params?.sampler || "",
    scheduler: model.params?.scheduler || "", clip_skip: String(model.params?.clip_skip || ""),
    guidance: String(model.params?.guidance || ""), shift: String(model.params?.shift || ""), weight: String(model.params?.weight || ""),
  });
  const [busy, setBusy] = useState(false);
  const baseChoices = image ? row.base_models || [] : lora ? (row.model_rows || []).filter((candidate) => candidate.kind !== "lora").map((candidate) => candidate.id) : [];
  const whole = (value: string) => /^\d+$/.test(value.trim()) ? Number(value.trim()) : null;
  const contextNumber = whole(context);
  const outputNumber = whole(output);
  const vramNumber = whole(vram);
  const invalidWindow = !image && !lora && (contextNumber === null || outputNumber === null || (!!contextNumber !== !!outputNumber));
  // The same verdict the ingest form draws, on the row as it already is (ADR 0089). Without it
  // this dialog was three raw number boxes: an operator correcting a window had to price the KV
  // cache in their head, which is exactly what nobody should be asked to do — and is how a row
  // came to declare 262,144 on a card that holds a quarter of that.
  const editWeightsMiB = Math.round((model.file_rows || []).reduce((sum, f) => sum + (f.bytes || 0), 0) / 1048576);
  const editKv = !image && !lora ? kvPriceOf(model) : NO_KV;
  const editCardMiB = lora ? 0 : row.class?.vram_mib || 0;
  const editFit = modelFit(editWeightsMiB, editKv, contextNumber || 0, editCardMiB, row.classes || []);
  // The largest window this card actually holds, offered as one press. 0 when it cannot be
  // said (no geometry, no card, or the weights alone already fill it) and the button is then
  // not drawn — an "auto" that quietly does nothing is worse than no button.
  // 0 when it cannot be said — no geometry, no card, no stored ceiling, or the weights alone
  // already fill the card — and the button is then not drawn. An offer computed without an
  // upper bound would propose windows the model was never trained for, and an "auto" that
  // quietly does nothing is worse than no button.
  const editBestWindow = !image && !lora
    ? windowThatFits(editWeightsMiB, editKv, editCardMiB, model.context_length || 0)
    : 0;
  // What somebody measured this family at, when anybody has, and only for the BUILD they measured
  // (ADR 0094 decision 8). Read off the family being EDITED rather than the row's stored one, so
  // correcting the family and taking the measurement are the same visit.
  const editMeasured = image && !lora
    ? familyVramMeasurement(baseModel, (model.file_rows || []).map((f) => f.s3Key))
    : null;
  const invalidVram = vramNumber === null;
  const invalidBase = !image && lora ? !baseChoices.includes(baseModel) : image && baseChoices.length > 0 && !baseChoices.includes(baseModel);
  const validation = invalidWindow ? tr("admin.catalog_edit_window_invalid" as never) : invalidVram ? tr("admin.catalog_edit_vram_invalid" as never) : invalidBase ? tr("admin.engines_wizard_need_family") : "";
  const save = async () => {
    if (validation) return;
    const number = (value: string) => { const parsed = Number(value.trim()); return Number.isFinite(parsed) && parsed > 0 ? parsed : 0; };
    const paramsBody = Object.fromEntries(Object.entries(params).flatMap(([key, value]) => value.trim()
      ? [[key, key === "sampler" || key === "scheduler" ? value.trim() : number(value)]] : []));
    setBusy(true);
    try {
      await onSave({
        description: description.trim(), base_model: baseModel, vram_mib: vramNumber || 0,
        ...(!image && !lora ? { context_tokens: contextNumber || 0, max_output_tokens: outputNumber || 0 } : {}),
        ...(image && !lora ? { negative_prompt: negative.trim() } : {}),
        ...(image && lora ? { trained_words: trainedWords.split(/[\n,]/).map((word) => word.trim()).filter(Boolean) } : {}),
        ...(image ? { params: paramsBody } : {}),
      });
    } finally { setBusy(false); }
  };

  return <Modal title={`${tr("admin.catalog_edit" as never)} — ${model.id}`} className="engine-registered-edit" onClose={onClose} lockClose={busy}>
    <div className="ui-modal-body engine-operation-body"><div className="engine-operation-grid">
      <label><span>{tr("admin.engines_model_add_desc")}</span><input value={description} onChange={(event) => setDescription(event.currentTarget.value)} /></label>
      {(image || lora) && <label><span>{tr(!image && lora ? "admin.engines_model_add_lora_base" : "admin.engines_model_add_family")}</span>{baseChoices.length || (!image && lora)
        ? <select value={baseModel} disabled={!baseChoices.length} onChange={(event) => setBaseModel(event.currentTarget.value)}><option value="">—</option>{baseChoices.map((base) => <option key={base} value={base}>{base}</option>)}</select>
        : <input value={baseModel} onChange={(event) => setBaseModel(event.currentTarget.value)} />}</label>}
      {!image && !lora && <><label><span>{tr("admin.engines_model_window_context")}</span><input inputMode="numeric" value={context} onChange={(event) => setContext(event.currentTarget.value)} /></label><label><span>{tr("admin.engines_model_window_output")}</span><input inputMode="numeric" value={output} onChange={(event) => setOutput(event.currentTarget.value)} /></label></>}
      <label><span>{tr("admin.engines_model_vram_edit")}</span><input inputMode="numeric" value={vram} onChange={(event) => setVram(event.currentTarget.value)} /></label>
      {image && !lora && <label><span>{tr("admin.engines_model_negative")}</span><input value={negative} onChange={(event) => setNegative(event.currentTarget.value)} /></label>}
      {image && lora && <label><span>{tr("admin.engines_model_trigger")}</span><input value={trainedWords} onChange={(event) => setTrainedWords(event.currentTarget.value)} /></label>}
    </div>
    {/* The measurement is OFFERED, never applied (ADR 0094 decision 8). `vram_mib` means "the
        operator measured it", so the press is what makes the number a declaration — and it sits
        beside the field rather than in the grid, next to the window's own one-press fit below. */}
    {editMeasured && <p className="muted engine-operation-vram-measured">
      {(tr("admin.engines_vram_measured" as never) as string)
        .replace("{n}", editMeasured.mib.toLocaleString()).replace("{s}", editMeasured.size)
        .replace("{b}", String(editMeasured.batch)).replace("{i}", String(editMeasured.inputs))
        .replace("{f}", editMeasured.file).replace("{c}", editMeasured.card)}
      {" "}
      <Button variant="ghost" disabled={busy || vramNumber === editMeasured.mib}
        onClick={() => setVram(String(editMeasured.mib))}>
        {(tr("admin.engines_vram_measured_use" as never) as string).replace("{n}", editMeasured.mib.toLocaleString())}
      </Button>
    </p>}
    {!image && !lora && <p className="engine-operation-window-hint muted">
      {!!model.context_length && <>{(tr("admin.engines_ingest_ctx_ceiling" as never) as string).replace("{n}", model.context_length.toLocaleString())} </>}
      {!!editBestWindow && <Button variant="ghost" disabled={busy || editBestWindow === contextNumber} onClick={() => {
        setContext(String(editBestWindow)); setOutput(String(Math.floor(editBestWindow / 8)));
      }}>{(tr("admin.catalog_edit_window_fit" as never) as string).replace("{n}", editBestWindow.toLocaleString())}</Button>}
      {!kvKnown(editKv) && <> {tr("admin.fit_no_kv" as never)}</>}
    </p>}
    {!image && !lora && editFit.needMiB > 0 && <p className={`engine-operation-fit ${editFit.state === "over" ? "form-err" : "muted"}`}>
      {(tr("admin.engines_ingest_fit_weights") as string).replace("{n}", String(editFit.weightsMiB))}
      {` · ${editFit.kvMiB ? (tr("admin.engines_ingest_fit_kv") as string).replace("{n}", String(editFit.kvMiB)).replace("{c}", String(contextNumber || 0)) : tr("admin.engines_ingest_fit_kv_unread")}`}
      {editCardMiB ? ` · ${(tr("admin.engines_ingest_fit_card") as string).replace("{n}", String(editFit.needMiB)).replace("{c}", String(editCardMiB))} ` : " "}
      <FitTag fit={editFit} />
    </p>}
    {image && <details className="engine-operation-advanced"><summary>{tr("admin.catalog_advanced" as never)}</summary><div className="engine-operation-grid">{(Object.keys(params) as (keyof EngineParams)[]).map((key) => <label key={key}><span>{tr((`admin.engines_params_${key}`) as never)}</span><input value={params[key]} onChange={(event) => setParams((current) => ({ ...current, [key]: event.currentTarget.value }))} /></label>)}</div></details>}
    {validation && <p className="form-err">{validation}</p>}
    {error && <p className="form-err">{error}</p>}
    <footer className="engine-operation-footer"><Button variant="ghost" onClick={onClose} disabled={busy}>{tr("common.cancel")}</Button><Button variant="primary" disabled={busy || !!validation} onClick={() => void save()}>{tr("common.save")}</Button></footer></div>
  </Modal>;
}
