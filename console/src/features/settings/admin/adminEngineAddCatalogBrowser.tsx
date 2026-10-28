/** The question a page of hits answers. Two lists are the same list when this is equal — which is
 *  what decides both where the scroll position is filed and whether a search starts at the top. */
function conditionOf(query: string, source: string, sort: string, family: string): string {
  return `${query}\u0000${source}\u0000${sort}\u0000${family}`;
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
