import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { Button, IconButton } from "../../../ui/Button.tsx";
import { Modal } from "../../../ui/Modal.tsx";
import { ViewHead } from "../../../ui/ViewHead.tsx";
import { type ModelKind } from "./adminEngineModels.tsx";
import {
  engineIsImage,
  engineIsRemote,
  engineTitle,
  useEngineRows,
  type CompleteAnswer,
  type EngineApiError,
  type EngineModel,
  type EngineObjectRow,
  type EngineParams,
  type EngineRow,
  type IngestCandidate,
  type IngestHit,
  type IngestJob,
  type IngestPlan,
  type IngestSearchAnswer,
  type IngestSearchRequest,
  type IngestVersion,
  type ResolvedSource,
} from "./engineTypes.ts";

type CatalogView = "search" | "registered";
type CatalogSource = "hf" | "civitai" | "civitai-red";

/** Full-pane catalogue (ADR 0085 decision 8). Two tabs and no wizard: 探す opens one plan card
 * per press, 登録済み holds the rows and, under them, the bucket itself. */
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

/** The ledger route, shared by both tabs: the bucket is the only thing that knows what this
 * deployment holds, so "have I got this already" and "what is in there" read the same answer. */
function objectsPath(engineKey: string): string {
  return `api/admin/engines/${encodeURIComponent(engineKey)}/objects`;
}

function CatalogBrowser({ row, kind, onKind, readOnly, onChanged, image }: CatalogProps & { image: boolean }) {
  const tr = useT();
  const [source, setSource] = useState<CatalogSource>(image ? "civitai" : "hf");
  const [sort, setSort] = useState(image ? "newest" : "updated");
  // The family filter, as one of the ENGINE's own base models — never an upstream name. Empty is
  // every family, which is what a browse was before this existed.
  const [family, setFamily] = useState("");
  const [query, setQuery] = useState("");
  const [submittedQuery, setSubmittedQuery] = useState("");
  const [hits, setHits] = useState<IngestHit[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [busyMore, setBusyMore] = useState(false);
  const [err, setErr] = useState("");
  const [plan, setPlan] = useState<{ hit?: IngestHit; source?: CatalogSource } | null>(null);
  const [preview, setPreview] = useState<IngestHit | null>(null);
  const [objects, setObjects] = useState<EngineObjectRow[] | null>(null);
  const [ledgerState, setLedgerState] = useState<"checking" | "ready" | "failed">("checking");
  const [started, setStarted] = useState("");
  const requestSeq = useRef(0);

  useEffect(() => {
    setSource(image ? "civitai" : "hf");
    setSort(image ? "newest" : "updated");
    setHits(null);
    setCursor("");
  }, [image, row.key]);

  const loadObjects = useCallback(async () => {
    setLedgerState("checking");
    try {
      const answer = await api(objectsPath(row.key));
      if (answer?.error) { setObjects(null); setLedgerState("failed"); return; }
      setObjects(Array.isArray(answer?.objects) ? answer.objects : []);
      setLedgerState("ready");
    } catch { setObjects(null); setLedgerState("failed"); }
  }, [row.key]);
  useEffect(() => { setObjects(null); void loadObjects(); }, [loadObjects]);

  const search = useCallback(async (more = false) => {
    const seq = ++requestSeq.current;
    const body: IngestSearchRequest = {
      q: more ? submittedQuery : query, source, sort, lora: kind === "lora",
      ...(family ? { family } : {}), ...(more && cursor ? { cursor } : {}),
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
  }, [cursor, family, kind, query, row.key, sort, source, submittedQuery]);

  useEffect(() => { void search(false); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [row.key, kind, source, sort, family]);
  const switchSource = (next: CatalogSource) => {
    setSource(next); setSort(next === "civitai" || next === "civitai-red" ? "newest" : "updated"); setHits(null); setCursor("");
  };
  const sortOptions = source === "civitai" || source === "civitai-red" ? ["newest", "downloads", "trending", "likes"] : ["updated", "downloads", "trending", "likes"];

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
        {image && !!(row.base_models || []).length && <label className="engine-catalog-family">
          <span>{tr("admin.catalog_family" as never)}</span>
          {/* The engine's OWN vocabulary, served by the CP — not a list this file keeps. A
              deployment that grows a family offers it here without a Console change. */}
          <select value={family} onChange={(event) => { setFamily(event.currentTarget.value); setHits(null); setCursor(""); }}>
            <option value="">{tr("admin.catalog_family_all" as never)}</option>
            {(row.base_models || []).map((option) => <option key={option} value={option}>{option}</option>)}
          </select>
        </label>}
        <label className="engine-catalog-sort"><span>{tr("admin.catalog_sort" as never)}</span>
          <select value={sort} onChange={(event) => setSort(event.currentTarget.value)}>
            {sortOptions.map((option) => <option key={option} value={option}>{tr((`admin.engines_ingest_sort_${option}`) as never)}</option>)}
          </select>
        </label>
        {!readOnly && <Button variant="ghost" small icon="link" onClick={() => setPlan({ source })}>{tr("admin.catalog_manual" as never)}</Button>}
      </div>
      {(source === "civitai" || source === "civitai-red") && sort === "newest" && <p className="muted engine-catalog-sort-note">{tr("admin.catalog_civitai_newest_note" as never)}</p>}
      {ledgerState === "checking" && <p className="muted engine-catalog-storage-note">{tr("admin.catalog_storage_checking" as never)}</p>}
      {ledgerState === "failed" && <p className="form-err engine-catalog-storage-note">{tr("admin.catalog_storage_unavailable" as never)}</p>}
      {started && <p className="muted engine-catalog-started">{started}</p>}
      {err && <p className="form-err">{err}</p>}
      {busy && !busyMore && <p className="muted engine-catalog-loading"><Icon name="loading" spin /> {tr("admin.engines_ingest_searching" as never)}</p>}
      {!busy && hits?.length === 0 && <p className="muted engine-catalog-zero">{tr("admin.engines_ingest_search_none")}</p>}
      {!!hits?.length && <ul className="engine-catalog-grid">{hits.map((hit) => {
        const props: BrowseCardProps = {
          hit, kind, saved: savedObjectsForHit(hit, objects || []), objects: objects || [], ledgerState, readOnly,
          onTakeIn: () => setPlan({ hit }),
        };
        return image
          ? <ImageCatalogCard key={`${hit.source}:${hit.model_ref || hit.ref}:${hit.ref}`} {...props} onPreview={() => setPreview(hit)} />
          : <LLMCatalogCard key={`${hit.source}:${hit.model_ref || hit.ref}:${hit.ref}`} {...props} />;
      })}</ul>}
      {cursor && query === submittedQuery && <CatalogMore busy={busy} loading={busy && busyMore}
        count={hits?.length || 0} onMore={() => void search(true)} />}
      {plan && <IngestPlanDialog row={row} kind={kind} hit={plan.hit} initialSource={plan.source}
        onClose={() => setPlan(null)}
        onStarted={() => {
          setPlan(null);
          setStarted(tr("admin.catalog_started" as never) as string);
          void loadObjects();
          onChanged();
        }} />}
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
  saved: EngineObjectRow[];
  objects: EngineObjectRow[];
  ledgerState: "checking" | "ready" | "failed";
  readOnly: boolean;
  onTakeIn: () => void;
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
      <CatalogSavedState hit={hit} saved={saved} objects={actions.objects} ledgerState={actions.ledgerState} />
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
      <CatalogSavedState hit={hit} saved={saved} objects={actions.objects} ledgerState={actions.ledgerState} />
    </div></div>
    <BrowseCardFooter hit={hit} {...actions} />
  </li>;
}

function CatalogSavedState({ hit, saved, objects, ledgerState }: Pick<BrowseCardProps, "hit" | "saved" | "objects" | "ledgerState">) {
  const tr = useT();
  if (ledgerState === "checking") return <p className="muted engine-catalog-saved">{tr("admin.catalog_storage_checking" as never)}</p>;
  if (ledgerState === "failed") return <p className="muted engine-catalog-saved">{tr("admin.catalog_storage_unavailable" as never)}</p>;
  const sourceObjects = sourceObjectsForHit(hit, objects);
  if (saved.length) return <p className="engine-catalog-saved">{tr("admin.catalog_saved_count" as never, { count: saved.length } as never)}</p>;
  if (sourceObjects.some((object) => object.state === "missing")) return <p className="muted engine-catalog-saved">{tr("admin.catalog_saved_unknown" as never)}</p>;
  return <p className="muted engine-catalog-saved">{tr("admin.catalog_saved_none" as never)}</p>;
}

/** One button (ADR 0085 decision 3). `attach` and `replace` are gone from this screen: a part is
 * never the subject, and swapping one is an act on the checkpoint that reads it. */
function BrowseCardFooter({ hit, readOnly, onTakeIn }: Pick<BrowseCardProps, "hit" | "readOnly" | "onTakeIn">) {
  const tr = useT();
  return <footer className="engine-catalog-card-footer">
    {hit.url && <a href={hit.url} target="_blank" rel="noopener noreferrer">{tr("admin.catalog_source_page" as never)}</a>}
    {!readOnly && <span><Button variant="primary" small aria-label={`${tr("admin.catalog_add" as never)}: ${hit.name}`} onClick={onTakeIn}>{tr("admin.catalog_add" as never)}</Button></span>}
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
      const props: BrowseCardProps = { hit, kind, saved: [], objects: [], ledgerState: "ready", readOnly: true, onTakeIn: noop };
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
  const [objects, setObjects] = useState<EngineObjectRow[] | null>(null);
  /** Told apart from "not read yet": an empty prefix and a bucket nobody could list are
   *  different answers, and drawing the second as the first says this deployment holds nothing. */
  const [ledgerFailed, setLedgerFailed] = useState(false);
  const [checkedAt, setCheckedAt] = useState("");
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState<EngineApiError | null>(null);
  const [note, setNote] = useState("");
  const [edit, setEdit] = useState<EngineModel | null>(null);
  const [deleting, setDeleting] = useState<EngineModel | null>(null);
  const [purge, setPurge] = useState(false);
  const [completing, setCompleting] = useState<{ model: EngineModel; answer: CompleteAnswer } | null>(null);
  const [deletingObject, setDeletingObject] = useState<EngineObjectRow | null>(null);
  const [vramAsk, setVramAsk] = useState<{ model: EngineModel; patch: Record<string, unknown>; message?: string } | null>(null);
  const models = (row.model_rows || []).filter((model) => (model.kind === "lora") === (kind === "lora"));

  const loadObjects = useCallback(async () => {
    const answer = await api(objectsPath(row.key));
    if (answer?.error) { setObjects(null); setLedgerFailed(true); return; }
    setLedgerFailed(false);
    setObjects(Array.isArray(answer?.objects) ? answer.objects : []);
    setCheckedAt(answer?.checked_at || "");
  }, [row.key]);
  useEffect(() => { void loadObjects(); }, [loadObjects]);
  // A download runs for minutes and has no list of its own any more (ADR 0085 decision 6): it is
  // its destination object's `uploading` state, so the ledger is what polls.
  const live = (objects || []).some((object) => object.state === "uploading");
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => void loadObjects(), 5000);
    return () => clearInterval(timer);
  }, [live, loadObjects]);

  const visible = [...models].filter((model) => {
    const words = [model.id, model.description, model.base_model, model.license_name, model.license].filter(Boolean).join(" ").toLowerCase();
    return words.includes(query.trim().toLowerCase());
  }).sort((left, right) => {
    if (sort === "enabled") return Number(right.enabled) - Number(left.enabled) || left.id.localeCompare(right.id);
    if (sort === "default") return Number(!!(right.selected || right.default)) - Number(!!(left.selected || left.default)) || left.id.localeCompare(right.id);
    return left.id.localeCompare(right.id);
  });

  const callModel = async (model: EngineModel, method: string, body?: Record<string, unknown>, purgeBytes = false) => {
    setBusy(model.id); setErr(null);
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
          setErr(answer.error as EngineApiError);
        }
        return false;
      }
      await onChanged();
      await loadObjects();
      return true;
    } finally { setBusy(""); }
  };

  /** 揃える (ADR 0085 decision 3). The row is the subject and the CP does the reading: what the
   * family needs, what the row has, what the bucket holds. `{check: true}` is the same answer
   * without the act, which is what decides whether anybody is asked anything at all. */
  const completeModel = useCallback(async (id: string, body: Record<string, unknown> = {}): Promise<CompleteAnswer | undefined> => {
    setBusy(id); setErr(null);
    try {
      const answer = await apiJSON(
        `api/admin/engines/${encodeURIComponent(row.key)}/models/${encodeURIComponent(id)}/complete`, "POST", body,
      );
      if (answer?.error) { setErr(answer.error as EngineApiError); return undefined; }
      return answer as CompleteAnswer;
    } finally { setBusy(""); }
  }, [row.key]);

  const completeNote = (answer: CompleteAnswer) => tr((answer.action === "none" ? "admin.catalog_complete_none"
    : answer.action === "attached" ? "admin.catalog_complete_attached"
      // A move is not a download and must not be reported as one: nothing crosses the internet,
      // so an operator told to watch for a transfer would be watching for something that never
      // appears.
      : answer.action === "moving" ? "admin.catalog_complete_moving"
        : answer.action === "unknown" ? "admin.catalog_complete_unknown"
          : "admin.catalog_complete_started") as never) as string;

  const runComplete = async (id: string, body: Record<string, unknown> = {}) => {
    const answer = await completeModel(id, body);
    if (!answer) return;
    setNote(completeNote(answer));
    await onChanged();
    await loadObjects();
  };

  const align = async (model: EngineModel) => {
    setNote("");
    const check = await completeModel(model.id, { check: true });
    if (!check) return;
    // Only two things are worth a dialog: a role with several candidates, and bytes somebody has
    // to agree to pay for. Everything else just happens.
    if (check.action === "choose" || (check.bytes_to_download || 0) > 0) { setCompleting({ model, answer: check }); return; }
    await runComplete(model.id);
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

  /** The one object-side act, and it is still an act on a model: an object in the bucket that no
   * row declares becomes a row, and the encoders and VAE the ledger already holds are attached by
   * the same press (ADR 0085 decision 3). */
  const registerObject = async (key: string) => {
    if (!key) return;
    setBusy(`object:${key}`); setErr(null); setNote("");
    try {
      const answer = await apiJSON(objectsPath(row.key) + "/register", "POST", { key });
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
      const registered = answer as { model_id?: string; complete?: CompleteAnswer };
      const head = (tr("admin.catalog_ledger_registered" as never) as string).replace("{id}", registered.model_id || "");
      setNote(registered.complete ? `${head} ${completeNote(registered.complete)}` : head);
      await onChanged();
      await loadObjects();
    } finally { setBusy(""); }
  };

  const deleteObject = async (key: string) => {
    setBusy(`object:${key}`); setErr(null); setNote("");
    try {
      const answer = await apiJSON(objectsPath(row.key), "DELETE", { key });
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
      setDeletingObject(null);
      await loadObjects();
    } finally { setBusy(""); }
  };

  const dismissJob = async (id: string) => {
    setBusy(`job:${id}`); setErr(null);
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/${encodeURIComponent(id)}`, "DELETE");
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
      await loadObjects();
    } finally { setBusy(""); }
  };

  /** What the CP said to press (ADR 0085 decision 5). The refusal names a holder and a next act,
   * and this is where "the key is already recorded" turns into a button that clears it. */
  const runNext = async (error: EngineApiError) => {
    const next = error.next;
    if (!next) return;
    const target = next.target || error.holder?.id || "";
    setErr(null);
    switch (next.act) {
      case "register": return registerObject(next.target || error.holder?.key || "");
      case "complete": return runComplete(target);
      case "replace": return runComplete(target, { replace: true });
      case "forget_row": {
        const model = models.find((candidate) => candidate.id === target);
        if (model) await callModel(model, "DELETE");
        return;
      }
      case "dismiss_job": return dismissJob(target || error.holder?.id || "");
      case "wait": { await onChanged(); await loadObjects(); return; }
    }
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
      <IconButton icon="refresh" label={tr("admin.refresh")} onClick={() => void loadObjects()} />
    </div>
    <EngineRefusal error={err} busy={!!busy} onNext={() => void runNext(err as EngineApiError)} />
    {note && <p className="muted engine-registered-note">{note}</p>}
    {!visible.length && <p className="muted">{tr(kind === "lora" ? "admin.engines_loras_empty" : "admin.engines_catalog_empty")}</p>}
    <ul className="engine-registered-grid">{visible.map((model) => {
      const status = registeredPresence(model, objects);
      const started = !!(model.selected || model.default);
      const pending = busy === model.id;
      return <li key={model.id} className="engine-registered-card" aria-label={model.id}>
        <header><strong className="mono">{model.id}</strong><span className={`engines-model-tag ${model.enabled ? "on" : "off"}`}>{tr(model.enabled ? "admin.engines_model_is_on" : "admin.engines_model_is_off")}</span>
          {started && <span className="engines-model-tag lead">{tr("admin.engines_model_started")}</span>}
          <span className={`engines-model-tag ${status.tone}`}>{tr((`admin.catalog_registered_${status.state}`) as never, { present: status.present, total: status.total } as never)}</span>
          {!!model.files_missing?.length && <span className="engines-model-tag warn">{(tr("admin.engines_model_files_missing_tag" as never) as string).replace("{f}", model.files_missing.join(" "))}</span>}</header>
        {engineIsImage(row) ? <ImageRegisteredCardBody model={model} /> : <LLMRegisteredCardBody model={model} />}
        <RegisteredParts model={model} objects={objects} />
        <footer>
          {isSuper && !readOnly && <Button variant="ghost" small icon="edit" aria-label={`${tr("admin.catalog_edit" as never)}: ${model.id}`} onClick={() => setEdit(model)}>{tr("admin.catalog_edit" as never)}</Button>}
          {isSuper && !readOnly && <Button small aria-label={`${tr(model.enabled ? "admin.engines_model_disable" : "admin.engines_model_enable")}: ${model.id}`} disabled={pending} onClick={() => void guardedChange(model, { enabled: !model.enabled })}>{tr(model.enabled ? "admin.engines_model_disable" : "admin.engines_model_enable")}</Button>}
          {isSuper && !readOnly && model.kind !== "lora" && !started && <Button variant="primary" small aria-label={`${tr("admin.engines_model_select")}: ${model.id}`} disabled={pending} onClick={() => void guardedChange(model, engineIsImage(row) ? { selected: true } : { default: true })}>{tr("admin.engines_model_select")}</Button>}
          {/* 揃える is on every row, not only a marked one: what it does is read off the ledger at
              the press, and a row that has everything answers "nothing to do" in one call. */}
          {!readOnly && <Button variant={model.files_missing?.length || model.vae_missing ? "primary" : undefined} small
            aria-label={`${tr("admin.catalog_complete" as never)}: ${model.id}`} disabled={pending}
            onClick={() => void align(model)}>{tr(pending ? "admin.catalog_complete_busy" as never : "admin.catalog_complete" as never)}</Button>}
          {isSuper && !readOnly && <Button variant="danger" small aria-label={`${tr("admin.engines_model_forget")}: ${model.id}`} disabled={pending || started} onClick={() => { setDeleting(model); setPurge(false); }}>{tr("admin.engines_model_forget")}</Button>}
        </footer>
      </li>;
    })}</ul>
    <EngineLedger objects={objects} failed={ledgerFailed} checkedAt={checkedAt} busy={busy} readOnly={readOnly}
      onRegister={(object) => void registerObject(object.key)}
      onDelete={(object) => setDeletingObject(object)}
      onDismiss={(id) => void dismissJob(id)} />
    {completing && <CompleteDialog row={row} model={completing.model} answer={completing.answer}
      onClose={() => setCompleting(null)}
      onRun={async (body) => { setCompleting(null); await runComplete(completing.model.id, body); }} />}
    {edit && <RegisteredEditDialog row={row} model={edit} error={err ? errDetail(err) : ""} onClose={() => setEdit(null)} onSave={async (body) => {
      if (await callModel(edit, "PUT", body)) setEdit(null);
    }} />}
    {deleting && <Modal title={`${tr("admin.engines_model_forget")} — ${deleting.id}`} className="engine-registered-confirm" onClose={() => setDeleting(null)} lockClose={busy === deleting.id}>
      <div className="ui-modal-body engine-operation-body"><p>{tr(purge ? "admin.engines_model_forget_purge_note" : "admin.engines_model_forget_note")}</p><label className="engine-operation-check"><input type="checkbox" checked={purge} onChange={(event) => setPurge(event.currentTarget.checked)} /><span>{tr("admin.engines_model_forget_purge")}</span></label>
        <footer className="engine-operation-footer"><Button variant="ghost" onClick={() => setDeleting(null)}>{tr("common.cancel")}</Button><Button variant="danger" onClick={async () => { if (await callModel(deleting, "DELETE", undefined, purge)) setDeleting(null); }}>{tr("admin.engines_model_forget")}</Button></footer></div>
    </Modal>}
    {deletingObject && <Modal title={`${tr("admin.catalog_ledger_delete" as never)} — ${deletingObject.key}`} className="engine-ledger-confirm" onClose={() => setDeletingObject(null)} lockClose={busy === `object:${deletingObject.key}`}>
      <div className="ui-modal-body engine-operation-body"><p>{tr("admin.catalog_ledger_delete_note" as never)}</p><p className="mono">{deletingObject.key}</p>
        <footer className="engine-operation-footer"><Button variant="ghost" onClick={() => setDeletingObject(null)}>{tr("common.cancel")}</Button>
          <Button variant="danger" onClick={() => void deleteObject(deletingObject.key)}>{tr("admin.catalog_ledger_delete" as never)}</Button></footer></div>
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

/** A refusal, with who is holding the thing and the one button that clears it. Drawn even when
 * the CP sent neither — an error line without a next act is still the error line. */
function EngineRefusal({ error, busy, onNext }: { error: EngineApiError | null; busy: boolean; onNext: () => void }) {
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

/** バケツ — the bucket, listed (ADR 0085 decision 2 and 7).
 *
 * 🔴 Orphans and misplaced objects sort first because they are the only rows here anybody has to
 * act on: everything else is provenance for a row that already works. A part gets no button of
 * its own — it is attached, and moved, by the 揃える of whichever checkpoint reads it. */
function EngineLedger({ objects, failed, checkedAt, busy, readOnly, onRegister, onDelete, onDismiss }: {
  objects: EngineObjectRow[] | null;
  failed: boolean;
  checkedAt: string;
  busy: string;
  readOnly: boolean;
  onRegister: (object: EngineObjectRow) => void;
  onDelete: (object: EngineObjectRow) => void;
  onDismiss: (id: string) => void;
}) {
  const tr = useT();
  const sorted = [...(objects || [])].sort((left, right) => ledgerRank(left) - ledgerRank(right) || left.key.localeCompare(right.key));
  return <section className="engine-ledger" aria-label={tr("admin.catalog_ledger_title" as never)}>
    <header className="engine-ledger-head">
      <strong>{tr("admin.catalog_ledger_title" as never)}</strong>
      {checkedAt && <span className="muted">{(tr("admin.catalog_ledger_checked" as never) as string).replace("{t}", fmtDateTime(checkedAt))}</span>}
    </header>
    <p className="admin-hint">{tr("admin.catalog_ledger_note" as never)}</p>
    {objects === null && <p className={failed ? "form-err" : "muted"}>{tr((failed ? "admin.catalog_ledger_unavailable" : "admin.catalog_storage_checking") as never)}</p>}
    {objects !== null && !objects.length && <p className="muted">{tr("admin.catalog_ledger_empty" as never)}</p>}
    {!!sorted.length && <ul className="engine-ledger-list">{sorted.map((object) => {
      const orphan = !(object.declared_by || []).length;
      const pending = busy === `object:${object.key}` || (!!object.job && busy === `job:${object.job.id}`);
      return <li key={object.key} className={`engine-ledger-row${orphan ? " orphan" : ""}`} aria-label={object.key}>
        <span className="mono engine-ledger-key">{object.key}</span>
        <span className="engine-ledger-tags">
          <span className="engines-model-tag">{object.role_dir}</span>
          {object.placement === "misplaced" && <span className="engines-model-tag warn">{tr("admin.catalog_ledger_misplaced" as never)}</span>}
          <span className={`engines-model-tag ${object.state === "present" ? "on" : object.state === "failed" || object.state === "missing" ? "bad" : "lead"}`}>
            {tr((`admin.catalog_ledger_state_${object.state}`) as never)}
          </span>
          {!!object.bytes && <span className="engines-model-tag">{formatBytes(object.bytes)}</span>}
          {object.license && <span className="engines-model-tag">{object.license}</span>}
        </span>
        <span className="muted engine-ledger-declared">{orphan
          ? tr("admin.catalog_ledger_orphan" as never)
          : (tr("admin.catalog_ledger_declared" as never) as string).replace("{m}", (object.declared_by || [])
            .map((holder) => holder.flag ? `${holder.model_id} (${holder.flag})` : holder.model_id).join(", "))}</span>
        {object.source && <span className="muted mono engine-ledger-source">{object.source}</span>}
        {object.job?.message && <span className="form-err engine-ledger-message">{object.job.message}</span>}
        {!readOnly && <span className="engine-ledger-acts">
          {object.state === "failed" && object.job
            ? <Button small variant="danger" disabled={pending} aria-label={`${tr("admin.catalog_ledger_delete" as never)}: ${object.key}`}
              onClick={() => onDismiss(object.job!.id)}>{tr("admin.catalog_ledger_delete" as never)}</Button>
            : <>
              {orphan && object.state === "present" && isMainObject(object) && <Button small variant="primary" disabled={pending}
                aria-label={`${tr("admin.catalog_ledger_register" as never)}: ${object.key}`}
                onClick={() => onRegister(object)}>{tr("admin.catalog_ledger_register" as never)}</Button>}
              {orphan && object.state === "present" && <Button small variant="danger" disabled={pending}
                aria-label={`${tr("admin.catalog_ledger_delete" as never)}: ${object.key}`}
                onClick={() => onDelete(object)}>{tr("admin.catalog_ledger_delete" as never)}</Button>}
            </>}
        </span>}
      </li>;
    })}</ul>}
  </section>;
}

/** What has to be looked at first: a failure, then anything nobody declares or that no loader can
 * see, then work in flight, then a row pointing at nothing. */
function ledgerRank(object: EngineObjectRow): number {
  if (object.state === "failed") return 0;
  if (object.placement === "misplaced" || !(object.declared_by || []).length) return 1;
  if (object.state === "uploading") return 2;
  if (object.state === "missing") return 3;
  return 4;
}

/** A main file — the thing a person means by "a model" — rather than a part. `register` is
 * offered on these only; a misplaced one still names its role directory in the key, which is how
 * `image/checkpoints/split_files/diffusion_models/x.safetensors` is recognised. */
function isMainObject(object: EngineObjectRow): boolean {
  if (object.role_dir === "checkpoints" || object.role_dir === "diffusion_models") return true;
  return /(^|\/)(checkpoints|diffusion_models)\//.test(object.key);
}

/** The plan card (ADR 0085 decision 4). One press, and the CP decided what that press does.
 *
 * 🔴 No role selector, no key field, no parts checkbox, no attach/replace: every one of those was
 * a question a person who does not know what a text encoder is cannot answer, and getting one
 * wrong produced a row that looked complete and would not generate. What is left is the version
 * and the file — which repository and which of its files — and the licence. */
function IngestPlanDialog({ row, kind, hit, initialSource, onClose, onStarted }: {
  row: EngineRow; kind: ModelKind; hit?: IngestHit;
  initialSource?: CatalogSource;
  onClose: () => void; onStarted: (job: IngestJob) => void;
}) {
  const tr = useT();
  const image = engineIsImage(row);
  const isLora = kind === "lora";
  const [sourceType, setSourceType] = useState<CatalogSource>(hit?.source === "civitai" || initialSource === "civitai" || initialSource === "civitai-red" ? "civitai" : "hf");
  const [manualRef, setManualRef] = useState(hit?.model_ref || hit?.ref || "");
  const [versions, setVersions] = useState<IngestVersion[]>([]);
  const [versionRef, setVersionRef] = useState(hit?.ref || "");
  const [files, setFiles] = useState<IngestCandidate[]>([]);
  const [file, setFile] = useState("");
  const [resolved, setResolved] = useState<ResolvedSource | null>(null);
  const [plan, setPlan] = useState<IngestPlan | null>(null);
  const [replanned, setReplanned] = useState(false);
  const [id, setId] = useState("");
  const [idEdited, setIdEdited] = useState(false);
  const [description, setDescription] = useState("");
  const [baseModel, setBaseModel] = useState("");
  const [context, setContext] = useState("");
  const [output, setOutput] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [trainedWords, setTrainedWords] = useState((hit?.trained_words || []).join(", "));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<EngineApiError | null>(null);
  const inspectSeq = useRef(0);
  const filesSeq = useRef(0);
  const [params, setParams] = useState<Record<keyof EngineParams, string>>({
    steps: "", cfg: "", sampler: "", scheduler: "", clip_skip: "", weight: "",
  });
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
    setVersions([]); setVersionRef(""); setFiles([]); setFile(""); setResolved(null); setPlan(null);
    setAccepted(false); setBusy(false); setErr(null);
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
    setErr(null); setFiles([]); setFile(""); setResolved(null); setPlan(null);
    const source = sourceType === "civitai"
      ? { civitai: { versionId: Number(selectedVersion), file: "" } }
      : { hf: { repo, revision: selectedVersion, file: "" } };
    const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/files`, "POST", { source });
    if (seq !== filesSeq.current || (parentSeq !== undefined && parentSeq !== inspectSeq.current)) return;
    if (answer?.error) { setErr(answer.error as EngineApiError); return; }
    const offered = (Array.isArray(answer?.files) ? answer.files : []) as IngestCandidate[];
    setFiles(offered);
    if (offered.length === 1) setFile(offered[0].name);
  }, [repo, row.key, sourceType]);

  const inspect = useCallback(async () => {
    if (!repo) return;
    const seq = ++inspectSeq.current;
    ++filesSeq.current;
    setBusy(true); setErr(null);
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
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
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
    if (!file) return;
    let live = true;
    setResolved(null); setPlan(null); setAccepted(false); setReplanned(false);
    apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/resolve`, "POST", {
      source: sourceBody(file), kind: isLora ? "lora" : image ? "checkpoint" : "gguf",
    }).then((answer) => {
      if (!live) return;
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
      const found = answer as ResolvedSource;
      setResolved(found);
      if (found.plan) {
        setPlan(found.plan);
        if (!idEdited) setId(found.plan.id || "");
        if (found.plan.base_model) setBaseModel(found.plan.base_model);
      }
      if (found.context_length && !context) {
        setContext(String(found.context_length)); setOutput(String(Math.floor(found.context_length / 8)));
      }
      if (found.params_hint) setParams((current) => ({
        ...current,
        ...Object.fromEntries(Object.entries(found.params_hint || {}).map(([key, value]) => [key, String(value)])),
      }));
      if (found.trained_words?.length && !trainedWords.trim()) setTrainedWords(found.trained_words.join(", "));
    });
    return () => { live = false; };
    // Existing typed settings deliberately outrank metadata suggestions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file, image, isLora, row.key, sourceBody]);

  // The family is asked ONLY when the CP could not read one, and only from the candidates it
  // knows. An LLM LoRA is pinned to a registered model rather than to a family name.
  const familyOptions = isLora && !image
    ? (row.model_rows || []).filter((model) => model.kind !== "lora").map((model) => model.id)
    : plan?.base_model_candidates || [];
  const needsFamily = !plan?.base_model && familyOptions.length > 0;
  const bytes = plan?.bytes_to_download ?? resolved?.bytes ?? 0;
  const contextTokens = Number(context.trim().replace(/[_,]/g, "")) || 0;
  const weightsMiB = bytes ? Math.round(bytes / 1048576) : 0;
  const kvMiB = !image && !isLora && resolved?.kv_mib_per_1k_tokens && contextTokens
    ? Math.round((resolved.kv_mib_per_1k_tokens * contextTokens) / 1024)
    : 0;
  const needMiB = weightsMiB + kvMiB;
  const cardMiB = row.class?.vram_mib;
  const missing = (() => {
    if (!repo) return tr("admin.catalog_need_source" as never) as string;
    if (!plainURL && !versionRef) return tr("admin.catalog_need_version" as never) as string;
    if (!file) return tr(plainURL ? "admin.catalog_need_checksum" as never : "admin.catalog_need_file" as never) as string;
    if (!plan) return tr("admin.catalog_plan_building" as never) as string;
    if (resolved?.can_ingest === false) return tr("admin.engines_wizard_cannot") as string;
    if (!id.trim()) return tr("admin.engines_wizard_need_id") as string;
    if (needsFamily && !baseModel) return tr("admin.engines_wizard_need_family") as string;
    if (!accepted) return tr("admin.catalog_need_license" as never) as string;
    return "";
  })();

  const start = async () => {
    if (missing || !plan) return;
    setBusy(true); setErr(null);
    const n = (value: string) => { const parsed = Number(value.trim().replace(/[_,]/g, "")); return Number.isFinite(parsed) && parsed > 0 ? parsed : 0; };
    const paramsBody = Object.fromEntries(Object.entries(params).flatMap(([key, value]) => value.trim()
      ? [[key, key === "sampler" || key === "scheduler" ? value.trim() : n(value)]] : []));
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest`, "POST", {
        // 🔴 The source and the CP's own plan, and nothing else about the destination. The key,
        // the role, the parts and the reuse decision are all inside `plan_token`: three parties
        // deciding one key is what produced the 400 `s3Key must be empty or identical`.
        source: sourceBody(file), plan_token: plan.plan_token,
        id: id.trim(), ...(baseModel ? { base_model: baseModel } : {}),
        description: description.trim(),
        ...(!image && !isLora ? { context_tokens: n(context), max_output_tokens: n(output) } : {}),
        ...(image ? { params: paramsBody } : {}),
        ...(isLora ? { trained_words: trainedWords.split(/[\n,]/).map((word) => word.trim()).filter(Boolean) } : {}),
        license_accepted: true,
      });
      if (answer?.error) {
        const error = answer.error as EngineApiError;
        // The plan is a quote and the press is the purchase. When anything material moved, the
        // CP answers the NEW plan and the card is redrawn from it — including the licence tick,
        // because what was accepted is not what would now be taken in.
        if (error.code === "engine_plan_stale" && error.plan) {
          setPlan(error.plan); setReplanned(true); setAccepted(false);
          if (!idEdited) setId(error.plan.id || "");
          return;
        }
        setErr(error);
        return;
      }
      onStarted(answer as IngestJob);
    } finally { setBusy(false); }
  };

  const planTitle = hit?.name
    ? `${tr("admin.catalog_plan_title" as never)} — ${hit.name}`
    : tr("admin.catalog_plan_title" as never);
  return <Modal title={planTitle} className="engine-catalog-operation engine-catalog-plan" onClose={onClose} lockClose={busy}>
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
        {needsFamily && <label><span>{tr(isLora && !image ? "admin.engines_model_add_lora_base" : "admin.engines_model_add_family")}</span><select value={baseModel} onChange={(event) => setBaseModel(event.currentTarget.value)}>
          <option value="">{tr(isLora && !image ? "admin.engines_model_add_lora_base_pick" : "admin.engines_model_add_family_pick")}</option>{familyOptions.map((base) => <option key={base} value={base}>{base}</option>)}</select></label>}
      </div>
      {file && !plan && !err && <p className="muted engine-plan-building"><Icon name="loading" spin /> {tr("admin.catalog_plan_building" as never)}</p>}
      {replanned && <p className="admin-hint engine-plan-stale">{tr("admin.catalog_plan_stale" as never)}</p>}
      {plan && <div className="engine-plan">
        <ul className="engine-plan-files" aria-label={tr("admin.catalog_plan_files" as never)}>{plan.files.map((planned) => <li key={`${planned.flag || "whole"}:${planned.name}`}>
          <span className="mono engine-plan-role">{planned.flag || tr("admin.catalog_plan_whole" as never)}</span>
          <span className="mono engine-plan-name">{planned.name}</span>
          {/* A move and a reuse cost nothing, and saying so with a size beside them is what makes
              the total untrustworthy: those bytes are already paid for. */}
          <span className={`engine-plan-cost${planned.action === "download" ? "" : " free"}`}>{planned.action === "download"
            ? (planned.bytes ? `${Math.round(planned.bytes / 1048576)} MiB` : tr("admin.catalog_size_unknown" as never))
            : tr((`admin.catalog_plan_action_${planned.action}`) as never)}</span>
        </li>)}</ul>
        <p className="engine-plan-total">{plan.bytes_to_download
          ? (tr("admin.catalog_plan_total" as never) as string).replace("{n}", formatBytes(plan.bytes_to_download))
          : tr("admin.catalog_plan_total_none" as never)}</p>
        {(plan.warnings || []).map((warning) => <p key={warning} className="admin-hint engine-plan-warning">{warning}</p>)}
      </div>}
      {resolved && <div className="engine-operation-facts">
        {(resolved.license_name || resolved.license) && <span>{resolved.license_name || resolved.license}</span>}
        {/* The "no" verdict is deliberately NOT a chip here: it is the red sentence below, and
            saying it twice in two wordings reads as two different restrictions. */}
        {resolved.commercial_use && resolved.commercial_use !== "no" && <span>{tr((`admin.catalog_commercial_${resolved.commercial_use}`) as never)}</span>}
        {resolved.restrictions?.map((code) => <span key={code} className={CATALOG_HARD_RESTRICTIONS.has(code) ? "warn" : ""}>{tMaybe(`admin.engines_limit_${code}`) ?? code}</span>)}
        {resolved.gated && !resolved.restrictions?.length && <span className="warn">{tr("admin.engines_ingest_hit_gated")}</span>}</div>}
      {resolved?.commercial_use === "no" && <p className="form-err">{tr("admin.engines_ingest_noncommercial")}</p>}
      {/* 🔴 The wall is a REFUSAL only while nobody can answer it. With an account registered the
          download is attempted as that account, and the CP cannot say in advance whether it
          satisfies this uploader — the same position `gated_needs_acceptance` is in, so it reads
          as the same kind of warning rather than a red dead end. */}
      {resolved?.login_required && !resolved.civitai_needs_account && <p className="form-err">{tr("admin.engines_ingest_civitai_login")}</p>}
      {resolved?.civitai_needs_account && <p className="muted">{tr("admin.engines_ingest_civitai_account_first" as never)}</p>}
      {resolved?.gated && resolved.can_ingest === false && !resolved.login_required && <p className="form-err">{tr("admin.engines_ingest_gated_no_token")}</p>}
      {resolved?.gated_needs_acceptance && <p className="muted">{tr("admin.engines_ingest_gated_accept_first")}</p>}
      {needMiB > 0 && <p className={`engine-operation-fit ${cardMiB && needMiB > cardMiB && !isLora ? "form-err" : "muted"}`}>{(tr("admin.engines_ingest_fit_weights") as string).replace("{n}", String(weightsMiB))}{!image && !isLora ? ` · ${kvMiB ? (tr("admin.engines_ingest_fit_kv") as string).replace("{n}", String(kvMiB)).replace("{c}", String(contextTokens)) : tr("admin.engines_ingest_fit_kv_unread")}` : ""}{cardMiB ? ` · ${(tr("admin.engines_ingest_fit_card") as string).replace("{n}", String(needMiB)).replace("{c}", String(cardMiB))}` : ""}</p>}
      <label className="engine-operation-check"><input type="checkbox" checked={accepted} onChange={(event) => setAccepted(event.currentTarget.checked)} /><span>{tr("admin.engines_ingest_accept")}</span></label>
      <details className="engine-operation-advanced"><summary>{tr("admin.catalog_advanced" as never)}</summary><div className="engine-operation-grid">
        <label><span>{tr("admin.engines_model_add_id")}</span><input value={id} onChange={(event) => { setIdEdited(true); setId(event.currentTarget.value); }} /></label>
        <label><span>{tr("admin.engines_model_add_desc")}</span><input value={description} onChange={(event) => setDescription(event.currentTarget.value)} /></label>
        {!image && !isLora && <label><span>{tr("admin.engines_model_add_ctx")}</span><input value={context} onChange={(event) => setContext(event.currentTarget.value)} inputMode="numeric" /></label>}
        {!image && !isLora && <label><span>{tr("admin.engines_model_add_out")}</span><input value={output} onChange={(event) => setOutput(event.currentTarget.value)} inputMode="numeric" /></label>}
        {isLora && <label><span>{tr("admin.engines_model_trigger")}</span><input value={trainedWords} onChange={(event) => setTrainedWords(event.currentTarget.value)} /></label>}
        {image && (Object.keys(params) as (keyof EngineParams)[]).map((key) => <label key={key}><span>{tr((`admin.engines_params_${key}`) as never)}</span><input value={params[key]} onChange={(event) => setParams((current) => ({ ...current, [key]: event.currentTarget.value }))} /></label>)}
      </div></details>
      <EngineRefusal error={err} busy={busy} onNext={() => { setErr(null); void inspect(); }} />
      <footer className="engine-operation-footer"><span className="muted">{missing}</span><Button variant="ghost" onClick={onClose} disabled={busy}>{tr("common.cancel")}</Button><Button variant="primary" onClick={() => void start()} disabled={busy || !!missing}>{tr("admin.engines_ingest_go")}</Button></footer>
    </div>
  </Modal>;
}

/** The dialog behind 揃える (complete), opened only when there is something to choose or to pay
 * for (ADR 0085 decision 3). This is the ONE place a person picks a part, and they pick it FOR a
 * checkpoint:
 * the frame is the role the workflow reads, and the candidates are what the ledger holds. */
function CompleteDialog({ row, model, answer, onClose, onRun }: {
  row: EngineRow;
  model: EngineModel;
  answer: CompleteAnswer;
  onClose: () => void;
  onRun: (body: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const [choices, setChoices] = useState<Record<string, string>>({});
  const [accepted, setAccepted] = useState(false);
  const files = answer.files || [];
  const download = answer.bytes_to_download || 0;
  const undecided = files.some((file) => file.action === "choose" && !choices[file.flag]);
  // Choosing something else for a slot that is already filled IS the swap (today's `replace`).
  const replace = files.some((file) => !!file.key && !!choices[file.flag] && choices[file.flag] !== file.key);
  const missing = undecided ? tr("admin.catalog_complete_pick" as never) as string
    : download > 0 && !accepted ? tr("admin.catalog_need_license" as never) as string : "";

  return <Modal title={`${tr("admin.catalog_complete" as never)} — ${model.id}`} className="engine-complete" onClose={onClose}>
    <div className="ui-modal-body engine-operation-body">
      <p className="admin-hint">{tr("admin.catalog_complete_note" as never)}</p>
      <ul className="engine-complete-files">{files.map((file) => <li key={file.flag} aria-label={file.flag}>
        <span className="mono engine-plan-role">{file.flag || tr("admin.catalog_plan_whole" as never)}</span>
        <span className="engine-complete-action">{tr((`admin.catalog_complete_file_${file.action}`) as never)}</span>
        {file.key && <span className="mono engine-complete-key">{file.key}</span>}
        {!!file.bytes && file.action === "download" && <span>{formatBytes(file.bytes)}</span>}
        {!!file.candidates?.length && <select aria-label={`${tr("admin.catalog_complete_pick" as never)}: ${file.flag}`}
          value={choices[file.flag] ?? (file.action === "choose" ? "" : file.key || "")}
          onChange={(event) => setChoices((current) => ({ ...current, [file.flag]: event.currentTarget.value }))}>
          {file.action === "choose" && <option value="">{tr("admin.catalog_complete_pick" as never)}</option>}
          {file.key && file.action !== "choose" && <option value={file.key}>{tr("admin.catalog_complete_keep" as never)}</option>}
          {(file.candidates || []).filter((candidate) => candidate.key !== file.key).map((candidate) => <option key={candidate.key} value={candidate.key}>
            {candidate.key}{candidate.bytes ? ` (${formatBytes(candidate.bytes)})` : ""}
          </option>)}
        </select>}
      </li>)}</ul>
      {download > 0 && <>
        <p className="engine-plan-total">{(tr("admin.catalog_plan_total" as never) as string).replace("{n}", formatBytes(download))}</p>
        <label className="engine-operation-check"><input type="checkbox" checked={accepted} onChange={(event) => setAccepted(event.currentTarget.checked)} /><span>{tr("admin.engines_ingest_accept")}</span></label>
      </>}
      {engineIsImage(row) && !!model.base_model && <p className="muted">{tr("admin.catalog_family" as never)}: {model.base_model}</p>}
      <footer className="engine-operation-footer"><span className="muted">{missing}</span>
        <Button variant="ghost" onClick={onClose}>{tr("common.cancel")}</Button>
        <Button variant="primary" disabled={!!missing} onClick={() => onRun({
          ...(Object.keys(choices).length ? { choices } : {}),
          ...(replace ? { replace: true } : {}),
          ...(download > 0 ? { license_accepted: true } : {}),
        })}>{tr("admin.catalog_complete" as never)}</Button></footer>
    </div>
  </Modal>;
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

type RegisteredPresence = {
  state: "present" | "partial" | "missing" | "unknown";
  tone: "on" | "warn" | "bad" | "";
  present: number;
  total: number;
};

/** The row's badge, read off the LEDGER rather than off a per-row existence check. An object the
 * ledger does not list is `missing` — listing is what the bucket answers, so a key nobody sees is
 * a key that is not there. */
function registeredPresence(model: EngineModel, objects: EngineObjectRow[] | null): RegisteredPresence {
  const keys = (model.file_rows || []).map((file) => file.s3Key).filter(Boolean);
  if (!keys.length || objects === null) return { state: "unknown", tone: "", present: 0, total: keys.length };
  const present = keys.filter((key) => objects.find((object) => object.key === key)?.state === "present").length;
  if (present === keys.length) return { state: "present", tone: "on", present, total: keys.length };
  if (present === 0) return { state: "missing", tone: "bad", present, total: keys.length };
  return { state: "partial", tone: "warn", present, total: keys.length };
}

function RegisteredParts({ model, objects }: { model: EngineModel; objects: EngineObjectRow[] | null }) {
  const tr = useT();
  const rows = model.file_rows || [];
  if (!rows.length) return <p className="muted engine-registered-no-parts">{tr("admin.catalog_parts_unknown" as never)}</p>;
  return <ul className="engine-registered-parts" aria-label={`${tr("admin.catalog_files" as never)}: ${model.id}`}>{rows.map((part) => {
    const known = objects?.find((candidate) => candidate.key === part.s3Key);
    const state = objects === null ? "unknown" : known?.state === "present" ? "present" : known ? known.state : "missing";
    return <li key={`${part.flag || "whole"}:${part.s3Key}`}><span className="mono">{part.flag || tr("admin.engines_model_add_part_whole")}</span><span className="mono engine-registered-key">{part.s3Key}</span>
      {part.bytes ? <span>{formatBytes(part.bytes)}</span> : null}
      <span className={`engines-model-tag ${state === "present" ? "on" : state === "missing" || state === "failed" ? "bad" : ""}`}>{tr((`admin.catalog_file_${state}`) as never)}</span>
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

/** Count concrete source objects, never repositories. A `missing` entry stays distinct from an
 * absent one: the ledger says a row points at nothing, which is not "nobody took this in". */
export function savedObjectsForHit(hit: IngestHit, objects: EngineObjectRow[]): EngineObjectRow[] {
  return sourceObjectsForHit(hit, objects).filter((object) => object.state === "present");
}

function sourceObjectsForHit(hit: IngestHit, objects: EngineObjectRow[]): EngineObjectRow[] {
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
