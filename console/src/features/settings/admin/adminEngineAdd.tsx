import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { Button, IconButton } from "../../../ui/Button.tsx";
import { Modal } from "../../../ui/Modal.tsx";
import { ViewHead } from "../../../ui/ViewHead.tsx";
import {
  EngineIngestJobs,
  EngineModelAdd,
  engineIdFromFile,
  engineIngestPrefix,
  type EngineIngestAct,
  type ModelKind,
  type ModelPrefill,
} from "./adminEngineModels.tsx";
import {
  engineIsImage,
  engineIsRemote,
  engineTitle,
  useEngineRows,
  type EngineParams,
  type EngineModel,
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
type CatalogSource = "hf" | "civitai" | "civitai-red";

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
    <div className="engines-add-pane engine-catalog-pane admin-stage">
      <ViewHead actions={headerActions}>
        <span className="view-title"><Icon name="download" /> {tr("admin.catalog_title" as never)}</span>
      </ViewHead>
      {err && <p className="form-err pad">{err}</p>}
      {rows !== null && rows.length === 0 && (
        <NoEngineCatalog />
      )}
      {row && <>
        <div className="engine-catalog-nav">
          <div className="engine-catalog-role-tabs" role="tablist" aria-label={tr("admin.catalog_role_label" as never)}>
            {(rows || []).map((candidate) => (
              <Button key={candidate.key} variant="ghost" small role="tab" aria-selected={candidate.key === row.key}
                className={candidate.key === row.key ? "active" : ""} onClick={() => setSelectedKey(candidate.key)}>
                <Icon name={engineIsImage(candidate) ? "file-media" : "comment"} />
                {tr(engineIsImage(candidate) ? "admin.engines_role_image" : "admin.engines_role_llm")}
              </Button>
            ))}
          </div>
          <div className="seg engine-catalog-view-tabs" role="tablist" aria-label={tr("admin.catalog_view_label" as never)}>
            {(["search", "registered"] as const).map((next) => (
              <Button key={next} variant="ghost" small role="tab" aria-selected={view === next}
                className={"seg-btn" + (view === next ? " active" : "")} onClick={() => setView(next)}>
                {tr((`admin.catalog_view_${next}`) as never)}
              </Button>
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
          <RegisteredCatalog row={row} kind={kind} onKind={setKind} isSuper={isSuper}
            readOnly={engineIsRemote(row)} onChanged={load} />
        )}
      </>}
    </div>
  );
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
  const [submittedQuery, setSubmittedQuery] = useState("");
  const [hits, setHits] = useState<IngestHit[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [busyMore, setBusyMore] = useState(false);
  const [err, setErr] = useState("");
  const [operation, setOperation] = useState<{ hit?: IngestHit; act: EngineIngestAct; source?: CatalogSource } | null>(null);
  const [preview, setPreview] = useState<IngestHit | null>(null);
  const [storage, setStorage] = useState<EngineStorageAnswer | null>(null);
  const [storageState, setStorageState] = useState<"checking" | "ready" | "failed">("checking");
  const [startedJob, setStartedJob] = useState<IngestJob | undefined>();
  const requestSeq = useRef(0);

  useEffect(() => {
    setSource(image ? "civitai" : "hf");
    setSort(image ? "newest" : "updated");
    setHits(null);
    setCursor("");
  }, [image, row.key]);

  const loadStorage = useCallback(async () => {
    setStorageState("checking");
    try {
      const answer = await api(`api/admin/engines/${encodeURIComponent(row.key)}/storage`);
      if (answer?.error) { setStorage(null); setStorageState("failed"); return; }
      setStorage({ files: Array.isArray(answer?.files) ? answer.files : [], checked_at: answer?.checked_at });
      setStorageState("ready");
    } catch { setStorage(null); setStorageState("failed"); }
  }, [row.key]);
  useEffect(() => { setStorage(null); void loadStorage(); }, [loadStorage]);

  const search = useCallback(async (more = false) => {
    const seq = ++requestSeq.current;
    const body: IngestSearchRequest = {
      q: more ? submittedQuery : query, source, sort, lora: kind === "lora", ...(more && cursor ? { cursor } : {}),
    };
    setBusy(true); setBusyMore(more); setErr("");
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/search`, "POST", body);
      if (seq !== requestSeq.current) return;
      if (answer?.error) { setErr(errDetail(answer.error)); if (!more) setHits(null); return; }
      const page = (answer || {}) as IngestSearchAnswer;
      const next = Array.isArray(page.hits) ? page.hits : [];
      setHits((current) => more && current ? [...current, ...next] : next);
      if (!more) setSubmittedQuery(query);
      setCursor(page.next_cursor || "");
    } finally { if (seq === requestSeq.current) { setBusy(false); setBusyMore(false); } }
  }, [cursor, kind, query, row.key, sort, source, submittedQuery]);

  useEffect(() => { void search(false); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [row.key, kind, source, sort]);
  const switchSource = (next: CatalogSource) => {
    setSource(next); setSort(next === "civitai" || next === "civitai-red" ? "newest" : "updated"); setHits(null); setCursor("");
  };
  const sortOptions = source === "civitai" || source === "civitai-red" ? ["newest", "downloads", "trending", "likes"] : ["updated", "downloads", "trending", "likes"];
  const modelRows = (row.model_rows || []).filter((model) => (model.kind === "lora") === (kind === "lora"));

  return (
    <section
      className="engine-catalog-browser"
      aria-label={tr(image ? "admin.catalog_image_title" as never : "admin.catalog_llm_title" as never)}
    >
      <div className="engine-catalog-toolbar">
        <span className="seg sm">
          {(["model", "lora"] as const).map((next) => <Button key={next} variant="ghost" small
            className={"seg-btn" + (kind === next ? " active" : "")} onClick={() => onKind(next)}>
            {tr(next === "lora" ? "admin.engines_tab_loras" : "admin.engines_tab_models")}
          </Button>)}
        </span>
        {image && <span className="seg sm">{(["civitai", "civitai-red", "hf"] as const).map((next) => <Button key={next} variant="ghost" small
          className={"seg-btn" + (source === next ? " active" : "")} onClick={() => switchSource(next)}>
          {tr((`admin.engines_ingest_source_${next}`) as never)}
        </Button>)}</span>}
        <form className="engine-catalog-search" onSubmit={(event) => { event.preventDefault(); void search(false); }}>
          <input value={query} aria-label={tr("admin.engines_ingest_search")} placeholder={image ? "SDXL, Flux, style…" : "Qwen, Llama, coder…"}
            onChange={(event) => setQuery(event.currentTarget.value)} />
          <Button type="submit" variant="primary" small disabled={busy}>
            {busy && !busyMore && <Icon name="loading" spin />}
            {tr(query.trim() ? "admin.engines_ingest_search_go" : "admin.engines_ingest_browse_go")}
          </Button>
        </form>
        <label className="engine-catalog-sort"><span>{tr("admin.catalog_sort" as never)}</span>
          <select value={sort} onChange={(event) => setSort(event.currentTarget.value)}>
            {sortOptions.map((option) => <option key={option} value={option}>{tr((`admin.engines_ingest_sort_${option}`) as never)}</option>)}
          </select>
        </label>
        {!readOnly && <Button variant="ghost" small icon="link" onClick={() => setOperation({ act: "new", source })}>{tr("admin.catalog_manual" as never)}</Button>}
      </div>
      {(source === "civitai" || source === "civitai-red") && sort === "newest" && <p className="muted engine-catalog-sort-note">{tr("admin.catalog_civitai_newest_note" as never)}</p>}
      {storageState === "checking" && <p className="muted engine-catalog-storage-note">{tr("admin.catalog_storage_checking" as never)}</p>}
      {storageState === "failed" && <p className="form-err engine-catalog-storage-note">{tr("admin.catalog_storage_unavailable" as never)}</p>}
      {err && <p className="form-err">{err}</p>}
      {busy && !busyMore && <p className="muted engine-catalog-loading"><Icon name="loading" spin /> {tr("admin.engines_ingest_searching" as never)}</p>}
      {!busy && hits?.length === 0 && <p className="muted engine-catalog-zero">{tr("admin.engines_ingest_search_none")}</p>}
      {!!hits?.length && <ul className="engine-catalog-grid">{hits.map((hit) => {
        const props: BrowseCardProps = {
          hit, kind, saved: savedFilesForHit(hit, storage?.files || []), storage: storage?.files || [], storageState, readOnly,
          canAttach: modelRows.length > 0 && (row.file_flags || []).some(Boolean),
          canReplace: modelRows.length > 0, onOperation: (act) => setOperation({ hit, act }),
        };
        return image
          ? <ImageCatalogCard key={`${hit.source}:${hit.model_ref || hit.ref}:${hit.ref}`} {...props} onPreview={() => setPreview(hit)} />
          : <LLMCatalogCard key={`${hit.source}:${hit.model_ref || hit.ref}:${hit.ref}`} {...props} />;
      })}</ul>}
      {cursor && query === submittedQuery && <CatalogMore busy={busy} loading={busy && busyMore}
        count={hits?.length || 0} onMore={() => void search(true)} />}
      <CatalogJobs engineKey={row.key} started={startedJob} onCompleted={() => { void loadStorage(); onChanged(); }} />
      {operation && <CatalogOperation row={row} kind={kind} hit={operation.hit} initialAct={operation.act} initialSource={operation.source}
        storage={storage?.files || []} onClose={() => setOperation(null)} onStarted={(job) => { setStartedJob(job); setOperation(null); void loadStorage(); onChanged(); }} />}
      {preview?.preview_url && <Modal title={preview.name} className="engine-catalog-lightbox" onClose={() => setPreview(null)}>
        <img className="ui-modal-body" src={preview.preview_url} alt={preview.name} />
      </Modal>}
    </section>
  );
}

/** The end of the list fetches the next page by itself, and keeps the button.
 *
 * The button is not a leftover: it is what still works when the observer cannot run (no
 * IntersectionObserver, a pane that is not the scroller), and it is the deliberate way past the
 * guard below. `count` is that guard — a page that added no row stops the automatic chain, so an
 * upstream error with a live cursor cannot turn one landing at the bottom into an endless
 * request loop. */
function CatalogMore({ busy, loading, count, onMore }: { busy: boolean; loading: boolean; count: number; onMore: () => void }) {
  const tr = useT();
  const sentinel = useRef<HTMLDivElement | null>(null);
  // Read through refs because the observer is deliberately NOT rebuilt when these change:
  // re-observing reports the current state immediately, and doing that on every render of a
  // visible sentinel is a second request for the same landing.
  const fire = useRef(onMore);
  const seen = useRef(count);
  const autoAt = useRef(-1);
  // Updated in an effect, and declared BEFORE the one that observes: effects commit in source
  // order, so the observer below never reads a stale callback, and nothing is written during
  // render.
  useEffect(() => { fire.current = onMore; seen.current = count; });

  useEffect(() => {
    const node = sentinel.current;
    // Nothing is observed while a page is in flight, and observing again once it lands is what
    // continues the chain: an observer reports a CHANGE, so a short page that leaves the
    // sentinel on screen would otherwise stop the scroll dead.
    if (busy || !node || typeof IntersectionObserver !== "function") return;
    const io = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting) || autoAt.current === seen.current) return;
      autoAt.current = seen.current;
      fire.current();
    }, { rootMargin: "400px" });
    io.observe(node);
    return () => io.disconnect();
  }, [busy]);

  return <>
    <div ref={sentinel} className="engine-catalog-sentinel" aria-hidden="true" />
    <Button variant="ghost" small icon={loading ? undefined : "chevron-down"} className="engine-catalog-more"
      disabled={busy} onClick={() => { autoAt.current = -1; onMore(); }}>
      {loading && <Icon name="loading" spin />}
      {tr("admin.catalog_more" as never)}
    </Button>
  </>;
}

type BrowseCardProps = {
  hit: IngestHit;
  kind: ModelKind;
  saved: EngineStorageFile[];
  storage: EngineStorageFile[];
  storageState: "checking" | "ready" | "failed";
  readOnly: boolean;
  canAttach: boolean;
  canReplace: boolean;
  onOperation: (act: EngineIngestAct) => void;
};

function ImageCatalogCard({ hit, kind, saved, onPreview, ...actions }: BrowseCardProps & { onPreview: () => void }) {
  const tr = useT();
  const license = hit.license_name || hit.license;
  return <li className="engine-catalog-card" aria-label={hit.name}>
    <div className="engine-catalog-card-main"><div className="engine-catalog-card-copy">
      <div className="engine-catalog-card-title"><span>{hit.name}</span><span className="engines-model-tag">{kind === "lora" ? "LoRA" : tr("admin.catalog_checkpoint" as never)}</span></div>
      <div className="engine-catalog-card-tags">
        <span className="engines-model-tag">{hit.source === "civitai" ? "Civitai" : "Hugging Face"}</span>
        {/* Civitai's own content rating. Drawn on every Civitai hit, not only ones from the
            civitai-red tab — the plain tab's own default query still answers a nonzero level
            (measured), so a card without this would read as "safe" on a false premise. */}
        {!!hit.nsfw_level && <span className="engines-model-tag">{(tr("admin.engines_ingest_hit_nsfw_level" as never) as string).replace("{n}", String(hit.nsfw_level))}</span>}
        {hit.base_model && <span className="engines-model-tag">{tr("admin.catalog_family" as never)}: {hit.base_model}</span>}
        {license && <span className="engines-model-tag">{license}</span>}
        <CatalogRestrictionTags value={hit} />
      </div>
      <p className="muted engine-catalog-card-stats">
        {hit.downloads ? `${compactCount(hit.downloads)} ${tr("admin.engines_ingest_hit_downloads")}` : ""}
        {hit.likes ? ` · ${compactCount(hit.likes)} ${tr("admin.engines_ingest_hit_likes")}` : ""}
        {hit.updated_at ? ` · ${tr("admin.engines_ingest_hit_updated")} ${fmtDateTime(hit.updated_at)}` : ""}
        {hit.published_at ? ` · ${tr("admin.engines_ingest_hit_published")} ${fmtDateTime(hit.published_at)}` : ""}
      </p>
      <CatalogSavedState hit={hit} saved={saved} storage={actions.storage} storageState={actions.storageState} />
    </div>{hit.preview_url && <Button variant="ghost" className="engine-catalog-thumb" onClick={onPreview} aria-label={`${tr("admin.catalog_preview" as never)}: ${hit.name}`}><img src={hit.thumb_url || hit.preview_url} alt="" loading="lazy" /></Button>}</div>
    <BrowseCardFooter hit={hit} {...actions} />
  </li>;
}

function LLMCatalogCard({ hit, kind, saved, ...actions }: BrowseCardProps) {
  const tr = useT();
  const license = hit.license_name || hit.license;
  return <li className="engine-catalog-card engine-catalog-card-llm" aria-label={hit.name}>
    <div className="engine-catalog-card-main"><div className="engine-catalog-card-copy">
      <div className="engine-catalog-card-title"><span>{hit.name}</span><span className="engines-model-tag">{kind === "lora" ? "LoRA" : "GGUF"}</span></div>
      <div className="engine-catalog-card-tags"><span className="engines-model-tag">Hugging Face</span>
        {hit.bytes ? <span className="engines-model-tag">{formatBytes(hit.bytes)}</span> : null}
        {hit.context_length ? <span className="engines-model-tag">{tr("admin.catalog_context" as never)}: {hit.context_length.toLocaleString()}</span> : null}
        {license && <span className="engines-model-tag">{license}</span>}
        <CatalogRestrictionTags value={hit} />
      </div>
      <p className="muted engine-catalog-card-stats">
        {hit.downloads ? `${compactCount(hit.downloads)} ${tr("admin.engines_ingest_hit_downloads")}` : ""}
        {hit.likes ? ` · ${compactCount(hit.likes)} ${tr("admin.engines_ingest_hit_likes")}` : ""}
        {hit.updated_at ? ` · ${tr("admin.engines_ingest_hit_updated")} ${fmtDateTime(hit.updated_at)}` : ""}
      </p>
      <CatalogSavedState hit={hit} saved={saved} storage={actions.storage} storageState={actions.storageState} />
    </div></div>
    <BrowseCardFooter hit={hit} {...actions} />
  </li>;
}

function CatalogSavedState({ hit, saved, storage, storageState }: Pick<BrowseCardProps, "hit" | "saved" | "storage" | "storageState">) {
  const tr = useT();
  if (storageState === "checking") return <p className="muted engine-catalog-saved">{tr("admin.catalog_storage_checking" as never)}</p>;
  if (storageState === "failed") return <p className="muted engine-catalog-saved">{tr("admin.catalog_storage_unavailable" as never)}</p>;
  const sourceFiles = sourceFilesForHit(hit, storage);
  if (saved.length) return <p className="engine-catalog-saved">{tr("admin.catalog_saved_count" as never, { count: saved.length } as never)}</p>;
  if (sourceFiles.some((file) => file.state === "unknown")) return <p className="muted engine-catalog-saved">{tr("admin.catalog_saved_unknown" as never)}</p>;
  return <p className="muted engine-catalog-saved">{tr("admin.catalog_saved_none" as never)}</p>;
}

function BrowseCardFooter({ hit, readOnly, canAttach, canReplace, onOperation }: Omit<BrowseCardProps, "kind" | "saved">) {
  const tr = useT();
  return <footer className="engine-catalog-card-footer">
    {hit.url && <a href={hit.url} target="_blank" rel="noopener noreferrer">{tr("admin.catalog_source_page" as never)}</a>}
    {!readOnly && <span><Button variant="primary" small aria-label={`${tr("admin.catalog_add" as never)}: ${hit.name}`} onClick={() => onOperation("new")}>{tr("admin.catalog_add" as never)}</Button>
      {canAttach && <Button small aria-label={`${tr("admin.catalog_attach" as never)}: ${hit.name}`} onClick={() => onOperation("attach")}>{tr("admin.catalog_attach" as never)}</Button>}
      {canReplace && <Button small aria-label={`${tr("admin.catalog_replace" as never)}: ${hit.name}`} onClick={() => onOperation("replace")}>{tr("admin.catalog_replace" as never)}</Button>}</span>}
  </footer>;
}

const CATALOG_HARD_RESTRICTIONS = new Set(["gated_auto", "gated_manual", "paid", "early_access", "private", "generate_only", "unscanned", "pickle"]);

function CatalogRestrictionTags({ value }: { value: Pick<IngestHit, "restrictions" | "gated" | "login_required" | "commercial_use" | "gated_needs_acceptance" | "trained_words"> }) {
  const tr = useT();
  return <>
    {value.login_required === "yes" && <span className="engines-model-tag warn" title={tr("admin.engines_hit_login_note")}>{tr("admin.engines_hit_login_required")}</span>}
    {value.restrictions?.length ? value.restrictions.map((code) => <span key={code} className={`engines-model-tag${CATALOG_HARD_RESTRICTIONS.has(code) ? " warn" : ""}`}>{tMaybe(`admin.engines_limit_${code}`) ?? code}</span>)
      : value.gated && <span className="engines-model-tag warn">{tr("admin.engines_ingest_hit_gated")}</span>}
    {value.commercial_use === "no" && <span className="engines-model-tag warn">{tr("admin.engines_model_noncommercial")}</span>}
    {value.gated_needs_acceptance && <span className="engines-model-tag warn">{tr("admin.catalog_gate_accept" as never)}</span>}
    {!!value.trained_words?.length && <span className="engines-model-tag">{tr("admin.engines_hit_trigger")}: {value.trained_words.join(", ")}</span>}
  </>;
}

function NoEngineCatalog() {
  const tr = useT();
  const [role, setRole] = useState<"image" | "llm">("image");
  const [kind, setKind] = useState<ModelKind>("model");
  const [source, setSource] = useState<CatalogSource>("civitai");
  const [sort, setSort] = useState("newest");
  const [query, setQuery] = useState("");
  const [submittedQuery, setSubmittedQuery] = useState("");
  const [hits, setHits] = useState<IngestHit[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [preview, setPreview] = useState<IngestHit | null>(null);
  const requestSeq = useRef(0);
  const image = role === "image";

  const search = useCallback(async (more = false) => {
    const seq = ++requestSeq.current;
    setBusy(true); setErr("");
    try {
      const wireKind = image ? "checkpoint" : "gguf";
      const answer = await apiJSON(`api/admin/engines/search?kind=${encodeURIComponent(wireKind)}`, "POST", {
        q: more ? submittedQuery : query, source, sort, lora: kind === "lora", ...(more && cursor ? { cursor } : {}),
      });
      if (seq !== requestSeq.current) return;
      if (answer?.error) { setErr(errDetail(answer.error)); if (!more) setHits(null); return; }
      const page = answer as IngestSearchAnswer;
      const next = Array.isArray(page?.hits) ? page.hits : [];
      setHits((current) => more && current ? [...current, ...next] : next);
      if (!more) setSubmittedQuery(query);
      setCursor(page?.next_cursor || "");
    } finally { if (seq === requestSeq.current) setBusy(false); }
  }, [cursor, image, kind, query, sort, source, submittedQuery]);

  useEffect(() => { void search(false); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [image, kind, source, sort]);
  const switchRole = (next: "image" | "llm") => {
    setRole(next); setSource(next === "image" ? "civitai" : "hf"); setSort(next === "image" ? "newest" : "updated"); setHits(null); setCursor("");
  };
  const switchSource = (next: CatalogSource) => {
    setSource(next); setSort(next === "civitai" || next === "civitai-red" ? "newest" : "updated");
  };
  const sortOptions = source === "civitai" || source === "civitai-red" ? ["newest", "downloads", "trending", "likes"] : ["updated", "downloads", "trending", "likes"];
  const noop = () => {};

  return <section className="engine-catalog-browser engine-catalog-no-engine" aria-label={tr(image ? "admin.catalog_image_title" as never : "admin.catalog_llm_title" as never)}>
    <div className="engine-catalog-nav engine-catalog-no-engine-nav">
      <div className="engine-catalog-role-tabs" role="tablist" aria-label={tr("admin.catalog_role_label" as never)}>
        <Button variant="ghost" small role="tab" aria-selected={image} className={image ? "active" : ""} onClick={() => switchRole("image")}><Icon name="file-media" />{tr("admin.engines_role_image")}</Button>
        <Button variant="ghost" small role="tab" aria-selected={!image} className={!image ? "active" : ""} onClick={() => switchRole("llm")}><Icon name="comment" />{tr("admin.engines_role_llm")}</Button>
      </div>
      <span className="engines-model-tag lead">{tr("admin.catalog_view_search" as never)}</span>
    </div>
    <p className="admin-hint">{tr("admin.engines_browse_note")}</p>
    <div className="engine-catalog-toolbar">
      <span className="seg sm">{(["model", "lora"] as const).map((next) => <Button key={next} variant="ghost" small className={`seg-btn${kind === next ? " active" : ""}`} onClick={() => setKind(next)}>{tr(next === "lora" ? "admin.engines_tab_loras" : "admin.engines_tab_models")}</Button>)}</span>
      {image && <span className="seg sm">{(["civitai", "civitai-red", "hf"] as const).map((next) => <Button key={next} variant="ghost" small className={`seg-btn${source === next ? " active" : ""}`} onClick={() => switchSource(next)}>{tr((`admin.engines_ingest_source_${next}`) as never)}</Button>)}</span>}
      <form className="engine-catalog-search" onSubmit={(event) => { event.preventDefault(); void search(false); }}><input aria-label={tr("admin.engines_ingest_search")} value={query} onChange={(event) => setQuery(event.currentTarget.value)} /><Button type="submit" variant="primary" small disabled={busy}>{tr(query.trim() ? "admin.engines_ingest_search_go" : "admin.engines_ingest_browse_go")}</Button></form>
      <label className="engine-catalog-sort"><span>{tr("admin.catalog_sort" as never)}</span><select value={sort} onChange={(event) => setSort(event.currentTarget.value)}>{sortOptions.map((option) => <option key={option} value={option}>{tr((`admin.engines_ingest_sort_${option}`) as never)}</option>)}</select></label>
    </div>
    {(source === "civitai" || source === "civitai-red") && sort === "newest" && <p className="muted engine-catalog-sort-note">{tr("admin.catalog_civitai_newest_note" as never)}</p>}
    {err && <p className="form-err">{err}</p>}
    {hits?.length === 0 && <p className="muted engine-catalog-zero">{tr("admin.engines_ingest_search_none")}</p>}
    {!!hits?.length && <ul className="engine-catalog-grid">{hits.map((hit) => {
      const props: BrowseCardProps = { hit, kind, saved: [], storage: [], storageState: "ready", readOnly: true, canAttach: false, canReplace: false, onOperation: noop };
      return image ? <ImageCatalogCard key={`${hit.source}:${hit.model_ref || hit.ref}:${hit.ref}`} {...props} onPreview={() => setPreview(hit)} /> : <LLMCatalogCard key={`${hit.source}:${hit.model_ref || hit.ref}:${hit.ref}`} {...props} />;
    })}</ul>}
    {cursor && query === submittedQuery && <CatalogMore busy={busy} loading={busy} count={hits?.length || 0} onMore={() => void search(true)} />}
    {preview?.preview_url && <Modal title={preview.name} className="engine-catalog-lightbox" onClose={() => setPreview(null)}><img className="ui-modal-body" src={preview.preview_url} alt={preview.name} /></Modal>}
  </section>;
}

function RegisteredCatalog({ row, kind, onKind, isSuper, readOnly, onChanged }: CatalogProps & { isSuper: boolean }) {
  const tr = useT();
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState("name");
  const [storage, setStorage] = useState<EngineStorageFile[] | null>(null);
  const [jobs, setJobs] = useState<IngestJob[]>([]);
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const [edit, setEdit] = useState<EngineModel | null>(null);
  const [deleting, setDeleting] = useState<EngineModel | null>(null);
  const [purge, setPurge] = useState(false);
  const [operation, setOperation] = useState<{ act: EngineIngestAct; modelId?: string } | null>(null);
  const [prefill, setPrefill] = useState<ModelPrefill | null>(null);
  const [vramAsk, setVramAsk] = useState<{ model: EngineModel; patch: Record<string, unknown>; message?: string } | null>(null);
  const models = (row.model_rows || []).filter((model) => (model.kind === "lora") === (kind === "lora"));

  const loadAux = useCallback(async () => {
    const [stored, history] = await Promise.all([
      api(`api/admin/engines/${encodeURIComponent(row.key)}/storage`),
      api(`api/admin/engines/${encodeURIComponent(row.key)}/ingest`),
    ]);
    setStorage(stored?.error ? null : Array.isArray(stored?.files) ? stored.files : []);
    if (!history?.error) setJobs(Array.isArray(history?.jobs) ? history.jobs : []);
  }, [row.key]);
  useEffect(() => { void loadAux(); }, [loadAux]);
  const live = jobs.some((job) => job.state === "pending" || job.state === "running");
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => void loadAux(), 5000);
    return () => clearInterval(timer);
  }, [live, loadAux]);

  const visible = [...models].filter((model) => {
    const words = [model.id, model.description, model.base_model, model.license_name, model.license].filter(Boolean).join(" ").toLowerCase();
    return words.includes(query.trim().toLowerCase());
  }).sort((left, right) => {
    if (sort === "enabled") return Number(right.enabled) - Number(left.enabled) || left.id.localeCompare(right.id);
    if (sort === "default") return Number(!!(right.selected || right.default)) - Number(!!(left.selected || left.default)) || left.id.localeCompare(right.id);
    return left.id.localeCompare(right.id);
  });

  const callModel = async (model: EngineModel, method: string, body?: Record<string, unknown>, purgeBytes = false) => {
    setBusy(model.id); setErr("");
    try {
      const answer = await apiJSON(
        `api/admin/engines/${encodeURIComponent(row.key)}/models/${encodeURIComponent(model.id)}${purgeBytes ? "?purge=1" : ""}`,
        method,
        body,
      );
      if (answer?.error) {
        if (method === "PUT" && body && answer.error.code === "engine_vram_confirm") {
          setEdit(null);
          setVramAsk({ model, patch: body, message: answer.error.message });
        } else {
          setErr(errDetail(answer.error));
        }
        return false;
      }
      await onChanged();
      await loadAux();
      return true;
    } finally { setBusy(""); }
  };
  const guardedChange = async (model: EngineModel, patch: Record<string, unknown>) => {
    const loadsModel = !!(patch.enabled || patch.selected || patch.default);
    const cardMiB = row.class?.vram_mib || 0;
    if (loadsModel && model.kind !== "lora" && cardMiB > 0 && !!model.vram_need_mib && model.vram_need_mib > cardMiB) {
      setVramAsk({ model, patch });
      return;
    }
    await callModel(model, "PUT", patch);
  };
  const addModel = async (body: Record<string, unknown>) => {
    setBusy("+");
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/models`, "POST", body);
      if (answer?.error) { setErr(errDetail(answer.error)); return; }
      await onChanged();
      await loadAux();
    } finally { setBusy(""); }
  };
  const forgetJob = async (id: string) => {
    setBusy(`job:${id}`);
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/${encodeURIComponent(id)}`, "DELETE");
      if (answer?.error) { setErr(errDetail(answer.error)); return; }
      setJobs(Array.isArray(answer?.jobs) ? answer.jobs : []);
    } finally { setBusy(""); }
  };

  return <section className="engine-catalog-registered" aria-label={tr("admin.catalog_registered_title" as never)}>
    {!isSuper && <p className="admin-hint">{tr("admin.engines_tenant_scope")}</p>}
    <div className="engine-catalog-toolbar">
      <span className="seg sm">{(["model", "lora"] as const).map((next) => <Button key={next} variant="ghost" small
        className={"seg-btn" + (kind === next ? " active" : "")} onClick={() => onKind(next)}>{tr(next === "lora" ? "admin.engines_tab_loras" : "admin.engines_tab_models")}</Button>)}</span>
      <label className="engine-registered-search"><span>{tr("admin.catalog_registered_search" as never)}</span><input value={query} onChange={(event) => setQuery(event.currentTarget.value)} /></label>
      <label className="engine-catalog-sort"><span>{tr("admin.catalog_sort" as never)}</span><select value={sort} onChange={(event) => setSort(event.currentTarget.value)}>
        <option value="name">{tr("admin.catalog_sort_name" as never)}</option><option value="enabled">{tr("admin.catalog_sort_enabled" as never)}</option><option value="default">{tr("admin.catalog_sort_default" as never)}</option>
      </select></label>
      <IconButton icon="refresh" label={tr("admin.refresh")} onClick={() => void loadAux()} />
    </div>
    {err && <p className="form-err">{err}</p>}
    {!visible.length && <p className="muted">{tr(kind === "lora" ? "admin.engines_loras_empty" : "admin.engines_catalog_empty")}</p>}
    <ul className="engine-registered-grid">{visible.map((model) => {
      const status = registeredStorage(model, storage);
      const started = !!(model.selected || model.default);
      const pending = busy === model.id;
      return <li key={model.id} className="engine-registered-card" aria-label={model.id}>
        <header><strong className="mono">{model.id}</strong><span className={`engines-model-tag ${model.enabled ? "on" : "off"}`}>{tr(model.enabled ? "admin.engines_model_is_on" : "admin.engines_model_is_off")}</span>
          {started && <span className="engines-model-tag lead">{tr("admin.engines_model_started")}</span>}
          <span className={`engines-model-tag ${status.tone}`}>{tr((`admin.catalog_registered_${status.state}`) as never, { present: status.present, total: status.total } as never)}</span></header>
        {engineIsImage(row) ? <ImageRegisteredCardBody model={model} /> : <LLMRegisteredCardBody model={model} />}
        <RegisteredParts model={model} storage={storage} />
        <footer>
          {isSuper && !readOnly && <Button variant="ghost" small icon="edit" aria-label={`${tr("admin.catalog_edit" as never)}: ${model.id}`} onClick={() => setEdit(model)}>{tr("admin.catalog_edit" as never)}</Button>}
          {isSuper && !readOnly && <Button small aria-label={`${tr(model.enabled ? "admin.engines_model_disable" : "admin.engines_model_enable")}: ${model.id}`} disabled={pending} onClick={() => void guardedChange(model, { enabled: !model.enabled })}>{tr(model.enabled ? "admin.engines_model_disable" : "admin.engines_model_enable")}</Button>}
          {isSuper && !readOnly && model.kind !== "lora" && !started && <Button variant="primary" small aria-label={`${tr("admin.engines_model_select")}: ${model.id}`} disabled={pending} onClick={() => void guardedChange(model, engineIsImage(row) ? { selected: true } : { default: true })}>{tr("admin.engines_model_select")}</Button>}
          {!readOnly && <Button small aria-label={`${tr("admin.catalog_parts" as never)}: ${model.id}`} onClick={() => setOperation({ act: (row.file_flags || []).length ? "attach" : "replace", modelId: model.id })}>{tr("admin.catalog_parts" as never)}</Button>}
          {isSuper && !readOnly && <Button variant="danger" small aria-label={`${tr("admin.engines_model_forget")}: ${model.id}`} disabled={pending || started} onClick={() => { setDeleting(model); setPurge(false); }}>{tr("admin.engines_model_forget")}</Button>}
        </footer>
      </li>;
    })}</ul>
    {isSuper && !readOnly && <details className="engine-registered-tools"><summary>{tr("admin.catalog_manual_s3" as never)}</summary>
      <EngineModelAdd busy={busy === "+"} isImage={engineIsImage(row)} isLora={kind === "lora"}
        baseModels={row.base_models} fileFlags={row.file_flags} modelIds={(row.model_rows || []).map((model) => model.id)}
        prefill={prefill} onAdd={(body) => void addModel(body)} />
    </details>}
    <EngineIngestJobs jobs={jobs} busy={busy.replace(/^job:/, "")} readOnly={readOnly}
      onForget={(id) => void forgetJob(id)} onReuse={isSuper && !readOnly ? (job) => {
        onKind(job.kind === "lora" ? "lora" : "model");
        setPrefill({ id: job.model_id, s3Key: job.s3_key || "", flag: job.file_flag || "", source: job.source || "", usedBy: job.key_used_by || "" });
      } : undefined} />
    {operation && <CatalogOperation row={row} kind={kind} initialAct={operation.act} initialTarget={operation.modelId} storage={storage || []}
      onClose={() => setOperation(null)} onStarted={() => { setOperation(null); void loadAux(); onChanged(); }} />}
    {edit && <RegisteredEditDialog row={row} model={edit} error={err} onClose={() => setEdit(null)} onSave={async (body) => {
      if (await callModel(edit, "PUT", body)) setEdit(null);
    }} />}
    {deleting && <Modal title={`${tr("admin.engines_model_forget")} — ${deleting.id}`} className="engine-registered-confirm" onClose={() => setDeleting(null)} lockClose={busy === deleting.id}>
      <div className="ui-modal-body engine-operation-body"><p>{tr(purge ? "admin.engines_model_forget_purge_note" : "admin.engines_model_forget_note")}</p><label className="engine-operation-check"><input type="checkbox" checked={purge} onChange={(event) => setPurge(event.currentTarget.checked)} /><span>{tr("admin.engines_model_forget_purge")}</span></label>
        <footer className="engine-operation-footer"><Button variant="ghost" onClick={() => setDeleting(null)}>{tr("common.cancel")}</Button><Button variant="danger" onClick={async () => { if (await callModel(deleting, "DELETE", undefined, purge)) setDeleting(null); }}>{tr("admin.engines_model_forget")}</Button></footer></div>
    </Modal>}
    {vramAsk && <Modal title={`${tr("admin.engines_vram_confirm_go")} — ${vramAsk.model.id}`} className="engine-registered-confirm" onClose={() => setVramAsk(null)}>
      <div className="ui-modal-body engine-operation-body"><p className="form-err">{vramAsk.message || (tr("admin.engines_vram_confirm" as never) as string)
        .replace("{id}", vramAsk.model.id)
        .replace("{n}", String(vramAsk.model.vram_need_mib || 0))
        .replace("{m}", String(row.class?.vram_mib || 0))
        .replace("{src}", tr((`admin.engines_vram_src_${vramAsk.model.vram_need_source || "unknown"}`) as never) as string)}</p>
        <footer className="engine-operation-footer"><Button variant="ghost" onClick={() => setVramAsk(null)}>{tr("common.cancel")}</Button><Button variant="primary" onClick={async () => {
          const ask = vramAsk;
          setVramAsk(null);
          await callModel(ask.model, "PUT", { ...ask.patch, confirm_vram: true });
        }}>{tr("admin.engines_vram_confirm_go")}</Button></footer></div>
    </Modal>}
  </section>;
}

function ImageRegisteredCardBody({ model }: { model: EngineModel }) {
  const tr = useT();
  return <div className="engine-registered-body"><p>{model.description || tr("admin.catalog_no_description" as never)}</p><div className="engine-catalog-card-tags">
    <span className="engines-model-tag">{model.kind === "lora" ? "LoRA" : tr("admin.catalog_checkpoint" as never)}</span>
    {model.base_model && <span className="engines-model-tag">{tr("admin.catalog_family" as never)}: {model.base_model}</span>}
    {model.precision && <span className="engines-model-tag">{model.precision}</span>}
    {model.vram_need_mib && <span className="engines-model-tag">VRAM {model.vram_need_mib} MiB</span>}
    {(model.license_name || model.license) && <span className="engines-model-tag">{model.license_name || model.license}</span>}
    {model.vae_missing && <span className="engines-model-tag warn">VAE</span>}
    {model.commercial_use === "no" && <span className="engines-model-tag warn">{tr("admin.engines_model_noncommercial")}</span>}
  </div></div>;
}

function LLMRegisteredCardBody({ model }: { model: EngineModel }) {
  const tr = useT();
  const bytes = (model.file_rows || []).reduce((sum, file) => sum + (file.bytes || 0), 0);
  return <div className="engine-registered-body"><p>{model.description || tr("admin.catalog_no_description" as never)}</p><div className="engine-catalog-card-tags">
    <span className="engines-model-tag">{model.kind === "lora" ? "LoRA" : "GGUF"}</span>
    {bytes > 0 && <span className="engines-model-tag">{formatBytes(bytes)}</span>}
    {model.context_tokens && <span className="engines-model-tag">{tr("admin.catalog_context" as never)}: {model.context_tokens.toLocaleString()}</span>}
    {model.max_output_tokens && <span className="engines-model-tag">{tr("admin.catalog_output" as never)}: {model.max_output_tokens.toLocaleString()}</span>}
    {model.precision && <span className="engines-model-tag">{model.precision}</span>}
    {model.vram_need_mib && <span className="engines-model-tag">VRAM {model.vram_need_mib} MiB</span>}
    {(model.license_name || model.license) && <span className="engines-model-tag">{model.license_name || model.license}</span>}
  </div></div>;
}

type RegisteredStorage = {
  state: "present" | "partial" | "missing" | "unknown";
  tone: "on" | "warn" | "bad" | "";
  present: number;
  total: number;
};

function registeredStorage(model: EngineModel, storage: EngineStorageFile[] | null): RegisteredStorage {
  const keys = (model.file_rows || []).map((file) => file.s3Key).filter(Boolean);
  if (!keys.length || storage === null) return { state: "unknown", tone: "", present: 0, total: keys.length };
  const states = keys.map((key) => storage.find((file) => file.s3_key === key)?.state || "unknown");
  const present = states.filter((state) => state === "present").length;
  if (present === keys.length) return { state: "present", tone: "on", present, total: keys.length };
  if (states.every((state) => state === "missing")) return { state: "missing", tone: "bad", present, total: keys.length };
  if (states.every((state) => state !== "unknown") && present > 0) return { state: "partial", tone: "warn", present, total: keys.length };
  return { state: "unknown", tone: "", present, total: keys.length };
}

function RegisteredParts({ model, storage }: { model: EngineModel; storage: EngineStorageFile[] | null }) {
  const tr = useT();
  const rows = model.file_rows || [];
  if (!rows.length) return <p className="muted engine-registered-no-parts">{tr("admin.catalog_parts_unknown" as never)}</p>;
  return <ul className="engine-registered-parts" aria-label={`${tr("admin.catalog_parts" as never)}: ${model.id}`}>{rows.map((part) => {
    const known = storage?.find((candidate) => candidate.s3_key === part.s3Key);
    const state = known?.state || "unknown";
    return <li key={`${part.flag || "whole"}:${part.s3Key}`}><span className="mono">{part.flag || tr("admin.engines_model_add_part_whole")}</span><span className="mono engine-registered-key">{part.s3Key}</span>
      {part.bytes ? <span>{formatBytes(part.bytes)}</span> : null}
      <span className={`engines-model-tag ${state === "present" ? "on" : state === "missing" ? "bad" : ""}`}>{tr((`admin.catalog_file_${state}`) as never)}</span>
      {part.source_url ? <a href={part.source_url} target="_blank" rel="noopener noreferrer">{tr("admin.catalog_source_page" as never)}</a> : part.source ? <span className="muted mono">{part.source}</span> : null}</li>;
  })}</ul>;
}

function RegisteredEditDialog({ row, model, error, onClose, onSave }: {
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
    scheduler: model.params?.scheduler || "", clip_skip: String(model.params?.clip_skip || ""), weight: String(model.params?.weight || ""),
  });
  const [busy, setBusy] = useState(false);
  const baseChoices = image ? row.base_models || [] : lora ? (row.model_rows || []).filter((candidate) => candidate.kind !== "lora").map((candidate) => candidate.id) : [];
  const whole = (value: string) => /^\d+$/.test(value.trim()) ? Number(value.trim()) : null;
  const contextNumber = whole(context);
  const outputNumber = whole(output);
  const vramNumber = whole(vram);
  const invalidWindow = !image && !lora && (contextNumber === null || outputNumber === null || (!!contextNumber !== !!outputNumber));
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
    {image && <details className="engine-operation-advanced"><summary>{tr("admin.catalog_advanced" as never)}</summary><div className="engine-operation-grid">{(Object.keys(params) as (keyof EngineParams)[]).map((key) => <label key={key}><span>{tr((`admin.engines_params_${key}`) as never)}</span><input value={params[key]} onChange={(event) => setParams((current) => ({ ...current, [key]: event.currentTarget.value }))} /></label>)}</div></details>}
    {validation && <p className="form-err">{validation}</p>}
    {error && <p className="form-err">{error}</p>}
    <footer className="engine-operation-footer"><Button variant="ghost" onClick={onClose} disabled={busy}>{tr("common.cancel")}</Button><Button variant="primary" disabled={busy || !!validation} onClick={() => void save()}>{tr("common.save")}</Button></footer></div>
  </Modal>;
}

function CatalogOperation({ row, kind, hit, initialAct, initialSource, initialTarget, storage, onClose, onStarted }: {
  row: EngineRow; kind: ModelKind; hit?: IngestHit; initialAct: EngineIngestAct;
  initialSource?: CatalogSource;
  initialTarget?: string;
  storage: EngineStorageFile[]; onClose: () => void; onStarted: (job: IngestJob) => void;
}) {
  const tr = useT();
  const image = engineIsImage(row);
  const isLora = kind === "lora";
  const act = initialAct;
  const [sourceType, setSourceType] = useState<CatalogSource>(hit?.source === "civitai" || initialSource === "civitai" || initialSource === "civitai-red" ? "civitai" : "hf");
  const [manualRef, setManualRef] = useState(hit?.model_ref || hit?.ref || "");
  const [versions, setVersions] = useState<IngestVersion[]>([]);
  const [versionRef, setVersionRef] = useState(hit?.ref || "");
  const [files, setFiles] = useState<IngestCandidate[]>([]);
  const [file, setFile] = useState("");
  const [resolved, setResolved] = useState<ResolvedSource | null>(null);
  const [targetId, setTargetId] = useState(initialTarget || "");
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
  const [trainedWords, setTrainedWords] = useState((hit?.trained_words || []).join(", "));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const inspectSeq = useRef(0);
  const filesSeq = useRef(0);
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
  const civitaiPage = rawRef.match(/^https?:\/\/(?:[\w-]+\.)*civitai\.(?:com|red)\/models\/(\d+)/);
  const civitaiVersionParam = rawRef.match(/[?&]modelVersionId=(\d+)/);
  const civitaiLegacy = rawRef.match(/^civitai:(\d+)$/);
  const civitaiModelRef = hit?.model_ref || civitaiPage?.[1] || "";
  const pastedVersion = (sourceType === "civitai" ? civitaiVersionParam?.[1] || civitaiLegacy?.[1] : hfURL?.[2]) || "";
  const repo = sourceType === "civitai" ? civitaiModelRef || pastedVersion : hit?.model_ref || hfURL?.[1] || rawRef || hit?.ref || "";
  const pastedFile = sourceType === "hf" ? hfURL?.[3] || "" : "";
  const civitaiVersionURL = /^https?:\/\/(?:[\w-]+\.)*civitai\.(?:com|red)\//.test(rawRef) && !!civitaiVersionParam;
  const plainURL = /^https?:\/\//.test(rawRef) && !hfURL && !civitaiVersionURL;
  const resetInspection = () => {
    ++inspectSeq.current; ++filesSeq.current;
    setVersions([]); setVersionRef(""); setFiles([]); setFile(""); setResolved(null); setAccepted(false); setBusy(false); setErr("");
  };
  const changeManualRef = (value: string) => {
    setManualRef(value);
    if (/^civitai:\d+$/.test(value.trim()) || (/civitai\.(?:com|red)\//.test(value) && /[?&]modelVersionId=\d+/.test(value))) setSourceType("civitai");
    else if (/huggingface\.co\//.test(value)) setSourceType("hf");
    resetInspection();
  };

  const sourceBody = useCallback((fileName = file) => {
    if (plainURL) return { url: repo, sha256: fileName.trim() };
    if (sourceType === "civitai") return { civitai: { versionId: Number(versionRef || hit?.ref), file: fileName } };
    return { hf: { repo, revision: versionRef, file: fileName } };
  }, [file, hit?.ref, plainURL, repo, sourceType, versionRef]);

  const loadFiles = useCallback(async (selectedVersion: string, parentSeq?: number) => {
    const seq = ++filesSeq.current;
    setErr(""); setFiles([]); setFile(""); setResolved(null);
    const source = sourceType === "civitai"
      ? { civitai: { versionId: Number(selectedVersion), file: "" } }
      : { hf: { repo, revision: selectedVersion, file: "" } };
    const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/files`, "POST", { source });
    if (seq !== filesSeq.current || (parentSeq !== undefined && parentSeq !== inspectSeq.current)) return;
    if (answer?.error) { setErr(errDetail(answer.error)); return; }
    const offered = (Array.isArray(answer?.files) ? answer.files : []) as IngestCandidate[];
    setFiles(offered);
    if (offered.length === 1) setFile(offered[0].name);
  }, [repo, row.key, sourceType]);

  const inspect = useCallback(async () => {
    if (!repo) return;
    const seq = ++inspectSeq.current;
    ++filesSeq.current;
    setBusy(true); setErr("");
    try {
      if (plainURL) { setVersions([]); setFiles([]); return; }
      const requestedVersion = versionRef || pastedVersion;
      if (sourceType === "civitai" && !civitaiModelRef && pastedVersion) {
        setVersions([{ ref: pastedVersion, name: pastedVersion }]);
        setVersionRef(pastedVersion);
        await loadFiles(pastedVersion, seq);
        return;
      }
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/versions`, "POST", {
        source: sourceType, ref: hit?.ref || pastedVersion || repo, model_ref: sourceType === "civitai" ? civitaiModelRef : repo,
      });
      if (seq !== inspectSeq.current) return;
      if (answer?.error) { setErr(errDetail(answer.error)); return; }
      const offered = (Array.isArray(answer?.versions) ? answer.versions : []) as IngestVersion[];
      setVersions(offered);
      const first = offered.find((version) => version.ref === requestedVersion)?.ref || offered[0]?.ref || requestedVersion;
      setVersionRef(first);
      if (first) {
        await loadFiles(first, seq);
        if (seq === inspectSeq.current && pastedFile) setFile(pastedFile);
      }
    } finally { if (seq === inspectSeq.current) setBusy(false); }
  }, [civitaiModelRef, hit?.ref, loadFiles, pastedFile, pastedVersion, plainURL, repo, row.key, sourceType, versionRef]);

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
      const selectableBases = isLora && !image
        ? (row.model_rows || []).filter((model) => model.kind !== "lora").map((model) => model.id)
        : row.base_models || [];
      if (found.base_model_suggest && !baseModel && !(isLora && !image) && (!selectableBases.length || selectableBases.includes(found.base_model_suggest))) {
        setBaseModel(found.base_model_suggest);
      }
      if (found.context_length && !context) {
        setContext(String(found.context_length)); setOutput(String(Math.floor(found.context_length / 8)));
      }
      if (found.params_hint) setParams((current) => ({
        ...current,
        ...Object.fromEntries(Object.entries(found.params_hint || {}).map(([key, value]) => [key, String(value)])),
      }));
      if (found.trained_words?.length && !trainedWords.trim()) setTrainedWords(found.trained_words.join(", "));
      setWithVae(found.vae_bundled === "no" && !!found.family_vae && !found.family_vae.unreachable);
    });
    return () => { live = false; };
    // Existing typed settings deliberately outrank metadata suggestions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file, image, isLora, row.key, sourceBody]);

  const selectedFile = files.find((candidate) => candidate.name === file);
  const immutableReuse = exactReusableStorage(storage, resolved?.artifact_identity);
  const bytes = resolved?.bytes || selectedFile?.bytes || 0;
  const contextTokens = Number(context.trim().replace(/[_,]/g, "")) || 0;
  const weightsMiB = bytes ? Math.round(bytes / 1048576) : 0;
  const kvMiB = !image && !isLora && resolved?.kv_mib_per_1k_tokens && contextTokens
    ? Math.round((resolved.kv_mib_per_1k_tokens * contextTokens) / 1024)
    : 0;
  const needMiB = weightsMiB + kvMiB;
  const cardMiB = row.class?.vram_mib;
  const tooLarge = !!cardMiB && needMiB > cardMiB && !isLora;
  const baseOptions = isLora && !image
    ? (row.model_rows || []).filter((model) => model.kind !== "lora").map((model) => model.id)
    : row.base_models || [];
  const validBase = !baseOptions.length || (!!baseModel && baseOptions.includes(baseModel));
  const missing = (() => {
    if (!repo) return tr("admin.catalog_need_source" as never) as string;
    if (!plainURL && !versionRef) return tr("admin.catalog_need_version" as never) as string;
    if (!file) return tr(plainURL ? "admin.catalog_need_checksum" as never : "admin.catalog_need_file" as never) as string;
    if (!resolved) return tr("admin.catalog_resolving" as never) as string;
    if (resolved.can_ingest === false) return tr("admin.engines_wizard_cannot") as string;
    if (act !== "new" && !targetId) return tr("admin.catalog_need_target" as never) as string;
    if (act !== "new" && flags.length > 0 && !partChosen) return tr("admin.catalog_need_part" as never) as string;
    if (act === "new" && !id.trim()) return tr("admin.engines_wizard_need_id") as string;
    if (act === "new" && !validBase) return tr("admin.engines_wizard_need_family") as string;
    if (act === "new" && isLora && !image && !baseModel) return tr("admin.engines_wizard_need_family") as string;
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
        params: paramsBody, trained_words: isLora ? trainedWords.split(/[\n,]/).map((word) => word.trim()).filter(Boolean) : [],
        license_accepted: true, with_family_vae: withVae,
        ...(immutableReuse ? { reuse_s3_key: immutableReuse.s3_key } : {}),
      });
      if (answer?.error) { setErr(errDetail(answer.error)); return; }
      onStarted(answer as IngestJob);
    } finally { setBusy(false); }
  };

  const operationTitle = hit?.name
    ? `${tr("admin.catalog_operation_title" as never)} — ${hit.name}`
    : tr("admin.catalog_operation_title" as never);
  return <Modal title={operationTitle} className="engine-catalog-operation" onClose={onClose} lockClose={busy}>
    <div className="ui-modal-body engine-operation-body">
      {!hit && <div className="engine-operation-manual">
        {image && <select value={sourceType} onChange={(event) => { setSourceType(event.currentTarget.value as CatalogSource); resetInspection(); }}><option value="civitai">Civitai</option><option value="hf">Hugging Face / URL</option></select>}
        <input value={manualRef} onChange={(event) => changeManualRef(event.currentTarget.value)} placeholder="owner/repository or https://…" />
        <Button small onClick={() => void inspect()} disabled={busy || !manualRef.trim()}>{tr("admin.catalog_inspect" as never)}</Button>
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
        {act === "new" && (baseOptions.length > 0 || (isLora && !image)) && <label><span>{tr(isLora && !image ? "admin.engines_model_add_lora_base" : "admin.engines_model_add_family")}</span><select value={baseModel} onChange={(event) => setBaseModel(event.currentTarget.value)}>
          <option value="">{tr(isLora && !image ? "admin.engines_model_add_lora_base_pick" : "admin.engines_model_add_family_pick")}</option>{baseOptions.map((base) => <option key={base} value={base}>{base}</option>)}</select></label>}
      </div>
      {resolved && <div className="engine-operation-facts"><span>{resolved.bytes ? `${Math.round(resolved.bytes / 1048576)} MiB` : tr("admin.catalog_size_unknown" as never)}</span>
        {(resolved.license_name || resolved.license) && <span>{resolved.license_name || resolved.license}</span>}
        {resolved.restrictions?.map((code) => <span key={code} className={CATALOG_HARD_RESTRICTIONS.has(code) ? "warn" : ""}>{tMaybe(`admin.engines_limit_${code}`) ?? code}</span>)}
        {resolved.gated && !resolved.restrictions?.length && <span className="warn">{tr("admin.engines_ingest_hit_gated")}</span>}
        {immutableReuse && <span className="ok">{tr("admin.catalog_reuse_present" as never)}</span>}</div>}
      {resolved?.commercial_use === "no" && <p className="form-err">{tr("admin.engines_ingest_noncommercial")}</p>}
      {resolved?.login_required && <p className="form-err">{tr("admin.engines_ingest_civitai_login")}</p>}
      {resolved?.gated && resolved.can_ingest === false && !resolved.login_required && <p className="form-err">{tr("admin.engines_ingest_gated_no_token")}</p>}
      {resolved?.gated_needs_acceptance && <p className="muted">{tr("admin.engines_ingest_gated_accept_first")}</p>}
      {resolved?.vae_bundled === "no" && !resolved.family_vae && <p className="form-err">{tr("admin.engines_wizard_vae_none")}</p>}
      {resolved?.vae_bundled === "no" && resolved.family_vae && <label className="engine-operation-check"><input type="checkbox" checked={withVae} disabled={!!resolved.family_vae.unreachable} onChange={(event) => setWithVae(event.currentTarget.checked)} /><span>{(tr(resolved.family_vae.staged ? "admin.engines_wizard_vae_staged" : "admin.engines_wizard_vae_take") as string)
        .replace("{f}", `${resolved.family_vae.repo}/${resolved.family_vae.file}`)
        .replace("{n}", resolved.family_vae.bytes ? formatBytes(resolved.family_vae.bytes) : "?")
        .replace("{l}", resolved.family_vae.license || "?")}{resolved.family_vae.unreachable ? ` — ${tr("admin.catalog_vae_unreachable" as never)}` : ""}</span></label>}
      {resolved?.params_hint && resolved.params_hint_quote && <div className="engine-operation-hint"><p className="muted">{tr("admin.engines_params_hint_found")} <q>{resolved.params_hint_quote}</q></p><Button variant="ghost" small onClick={() => setParams((current) => ({ ...current, ...Object.fromEntries(Object.entries(resolved.params_hint || {}).map(([key, value]) => [key, String(value)])) }))}>{tr("admin.engines_params_hint_apply")}</Button></div>}
      {needMiB > 0 && <p className={`engine-operation-fit ${tooLarge ? "form-err" : "muted"}`}>{(tr("admin.engines_ingest_fit_weights") as string).replace("{n}", String(weightsMiB))}{!image && !isLora ? ` · ${kvMiB ? (tr("admin.engines_ingest_fit_kv") as string).replace("{n}", String(kvMiB)).replace("{c}", String(contextTokens)) : tr("admin.engines_ingest_fit_kv_unread")}` : ""}{cardMiB ? ` · ${(tr("admin.engines_ingest_fit_card") as string).replace("{n}", String(needMiB)).replace("{c}", String(cardMiB))}` : ""}</p>}
      {tooLarge && <label className="engine-operation-check warn"><input type="checkbox" checked={confirmVram} onChange={(event) => setConfirmVram(event.currentTarget.checked)} /><span>{tr(!image && !isLora ? "admin.catalog_vram_warning_llm" as never : "admin.catalog_vram_warning" as never, { need: needMiB, card: cardMiB } as never)}</span></label>}
      <label className="engine-operation-check"><input type="checkbox" checked={accepted} onChange={(event) => setAccepted(event.currentTarget.checked)} /><span>{tr("admin.engines_ingest_accept")}</span></label>
      <details className="engine-operation-advanced"><summary>{tr("admin.catalog_advanced" as never)}</summary><div className="engine-operation-grid">
        <label><span>{tr("admin.engines_model_add_desc")}</span><input value={description} onChange={(event) => setDescription(event.currentTarget.value)} /></label>
        {!image && !isLora && <label><span>{tr("admin.engines_model_add_ctx")}</span><input value={context} onChange={(event) => setContext(event.currentTarget.value)} inputMode="numeric" /></label>}
        {!image && !isLora && <label><span>{tr("admin.engines_model_add_out")}</span><input value={output} onChange={(event) => setOutput(event.currentTarget.value)} inputMode="numeric" /></label>}
        {isLora && <label><span>{tr("admin.engines_model_trigger")}</span><input value={trainedWords} onChange={(event) => setTrainedWords(event.currentTarget.value)} /></label>}
        {image && (Object.keys(params) as (keyof EngineParams)[]).map((key) => <label key={key}><span>{tr((`admin.engines_params_${key}`) as never)}</span><input value={params[key]} onChange={(event) => setParams((current) => ({ ...current, [key]: event.currentTarget.value }))} /></label>)}
      </div></details>
      {err && <p className="form-err">{err}</p>}
      <footer className="engine-operation-footer"><span className="muted">{missing}</span><Button variant="ghost" onClick={onClose} disabled={busy}>{tr("common.cancel")}</Button><Button variant="primary" onClick={() => void start()} disabled={busy || !!missing}>{tr("admin.engines_ingest_go")}</Button></footer>
    </div>
  </Modal>;
}

function CatalogJobs({ engineKey, started, onCompleted }: { engineKey: string; started?: IngestJob; onCompleted: () => void }) {
  const tr = useT();
  const [jobs, setJobs] = useState<IngestJob[]>([]);
  const hadLive = useRef(false);
  const load = useCallback(async () => {
    const answer = await api(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest`);
    if (!answer?.error) setJobs(Array.isArray(answer?.jobs) ? answer.jobs : []);
  }, [engineKey]);
  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    if (!started?.id) return;
    setJobs((current) => [started, ...current.filter((job) => job.id !== started.id)]);
    void load();
  }, [load, started]);
  const live = jobs.some((job) => job.state === "pending" || job.state === "running");
  useEffect(() => {
    if (hadLive.current && !live) onCompleted();
    hadLive.current = live;
  }, [live, onCompleted]);
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
  return sourceFilesForHit(hit, files).filter((file) => file.state === "present");
}

function sourceFilesForHit(hit: IngestHit, files: EngineStorageFile[]): EngineStorageFile[] {
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
export function exactReusableStorage(files: EngineStorageFile[], identity?: string): EngineStorageFile | undefined {
  if (!identity) return undefined;
  return files.find((file) => file.state === "present" && file.reusable === true && file.artifact_identity === identity);
}

function compactCount(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  if (value >= 1_000) return `${Math.round(value / 1_000)}k`;
  return String(Math.round(value));
}

function formatBytes(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB`;
  if (value >= 1_000_000) return `${Math.round(value / 1_000_000)} MB`;
  if (value >= 1_000) return `${Math.round(value / 1_000)} kB`;
  return `${value} B`;
}
