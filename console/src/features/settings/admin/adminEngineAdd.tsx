import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { Modal } from "../../../ui/Modal.tsx";
import { ViewHead } from "../../../ui/ViewHead.tsx";
import { EngineIngest, EngineModelsAdminView, engineIdFromFile, engineIngestPrefix, type EngineIngestAct, type ModelKind } from "./adminEngineModels.tsx";
import {
  engineIsImage,
  engineIsRemote,
  engineTitle,
  useEngineRows,
  type EngineParams,
  type EngineRow,
  type EngineStorageAnswer,
  type EngineStorageFile,
  type IngestCandidate,
  type IngestHit,
  type IngestJob,
  type IngestSearchAnswer,
  type IngestSearchRequest,
  type IngestVersion,
  type ResolvedSource,
} from "./engineTypes.ts";

type CatalogView = "search" | "registered";
type CatalogSource = "hf" | "civitai";

/** Full-pane catalogue. The former four-step wizard is intentionally not mounted: browsing
 * stays visible and a card opens one operation dialog for version, file, destination and
 * settings. */
export function EngineAddView({ engineKey, lora, initialView = "search", headerActions }: {
  engineKey: string;
  lora: boolean;
  initialView?: CatalogView;
  headerActions?: ReactNode;
}) {
  const tr = useT();
  const { rows, isSuper, err, load } = useEngineRows();
  const [selectedKey, setSelectedKey] = useState(engineKey);
  const [view, setView] = useState<CatalogView>(initialView);
  const [kind, setKind] = useState<ModelKind>(lora ? "lora" : "model");
  const row = (rows || []).find((candidate) => candidate.key === selectedKey) || (rows || [])[0];

  useEffect(() => {
    if (rows?.length && !rows.some((candidate) => candidate.key === selectedKey)) setSelectedKey(rows[0].key);
  }, [rows, selectedKey]);

  return (
    <div className="engine-catalog-pane admin-stage">
      <ViewHead actions={headerActions}>
        <span className="view-title"><Icon name="download" /> {tr("admin.catalog_title" as never)}</span>
      </ViewHead>
      {err && <p className="form-err pad">{err}</p>}
      {rows !== null && rows.length === 0 && (
        <section className="admin-panel engine-catalog-empty">
          <p>{tr("admin.engines_none")}</p>
          <p className="muted">{tr("admin.engines_browse_note")}</p>
        </section>
      )}
      {row && <>
        <div className="engine-catalog-nav">
          <div className="engine-catalog-role-tabs" role="tablist" aria-label={tr("admin.catalog_role_label" as never)}>
            {(rows || []).map((candidate) => (
              <button key={candidate.key} type="button" role="tab" aria-selected={candidate.key === row.key}
                className={candidate.key === row.key ? "active" : ""} onClick={() => setSelectedKey(candidate.key)}>
                <Icon name={engineIsImage(candidate) ? "file-media" : "comment"} />
                {tr(engineIsImage(candidate) ? "admin.engines_role_image" : "admin.engines_role_llm")}
              </button>
            ))}
          </div>
          <div className="seg engine-catalog-view-tabs" role="tablist" aria-label={tr("admin.catalog_view_label" as never)}>
            {(["search", "registered"] as const).map((next) => (
              <button key={next} type="button" role="tab" aria-selected={view === next}
                className={"seg-btn" + (view === next ? " active" : "")} onClick={() => setView(next)}>
                {tr((`admin.catalog_view_${next}`) as never)}
              </button>
            ))}
          </div>
          <span className="muted mono engine-catalog-engine-name">{engineTitle(row)}</span>
        </div>
        {engineIsRemote(row) && <p className="admin-hint pad">{tr("admin.engines_remote_catalog")} {row.url || ""}</p>}
        {view === "search" ? (
          engineIsImage(row)
            ? <ImageCatalog row={row} kind={kind} onKind={setKind} readOnly={engineIsRemote(row)} onChanged={load} />
            : <LLMCatalog row={row} kind={kind} onKind={setKind} readOnly={engineIsRemote(row)} onChanged={load} />
        ) : (
          <div className="engine-catalog-registered">
            {!isSuper && <p className="admin-hint">{tr("admin.engines_tenant_scope")}</p>}
            <EngineModelsAdminView initialEngineKey={row.key} initialKind={kind} embedded />
          </div>
        )}
      </>}
    </div>
  );
}

/** Compatibility harness that keeps the former ingest's detailed parser/guard regression suite
 * available. Product navigation never mounts the legacy workflow. */
export function LegacyEngineAddView({ engineKey, lora }: { engineKey: string; lora: boolean }) {
  const tr = useT();
  const { rows, load } = useEngineRows();
  const [job, setJob] = useState<IngestJob | null>(null);
  const row = (rows || []).find((candidate) => candidate.key === engineKey);
  if (!row) return null;
  if (job) return <p>{(tr("admin.engines_add_pane_running") as string).replace("{id}", job.model_id)}</p>;
  return <EngineIngest engineKey={engineKey} isImage={engineIsImage(row)} isLora={lora}
    baseModels={row.base_models} fileFlags={row.file_flags}
    models={(row.model_rows || []).filter((model) => (model.kind === "lora") === lora)}
    cardMiB={row.class?.vram_mib} busy={false} open setOpen={() => {}} onStarted={load} onJob={setJob} />;
}

type CatalogProps = {
  row: EngineRow;
  kind: ModelKind;
  onKind: (kind: ModelKind) => void;
  readOnly: boolean;
  onChanged: () => void;
};

export function ImageCatalog(props: CatalogProps) { return <CatalogBrowser {...props} image />; }
export function LLMCatalog(props: CatalogProps) { return <CatalogBrowser {...props} image={false} />; }

function CatalogBrowser({ row, kind, onKind, readOnly, onChanged, image }: CatalogProps & { image: boolean }) {
  const tr = useT();
  const [source, setSource] = useState<CatalogSource>(image ? "civitai" : "hf");
  const [sort, setSort] = useState(image ? "newest" : "updated");
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<IngestHit[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [operation, setOperation] = useState<{ hit?: IngestHit; act: EngineIngestAct } | null>(null);
  const [preview, setPreview] = useState<IngestHit | null>(null);
  const [storage, setStorage] = useState<EngineStorageAnswer | null>(null);
  const requestSeq = useRef(0);

  useEffect(() => {
    setSource(image ? "civitai" : "hf");
    setSort(image ? "newest" : "updated");
    setHits(null);
    setCursor("");
  }, [image, row.key]);

  const loadStorage = useCallback(async () => {
    try {
      const answer = await api(`api/admin/engines/${encodeURIComponent(row.key)}/storage`);
      if (!answer?.error) setStorage({ files: Array.isArray(answer?.files) ? answer.files : [], checked_at: answer?.checked_at });
    } catch { setStorage(null); }
  }, [row.key]);
  useEffect(() => { setStorage(null); void loadStorage(); }, [loadStorage]);

  const search = useCallback(async (more = false) => {
    const seq = ++requestSeq.current;
    const body: IngestSearchRequest = {
      q: query, source, sort, lora: kind === "lora", ...(more && cursor ? { cursor } : {}),
    };
    setBusy(true); setErr("");
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/search`, "POST", body);
      if (seq !== requestSeq.current) return;
      if (answer?.error) { setErr(errDetail(answer.error)); if (!more) setHits(null); return; }
      const page = (answer || {}) as IngestSearchAnswer;
      const next = Array.isArray(page.hits) ? page.hits : [];
      setHits((current) => more && current ? [...current, ...next] : next);
      setCursor(page.next_cursor || "");
    } finally { if (seq === requestSeq.current) setBusy(false); }
  }, [cursor, kind, query, row.key, sort, source]);

  useEffect(() => { void search(false); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [row.key, kind, source, sort]);
  const switchSource = (next: CatalogSource) => {
    setSource(next); setSort(next === "civitai" ? "newest" : "updated"); setHits(null); setCursor("");
  };
  const sortOptions = source === "civitai" ? ["newest", "downloads", "trending", "likes"] : ["updated", "downloads", "trending", "likes"];
  const modelRows = (row.model_rows || []).filter((model) => (model.kind === "lora") === (kind === "lora"));

  return (
    <section className="engine-catalog-browser">
      <div className="engine-catalog-toolbar">
        <span className="seg sm">
          {(["model", "lora"] as const).map((next) => <button key={next} type="button"
            className={"seg-btn" + (kind === next ? " active" : "")} onClick={() => onKind(next)}>
            {tr(next === "lora" ? "admin.engines_tab_loras" : "admin.engines_tab_models")}
          </button>)}
        </span>
        {image && <span className="seg sm">{(["civitai", "hf"] as const).map((next) => <button key={next}
          type="button" className={"seg-btn" + (source === next ? " active" : "")} onClick={() => switchSource(next)}>
          {tr((`admin.engines_ingest_source_${next}`) as never)}
        </button>)}</span>}
        <form className="engine-catalog-search" onSubmit={(event) => { event.preventDefault(); void search(false); }}>
          <input value={query} aria-label={tr("admin.engines_ingest_search")} placeholder={image ? "SDXL, Flux, style…" : "Qwen, Llama, coder…"}
            onChange={(event) => setQuery(event.currentTarget.value)} />
          <button type="submit" className="primary sm" disabled={busy}>{tr(query.trim() ? "admin.engines_ingest_search_go" : "admin.engines_ingest_browse_go")}</button>
        </form>
        <label className="engine-catalog-sort"><span>{tr("admin.catalog_sort" as never)}</span>
          <select value={sort} onChange={(event) => setSort(event.currentTarget.value)}>
            {sortOptions.map((option) => <option key={option} value={option}>{tr((`admin.engines_ingest_sort_${option}`) as never)}</option>)}
          </select>
        </label>
        {!readOnly && <button type="button" className="ghost sm" onClick={() => setOperation({ act: "new" })}>{tr("admin.catalog_manual" as never)}</button>}
      </div>
      {source === "civitai" && sort === "newest" && <p className="muted engine-catalog-sort-note">{tr("admin.catalog_civitai_newest_note" as never)}</p>}
      {storage === null && <p className="muted engine-catalog-storage-note">{tr("admin.catalog_storage_checking" as never)}</p>}
      {err && <p className="form-err">{err}</p>}
      {hits?.length === 0 && <p className="muted engine-catalog-zero">{tr("admin.engines_ingest_search_none")}</p>}
      {!!hits?.length && <ul className="engine-catalog-grid">{hits.map((hit) => <CatalogCard
        key={`${hit.source}:${hit.model_ref || hit.ref}:${hit.ref}`} hit={hit} image={image}
        saved={savedFilesForHit(hit, storage?.files || [])} readOnly={readOnly}
        canAttach={modelRows.length > 0 && (row.file_flags || []).some(Boolean)} canReplace={modelRows.length > 0}
        onPreview={() => setPreview(hit)} onOperation={(act) => setOperation({ hit, act })} />)}</ul>}
      {cursor && <button type="button" className="engine-catalog-more" disabled={busy} onClick={() => void search(true)}>{tr("admin.catalog_more" as never)}</button>}
      <CatalogJobs engineKey={row.key} />
      {operation && <CatalogOperation row={row} kind={kind} hit={operation.hit} initialAct={operation.act}
        storage={storage?.files || []} onClose={() => setOperation(null)} onStarted={() => { setOperation(null); void loadStorage(); onChanged(); }} />}
      {preview?.preview_url && <Modal title={preview.name} className="engine-catalog-lightbox" onClose={() => setPreview(null)}>
        <img src={preview.preview_url} alt={preview.name} />
      </Modal>}
    </section>
  );
}

function CatalogCard({ hit, image, saved, readOnly, canAttach, canReplace, onPreview, onOperation }: {
  hit: IngestHit; image: boolean; saved: EngineStorageFile[]; readOnly: boolean; canAttach: boolean; canReplace: boolean;
  onPreview: () => void; onOperation: (act: EngineIngestAct) => void;
}) {
  const tr = useT();
  const license = hit.license_name || hit.license;
  return <li className="engine-catalog-card">
    <div className="engine-catalog-card-main"><div className="engine-catalog-card-copy">
      <div className="engine-catalog-card-title"><span>{hit.name}</span><span className="engines-model-tag">{hit.source === "civitai" ? "Civitai" : "Hugging Face"}</span></div>
      <div className="engine-catalog-card-tags">
        {license && <span className="engines-model-tag">{license}</span>}{hit.base_model && <span className="engines-model-tag">{hit.base_model}</span>}
        {hit.gated && <span className="engines-model-tag warn">{tr("admin.engines_ingest_hit_gated")}</span>}
        {hit.login_required === "yes" && <span className="engines-model-tag warn">{tr("admin.engines_hit_login_required")}</span>}
      </div>
      <p className="muted engine-catalog-card-stats">
        {hit.downloads ? `${compactCount(hit.downloads)} ${tr("admin.engines_ingest_hit_downloads")}` : ""}
        {hit.likes ? ` · ${compactCount(hit.likes)} ${tr("admin.engines_ingest_hit_likes")}` : ""}
        {hit.updated_at ? ` · ${tr("admin.engines_ingest_hit_updated")} ${fmtDateTime(hit.updated_at)}` : ""}
        {hit.published_at ? ` · ${tr("admin.engines_ingest_hit_published")} ${fmtDateTime(hit.published_at)}` : ""}
      </p>
      {saved.length > 0 && <p className="engine-catalog-saved">{tr("admin.catalog_saved_count" as never, { count: saved.length } as never)}</p>}
    </div>{image && hit.preview_url && <button type="button" className="engine-catalog-thumb" onClick={onPreview} aria-label={tr("admin.catalog_preview" as never)}><img src={hit.preview_url} alt="" loading="lazy" /></button>}</div>
    <footer className="engine-catalog-card-footer">
      {hit.url && <a href={hit.url} target="_blank" rel="noopener noreferrer">{tr("admin.catalog_source_page" as never)}</a>}
      {!readOnly && <span><button type="button" className="primary sm" onClick={() => onOperation("new")}>{tr("admin.catalog_add" as never)}</button>
        {canAttach && <button type="button" className="sm" onClick={() => onOperation("attach")}>{tr("admin.catalog_attach" as never)}</button>}
        {canReplace && <button type="button" className="sm" onClick={() => onOperation("replace")}>{tr("admin.catalog_replace" as never)}</button>}</span>}
    </footer>
  </li>;
}

function CatalogOperation({ row, kind, hit, initialAct, storage, onClose, onStarted }: {
  row: EngineRow; kind: ModelKind; hit?: IngestHit; initialAct: EngineIngestAct;
  storage: EngineStorageFile[]; onClose: () => void; onStarted: () => void;
}) {
  const tr = useT();
  const image = engineIsImage(row);
  const isLora = kind === "lora";
  const [act, setAct] = useState(initialAct);
  const [sourceType, setSourceType] = useState<CatalogSource>(hit?.source === "civitai" ? "civitai" : "hf");
  const [manualRef, setManualRef] = useState(hit?.model_ref || hit?.ref || "");
  const [versions, setVersions] = useState<IngestVersion[]>([]);
  const [versionRef, setVersionRef] = useState(hit?.ref || "");
  const [files, setFiles] = useState<IngestCandidate[]>([]);
  const [file, setFile] = useState("");
  const [resolved, setResolved] = useState<ResolvedSource | null>(null);
  const [targetId, setTargetId] = useState("");
  const [fileFlag, setFileFlag] = useState("");
  const [partChosen, setPartChosen] = useState(false);
  const [id, setId] = useState("");
  const [description, setDescription] = useState("");
  const [baseModel, setBaseModel] = useState("");
  const [context, setContext] = useState("");
  const [output, setOutput] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [confirmVram, setConfirmVram] = useState(false);
  const [withVae, setWithVae] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [params, setParams] = useState<Record<keyof EngineParams, string>>({
    steps: "", cfg: "", sampler: "", scheduler: "", clip_skip: "", weight: "",
  });
  const models = (row.model_rows || []).filter((model) => (model.kind === "lora") === isLora);
  const target = models.find((model) => model.id === targetId);
  const flags = row.file_flags || [];
  const taken = new Set((target?.file_rows || []).map((part) => part.flag || ""));
  const slots = flags.filter((flag) => act === "attach" ? !!flag && !taken.has(flag) : taken.has(flag));
  const rawRef = manualRef.trim();
  const hfURL = rawRef.match(/^https?:\/\/huggingface\.co\/([^/?#]+\/[^/?#]+)(?:\/(?:blob|resolve)\/([^/?#]+)\/([^?#]+))?/);
  const civitaiURL = rawRef.match(/^https?:\/\/(?:[\w-]+\.)*civitai\.com\/models\/(\d+).*?[?&]modelVersionId=(\d+)/);
  const civitaiLegacy = rawRef.match(/^civitai:(\d+)$/);
  const repo = hit?.model_ref || (sourceType === "civitai" ? civitaiURL?.[1] : hfURL?.[1]) || rawRef || hit?.ref || "";
  const pastedVersion = (sourceType === "civitai" ? civitaiURL?.[2] || civitaiLegacy?.[1] : hfURL?.[2]) || "";
  const pastedFile = sourceType === "hf" ? hfURL?.[3] || "" : "";
  const plainURL = /^https?:\/\//.test(rawRef) && !hfURL && !civitaiURL;

  const sourceBody = useCallback((fileName = file) => {
    if (plainURL) return { url: repo, sha256: fileName.trim() };
    if (sourceType === "civitai") return { civitai: { versionId: Number(versionRef || hit?.ref), file: fileName } };
    return { hf: { repo, revision: versionRef, file: fileName } };
  }, [file, hit?.ref, plainURL, repo, sourceType, versionRef]);

  const loadFiles = useCallback(async (selectedVersion: string) => {
    setErr(""); setFiles([]); setFile(""); setResolved(null);
    const source = sourceType === "civitai"
      ? { civitai: { versionId: Number(selectedVersion), file: "" } }
      : { hf: { repo, revision: selectedVersion, file: "" } };
    const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/files`, "POST", { source });
    if (answer?.error) { setErr(errDetail(answer.error)); return; }
    const offered = (Array.isArray(answer?.files) ? answer.files : []) as IngestCandidate[];
    setFiles(offered);
    if (offered.length === 1) setFile(offered[0].name);
  }, [repo, row.key, sourceType]);

  const inspect = useCallback(async () => {
    if (!repo) return;
    setBusy(true); setErr("");
    try {
      if (plainURL) { setVersions([]); setFiles([]); return; }
      const requestedVersion = versionRef || pastedVersion;
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/versions`, "POST", {
        source: sourceType, ref: hit?.ref || pastedVersion || repo, model_ref: hit?.model_ref || civitaiURL?.[1] || repo,
      });
      if (answer?.error) { setErr(errDetail(answer.error)); return; }
      const offered = (Array.isArray(answer?.versions) ? answer.versions : []) as IngestVersion[];
      setVersions(offered);
      const first = offered.find((version) => version.ref === requestedVersion)?.ref || offered[0]?.ref || requestedVersion;
      setVersionRef(first);
      if (first) {
        await loadFiles(first);
        if (pastedFile) setFile(pastedFile);
      }
    } finally { setBusy(false); }
  }, [civitaiURL, hit?.model_ref, hit?.ref, loadFiles, pastedFile, pastedVersion, plainURL, repo, row.key, sourceType, versionRef]);

  useEffect(() => { if (hit) void inspect(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);
  useEffect(() => {
    if (act !== "new" && models[0] && !targetId) setTargetId(models[0].id);
  }, [act, models, targetId]);

  useEffect(() => {
    if (!file) return;
    if (!id) setId(engineIdFromFile(file));
    let live = true;
    setResolved(null); setAccepted(false);
    apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/resolve`, "POST", {
      source: sourceBody(file), kind: isLora ? "lora" : image ? "checkpoint" : "gguf",
    }).then((answer) => {
      if (!live) return;
      if (answer?.error) { setErr(errDetail(answer.error)); return; }
      const found = answer as ResolvedSource;
      setResolved(found);
      if (found.base_model_suggest && !baseModel) setBaseModel(found.base_model_suggest);
      if (found.context_length && !context) {
        setContext(String(found.context_length)); setOutput(String(Math.floor(found.context_length / 8)));
      }
      setWithVae(found.vae_bundled === "no" && !!found.family_vae && !found.family_vae.unreachable);
    });
    return () => { live = false; };
    // Existing typed settings deliberately outrank metadata suggestions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file, image, isLora, row.key, sourceBody]);

  const selectedFile = files.find((candidate) => candidate.name === file);
  const immutableReuse = exactReusableStorage(storage, sourceType, repo, versionRef, file);
  const bytes = resolved?.bytes || selectedFile?.bytes || 0;
  const needMiB = bytes ? Math.ceil(bytes / 1048576) : 0;
  const cardMiB = row.class?.vram_mib;
  const tooLarge = !!cardMiB && needMiB > cardMiB && !isLora;
  const baseOptions = isLora && !image
    ? (row.model_rows || []).filter((model) => model.kind !== "lora").map((model) => model.id)
    : row.base_models || [];
  const missing = (() => {
    if (!repo) return tr("admin.catalog_need_source" as never) as string;
    if (!plainURL && !versionRef) return tr("admin.catalog_need_version" as never) as string;
    if (!file) return tr(plainURL ? "admin.catalog_need_checksum" as never : "admin.catalog_need_file" as never) as string;
    if (!resolved) return tr("admin.catalog_resolving" as never) as string;
    if (resolved.can_ingest === false) return tr("admin.engines_wizard_cannot") as string;
    if (act !== "new" && !targetId) return tr("admin.catalog_need_target" as never) as string;
    if (act !== "new" && flags.length > 0 && !partChosen) return tr("admin.catalog_need_part" as never) as string;
    if (act === "new" && !id.trim()) return tr("admin.engines_wizard_need_id") as string;
    if (act === "new" && baseOptions.length > 0 && !baseModel) return tr("admin.engines_wizard_need_family") as string;
    if (!accepted) return tr("admin.catalog_need_license" as never) as string;
    if (tooLarge && !confirmVram) return tr("admin.catalog_need_vram" as never) as string;
    return "";
  })();

  const start = async () => {
    if (missing) return;
    setBusy(true); setErr("");
    const targetName = act === "new" ? id.trim() : targetId;
    const n = (value: string) => { const parsed = Number(value.trim().replace(/[_,]/g, "")); return Number.isFinite(parsed) && parsed > 0 ? parsed : 0; };
    const paramsBody = Object.fromEntries(Object.entries(params).flatMap(([key, value]) => value.trim()
      ? [[key, key === "sampler" || key === "scheduler" ? value.trim() : n(value)]] : []));
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest`, "POST", {
        id: targetName, kind: isLora ? "lora" : image ? "checkpoint" : "gguf",
        s3Key: `${engineIngestPrefix(image, fileFlag, isLora)}${file || targetName}`,
        source: sourceBody(file), description: description.trim(), base_model: baseModel, file_flag: fileFlag,
        attach: act === "attach", replace: act === "replace",
        context_tokens: !image && !isLora ? n(context) : 0,
        max_output_tokens: !image && !isLora ? n(output) : 0,
        params: paramsBody, license_accepted: true, with_family_vae: withVae,
        ...(immutableReuse ? { reuse_s3_key: immutableReuse.s3_key } : {}),
      });
      if (answer?.error) { setErr(errDetail(answer.error)); return; }
      onStarted();
    } finally { setBusy(false); }
  };

  return <Modal title={tr("admin.catalog_operation_title" as never)} className="engine-catalog-operation" onClose={onClose} lockClose={busy}>
    <div className="engine-operation-body">
      <div className="engine-operation-choice">{(["new", "attach", "replace"] as const).map((next) => <label key={next}>
        <input type="radio" name="catalog-act" checked={act === next} onChange={() => setAct(next)} />
        <span>{tr((`admin.catalog_act_${next}`) as never)}</span></label>)}</div>
      {!hit && <div className="engine-operation-manual">
        {image && <select value={sourceType} onChange={(event) => setSourceType(event.currentTarget.value as CatalogSource)}><option value="civitai">Civitai</option><option value="hf">Hugging Face / URL</option></select>}
        <input value={manualRef} onChange={(event) => setManualRef(event.currentTarget.value)} placeholder="owner/repository or https://…" />
        <button type="button" onClick={() => void inspect()} disabled={busy || !manualRef.trim()}>{tr("admin.catalog_inspect" as never)}</button>
      </div>}
      <div className="engine-operation-grid">
        {!plainURL && <label><span>{tr("admin.catalog_version" as never)}</span><select value={versionRef} onChange={(event) => { const next = event.currentTarget.value; setVersionRef(next); void loadFiles(next); }}>
          {!versions.length && <option value={versionRef}>{versionRef || "—"}</option>}{versions.map((version) => <option key={version.ref} value={version.ref}>{version.name}</option>)}
        </select></label>}
        <label><span>{tr(plainURL ? "admin.catalog_checksum" as never : "admin.catalog_file" as never)}</span>{files.length
          ? <select value={file} onChange={(event) => setFile(event.currentTarget.value)}><option value="">{tr("admin.catalog_pick_file" as never)}</option>{files.map((candidate) => <option key={candidate.ref || candidate.name} value={candidate.name}>{candidate.name}</option>)}</select>
          : <input value={file} onChange={(event) => setFile(event.currentTarget.value)} placeholder={plainURL ? "sha256" : "model.safetensors"} />}</label>
        {act !== "new" ? <label><span>{tr("admin.catalog_destination" as never)}</span><select value={targetId} onChange={(event) => { setTargetId(event.currentTarget.value); setFileFlag(""); setPartChosen(false); }}>
          <option value="">{tr("admin.catalog_pick_target" as never)}</option>{models.map((model) => <option key={model.id} value={model.id}>{model.id}</option>)}</select></label>
          : <label><span>{tr("admin.engines_model_add_id")}</span><input value={id} onChange={(event) => setId(event.currentTarget.value)} /></label>}
        {act !== "new" && flags.length > 0 && <label><span>{tr("admin.engines_model_add_part")}</span><select
          value={partChosen ? fileFlag || "__whole__" : ""}
          onChange={(event) => { setPartChosen(!!event.currentTarget.value); setFileFlag(event.currentTarget.value === "__whole__" ? "" : event.currentTarget.value); }}>
          <option value="">{tr("admin.catalog_pick_part" as never)}</option>{slots.map((slot) => <option key={slot || "__whole__"} value={slot || "__whole__"}>{slot || tr("admin.engines_model_add_part_whole")}</option>)}</select></label>}
        {act === "new" && baseOptions.length > 0 && <label><span>{tr("admin.engines_model_add_family")}</span><select value={baseModel} onChange={(event) => setBaseModel(event.currentTarget.value)}>
          <option value="">{tr("admin.engines_model_add_family_pick")}</option>{baseOptions.map((base) => <option key={base} value={base}>{base}</option>)}</select></label>}
      </div>
      {resolved && <div className="engine-operation-facts"><span>{resolved.bytes ? `${Math.round(resolved.bytes / 1048576)} MiB` : tr("admin.catalog_size_unknown" as never)}</span>
        {(resolved.license_name || resolved.license) && <span>{resolved.license_name || resolved.license}</span>}
        {resolved.gated && <span className="warn">{tr("admin.engines_ingest_hit_gated")}</span>}
        {immutableReuse && <span className="ok">{tr("admin.catalog_reuse_present" as never)}</span>}</div>}
      {resolved?.vae_bundled === "no" && resolved.family_vae && <label className="engine-operation-check"><input type="checkbox" checked={withVae} onChange={(event) => setWithVae(event.currentTarget.checked)} /><span>{tr("admin.catalog_with_vae" as never)}</span></label>}
      {tooLarge && <label className="engine-operation-check warn"><input type="checkbox" checked={confirmVram} onChange={(event) => setConfirmVram(event.currentTarget.checked)} /><span>{tr("admin.catalog_vram_warning" as never, { need: needMiB, card: cardMiB } as never)}</span></label>}
      <label className="engine-operation-check"><input type="checkbox" checked={accepted} onChange={(event) => setAccepted(event.currentTarget.checked)} /><span>{tr("admin.engines_ingest_accept")}</span></label>
      <details className="engine-operation-advanced"><summary>{tr("admin.catalog_advanced" as never)}</summary><div className="engine-operation-grid">
        <label><span>{tr("admin.engines_model_add_desc")}</span><input value={description} onChange={(event) => setDescription(event.currentTarget.value)} /></label>
        {!image && !isLora && <label><span>{tr("admin.engines_model_add_ctx")}</span><input value={context} onChange={(event) => setContext(event.currentTarget.value)} inputMode="numeric" /></label>}
        {!image && !isLora && <label><span>{tr("admin.engines_model_add_out")}</span><input value={output} onChange={(event) => setOutput(event.currentTarget.value)} inputMode="numeric" /></label>}
        {(Object.keys(params) as (keyof EngineParams)[]).map((key) => <label key={key}><span>{key}</span><input value={params[key]} onChange={(event) => setParams((current) => ({ ...current, [key]: event.currentTarget.value }))} /></label>)}
      </div></details>
      {err && <p className="form-err">{err}</p>}
      <footer className="engine-operation-footer"><span className="muted">{missing}</span><button type="button" onClick={onClose} disabled={busy}>{tr("common.cancel")}</button><button type="button" className="primary" onClick={() => void start()} disabled={busy || !!missing}>{tr("admin.engines_ingest_go")}</button></footer>
    </div>
  </Modal>;
}

function CatalogJobs({ engineKey }: { engineKey: string }) {
  const tr = useT();
  const [jobs, setJobs] = useState<IngestJob[]>([]);
  const load = useCallback(async () => {
    const answer = await api(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest`);
    if (!answer?.error) setJobs(Array.isArray(answer?.jobs) ? answer.jobs : []);
  }, [engineKey]);
  useEffect(() => { void load(); }, [load]);
  const live = jobs.some((job) => job.state === "pending" || job.state === "running");
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => void load(), 5000);
    return () => clearInterval(timer);
  }, [live, load]);
  if (!jobs.length) return null;
  return <details className="engine-catalog-jobs" open={live}>
    <summary>{tr("admin.engines_ingest_jobs_head")} <span className="engines-model-tag">{jobs.length}</span></summary>
    <ul>{jobs.slice(0, 6).map((job) => <li key={job.id}>
      <span className="mono">{job.model_id}</span>
      <span className={`engines-model-tag ${job.state === "done" ? "on" : job.state === "failed" ? "bad" : "lead"}`}>
        {tr((`admin.engines_ingest_state_${job.state}`) as never)}
      </span>
      {job.message && <span className="form-err">{job.message}</span>}
    </li>)}</ul>
  </details>;
}

/** Count concrete source files, never repositories. State stays visible separately and an
 * unknown check is never promoted to missing. */
export function savedFilesForHit(hit: IngestHit, files: EngineStorageFile[]): EngineStorageFile[] {
  const modelRef = hit.model_ref || (hit.source === "hf" ? hit.ref : "");
  if (!modelRef) return [];
  if (hit.source === "civitai") {
    return files.filter((file) => !!file.source && (
      file.source === `civitai:${hit.ref}` || file.source.startsWith(`civitai:${hit.ref}/`)
    ));
  }
  return files.filter((file) => !!file.source && (
    file.source.startsWith(`hf:${modelRef}/`) || file.source.startsWith(`hf:${modelRef}@`)
  ));
}

/** Reuse is stricter than display: only a present object with an immutable revision/file
 * identity qualifies. Legacy `hf:repo/file` strings remain visible but are never guessed. */
export function exactReusableStorage(files: EngineStorageFile[], source: CatalogSource,
  modelRef: string, versionRef: string, fileName: string): EngineStorageFile | undefined {
  if (!modelRef || !versionRef || !fileName) return undefined;
  if (source === "civitai") {
    return files.find((file) => file.state === "present" && (
      file.source === `civitai:${versionRef}/${fileName}` || file.source === `civitai:${versionRef}`
    ));
  }
  const identities = new Set([
    `hf:${modelRef}@${versionRef}/${fileName}`,
    `hf:${modelRef}/${fileName}@${versionRef}`,
  ]);
  return files.find((file) => file.state === "present" && !!file.source && identities.has(file.source));
}

function compactCount(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  if (value >= 1_000) return `${Math.round(value / 1_000)}k`;
  return String(Math.round(value));
}
