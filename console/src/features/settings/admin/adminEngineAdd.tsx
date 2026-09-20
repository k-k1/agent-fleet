import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { Button, IconButton } from "../../../ui/Button.tsx";
import { Modal } from "../../../ui/Modal.tsx";
import { ViewHead } from "../../../ui/ViewHead.tsx";
import { useScrollMemory } from "../../viewer/parts/useScrollMemory.ts";
import { type ModelKind } from "./adminEngineModels.tsx";
import { EngineDiscoverPanel, engineCanDiscover } from "./adminEngineDiscover.tsx";
import { setEngineAddView } from "./openEngineAdd.ts";
import {
  catalogKey,
  loadObjects as loadObjectsMemory,
  loadRegistered,
  loadSearch,
  loadShell,
  saveObjects as saveObjectsMemory,
  saveRegistered,
  saveSearch,
  saveShell,
} from "./catalogMemory.ts";
import { groupIsRepo, groupRegistered, registeredNeedsMeta, registeredTitle } from "./registeredGroups.ts";
import { modelFit, windowThatFits, windowWhenUnsized } from "./engineFit.ts";
import { familyVramMeasurement } from "./engineFamilyVram.ts";
import { CivitaiVersionLadder, FitTag, RepoQuantLadder } from "./adminEngineRepo.tsx";
import {
  engineIsImage,
  engineIsRemote,
  engineTitle,
  useEngineRows,
  type CatalogSource,
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

/** Full-pane catalogue (ADR 0085 decision 8). Two tabs and no wizard: 探す opens one plan card
 * per press, 登録済み holds the rows and, under them, the bucket itself. */
export function EngineAddView({ engineKey, lora, initialView = "search", paneId, headerActions }: {
  engineKey: string;
  lora: boolean;
  initialView?: CatalogView;
  /** Which pane this is, so a return lands on the face, engine and kind it was left on
   *  (catalogMemory). Absent outside a pane — the compatibility door in the settings panel — and
   *  then every mount shares one memory, which is what `clearCatalogMemory` is for in tests. */
  paneId?: string;
  headerActions?: ReactNode;
}) {
  const tr = useT();
  const { rows, isSuper, sources, err, load } = useEngineRows();
  // Read ONCE, at mount: after that this component owns the values and writes them back below.
  const [shell] = useState(() => loadShell(paneId));
  const [selectedKey, setSelectedKey] = useState(shell?.engineKey || engineKey);
  const [view, setView] = useState<CatalogView>(shell?.view || initialView);
  const [kind, setKind] = useState<ModelKind>(shell?.kind || (lora ? "lora" : "model"));
  const row = (rows || []).find((candidate) => candidate.key === selectedKey) || (rows || [])[0];

  useEffect(() => {
    if (rows?.length && !rows.some((candidate) => candidate.key === selectedKey)) setSelectedKey(rows[0].key);
  }, [rows, selectedKey]);

  useEffect(() => { saveShell(paneId, { engineKey: selectedKey, view, kind }); }, [paneId, selectedKey, view, kind]);

  // The open FACE, into the pane itself, so a browser reload comes back to it (the rest of the
  // memory is module scope and does not survive one). Only the face — see setEngineAddView.
  useEffect(() => {
    if (!paneId || view === initialView) return;
    setEngineAddView(paneId, engineKey, lora, view);
  }, [engineKey, initialView, lora, paneId, view]);

  return (
    <div className="engines-add-pane engine-catalog-pane admin-stage">
      <ViewHead actions={headerActions}>
        <span className="view-title"><Icon name="download" /> {tr("admin.catalog_title" as never)}</span>
      </ViewHead>
      {err && <p className="form-err pad">{err}</p>}
      {rows !== null && rows.length === 0 && (
        <NoEngineCatalog sources={sources} />
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
        {/* 🔴 Keyed by the engine and the kind, which is what makes each of them a separate memory:
            a face that changes either is a different list of a different thing, so it unmounts —
            saving what it held — and the next one mounts and restores its own. Without the key,
            React reuses the instance and an effect has to tear the state down by hand, which is the
            shape that kept throwing the page away. */}
        {view === "search" ? (
          engineIsImage(row)
            ? <ImageCatalog key={`${row.key}:${kind}`} row={row} kind={kind} onKind={setKind} paneId={paneId} sources={sources} readOnly={engineIsRemote(row)} onChanged={load} />
            : <LLMCatalog key={`${row.key}:${kind}`} row={row} kind={kind} onKind={setKind} paneId={paneId} sources={sources} readOnly={engineIsRemote(row)} onChanged={load} />
        ) : (
          <RegisteredCatalog key={`${row.key}:${kind}`} row={row} kind={kind} onKind={setKind} paneId={paneId} isSuper={isSuper}
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
  /** Which pane this face belongs to — half of its memory's key (catalogMemory). */
  paneId?: string;
  readOnly: boolean;
  onChanged: () => void;
};

/** The search half of the pane also needs the search sources this DEPLOYMENT offers, as the
 *  engine list answered them. The registered half never searches, so it does not take them. */
type BrowseProps = CatalogProps & { sources: CatalogSource[] };

export function ImageCatalog(props: BrowseProps) { return <CatalogBrowser {...props} image />; }
export function LLMCatalog(props: BrowseProps) { return <CatalogBrowser {...props} image={false} />; }

/** The ledger route, shared by both tabs: the bucket is the only thing that knows what this
 * deployment holds, so "have I got this already" and "what is in there" read the same answer. */
function objectsPath(engineKey: string): string {
  return `api/admin/engines/${encodeURIComponent(engineKey)}/objects`;
}

/** The question a page of hits answers. Two lists are the same list when this is equal — which is
 *  what decides both where the scroll position is filed and whether a search starts at the top. */
function conditionOf(query: string, source: string, sort: string, family: string): string {
  return `${query}\u0000${source}\u0000${sort}\u0000${family}`;
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
function otherOf(model: EngineModel, image: boolean): string {
  const source = (model.source || "").trim();
  if (image) return source.startsWith("civitai:") ? source.slice("civitai:".length) : "";
  if (model.kind === "lora" || !source.startsWith("hf:")) return "";
  const parts = source.slice("hf:".length).split("/");
  return parts.length >= 3 && parts[0] && parts[1] ? `${parts[0]}/${parts[1]}` : "";
}

function CatalogBrowser({ row, kind, onKind, paneId, sources, readOnly, onChanged, image }: BrowseProps & { image: boolean }) {
  const tr = useT();
  // This face's memory. The component is keyed by engine and kind, so the key is fixed for its
  // whole life and the read below happens exactly once — at mount, which is the return.
  const memoryKey = catalogKey(paneId, row.key, "search", kind);
  const [remembered] = useState(() => loadSearch(memoryKey));
  const [picked, setSource] = useState<CatalogSource>(remembered?.source ?? (image ? "civitai" : "hf"));
  // 🔴 The source in force is the picked one only while the deployment still offers it. A tab
  // strip that no longer draws `civitai-red` (engine_civitai_red.go) must not keep searching it
  // from a state set before the switch moved — the CP answers that 403, and a search nobody can
  // see the tab for reads as a broken panel.
  const source = sources.includes(picked) ? picked : image ? "civitai" : "hf";
  const [sort, setSort] = useState(remembered?.sort ?? (image ? "newest" : "updated"));
  // The family filter, as one of the ENGINE's own base models — never an upstream name. Empty is
  // every family, which is what a browse was before this existed.
  const [family, setFamily] = useState(remembered?.family ?? "");
  const [query, setQuery] = useState(remembered?.query ?? "");
  const [submittedQuery, setSubmittedQuery] = useState(remembered?.submittedQuery ?? "");
  const [hits, setHits] = useState<IngestHit[] | null>(remembered?.hits ?? null);
  const [cursor, setCursor] = useState(remembered?.cursor ?? "");
  const [busy, setBusy] = useState(false);
  const [busyMore, setBusyMore] = useState(false);
  const [err, setErr] = useState("");
  const [plan, setPlan] = useState<{ hit?: IngestHit; source?: CatalogSource } | null>(null);
  const [preview, setPreview] = useState<IngestHit | null>(null);
  const [heldObjects] = useState(() => loadObjectsMemory(row.key));
  const [objects, setObjects] = useState<EngineObjectRow[] | null>(heldObjects?.objects ?? null);
  const [ledgerState, setLedgerState] = useState<"checking" | "ready" | "failed">(heldObjects ? "ready" : "checking");
  const [started, setStarted] = useState("");
  // Whether the end of the list may fetch the next page by itself. A page that came back from
  // memory starts it OFF: the restored scroll position lands at the bottom, the sentinel is on
  // screen at once, and a return would spend an upstream request before anybody did anything —
  // which is the exact cost this memory exists to remove. The first wheel/touch/key on the list
  // arms it, and the manual button never went away.
  const [autoArmed, setAutoArmed] = useState(!remembered?.hits?.length);
  const requestSeq = useRef(0);
  const listRef = useRef<HTMLElement | null>(null);
  /** The question the page on screen answers. What a new search is compared against to decide
   *  whether the reader is still looking at the same list. */
  const condition = useRef(conditionOf(
    remembered?.submittedQuery ?? "", remembered?.source ?? source,
    remembered?.sort ?? sort, remembered?.family ?? family,
  ));

  useEffect(() => {
    saveSearch(memoryKey, { source: picked, sort, family, query, submittedQuery, hits, cursor });
  }, [memoryKey, picked, sort, family, query, submittedQuery, hits, cursor]);

  // One surface, keyed by the question: coming back to the same list returns to the place in it,
  // and a different list has no place recorded and opens at the top. 🔴 Through the hook, never
  // `scrollMemoryRef` directly — that returns a new function every call, and React would detach
  // and re-attach it on every render ([[react-ref-callback-identity-cleanup]]).
  const scrollMemory = useScrollMemory(
    `${memoryKey}\u0000${conditionOf(submittedQuery, source, sort, family)}`,
  )("list");
  const attachList = useCallback((element: HTMLElement | null) => {
    listRef.current = element;
    return scrollMemory(element);
  }, [scrollMemory]);

  // Whether anything is on screen to leave standing while the next listing is fetched. A ref and
  // not the state itself: this is read inside the callback, and making `objects` a dependency
  // would rebuild it on every listing and re-fire the effect that calls it.
  const listed = useRef(!!heldObjects);
  const loadObjects = useCallback(async () => {
    // Only a bucket nobody has listed yet says "checking". With a remembered listing on screen,
    // saying it again would blank every card's badge on the way back from another tab.
    if (!listed.current) setLedgerState("checking");
    try {
      const answer = await api(objectsPath(row.key));
      if (answer?.error) { setObjects(null); listed.current = false; setLedgerState("failed"); return; }
      const rows: EngineObjectRow[] = Array.isArray(answer?.objects) ? answer.objects : [];
      setObjects(rows);
      listed.current = true;
      saveObjectsMemory(row.key, { objects: rows, checkedAt: answer?.checked_at || "" });
      setLedgerState("ready");
    } catch { setObjects(null); listed.current = false; setLedgerState("failed"); }
  }, [row.key]);
  useEffect(() => { void loadObjects(); }, [loadObjects]);

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
      if (!more) {
        setSubmittedQuery(query);
        // A DIFFERENT question gets a fresh page and starts at the top; the same one re-asked
        // keeps its place, which is what pressing 検索 again is for. Done by hand because the
        // scroll memory cannot do it: its key changed with the question, and finding nothing
        // recorded under the new one correctly leaves the box where it is (useScrollMemory's
        // `arm`) — which would be halfway down a list nobody has read.
        const asked = conditionOf(query, source, sort, family);
        if (asked !== condition.current && listRef.current) listRef.current.scrollTop = 0;
        condition.current = asked;
      }
      setCursor(page.next_cursor || "");
    } finally { if (seq === requestSeq.current) { setBusy(false); setBusyMore(false); } }
  }, [cursor, family, kind, query, row.key, sort, source, submittedQuery]);

  // The list searches itself when the question changes — but NOT on the way back from another
  // tab, where the page it would fetch is the one already on screen. That return is the whole
  // point of the memory: a search is an upstream request, and Civitai sheds load with a 503.
  const restored = useRef(!!remembered?.hits);
  useEffect(() => {
    if (restored.current) { restored.current = false; return; }
    void search(false);
    /* eslint-disable-next-line react-hooks/exhaustive-deps */
  }, [source, sort, family]);
  const switchSource = (next: CatalogSource) => {
    setSource(next); setSort(next === "civitai" || next === "civitai-red" ? "newest" : "updated"); setHits(null); setCursor("");
  };
  const sortOptions = source === "civitai" || source === "civitai-red" ? ["newest", "downloads", "trending", "likes"] : ["updated", "downloads", "trending", "likes"];

  return (
    <section
      className="engine-catalog-browser"
      ref={attachList}
      // The first touch on a restored list is what lets its end fetch again (see `autoArmed`).
      // Listened for on the scroller rather than the sentinel: the reader arms it by moving, and
      // by then the sentinel may already have been on screen for a while.
      {...(autoArmed ? {} : {
        onWheel: () => setAutoArmed(true),
        onTouchStart: () => setAutoArmed(true),
        onKeyDown: () => setAutoArmed(true),
      })}
      aria-label={tr(image ? "admin.catalog_image_title" as never : "admin.catalog_llm_title" as never)}
    >
      <div className="engine-catalog-toolbar">
        <span className="seg sm">
          {(["model", "lora"] as const).map((next) => <Button key={next} variant="ghost" small
            className={"seg-btn" + (kind === next ? " active" : "")} onClick={() => onKind(next)}>
            {tr(next === "lora" ? "admin.engines_tab_loras" : "admin.engines_tab_models")}
          </Button>)}
        </span>
        {image && <span className="seg sm">{(["civitai", "civitai-red", "hf"] as const).filter((next) => sources.includes(next)).map((next) => <Button key={next} variant="ghost" small
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
        paused={!autoArmed} count={hits?.length || 0} onMore={() => void search(true)} />}
      {plan && <IngestPlanDialog row={row} kind={kind} hit={plan.hit} initialSource={plan.source}
        onClose={() => setPlan(null)}
        onStarted={(job) => {
          setPlan(null);
          // Which of the three the press turned out to be: a reuse or a move crosses no network,
          // so telling somebody to watch a download would have them watching for nothing.
          setStarted(tr((job?.action && job.action !== "download"
            ? "admin.catalog_started_no_download" : "admin.catalog_started") as never) as string);
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
 * request loop.
 *
 * `paused` is the second way past it, and it exists for the return from another tab: a restored
 * page comes back with its scroll position, which puts the sentinel on screen before anybody has
 * done anything, and the observer would answer that by spending an upstream request. So the
 * caller holds the observer off until the reader moves — the button below stays either way. */
function CatalogMore({ busy, loading, paused, count, onMore }: { busy: boolean; loading: boolean; paused?: boolean; count: number; onMore: () => void }) {
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
    if (busy || paused || !node || typeof IntersectionObserver !== "function") return;
    const io = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting) || autoAt.current === seen.current) return;
      autoAt.current = seen.current;
      fire.current();
    }, { rootMargin: "400px" });
    io.observe(node);
    return () => io.disconnect();
  }, [busy, paused]);

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

function NoEngineCatalog({ sources }: { sources: CatalogSource[] }) {
  const tr = useT();
  const [role, setRole] = useState<"image" | "llm">("image");
  const [kind, setKind] = useState<ModelKind>("model");
  const [picked, setSource] = useState<CatalogSource>("civitai");
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
  // Same rule as CatalogBrowser's: a source the deployment stopped offering is not searched.
  const source = sources.includes(picked) ? picked : image ? "civitai" : "hf";

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
      {image && <span className="seg sm">{(["civitai", "civitai-red", "hf"] as const).filter((next) => sources.includes(next)).map((next) => <Button key={next} variant="ghost" small className={`seg-btn${source === next ? " active" : ""}`} onClick={() => switchSource(next)}>{tr((`admin.engines_ingest_source_${next}`) as never)}</Button>)}</span>}
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

/** Whether a `complete` answer has a question in it. Two things are worth a dialog and nothing
 * else is: a role the CP wants a pick for — which it asks even with ONE candidate, because
 * `--clip_l` / `--clip_g` / `--t5xxl` share a directory and the CP will not guess which of them a
 * loose encoder is — and bytes somebody has to agree to pay for. */
function needsAsking(answer: CompleteAnswer): boolean {
  return answer.action === "choose" || (answer.bytes_to_download || 0) > 0;
}

function RegisteredCatalog({ row, kind, onKind, paneId, isSuper, readOnly, onChanged }: CatalogProps & { isSuper: boolean }) {
  const tr = useT();
  const memoryKey = catalogKey(paneId, row.key, "registered", kind);
  const [remembered] = useState(() => loadRegistered(memoryKey));
  const [heldObjects] = useState(() => loadObjectsMemory(row.key));
  const [query, setQuery] = useState(remembered?.query ?? "");
  const [sort, setSort] = useState(remembered?.sort ?? "name");
  const [objects, setObjects] = useState<EngineObjectRow[] | null>(heldObjects?.objects ?? null);
  /** Told apart from "not read yet": an empty prefix and a bucket nobody could list are
   *  different answers, and drawing the second as the first says this deployment holds nothing. */
  const [ledgerFailed, setLedgerFailed] = useState(false);
  /** Keys a delete was accepted for. 🔴 The CP has no `s3:DeleteObject` (ADR 0072 decision 7), so
   *  `DELETE …/objects` starts a TASK and answers `{deleting}` — the object stays `present` in
   *  the bucket for as long as that task takes. Without this the press looked like it did
   *  nothing, which is how it read on af-sandbox. */
  const [deletingKeys, setDeletingKeys] = useState<string[]>([]);
  const [checkedAt, setCheckedAt] = useState(heldObjects?.checkedAt ?? "");
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState<EngineApiError | null>(null);
  const [note, setNote] = useState("");
  const [edit, setEdit] = useState<EngineModel | null>(null);
  const [deleting, setDeleting] = useState<EngineModel | null>(null);
  const [purge, setPurge] = useState(false);
  const [completing, setCompleting] = useState<{ modelId: string; baseModel?: string; answer: CompleteAnswer } | null>(null);
  const [deletingObject, setDeletingObject] = useState<EngineObjectRow | null>(null);
  const [vramAsk, setVramAsk] = useState<{ model: EngineModel; patch: Record<string, unknown>; message?: string } | null>(null);
  /** Which category is open, `null` being all of them (ADR 0088).
   *
   *  🔴 `null` and not `""`: the group of rows that declare NO family is itself keyed `""`, so a
   *  sentinel of `""` makes "show everything" and "show the unclassified ones" the same value —
   *  which drew both chips selected and made the unclassified chip do nothing (seen on the
   *  rendered screen).
   *
   *  Held rather than derived so that a narrowed catalogue survives a reload of the rows, and read
   *  back through the groups below, so a family whose last row was forgotten opens the whole list
   *  instead of an empty one. */
  const [family, setFamily] = useState<string | null>(remembered ? remembered.family : null);
  const [lightbox, setLightbox] = useState<EngineModel | null>(null);
  /** Taking in ANOTHER size of a model this catalogue already has (ADR 0089). Carried as the
   *  repository and the file rather than as a pre-built request: the dialog is the one thing that
   *  resolves, plans and prices a press, and a second road into the ingest that skipped it would
   *  be a press nobody saw the cost of. */
  const [addFile, setAddFile] = useState<{ repo: string; file: string } | null>(null);
  /** Opening the ladder of what ELSE this model is published as — the other Civitai versions of a
   *  checkpoint, the other quantisations of a GGUF repository. The row, not a pre-built request:
   *  which of the two ladders it gets, and what it asks upstream with, is read off the row's own
   *  recorded source. */
  const [other, setOther] = useState<EngineModel | null>(null);
  /** Pre-filling the plan dialog for a Civitai version (ADR 0085 decision 4).
   *
   *  🔴 The source and the ref travel together. `IngestPlanDialog` picks its source type from
   *  `hit` and `initialSource` alone, so a Civitai URL handed over without the source arrives in
   *  a form set to Hugging Face and is parsed as a repository name. */
  const [addVersion, setAddVersion] = useState<{ modelRef: string; versionRef: string } | null>(null);
  const models = (row.model_rows || []).filter((model) => (model.kind === "lora") === (kind === "lora"));
  const image = engineIsImage(row);

  useEffect(() => { saveRegistered(memoryKey, { query, sort, family }); }, [memoryKey, query, sort, family]);

  // The list itself keeps its place across a tab switch. Its key carries no search condition —
  // this face filters rows it already has, so there is one list and one position for it.
  const listRef = useScrollMemory(memoryKey)("list");

  const loadObjects = useCallback(async () => {
    const answer = await api(objectsPath(row.key));
    if (answer?.error) { setObjects(null); setLedgerFailed(true); return; }
    setLedgerFailed(false);
    const rows: EngineObjectRow[] = Array.isArray(answer?.objects) ? answer.objects : [];
    setObjects(rows);
    setCheckedAt(answer?.checked_at || "");
    saveObjectsMemory(row.key, { objects: rows, checkedAt: answer?.checked_at || "" });
    // A key the listing no longer returns is gone — that, and nothing else, ends "deleting".
    setDeletingKeys((current) => current.filter((key) => rows.some((object) => object.key === key)));
  }, [row.key]);
  useEffect(() => { void loadObjects(); }, [loadObjects]);
  // A download runs for minutes and has no list of its own any more (ADR 0085 decision 6): it is
  // its destination object's `uploading` state, so the ledger is what polls. A delete is the same
  // shape from the other side — a task nobody can see the end of except by listing again.
  // 🔴 Read the JOB as well as the state. A key that already holds bytes keeps `state: "present"`
  // while a task writes over it (engine_objects.go:354 only promotes an ABSENT object to
  // `uploading`), so an ingest in flight is invisible if only the state is consulted.
  const live = deletingKeys.length > 0 || (objects || []).some(inFlight);
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => void loadObjects(), 5000);
    return () => clearInterval(timer);
  }, [live, loadObjects]);

  const visible = [...models].filter((model) => {
    // The publisher's name is searched too, and it is the half people type: a catalogue where
    // "meina" found nothing because the row is called `meinamix_meinav11_5038` was the state
    // this screen was in before the name was stored.
    const words = [model.id, model.display_name, model.version_name, model.description,
      model.base_model, model.license_name, model.license].filter(Boolean).join(" ").toLowerCase();
    return words.includes(query.trim().toLowerCase());
  }).sort((left, right) => {
    // 名前 orders by what the card SHOWS. Ordering by the id while drawing the name puts the
    // cards in an order the screen cannot explain — which is worse than either alone.
    const byName = (a: EngineModel, b: EngineModel) =>
      registeredTitle(a).title.localeCompare(registeredTitle(b).title) || a.id.localeCompare(b.id);
    if (sort === "enabled") return Number(right.enabled) - Number(left.enabled) || byName(left, right);
    if (sort === "default") return Number(!!(right.selected || right.default)) - Number(!!(left.selected || left.default)) || byName(left, right);
    return byName(left, right);
  });
  const groups = groupRegistered(visible, row, image);
  // A chip that no longer matches anything opens everything, rather than leaving the catalogue
  // empty with no visible reason: forgetting the last row of a family is exactly how that
  // happens, and the screen it leaves looks like a failed load.
  const openFamily = family !== null && groups.some((group) => group.key === family) ? family : null;
  const shown = openFamily === null ? groups : groups.filter((group) => group.key === openFamily);
  const unnamed = visible.filter(registeredNeedsMeta);

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

  const align = async (id: string, baseModel?: string) => {
    setNote("");
    const check = await completeModel(id, { check: true });
    if (!check) return;
    // Only two things are worth a dialog: a role the CP wants a pick for, and bytes somebody has
    // to agree to pay for. Everything else just happens.
    if (needsAsking(check)) { setCompleting({ modelId: id, baseModel, answer: check }); return; }
    await runComplete(id);
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
      // 🔴 `register` assigns bytes that are already here and starts no download of its own, so a
      // part that exists only upstream comes back as `download` in its own answer's `complete`.
      // The row is the subject of that, and this is the press that is already in somebody's hand
      // — leaving it as a note is how "I registered it and do not know what to do" happens.
      if (registered.model_id && registered.complete && needsAsking(registered.complete)) {
        setCompleting({ modelId: registered.model_id, answer: registered.complete });
      }
    } finally { setBusy(""); }
  };

  /** Re-read the model pages of rows that carry no name and no picture (ADR 0088).
   *
   * One request per row, in order, because each is an upstream read this deployment pays for and
   * the operator is watching the count: a fan-out would be a burst of requests at a site that
   * sheds load with a 503, on a press whose whole value is that it is explicit.
   *
   * 🔴 A row that fails does not stop the run and does not raise the refusal line on its own —
   * a `url:` source can never answer, and one of those would otherwise abort the other twenty.
   * The last refusal is kept and shown once at the end, where it explains the failures the
   * count reports. */
  const fillMeta = async (targets: EngineModel[]) => {
    if (!targets.length) return;
    setErr(null);
    let done = 0;
    let last: EngineApiError | null = null;
    for (const model of targets) {
      setBusy(`meta:${model.id}`);
      setNote((tr("admin.catalog_meta_busy" as never) as string)
        .replace("{n}", String(done + 1)).replace("{m}", String(targets.length)));
      const answer = await apiJSON(
        `api/admin/engines/${encodeURIComponent(row.key)}/models/${encodeURIComponent(model.id)}/meta`, "POST");
      if (answer?.error) last = answer.error as EngineApiError; else done += 1;
    }
    setBusy("");
    const failed = targets.length - done;
    setNote((tr("admin.catalog_meta_done" as never) as string)
      .replace("{n}", String(done)).replace("{f}", String(failed)));
    if (last) setErr(last);
    await onChanged();
  };

  const deleteObject = async (key: string) => {
    setBusy(`object:${key}`); setErr(null); setNote("");
    try {
      const answer = await apiJSON(objectsPath(row.key), "DELETE", { key });
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
      // The answer is `{deleting}`, not `{deleted}`: draw the row as such AT ONCE and keep
      // reloading until the bucket stops listing it.
      setDeletingKeys((current) => current.includes(key) ? current : [...current, key]);
      setDeletingObject(null);
      await loadObjects();
    } finally { setBusy(""); }
  };

  /** Turn one file an external ComfyUI already holds into a row (ADR 0082 decisions 6 and 7).
   *
   * 🔴 `POST …/models`, not `…/objects/register`: those bytes are on the far box and never in
   * this deployment's bucket, so there is no object to register and nothing to HeadObject. It is
   * the one write that still takes a key nobody verified (ADR 0085 decision 3), and this is the
   * only caller left. */
  const addModel = async (body: Record<string, unknown>) => {
    setBusy("discover"); setErr(null);
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/models`, "POST", body);
      if (answer?.error) { setErr(answer.error as EngineApiError); return answer; }
      await onChanged();
      return answer;
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

  return <section className="engine-catalog-registered" ref={listRef} aria-label={tr("admin.catalog_registered_title" as never)}>
    {!isSuper && <p className="admin-hint">{tr("admin.engines_tenant_scope")}</p>}
    <div className="engine-catalog-toolbar">
      <span className="seg sm">{(["model", "lora"] as const).map((next) => <Button key={next} variant="ghost" small
        className={"seg-btn" + (kind === next ? " active" : "")} onClick={() => onKind(next)}>{tr(next === "lora" ? "admin.engines_tab_loras" : "admin.engines_tab_models")}</Button>)}</span>
      <label className="engine-registered-search"><span>{tr("admin.catalog_registered_search" as never)}</span><input value={query} onChange={(event) => setQuery(event.currentTarget.value)} /></label>
      <label className="engine-catalog-sort"><span>{tr("admin.catalog_sort" as never)}</span><select value={sort} onChange={(event) => setSort(event.currentTarget.value)}>
        <option value="name">{tr("admin.catalog_sort_name" as never)}</option><option value="enabled">{tr("admin.catalog_sort_enabled" as never)}</option><option value="default">{tr("admin.catalog_sort_default" as never)}</option>
      </select></label>
      <IconButton icon="refresh" label={tr("admin.refresh")} onClick={() => void loadObjects()} />
      {/* The one press that fills a catalogue of ids in with names (ADR 0088). Drawn only while
          there is something to fill, so it retires itself: on a deployment whose rows all carry
          a name it is not a button anybody has to understand. */}
      {!readOnly && !!unnamed.length && <Button small icon="download" className="engine-registered-meta-all"
        disabled={!!busy} onClick={() => void fillMeta(unnamed)}>
        {(tr("admin.catalog_meta_all" as never) as string).replace("{n}", String(unnamed.length))}
      </Button>}
    </div>
    {/* The categories, as chips (ADR 0088). Above the list and not inside a card: the family is
        what tells two checkpoints apart at a glance, and as a tag in the middle of a card it was
        the one fact nobody could scan a page by. */}
    {groups.length > 1 && <div className="engine-registered-families" role="tablist" aria-label={tr("admin.catalog_family" as never)}>
      <Button variant="ghost" small role="tab" aria-selected={openFamily === null}
        className={"engine-family-chip" + (openFamily === null ? " active" : "")} onClick={() => setFamily(null)}>
        {tr("admin.catalog_family_all" as never)} <span className="muted">{visible.length}</span>
      </Button>
      {groups.map((group) => <Button key={group.key || "*"} variant="ghost" small role="tab"
        aria-selected={group.key === openFamily}
        className={"engine-family-chip" + (group.key === openFamily ? " active" : "")}
        onClick={() => setFamily(group.key === openFamily ? null : group.key)}>
        {groupLabel(group.key, tr)} <span className="muted">{group.models.length}</span>
      </Button>)}
    </div>}
    <EngineRefusal error={err} busy={!!busy} onNext={() => void runNext(err as EngineApiError)} />
    {note && <p className="muted engine-registered-note">{note}</p>}
    {!visible.length && <p className="muted">{tr(kind === "lora" ? "admin.engines_loras_empty" : "admin.engines_catalog_empty")}</p>}
    {shown.map((group) => <section key={group.key || "*"} className="engine-registered-group" aria-label={groupLabel(group.key, tr)}>
      {(groups.length > 1 || groupIsRepo(group.key)) && <h3 className="engine-registered-group-head">
        <span>{groupLabel(group.key, tr)}</span>
        <span className="muted">{(tr("admin.repo_held_count" as never) as string).replace("{n}", String(group.models.length))}</span>
      </h3>}
      {/* The other sizes of THIS model, one press away (ADR 0089). Only where the group names a
          repository — a row registered by hand has nothing to list — and only for the chat role,
          because a checkpoint repository publishes one checkpoint and not a ladder. */}
      {!image && groupIsRepo(group.key) && <RepoQuantLadder engine={row} repo={group.key}
        rows={group.models} readOnly={readOnly} onTakeIn={(file) => setAddFile({ repo: group.key, file })} />}
      <ul className="engine-registered-grid">{group.models.map((model) => {
      const status = registeredPresence(model, objects);
      const started = !!(model.selected || model.default);
      const pending = busy === model.id || busy === `meta:${model.id}`;
      const name = registeredTitle(model, group.key);
      const thumb = model.thumb_url || model.preview_url || "";
      return <li key={model.id} className="engine-registered-card" aria-label={model.id}>
        <header><span className="engine-registered-name">
          <strong>{name.title}</strong>
          {name.version && <span className="engine-registered-version">{name.version}</span>}
        </span>
          <span className={`engines-model-tag ${model.enabled ? "on" : "off"}`}>{tr(model.enabled ? "admin.engines_model_is_on" : "admin.engines_model_is_off")}</span>
          {started && <span className="engines-model-tag lead">{tr("admin.engines_model_started")}</span>}
          <span className={`engines-model-tag ${status.tone}`}>{tr((`admin.catalog_registered_${status.state}`) as never, { present: status.present, total: status.total } as never)}</span>
          {!!model.files_missing?.length && <span className="engines-model-tag warn">{(tr("admin.engines_model_files_missing_tag" as never) as string).replace("{f}", model.files_missing.join(" "))}</span>}</header>
        {/* The id keeps its own line under the title. It is the key the launch menu, the active
            set and every S3 path are written in, so it is on every card — and on a row whose
            title IS the id it is not repeated, because two copies of one string in two fonts
            read as two different facts. */}
        <div className="engine-registered-main">
          <div className="engine-registered-copy">
            {!name.bare && <p className="mono muted engine-registered-id">{model.id}</p>}
            {image ? <ImageRegisteredCardBody model={model} /> : <LLMRegisteredCardBody model={model} />}
          </div>
          {!!thumb && <Button variant="ghost" className="engine-registered-thumb"
            aria-label={`${tr("admin.catalog_preview" as never)}: ${name.title}`} onClick={() => setLightbox(model)}>
            <img src={thumb} alt="" loading="lazy" referrerPolicy="no-referrer" />
          </Button>}
        </div>
        <RegisteredParts model={model} objects={objects} />
        <footer>
          {/* Offered on a row with neither a name nor a picture, and on no other: it is a
              re-read of an upstream page, so a catalogue that already reads well must not carry
              a button inviting one row at a time of it. */}
          {!readOnly && registeredNeedsMeta(model) && <Button variant="ghost" small icon="download"
            aria-label={`${tr("admin.catalog_meta" as never)}: ${model.id}`} disabled={pending}
            onClick={() => void fillMeta([model])}>{tr("admin.catalog_meta" as never)}</Button>}
          {/* The other versions / the other sizes of THIS model, from the row that has one
              (ADR 0089 for chat, and its image counterpart). Offered only where there is
              something to list: a row whose source is a `url:` or a seeded one records no page,
              and a borrowed catalogue draws no acts at all. */}
          {!readOnly && !!otherOf(model, image) && <Button variant="ghost" small icon="versions"
            aria-label={`${tr(image ? "admin.catalog_other_versions" as never : "admin.catalog_other_sizes" as never)}: ${model.id}`}
            onClick={() => setOther(model)}>
            {tr(image ? "admin.catalog_other_versions" as never : "admin.catalog_other_sizes" as never)}
          </Button>}
          {isSuper && !readOnly && <Button variant="ghost" small icon="edit" aria-label={`${tr("admin.catalog_edit" as never)}: ${model.id}`} onClick={() => setEdit(model)}>{tr("admin.catalog_edit" as never)}</Button>}
          {isSuper && !readOnly && <Button small aria-label={`${tr(model.enabled ? "admin.engines_model_disable" : "admin.engines_model_enable")}: ${model.id}`} disabled={pending} onClick={() => void guardedChange(model, { enabled: !model.enabled })}>{tr(model.enabled ? "admin.engines_model_disable" : "admin.engines_model_enable")}</Button>}
          {isSuper && !readOnly && model.kind !== "lora" && !started && <Button variant="primary" small aria-label={`${tr("admin.engines_model_select")}: ${model.id}`} disabled={pending} onClick={() => void guardedChange(model, image ? { selected: true } : { default: true })}>{tr("admin.engines_model_select")}</Button>}
          {/* 揃える is on every row, not only a marked one: what it does is read off the ledger at
              the press, and a row that has everything answers "nothing to do" in one call. */}
          {!readOnly && <Button variant={model.files_missing?.length || model.vae_missing ? "primary" : undefined} small
            aria-label={`${tr("admin.catalog_complete" as never)}: ${model.id}`} disabled={pending}
            onClick={() => void align(model.id, model.base_model)}>{tr(pending ? "admin.catalog_complete_busy" as never : "admin.catalog_complete" as never)}</Button>}
          {isSuper && !readOnly && <Button variant="danger" small aria-label={`${tr("admin.engines_model_forget")}: ${model.id}`} disabled={pending || started} onClick={() => { setDeleting(model); setPurge(false); }}>{tr("admin.engines_model_forget")}</Button>}
        </footer>
      </li>;
    })}</ul>
    </section>)}
    <EngineLedger objects={objects} role={row.key} image={image} failed={ledgerFailed} checkedAt={checkedAt} busy={busy} readOnly={readOnly}
      deletingKeys={deletingKeys}
      onRegister={(object) => void registerObject(object.key)}
      onDelete={(object) => setDeletingObject(object)}
      onDismiss={(id) => void dismissJob(id)}
      onComplete={(id) => void align(id, models.find((model) => model.id === id)?.base_model)} />
    {/* The discovery button (ADR 0082 decisions 6 and 7): only for an external ComfyUI this
        control plane can dial directly. `engineCanDiscover` is the exact predicate the CP's own
        route gates on, so a row that would 400 there never shows the button here. It sits under
        the ledger because it is the same question one step further out — what is on the box
        rather than what is in the bucket — and for an external row the bucket is empty. */}
    {isSuper && !readOnly && engineCanDiscover(row) && (
      <EngineDiscoverPanel key={"discover/" + row.key} engineKey={row.key} busy={busy === "discover"} onAdd={addModel} />
    )}
    {completing && <CompleteDialog row={row} modelId={completing.modelId} baseModel={completing.baseModel} answer={completing.answer}
      onClose={() => setCompleting(null)}
      onRun={async (body) => { setCompleting(null); await runComplete(completing.modelId, body); }} />}
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
    {/* What else this model is published as. One press, one modal, and the press inside it opens
        the ordinary plan dialog — so the licence and the price are still seen on the one screen
        that has always shown them. */}
    {other && <Modal className="engine-catalog-operation engine-other-ladder"
      title={`${tr(image ? "admin.catalog_other_versions" as never : "admin.catalog_other_sizes" as never)} — ${registeredTitle(other).title}`}
      onClose={() => setOther(null)}>
      <div className="ui-modal-body engine-operation-body">
        {image
          ? <CivitaiVersionLadder engine={row} versionRef={otherOf(other, true)} rows={models} readOnly={readOnly}
            onTakeIn={(modelRef, version) => { setOther(null); setAddVersion({ modelRef, versionRef: version.ref }); }} />
          : <RepoQuantLadder engine={row} repo={otherOf(other, false)} rows={models} readOnly={readOnly} startOpen
            onTakeIn={(file) => { setOther(null); setAddFile({ repo: otherOf(other, false), file }); }} />}
        {/* What taking another one in does NOT do. The row is never rewritten in place: its id is
            the key the launch menu, the active set and every S3 path are written in, so a new
            version arrives as a new row and the swap is two presses the card already has. */}
        <p className="admin-hint">{tr("admin.catalog_other_note" as never)}</p>
      </div>
    </Modal>}
    {/* 🔴 The source travels WITH the ref. `IngestPlanDialog` reads its source type from `hit`
        and `initialSource` only, so a Civitai link passed alone opens a form still set to
        Hugging Face, which parses the whole URL as a repository name. The spelling is the one the
        dialog's own `civitaiPage` / `civitaiVersionParam` already read — no second parser. */}
    {addVersion && <IngestPlanDialog row={row} kind={kind} initialSource="civitai"
      // Without a model id — an older control plane that does not resolve one — the version-only
      // spelling is still a link this dialog reads, and the CP resolves the model behind it. It
      // is the same form a person pasting from the address bar arrives with.
      initialRef={addVersion.modelRef
        ? `https://civitai.com/models/${addVersion.modelRef}?modelVersionId=${addVersion.versionRef}`
        : `https://civitai.com/models/?modelVersionId=${addVersion.versionRef}`}
      onClose={() => setAddVersion(null)}
      onStarted={async () => { setAddVersion(null); await onChanged(); await loadObjects(); }} />}
    {/* 🔴 Handed the blob URL rather than a repo/file pair: that is the form the dialog already
        parses (`hfURL`), so this road opens the same pre-filled screen a pasted link does, and
        there is no second way in for the plan and the licence to be skipped. */}
    {addFile && <IngestPlanDialog row={row} kind={kind} initialSource="hf"
      initialRef={`https://huggingface.co/${addFile.repo}/blob/main/${addFile.file}`}
      onClose={() => setAddFile(null)}
      onStarted={async () => { setAddFile(null); await onChanged(); await loadObjects(); }} />}
    {lightbox?.preview_url && <Modal title={registeredTitle(lightbox).title} className="engine-catalog-lightbox" onClose={() => setLightbox(null)}>
      <img className="ui-modal-body" src={lightbox.preview_url} alt="" referrerPolicy="no-referrer" />
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

/** The heading a group of rows is drawn under (ADR 0088). The stored value verbatim — a family
 * is the provider's own spelling and a publisher is the repository's owner, and translating
 * either would name something that exists nowhere else on the screen. Only the empty group gets
 * a sentence, because "" is not a name but the absence of one. */
function groupLabel(key: string, tr: ReturnType<typeof useT>): string {
  return key || (tr("admin.catalog_family_none" as never) as string);
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
function EngineLedger({ objects, role, image, failed, checkedAt, busy, readOnly, deletingKeys, onRegister, onDelete, onDismiss, onComplete }: {
  objects: EngineObjectRow[] | null;
  /** The engine's own key, which is the prefix every one of these objects sits under. Needed
   *  because what counts as a model's own weights is read off the key, and the two roles write
   *  different layouts under it. */
  role: string;
  image: boolean;
  failed: boolean;
  checkedAt: string;
  busy: string;
  readOnly: boolean;
  /** Keys this Console asked to delete and the bucket still lists. Drawn as `deleting` until the
   *  listing drops them; the CP's own `job.state === "deleting"` says the same thing once it
   *  starts sending it, and either is enough. */
  deletingKeys: string[];
  onRegister: (object: EngineObjectRow) => void;
  onDelete: (object: EngineObjectRow) => void;
  onDismiss: (id: string) => void;
  onComplete: (modelId: string) => void;
}) {
  const tr = useT();
  const sorted = [...(objects || [])]
    // 🔴 A `missing` entry nobody declares is not a fact about anything: the row it belonged to is
    // gone and so are the bytes. The CP stopped sending those; one that arrives anyway is dropped
    // rather than drawn as a line with no subject and no act.
    .filter((object) => object.state !== "missing" || (object.declared_by || []).length > 0)
    .sort((left, right) => ledgerRank(left) - ledgerRank(right) || left.key.localeCompare(right.key));
  return <section className="engine-ledger" aria-label={tr("admin.catalog_ledger_title" as never)}>
    <header className="engine-ledger-head">
      <strong>{tr("admin.catalog_ledger_title" as never)}</strong>
      {checkedAt && <span className="muted">{(tr("admin.catalog_ledger_checked" as never) as string).replace("{t}", fmtDateTime(checkedAt))}</span>}
    </header>
    {/* What the list IS, always — and what to press, only where there is something to press. A
        borrowed row's catalogue is the far deployment's mirror and draws no act at all (ADR 0079
        decision 7), so the second sentence there would describe a button nobody has. */}
    <p className="admin-hint">{tr("admin.catalog_ledger_note" as never)}
      {!readOnly && <> {tr("admin.catalog_ledger_note_acts" as never)}</>}</p>
    {objects === null && <p className={failed ? "form-err" : "muted"}>{tr((failed ? "admin.catalog_ledger_unavailable" : "admin.catalog_storage_checking") as never)}</p>}
    {objects !== null && !objects.length && <p className="muted">{tr("admin.catalog_ledger_empty" as never)}</p>}
    {!!sorted.length && <ul className="engine-ledger-list">{sorted.map((object) => {
      const orphan = !(object.declared_by || []).length;
      const removing = deletingKeys.includes(object.key) || object.job?.state === "deleting";
      const taking = !removing && ingesting(object);
      const pending = busy === `object:${object.key}` || (!!object.job && busy === `job:${object.job.id}`);
      const holder = (object.declared_by || [])[0]?.model_id || "";
      return <li key={object.key} className={`engine-ledger-row${orphan ? " orphan" : ""}${removing ? " deleting" : ""}${taking ? " uploading" : ""}`} aria-label={object.key}>
        <span className="mono engine-ledger-key">{object.key}</span>
        <span className="engine-ledger-tags">
          <span className="engines-model-tag">{object.role_dir}</span>
          {object.placement === "misplaced" && <span className="engines-model-tag warn">{tr("admin.catalog_ledger_misplaced" as never)}</span>}
          <span className={`engines-model-tag ${removing || taking ? "lead" : object.state === "present" ? "on" : object.state === "failed" || object.state === "missing" ? "bad" : "lead"}`}>
            {tr((removing ? "admin.catalog_ledger_state_deleting"
              : taking ? "admin.catalog_ledger_state_uploading"
                : `admin.catalog_ledger_state_${object.state}`) as never)}
          </span>
          {!!object.bytes && <span className="engines-model-tag">{formatBytes(object.bytes)}</span>}
          {object.license && <span className="engines-model-tag">{object.license}</span>}
        </span>
        <span className="muted engine-ledger-declared">{orphan
          ? tr("admin.catalog_ledger_orphan" as never)
          : (tr("admin.catalog_ledger_declared" as never) as string).replace("{m}", (object.declared_by || [])
            .map((holder) => holder.flag ? `${holder.model_id} (${holder.flag})` : holder.model_id).join(", "))}</span>
        {object.source && <span className="muted mono engine-ledger-source">{object.source}</span>}
        {removing && <span className="muted engine-ledger-note">{tr("admin.catalog_ledger_deleting_note" as never)}</span>}
        {taking && <span className="muted engine-ledger-note">{tr("admin.catalog_ledger_uploading_note" as never)}</span>}
        {/* A row points at bytes that are not there. The ledger says whose problem it is and the
            act is that row's 揃える — the object side has nothing to press. */}
        {!removing && object.state === "missing" && !!holder && <span className="form-err engine-ledger-note">
          {(tr("admin.catalog_ledger_missing_note" as never) as string).replace("{m}", holder)}</span>}
        {object.job?.message && <span className="form-err engine-ledger-message">{object.job.message}</span>}
        {/* Nothing is pressable while a task owns the key. 登録 on a key an ingest is writing
            answers 409 `already declared by` — the refusal is right and the button was not. */}
        {!readOnly && !removing && !taking && <span className="engine-ledger-acts">
          {object.state === "missing" && !!holder
            ? <Button small variant="primary" disabled={pending} aria-label={`${tr("admin.catalog_complete" as never)}: ${object.key}`}
              onClick={() => onComplete(holder)}>{tr("admin.catalog_complete" as never)}</Button>
            : object.state === "failed" && object.job
            ? <Button small variant="danger" disabled={pending} aria-label={`${tr("admin.catalog_ledger_delete" as never)}: ${object.key}`}
              onClick={() => onDismiss(object.job!.id)}>{tr("admin.catalog_ledger_delete" as never)}</Button>
            : <>
              {orphan && object.state === "present" && isMainObject(object, role, image) && <Button small variant="primary" disabled={pending}
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

/** Whether a task is writing this key right now. Both spellings count: the ledger promotes an
 * object to `uploading` only when the bucket does not hold it yet, so a re-ingest over an
 * existing key is `present` with an `uploading` job — which is exactly the row that offered 登録
 * and answered 409 `already declared by` on af-sandbox. */
function ingesting(object: EngineObjectRow): boolean {
  return object.state === "uploading" || object.job?.state === "uploading";
}

function inFlight(object: EngineObjectRow): boolean {
  return ingesting(object) || object.job?.state === "deleting";
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
 * offered on these only, and this is the CP's `engineObjectIsMainFile` in TypeScript: a button the
 * route would refuse is worse than no button.
 *
 * The image role is ComfyUI's, one directory per loader, so a misplaced file still names its role
 * directory in the key — which is how `image/checkpoints/split_files/diffusion_models/x.safetensors`
 * is recognised. The llm role is flat: `llm/<file>.gguf` and nothing deeper, because `llm/loras/…`
 * is an adapter and `llm/<name>/shard.gguf` is one piece of a file. */
function isMainObject(object: EngineObjectRow, role: string, image: boolean): boolean {
  if (image) {
    if (object.role_dir === "checkpoints" || object.role_dir === "diffusion_models") return true;
    return /(^|\/)(checkpoints|diffusion_models)\//.test(object.key);
  }
  const prefix = `${role.trim()}/`;
  if (!object.key.startsWith(prefix)) return false;
  const rest = object.key.slice(prefix.length);
  return !!rest && !rest.includes("/") && MODEL_FILE_EXTS.some((ext) => rest.toLowerCase().endsWith(ext));
}

/** The names a loader could load — the CP's `engineModelFileExts`. The image branch above has no
 * need of them (a key under `checkpoints/` is a model file by where it is), but a flat role has
 * only the name to go on, and a `.json` beside a GGUF is not a model. */
const MODEL_FILE_EXTS = [".safetensors", ".gguf", ".ckpt", ".pt", ".sft", ".bin"];

/** The plan card (ADR 0085 decision 4). One press, and the CP decided what that press does.
 *
 * 🔴 No role selector, no key field, no parts checkbox, no attach/replace: every one of those was
 * a question a person who does not know what a text encoder is cannot answer, and getting one
 * wrong produced a row that looked complete and would not generate. What is left is the version
 * and the file — which repository and which of its files — and the licence. */
function IngestPlanDialog({ row, kind, hit, initialSource, initialRef, onClose, onStarted }: {
  row: EngineRow; kind: ModelKind; hit?: IngestHit;
  initialSource?: CatalogSource;
  /** Open the dialog as though this had been PASTED into the source box (ADR 0089) — a Hugging
   *  Face blob URL, which the parser below already turns into a repository, a revision and a
   *  file.
   *
   *  🔴 Its own prop and not a borrowed field of `hit`: `hit.ref` is the HF REVISION and
   *  `hit.model_ref` is the repository, so a URL smuggled through either arrives as one of those
   *  (measured — the request went out with `revision` set to the whole URL). This is a second way
   *  to fill the FORM and deliberately not a second way to start an ingest: the plan, the price
   *  and the licence are the same screen either way. */
  initialRef?: string;
  onClose: () => void; onStarted: (job: IngestJob) => void;
}) {
  const tr = useT();
  const image = engineIsImage(row);
  const isLora = kind === "lora";
  const [sourceType, setSourceType] = useState<CatalogSource>(hit?.source === "civitai" || initialSource === "civitai" || initialSource === "civitai-red" ? "civitai" : "hf");
  const [manualRef, setManualRef] = useState(initialRef || hit?.model_ref || hit?.ref || "");
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
  // The family the OPERATOR picked, as opposed to the one the CP named. Separate state rather
  // than a comparison against the last answer: comparing makes the trigger flip back the moment
  // the CP echoes the pick, which re-plans a third time instead of stopping.
  const [operatorFamily, setOperatorFamily] = useState("");
  const [familyChoices, setFamilyChoices] = useState<string[]>([]);
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
  const repo = sourceType === "civitai" ? civitaiModelRef || pastedVersion
    : hfURL?.[1] || hit?.model_ref || rawRef || hit?.ref || "";
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
      // A Civitai version with no model beside it used to short-circuit into a list of ONE, made
      // up here, because the route demanded a model id the Console did not have. The CP resolves
      // it from the version now, so the same question has one answer and one road to it — the
      // version list this then draws is the real one, and the other versions are selectable.
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

  // Opened ON something — a hit from the 探す tab, or a repository file from a catalogue card
  // (ADR 0089) — inspects itself. Opened empty, it waits for somebody to paste and press 調べる.
  useEffect(() => { if (hit || initialRef) void inspect(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  // The family the CP is asked to plan FOR: an image checkpoint's only, because that is what the
  // parts table and the main file's role are keyed by. An LLM LoRA's "base" is a registered
  // model's id, which is not a family and would be planned as an unknown one.
  const plannedFamily = image && !isLora ? baseModel : "";
  // What re-planning is triggered BY, which is not the same as what is sent. `baseModel` also
  // holds the family the CP named itself, and asking again with that changes nothing: the plan
  // that carried it was already built from it (enginePlanFor falls back to its own guess when the
  // body names none), so the second answer is identical. Measured on this screen: a row whose
  // family the CP can name resolved TWICE on open, blanking the plan card in between, and a
  // family it cannot name resolved once. Only an operator's pick belongs here.
  const chosenFamily = image && !isLora ? operatorFamily : "";
  // 🔴 The family is SENT, and a change to it re-plans (ADR 0094 decision 7). The parts a split
  // family needs are planned from it (engine_family_parts.go), so a card drawn while it is still
  // unknown prices the main file alone — and the press, which does send it, re-plans into three
  // and answers 409 `engine_plan_stale`. The person then accepts the licence again and presses
  // again, which is the "one press" this deployment's ingest is built around, spent twice.
  //
  // Reachable because a family the CP cannot guess is now a real case: the qwen-image-edit
  // families deliberately have no guess rule (a wrong family here silences the row's only mark —
  // engine_family_guess.go), so the selector is the ONLY place their family ever comes from.
  useEffect(() => {
    if (!file) return;
    let live = true;
    setResolved(null); setPlan(null); setAccepted(false); setReplanned(false);
    apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/resolve`, "POST", {
      source: sourceBody(file), kind: isLora ? "lora" : image ? "checkpoint" : "gguf",
      ...(plannedFamily ? { base_model: plannedFamily } : {}),
    }).then((answer) => {
      if (!live) return;
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
      const found = answer as ResolvedSource;
      setResolved(found);
      if (found.plan) {
        setPlan(found.plan);
        if (!idEdited) setId(found.plan.id || "");
        if (found.plan.base_model) setBaseModel(found.plan.base_model);
        // Remembered, because the CP offers candidates only while it cannot name the family
        // itself: the re-plan our own choice causes answers with none, and the selector would
        // disappear under the person who just used it.
        if (found.plan.base_model_candidates?.length) setFamilyChoices(found.plan.base_model_candidates);
      }
      if (found.context_length && !context) {
        // 🔴 The ceiling is not the setting (ADR 0089). The CP sends `context_length` labelled as
        // the architecture's maximum, and pre-filling the field with it priced the KV cache at
        // that maximum: 66,560 MiB for a 27B (measured 2026-09-18), which reads as "this will
        // never run" about a model that runs fine at 32768. So the field opens at the largest
        // window that actually fits the box this engine buys, and the ceiling is shown beside it
        // as what it is.
        //
        // 🔴 And when it CANNOT be fitted — no readable header, no card — the fallback is not
        // the ceiling either. Falling back to it put 262,144 in the field of the af-sandbox row
        // that then asked for 16 GiB of KV cache and took the L4 out of memory, which is the
        // same failure ADR 0089 was written about, reached through the error path instead of the
        // happy one. windowWhenUnsized is a stated fallback and the form says so.
        const weights = found.bytes ? Math.round(found.bytes / 1048576) : 0;
        const fitted = windowThatFits(weights, found.kv_mib_per_1k_tokens || 0,
          row.class?.vram_mib || 0, found.context_length);
        const window = fitted || windowWhenUnsized(found.context_length);
        setContext(String(window)); setOutput(String(Math.floor(window / 8)));
      }
      if (found.params_hint) setParams((current) => ({
        ...current,
        ...Object.fromEntries(Object.entries(found.params_hint || {}).map(([key, value]) => [key, String(value)])),
      }));
      if (found.trained_words?.length && !trainedWords.trim()) setTrainedWords(found.trained_words.join(", "));
    });
    return () => { live = false; };
    // Existing typed settings deliberately outrank metadata suggestions.
    //
    // ⚠️ The effect SETS `baseModel` and must therefore not depend on it: `chosenFamily` is the
    // operator's pick alone, so the CP naming a family here does not re-enter. What is SENT stays
    // `plannedFamily` (the whole of `baseModel`), because the press sends it too and a plan built
    // without it would go stale against one.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file, image, isLora, row.key, sourceBody, chosenFamily]);

  // The family is asked ONLY when the CP could not read one, and only from the candidates it
  // knows. An LLM LoRA is pinned to a registered model rather than to a family name.
  const familyOptions = isLora && !image
    ? (row.model_rows || []).filter((model) => model.kind !== "lora").map((model) => model.id)
    : plan?.base_model_candidates?.length ? plan.base_model_candidates : familyChoices;
  const needsFamily = !plan?.base_model && familyOptions.length > 0;
  // Drawn while the answer is still ours to change, which outlasts `needsFamily`: once the choice
  // has been sent the plan names a family and the press is no longer blocked, but the person is
  // still looking at a card they may want to re-aim.
  const asksFamily = familyOptions.length > 0 && (needsFamily || !!baseModel);
  const bytes = plan?.bytes_to_download ?? resolved?.bytes ?? 0;
  const contextTokens = Number(context.trim().replace(/[_,]/g, "")) || 0;
  const weightsMiB = bytes ? Math.round(bytes / 1048576) : 0;
  const cardMiB = row.class?.vram_mib || 0;
  // The verdict, from the one module that owns it (ADR 0089). A LoRA is deliberately left out:
  // an adapter is loaded beside a checkpoint and its own size is not what decides the start.
  const fit = modelFit(weightsMiB, !image && !isLora ? resolved?.kv_mib_per_1k_tokens || 0 : 0,
    contextTokens, isLora ? 0 : cardMiB, row.classes || []);
  const kvMiB = fit.kvMiB;
  const needMiB = fit.needMiB;
  // The family this press would take in is the plan's when the CP read one, and the operator's
  // answer when it had to ask (ADR 0094 decision 8 — the measurement is shown for the family, so
  // it must follow the selector rather than the plan alone), narrowed to the build the plan would
  // actually stage.
  const ingestMeasured = image && !isLora
    ? familyVramMeasurement(plan?.base_model || baseModel, (plan?.files || []).map((f) => f.name))
    : null;
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
        //
        // `kind` is the one thing the token cannot carry for us: the CP re-plans from this body
        // at the press and reads `lora` out of it to decide the directory (`image/loras/` versus
        // `image/checkpoints/`). Sent identical to the resolve's, or the re-plan disagrees with
        // the token — a 409 at best, an adapter staged under `checkpoints/` at worst.
        source: sourceBody(file), kind: isLora ? "lora" : image ? "checkpoint" : "gguf",
        plan_token: plan.plan_token,
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
        {asksFamily && <label><span>{tr(isLora && !image ? "admin.engines_model_add_lora_base" : "admin.engines_model_add_family")}</span><select value={baseModel} onChange={(event) => { setBaseModel(event.currentTarget.value); setOperatorFamily(event.currentTarget.value); }}>
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
          {/* One field, two meanings, told apart by the action: the upstream for a download and
              the key the bytes are at TODAY for a reuse or a move. Drawn unbranched, "いまの場所"
              would name a repository and a download would claim to come from the bucket. */}
          {planned.source && <span className="muted mono engine-plan-source">{planned.action === "download"
            ? planned.source
            : (tr("admin.catalog_plan_at" as never) as string).replace("{k}", planned.source)}</span>}
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
      {/* The window, OUT of the advanced fold (ADR 0089). It is the number that decides the
          verdict below — the KV cache is linear in it — and it spent this screen's whole life
          behind a `<summary>` the person reading the red line never opened. */}
      {!image && !isLora && !!resolved && <div className="engine-operation-window">
        <label><span>{tr("admin.engines_model_window_context")}</span>
          <input inputMode="numeric" value={context} onChange={(event) => setContext(event.currentTarget.value)} /></label>
        {!!resolved.context_length && <span className="muted">{(tr("admin.engines_ingest_ctx_ceiling" as never) as string)
          .replace("{n}", resolved.context_length.toLocaleString())}</span>}
      </div>}
      {needMiB > 0 && <p className={`engine-operation-fit ${fit.state === "over" ? "form-err" : "muted"}`}>
        {(tr("admin.engines_ingest_fit_weights") as string).replace("{n}", String(weightsMiB))}
        {!image && !isLora ? ` · ${kvMiB ? (tr("admin.engines_ingest_fit_kv") as string).replace("{n}", String(kvMiB)).replace("{c}", String(contextTokens)) : tr("admin.engines_ingest_fit_kv_unread")}` : ""}
        {cardMiB ? ` · ${(tr("admin.engines_ingest_fit_card") as string).replace("{n}", String(needMiB)).replace("{c}", String(cardMiB))} ` : " "}
        <FitTag fit={fit} />
      </p>}
      {/* Directly under the verdict the files' sum produced, because that sum is what this
          corrects (ADR 0094 decision 8). The press itself is NOT here: nothing the ingest sends
          may write `vram_mib`, or the column stops meaning "the operator measured it" — so this
          says the number and where to put it, and the row's Edit is where it goes in. */}
      {ingestMeasured && <p className="muted engine-operation-vram-measured">
        {(tr("admin.engines_vram_measured" as never) as string)
          .replace("{n}", ingestMeasured.mib.toLocaleString()).replace("{s}", ingestMeasured.size)
          .replace("{b}", String(ingestMeasured.batch)).replace("{i}", String(ingestMeasured.inputs))
        .replace("{f}", ingestMeasured.file).replace("{c}", ingestMeasured.card)}
        {" "}{tr("admin.engines_vram_measured_after" as never)}
      </p>}
      <label className="engine-operation-check"><input type="checkbox" checked={accepted} onChange={(event) => setAccepted(event.currentTarget.checked)} /><span>{tr("admin.engines_ingest_accept")}</span></label>
      <details className="engine-operation-advanced"><summary>{tr("admin.catalog_advanced" as never)}</summary><div className="engine-operation-grid">
        <label><span>{tr("admin.engines_model_add_id")}</span><input value={id} onChange={(event) => { setIdEdited(true); setId(event.currentTarget.value); }} /></label>
        <label><span>{tr("admin.engines_model_add_desc")}</span><input value={description} onChange={(event) => setDescription(event.currentTarget.value)} /></label>
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
function CompleteDialog({ row, modelId, baseModel, answer, onClose, onRun }: {
  row: EngineRow;
  /** The id, not the row: 登録 opens this for a model created by the same press, which the
   *  catalogue in hand does not list until the reload lands. */
  modelId: string;
  baseModel?: string;
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

  return <Modal title={`${tr("admin.catalog_complete" as never)} — ${modelId}`} className="engine-complete" onClose={onClose}>
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
      {engineIsImage(row) && !!baseModel && <p className="muted">{tr("admin.catalog_family" as never)}: {baseModel}</p>}
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
    return <li key={`${part.flag || "whole"}:${part.s3Key}`}>
      <span className="mono engine-registered-part-flag">{part.flag || tr("admin.engines_model_add_part_whole")}</span>
      <span className="mono engine-registered-key">{part.s3Key}</span>
      {/* The size and the page in ONE span, at the right end of the flag's line. Loose children
          left the narrow card (`@container engcard`) to place them itself, and it scattered them
          down four rows — the size on its own line, the link on another. */}
      <span className="engine-registered-part-meta">
        {part.bytes ? <span>{formatBytes(part.bytes)}</span> : null}
        {/* `present` draws no chip. The card's header already counts the parts ("3/3 files
            present"), so a chip on every line states the same fact once per file — and a badge
            that is on every line is one nobody reads. Every other state keeps it, which is what
            makes a chip mean "this line needs a hand"; `unknown` in particular is the whole
            card's state when the bucket could not be listed, and must stay visible. */}
        {state !== "present" && <span className={`engines-model-tag ${state === "missing" || state === "failed" ? "bad" : ""}`}>{tr((`admin.catalog_file_${state}`) as never)}</span>}
        {part.source_url ? <a href={part.source_url} target="_blank" rel="noopener noreferrer">{tr("admin.catalog_source_page" as never)}</a> : part.source ? <span className="muted mono">{part.source}</span> : null}
      </span>
    </li>;
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
  // The same verdict the ingest form draws, on the row as it already is (ADR 0089). Without it
  // this dialog was three raw number boxes: an operator correcting a window had to price the KV
  // cache in their head, which is exactly what nobody should be asked to do — and is how a row
  // came to declare 262,144 on a card that holds a quarter of that.
  const editWeightsMiB = Math.round((model.file_rows || []).reduce((sum, f) => sum + (f.bytes || 0), 0) / 1048576);
  const editKvPer1k = !image && !lora ? model.kv_mib_per_1k_tokens || 0 : 0;
  const editCardMiB = lora ? 0 : row.class?.vram_mib || 0;
  const editFit = modelFit(editWeightsMiB, editKvPer1k, contextNumber || 0, editCardMiB, row.classes || []);
  // The largest window this card actually holds, offered as one press. 0 when it cannot be
  // said (no geometry, no card, or the weights alone already fill it) and the button is then
  // not drawn — an "auto" that quietly does nothing is worse than no button.
  // 0 when it cannot be said — no geometry, no card, no stored ceiling, or the weights alone
  // already fill the card — and the button is then not drawn. An offer computed without an
  // upper bound would propose windows the model was never trained for, and an "auto" that
  // quietly does nothing is worse than no button.
  const editBestWindow = !image && !lora
    ? windowThatFits(editWeightsMiB, editKvPer1k, editCardMiB, model.context_length || 0)
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
      {!editKvPer1k && <> {tr("admin.fit_no_kv" as never)}</>}
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
