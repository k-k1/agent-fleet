import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Button, IconButton } from "../../../ui/Button.tsx";
import { Modal } from "../../../ui/Modal.tsx";
import { useScrollMemory } from "../../viewer/parts/useScrollMemory.ts";
import { EngineDiscoverPanel, engineCanDiscover } from "./adminEngineDiscover.tsx";
import { catalogKey, loadObjects as loadObjectsMemory, loadRegistered, saveObjects as saveObjectsMemory, saveRegistered } from "./catalogMemory.ts";
import { groupIsRepo, groupRegistered, registeredNeedsMeta, registeredTitle } from "./registeredGroups.ts";
import { CivitaiVersionLadder, RepoQuantLadder } from "./adminEngineRepo.tsx";
import { engineIsImage, type CompleteAnswer, type EngineApiError, type EngineModel, type EngineObjectRow, type EngineRow } from "./engineTypes.ts";
import { IngestPlanDialog } from "./adminEngineAddIngestPlan.tsx";
import { EngineLedger, inFlight } from "./adminEngineAddLedger.tsx";
import { RegisteredEditDialog } from "./adminEngineAddRegisteredEdit.tsx";
import { type CatalogProps, EngineRefusal, formatBytes, objectsPath, otherOf } from "./adminEngineAddShared.tsx";

/** Whether a `complete` answer has a question in it. Two things are worth a dialog and nothing
 * else is: a role the CP wants a pick for — which it asks even with ONE candidate, because
 * `--clip_l` / `--clip_g` / `--t5xxl` share a directory and the CP will not guess which of them a
 * loose encoder is — and bytes somebody has to agree to pay for. */
function needsAsking(answer: CompleteAnswer): boolean {
  return answer.action === "choose" || (answer.bytes_to_download || 0) > 0;
}

export function RegisteredCatalog({ row, kind, onKind, paneId, isSuper, readOnly, onChanged }: CatalogProps & { isSuper: boolean }) {
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
