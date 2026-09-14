import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { EngineDiscoverPanel, engineCanDiscover } from "./adminEngineDiscover.tsx";
import { ModelVaeFix, useVaeScan } from "./adminEngineVae.tsx";
import { openEngineAdd } from "./openEngineAdd.ts";
import { useSettingsUI } from "../store.ts";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { Button } from "../../../ui/Button.tsx";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import {
  engineIsImage,
  engineIsRemote,
  engineTitle,
  useEngineRows,
  type EngineModel,
  type EngineParams,
  type EngineRow,
  type EngineStorageFile,
  type IngestCandidate,
  type IngestHit,
  type IngestJob,
  type ResolvedSource,
} from "./engineTypes.ts";

// What an engine LOADS: the catalogue, the ingest that fills it, the upstream search that finds
// something to ingest, and the deployment's Hugging Face token.
//
// The other half of the panel — the mode, the box, the GPU rung, the uptime — is adminEngines.tsx
// and is about a machine. This screen is about model files, and it is the one a granted
// tenant_admin reaches (ADR 0072 open question 11): they may take a model in and may not buy a GPU.
//
// Two axes inside it, both drawn as tabs rather than as one long page:
//
//   - the ROLE (llm / image). They share no vocabulary at all — a GGUF has a context window, a
//     checkpoint has a family and a size list — and a deployment usually runs one of each, so
//     stacking both made a page where half of every form was about the other engine.
//   - MODEL vs LoRA. A LoRA is an accessory: it is never what an engine starts with, it declares
//     the family it was trained against, and the questions asked of it (which trigger words, what
//     strength) are asked of nothing else. Mixed into one list it was a row with a tag on it.

/** What a catalogue write answers with. The panel normally takes the engine's new row out of it,
 *  but a 409 `engine_vram_confirm` is a QUESTION rather than a failure: the same call repeated
 *  with `confirm_vram` goes through, and only the caller knows which call that was. */
type EngineModelAnswer = { error?: { code?: string; message?: string } } | undefined;

/** Writing one field of one catalogue row. Returns the answer so the caller can see the question
 *  above; every caller that has nothing to ask simply ignores it. */
type EngineModelChange = (id: string, patch: Record<string, unknown>) => Promise<EngineModelAnswer>;

export function EngineModelsAdminView({
  initialEngineKey = "",
  initialKind = "model",
  embedded = false,
}: {
  initialEngineKey?: string;
  initialKind?: ModelKind;
  embedded?: boolean;
} = {}) {
  const tr = useT();
  const { rows, isSuper, err, setErr, setRows, load } = useEngineRows();
  const closeAdmin = useSettingsUI((s) => s.closeAdmin);
  const [busy, setBusy] = useState("");
  /** Which JOB a request is in flight for. Its own state rather than `busy`: the two lists are
   *  loaded and refreshed independently, and one shared key would disable a model row because
   *  somebody pressed delete in the history below it. */
  const [busyJob, setBusyJob] = useState("");
  const [note, setNote] = useState("");
  /** 「モデルを追加」 opens as a PANE and this dialog gets out of the way (ADR 0072 follow-up).
   *
   * 🔴 Closing the admin dialog is part of the act, not a courtesy: a dialog renders ABOVE the
   * layout, so a pane opened from inside one is a screen nobody can see. What the operator is
   * doing next lives in the pane — including the enable press at the end of the download. */
  const openAdd = (v: boolean) => {
    if (!v) return;
    openEngineAdd(open.key, kind === "lora");
    closeAdmin();
  };
  const [jobs, setJobs] = useState<Record<string, IngestJob[]>>({});
  const [storage, setStorage] = useState<Record<string, EngineStorageFile[] | null>>({});
  /** Which engine's catalogue is open, by key. A key rather than an index so that a reload that
   *  reorders the list does not move somebody to another engine mid-ingest. */
  const [role, setRole] = useState(initialEngineKey);
  /** Models or adapters. It drives the LIST and the ingest form together, which is the point:
   *  the form used to carry its own model/LoRA selector while the search above it always asked
   *  for checkpoints, so choosing "LoRA" changed what the row would be registered as and
   *  nothing about what was on offer. */
  const [kind, setKind] = useState<ModelKind>(initialKind);
  useEffect(() => setRole(initialEngineKey), [initialEngineKey]);
  useEffect(() => setKind(initialKind), [initialKind]);
  /** The registration form, opened with a finished job's answers already in it.
   *
   * 🔴 "Take that file in again" is not an ingest: the bytes are in the bucket already (forget
   * a row without `?purge=1` and they stay — measured on the dev deployment, 2026-09-09: a
   * 491 MB object outlived its row), so this is `POST /models`, the route that registers a
   * staged file. What was missing was never the route: it was the LIST OF KEYS, which lived
   * only in the ingest history and had to be retyped from it by hand.
   *
   * It fills the form and stops there. The id and the family are questions for a person — one
   * is what members read in the launch menu and the other is the dispatch key a wrong answer
   * fails silently on — so the button opens the form rather than posting it. */
  const [prefill, setPrefill] = useState<ModelPrefill | null>(null);

  /** The ingest jobs for one engine. The CP reconciles against ECS inside this call, so asking
   *  is also what moves a finished job to `done` while somebody is watching. */
  const loadJobs = useCallback(async (key: string) => {
    const d = await api(`api/admin/engines/${encodeURIComponent(key)}/ingest`);
    if (!d?.error) setJobs((cur) => ({ ...cur, [key]: Array.isArray(d?.jobs) ? d.jobs : [] }));
  }, []);
  const loadStorage = useCallback(async (key: string) => {
    const d = await api(`api/admin/engines/${encodeURIComponent(key)}/storage`);
    setStorage((current) => ({
      ...current,
      [key]: d?.error ? null : Array.isArray(d?.files) ? d.files : [],
    }));
  }, []);
  useEffect(() => {
    (rows || []).forEach((e) => {
      loadJobs(e.key);
      loadStorage(e.key);
    });
  }, [rows, loadJobs, loadStorage]);
  // A download runs for minutes, so the list polls itself while one is in flight — and stops
  // the moment none is, because this is a screen somebody leaves open.
  useEffect(() => {
    const live = Object.entries(jobs).filter(([, js]) =>
      js.some((j) => j.state === "running" || j.state === "pending"),
    );
    if (live.length === 0) return;
    const t = setInterval(() => live.forEach(([key]) => loadJobs(key)), 10000);
    return () => clearInterval(t);
  }, [jobs, loadJobs]);

  // A job that has just finished created a catalogue row — disabled, and invisible until the
  // engine list is read again. Nothing else on this screen does that: the job poll above reads
  // only the job list. Measured on the dev deployment: the job reached "done" and the panel went
  // on showing the two models it already had, so the row an administrator has to enable was
  // reachable only by pressing refresh.
  const liveJobIds = useRef<Set<string>>(new Set());
  useEffect(() => {
    const live = new Set<string>();
    for (const [key, js] of Object.entries(jobs)) {
      for (const j of js) {
        if (j.state === "running" || j.state === "pending") live.add(key + "/" + j.id);
      }
    }
    // Only a transition out of "live" reloads. Failure counts too: it creates no row, but the
    // list is one request and re-reading it is cheaper than reasoning about which failures
    // could still have left one behind.
    let settled = false;
    liveJobIds.current.forEach((id) => {
      if (!live.has(id)) settled = true;
    });
    liveJobIds.current = live;
    if (settled) load();
  }, [jobs, load]);

  /** Enable / disable a model, make it the one the engine starts with, or correct one of its
   *  fields. The CP answers with the whole engine row, so the panel takes its new state from the
   *  server rather than guessing at the exclusivity rule — selecting one model clears another,
   *  and reproducing that here would be a second copy of a rule that has to be enforced in a
   *  transaction. */
  const setModel = async (key: string, id: string, patch: Record<string, unknown>) => {
    return callModel(key + "/" + id, `api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(id)}`, "PUT", patch, key);
  };

  const addModel = async (key: string, body: Record<string, unknown>) => {
    return callModel(key + "/+", `api/admin/engines/${encodeURIComponent(key)}/models`, "POST", body, key);
  };

  /** Forget the row, and optionally the bytes with it.
   *
   * 🔴 The two really are separate acts and the panel has to offer both. Forgetting alone
   * leaves the file in the bucket paying for itself with nothing able to reach it — measured on
   * the dev deployment (2026-09-09): a 491 MB row was forgotten and the object was still there.
   * `?purge=1` is what ADR 0072 decision 7 built the MODE=delete task for, and until now the
   * Console had no way to ask for it. The CP answers with what it started, which is worth
   * showing: the deletion is a Fargate task, not something that has already happened. */
  const forgetModel = async (key: string, id: string, purge: boolean) => {
    const q = purge ? "?purge=1" : "";
    await callModel(
      key + "/" + id,
      `api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(id)}${q}`,
      "DELETE",
      undefined,
      key,
    );
  };

  /** Forget one row of the ingest history.
   *
   * 🔴 The history is not only a progress display: until a catalogue row points at the key a
   * `done` job wrote, that row is the only place this deployment records that the file is in
   * the bucket — the CP cannot list it (ADR 0072 review R3). So the answer is taken from the
   * SERVER's remaining list rather than by dropping the row locally: the CP refuses a job that
   * is still running (409), and a panel that had already removed it would show the deletion it
   * did not get. */
  const forgetJob = async (key: string, id: string) => {
    setBusyJob(id);
    try {
      const d = await apiJSON(
        `api/admin/engines/${encodeURIComponent(key)}/ingest/${encodeURIComponent(id)}`,
        "DELETE",
      );
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setJobs((cur) => ({ ...cur, [key]: Array.isArray(d?.jobs) ? d.jobs : [] }));
    } finally {
      setBusyJob("");
    }
  };

  /** Open the registration form on a finished job's key.
   *
   * The TAB moves with it, because model and LoRA are two lists with two forms and the job says
   * which one it was: a LoRA prefilled into the checkpoint form would be registered as a
   * checkpoint, which is a row the engine can be told to start with and cannot load. */
  const reuseJobKey = (j: IngestJob) => {
    setKind(j.kind === "lora" ? "lora" : "model");
    setPrefill({
      id: j.model_id,
      s3Key: j.s3_key || "",
      flag: j.file_flag || "",
      source: j.source || "",
      usedBy: j.key_used_by || "",
    });
  };

  const callModel = async (
    busyKey: string,
    path: string,
    method: string,
    body: unknown,
    key: string,
  ): Promise<EngineModelAnswer> => {
    setBusy(busyKey);
    try {
      const d = await apiJSON(path, method, body);
      if (d?.error) {
        setErr(errDetail(d.error));
        // ...and handed BACK, because one refusal is a question rather than a failure: a 409
        // `engine_vram_confirm` is answered by repeating the same call, and only the caller
        // knows which call that was.
        return d;
      }
      setErr("");
      // What the CP started, in its own words ("deleting llm/x.gguf", or why it could not).
      // Deleting the bytes is a task that has been LAUNCHED, and saying so beats a row that
      // simply vanishes while gigabytes stay behind.
      setNote(typeof d?.purge === "string" ? d.purge : "");
      setRows((cur) => (cur || []).map((e) => (e.key === key ? { ...e, ...d } : e)));
      return d;
    } finally {
      setBusy("");
    }
  };

  // The headers nobody has read yet, read once. It runs on the panel's own load rather than
  // behind a button because the question is not one an operator should have to know to ask:
  // until it is answered, a checkpoint that can never generate looks exactly like one that can.
  //
  // ⚠️ Above the early returns below, like every other hook here: React counts them per render.
  const vaeUnreadable = useVaeScan(rows || [], load);

  if (rows === null) return <p className="muted pad">{tr("common.loading")}</p>;

  // 🔴 "There is nothing here" is the worst possible answer to "what could I run?". Looking at
  // what Hugging Face and Civitai hold needs no engine — no token, no bucket, no task — so this
  // screen is useful on a deployment that has adopted nothing, and it says what it cannot do
  // rather than pretending (ADR 0072 decision 11).
  if (rows.length === 0) {
    return (
      <div className="admin-stage">
        <section className="admin-panel">
          <p className="muted">{tr("admin.engines_none")}</p>
          <EngineBrowse />
        </section>
      </div>
    );
  }

  const open = rows.find((e) => e.key === role) || rows[0];
  const isImage = engineIsImage(open);
  /** 🔴 A borrowed catalogue is a MIRROR of the far deployment's, refreshed on a poll, and every
   *  write route here answers 400 `engine_not_ours` (ADR 0079 decision 7). So this screen shows
   *  the rows and offers none of the controls — the same shape, and for the same reason, as the
   *  reduced panel a granted tenant_admin gets: a button that can only produce an error message
   *  is worse than no button, and a disabled one invites an email asking to have it enabled.
   *  What replaces them is the sentence below, which names the deployment to go and edit it on. */
  const borrowed = engineIsRemote(open);

  return (
    <div className="admin-stage">
      {/* What a granted tenant_admin may do here, said once at the top (ADR 0072 open question
          11). Without it the reduced panel reads as a broken operator panel — the controls are
          not disabled, they are absent — and the one thing that has to be understood before
          taking a model in is that the catalogue is shared with every other tenant. */}
      {!isSuper && <p className="admin-hint pad">{tr("admin.engines_tenant_scope")}</p>}
      <section className="admin-panel">
        <div className="usage-toolbar">
          {/* The ROLE, as tabs — but only when there is a choice to make. A deployment with one
              engine gets its name and no tab strip, because a single tab is a control that
              cannot be operated. */}
          {!embedded && rows.length > 1 ? (
            <span className="seg sm">
              {rows.map((e) => (
                <button
                  key={e.key}
                  type="button"
                  className={"seg-btn" + (open.key === e.key ? " active" : "")}
                  onClick={() => {
                    setRole(e.key);
                    setPrefill(null);
                  }}
                >
                  {tr(engineIsImage(e) ? "admin.engines_role_image" : "admin.engines_role_llm")}
                </button>
              ))}
            </span>
          ) : !embedded ? (
            <span>{engineTitle(open)}</span>
          ) : null}
          {/* Models or adapters. Both roles have both: an image LoRA is chosen per request by
              family, and the llm role's is pinned to a model through a preset (ADR 0072
              decision 5). */}
          <span className="seg sm">
            {(["model", "lora"] as const).map((k) => (
              <button
                key={k}
                type="button"
                className={"seg-btn" + (kind === k ? " active" : "")}
                // A tab pressed BY HAND drops a pending prefill. Without this, switching to the
                // LoRA list minutes later reopens the form still holding the checkpoint whose
                // key was picked out of the history — an id and an S3 key nobody chose there.
                onClick={() => {
                  setKind(k);
                  setPrefill(null);
                }}
              >
                {tr(k === "lora" ? "admin.engines_tab_loras" : "admin.engines_tab_models")}
              </button>
            ))}
          </span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        {/* Which engine these rows belong to, when the tabs above say only "image". The key is
            what every error message and every S3 prefix uses. */}
        {rows.length > 1 && <p className="muted engines-role-name mono">{engineTitle(open)}</p>}
        {/* Why the controls below are missing, said before the list rather than after a 400. The
            far fleet's base URL goes with it because "edit it over there" is not actionable
            without naming which deployment — it is the same fact the machine screen prints, and
            an operator arriving here may not have been on that screen. */}
        {borrowed && (
          <p className="admin-hint pad">
            {tr("admin.engines_remote_catalog")}
            {open.url ? <span className="mono"> {open.url}</span> : null}
          </p>
        )}
        <EngineModels
          key={open.key + "/" + kind}
          row={open}
          kind={kind}
          busy={busy}
          readOnly={!isSuper || borrowed}
          prefill={prefill}
          onChange={(id, patch) => setModel(open.key, id, patch)}
          onForget={(id, purge) => forgetModel(open.key, id, purge)}
          onAdd={(body) => addModel(open.key, body)}
          onReload={load}
          vaeUnreadable={vaeUnreadable}
          storageFiles={storage[open.key]}
        />
        {/* The discovery button (ADR 0082 decisions 6 and 7): only for an external ComfyUI this
            control plane can dial directly. `engineCanDiscover` is the exact predicate the CP's
            own route gates on, so a row that would 400 there never shows the button here. */}
        {isSuper && engineCanDiscover(open) && (
          <EngineDiscoverPanel
            key={"discover/" + open.key}
            engineKey={open.key}
            busy={busy === open.key + "/+"}
            onAdd={(body) => addModel(open.key, body)}
          />
        )}
        {/* The ingest is a write too — `POST /ingest` is one of the five routes that answer 400
            for a borrowed role — and it is also the one that would spend money and bucket space
            on a file the far engine is never going to load: the box that stages files is the far
            deployment's active set, not ours. */}
        {!embedded && !borrowed && (
          <EngineIngest
            open={false}
            setOpen={openAdd}
            key={"ingest/" + open.key + "/" + kind}
            engineKey={open.key}
            isImage={isImage}
            isLora={kind === "lora"}
            baseModels={open.base_models}
            fileFlags={open.file_flags}
            models={open.model_rows}
            cardMiB={open.class?.vram_mib}
            busy={busy === open.key + "/ingest"}
            onStarted={() => loadJobs(open.key)}
          />
        )}
        <EngineIngestJobs
          jobs={jobs[open.key] || []}
          busy={busyJob}
          readOnly={borrowed}
          onForget={(id) => forgetJob(open.key, id)}
          // Registering a staged file is a super_admin's act, as it always was: it names an S3
          // key in the operator's bucket. A granted tenant_admin sees the history and may forget
          // their own rows, and gets no button that would 403. (A borrowed engine needs no test
          // here — `readOnly` above hides every action on that screen.)
          onReuse={isSuper ? reuseJobKey : undefined}
        />
      </section>
      {/* The deployment's Hugging Face token. One token serves every role and every tenant, so
          registering it is the operator's act; a tenant_admin who needs a gated repository asks
          for it (the ingest form already says so when a gated source is resolved). */}
      {isSuper && <HfTokenPanel />}
      {err && <p className="form-err pad">{err}</p>}
      {note && <p className="muted pad">{note}</p>}
    </div>
  );
}

/** Which half of a catalogue is on screen. A LoRA is an accessory rather than a model — never
 *  what an engine starts with, and pinned to a FAMILY rather than to a checkpoint — so the two
 *  lists answer different questions and the tab decides which one is being asked. */
export type ModelKind = "model" | "lora";

/** The model catalogue for one engine (ADR 0072 decision 7).
 *
 * Two controls per row and they are NOT the same question:
 *
 *   - enable/disable = "may this deployment use it at all". A disabled model is not synced onto
 *     the box and not offered to any session.
 *   - select = "this is the one the engine starts with". The image role holds ONE checkpoint,
 *     chosen by a startup flag, so exactly one row carries it; the llm role's equivalent is the
 *     model a request that named none gets.
 *
 * ⚠️ Selecting does NOT restart a running engine, and the note says so. The box holds one
 * checkpoint chosen at start, so the change lands at the next start (ADR 0072 decision 4) —
 * redeploying the service instead would kill whatever generation is in flight, and the person
 * pressing this button has not been asked about that. */
/** The catalogue list.
 *
 * `readOnly` is the reduced panel a granted tenant_admin sees (ADR 0072 open question 11): the
 * same rows, none of the controls. Enabling a model, choosing what the engine starts with and
 * forgetting a row all decide what EVERY OTHER TENANT runs off one shared box, so they stay the
 * operator's — and the CP refuses them anyway, which is the reason not to draw them disabled.
 *
 * `kind` splits it in two. A LoRA used to be a row in this list with a tag on it, which read as
 * "a model, but smaller" — and then half the controls beside it were absent (an engine is never
 * started with an adapter) and the one fact that decides whether it works at all, the FAMILY it
 * was trained against, was one item in a "·"-joined meta line. */
function EngineModels({
  row,
  kind,
  busy,
  readOnly,
  prefill,
  onChange,
  onForget,
  onAdd,
  onReload,
  vaeUnreadable,
  storageFiles,
}: {
  row: EngineRow;
  kind: ModelKind;
  busy: string;
  readOnly?: boolean;
  /** Answers taken out of a finished ingest job, for the registration form below the list.
   *  Passed through rather than held here: the history that produces it is a sibling of this
   *  component, not a child. */
  prefill?: ModelPrefill | null;
  onChange: EngineModelChange;
  onForget: (id: string, purge: boolean) => void;
  onAdd: (body: Record<string, unknown>) => void;
  /** Re-read the catalogue. Needed by the acts that change a row WITHOUT going through
   *  `onChange` — giving a row the VAE its checkpoint lacks writes a file, not a field. */
  onReload: () => void;
  /** Rows whose header the scan could not read, with the upstream's own words. A row in here is
   *  one the deployment could not ask about — said out loud, because silence there looks exactly
   *  like a healthy row. */
  vaeUnreadable?: Record<string, string>;
  /** Actual S3 existence from the dedicated check. Null/undefined is unknown, never missing. */
  storageFiles?: EngineStorageFile[] | null;
}) {
  const tr = useT();
  const wantLora = kind === "lora";
  const all = row.model_rows || [];
  const models = all.filter((m) => (m.kind === "lora") === wantLora);
  const isImage = row.api === "images";
  // Only a provider that dispatches on the family declares one, and only then is there
  // anything to choose between (see EngineRow.base_models).
  const families = row.base_models || [];
  // Which row is mid-confirm, and whether the bytes go too. Deleting gigabytes is not something
  // a single click should do, and "forget the row" and "delete the file" have to be told apart
  // BEFORE the press rather than explained afterwards.
  const [confirming, setConfirming] = useState("");
  const [purge, setPurge] = useState(false);
  // Which model is waiting on "yes, I know it may not fit" (ADR 0074 decision 6). The dialog is
  // raised HERE, before the request, because this is where the numbers are — the CP refuses the
  // unconfirmed call as well, for any other client.
  // `message` is the CP's own sentence, present only when the SERVER raised the question. An
  // edit to the window cannot be judged here: the KV geometry the new demand is computed from is
  // not on the wire, and a second copy of that formula in TypeScript would drift from the CP's
  // the first time either changed (ADR 0074 open question 7).
  const [vramAsk, setVramAsk] = useState<{
    id: string;
    patch: Record<string, unknown>;
    message?: string;
  } | null>(null);
  const cardMiB = row.class?.vram_mib || 0;
  /** True when this model's own demand is known AND larger than the card. `unknown` is not
   *  "too big": asking about every unmeasured model teaches people to click through the one
   *  that matters. */
  const tooBig = (m: EngineModel) =>
    cardMiB > 0 && m.kind !== "lora" && !!m.vram_need_mib && m.vram_need_mib > cardMiB;
  const change = (m: EngineModel, patch: Record<string, boolean | string>) => {
    const loading = !!(patch.enabled || patch.selected || patch.default);
    if (loading && tooBig(m)) {
      setVramAsk({ id: m.id, patch });
      return;
    }
    onChange(m.id, patch);
  };
  /** Save an edited FIELD, and turn the CP's "this may not fit" into the same question the
   *  enable button asks. Asked after the request rather than before it, unlike `change` above:
   *  what a new window costs is the CP's arithmetic, and the panel learns the answer by being
   *  refused. */
  const saveField = async (m: EngineModel, patch: Record<string, unknown>) => {
    const d = await onChange(m.id, patch);
    if (d?.error?.code === "engine_vram_confirm") {
      setVramAsk({ id: m.id, patch, message: d.error.message });
    }
  };
  return (
    <div className="engines-models">
      {/* An engine with no catalogue at all is the interesting case, not an empty section: the
          controller refuses to start it and every request is refused, so it gets a sentence
          rather than a blank area that reads as "still loading". */}
      {/* 🔴 Two different empties. No MODEL at all means the controller refuses to start this
          engine and every request is refused — that is an error. No LoRA is an ordinary state
          of a working deployment, and drawing it in red would make the adapter tab look broken
          on every deployment that has never wanted one. */}
      {models.length === 0 && (
        <p className={wantLora ? "muted" : "form-err"}>
          {tr(wantLora ? "admin.engines_loras_empty" : "admin.engines_catalog_empty")}
        </p>
      )}
      {/* "Nothing is enabled" is addressed to whoever can enable something. The reduced row does
          not carry has_models at all, so this is also the field that must not be read as false.
          Only about models: has_models counts non-LoRA rows, so on the adapter tab it is an
          answer to a question this list is not asking. */}
      {!readOnly && !wantLora && models.length > 0 && !row.has_models && (
        <p className="form-err">{tr("admin.engines_catalog_none_enabled")}</p>
      )}
      <ul className="engines-model-list">
        {models.map((m) => {
          const started = !!(m.selected || m.default);
          const pending = busy === row.key + "/" + m.id;
          const isLora = m.kind === "lora";
          return (
            <li key={m.id} className={m.enabled ? "engines-model on" : "engines-model"}>
              <div className="engines-model-head">
                <span className="mono engines-model-id">{m.id}</span>
                {/* On or off is stated, never left to the label of the button that would
                    change it — that label says the OPPOSITE of the state it describes. Dimming
                    the row instead is what a disabled control looks like (admin.css). */}
                <span className={m.enabled ? "engines-model-tag on" : "engines-model-tag off"}>
                  {tr(m.enabled ? "admin.engines_model_is_on" : "admin.engines_model_is_off")}
                </span>
                {started && (
                  <span className="engines-model-tag lead">{tr("admin.engines_model_started")}</span>
                )}
                {/* 🔴 The tag that used to say "LoRA" is gone: the LIST says it now. What an
                    adapter's row has to say instead is the family it was trained against —
                    paired with anything else the provider refuses it, and a row that declares
                    none is refused with everything (ADR 0072 decision 5). */}
                {isLora && (
                  <span className={"engines-model-tag" + (m.base_model ? "" : " warn")}>
                    {m.base_model || tr("admin.engines_lora_no_family")}
                  </span>
                )}
                {/* 🔴 In the head, not in the meta line: this is the one fact on the row that
                    can make offering the model somebody's own breach, and it has to be legible
                    without reading a licence name nobody recognises. The ingest form says the
                    same thing before the bytes are fetched (ADR 0072 decision 10); a row that
                    arrived before that check existed, or by hand, says it here. */}
                {m.commercial_use === "no" && (
                  <span className="engines-model-tag warn">{tr("admin.engines_model_noncommercial")}</span>
                )}
                <span className="engines-model-actions">
                  {/* "Start with this one" leads: it is the thing somebody came to this list to
                      do, and it implies the enable behind it. A LoRA is never what an engine is
                      started with, so the control that would say so is not offered for one. */}
                  {!readOnly && !isLora && !started && (
                    <button
                      type="button"
                      className="sm"
                      disabled={pending}
                      onClick={() => change(m, isImage ? { selected: true } : { default: true })}
                    >
                      {tr("admin.engines_model_select")}
                    </button>
                  )}
                  {!readOnly && (
                    <button
                      type="button"
                      className="sm"
                      disabled={pending}
                      onClick={() => change(m, { enabled: !m.enabled })}
                    >
                      {tr(m.enabled ? "admin.engines_model_disable" : "admin.engines_model_enable")}
                    </button>
                  )}
                  {/* Forgetting the ROW. The file stays in the bucket — the CP has no
                      s3:DeleteObject and is not getting one (ADR 0072 decision 7) — so the
                      label says "forget", not "delete", and the note below says why. */}
                  {!readOnly && (
                    <button
                      type="button"
                      className="sm danger"
                      disabled={pending || started}
                      onClick={() => {
                        setConfirming(m.id);
                        setPurge(false);
                      }}
                    >
                      {tr("admin.engines_model_forget")}
                    </button>
                  )}
                </span>
              </div>
              {m.description && <p className="muted engines-model-desc">{m.description}</p>}
              <p className="muted engines-model-meta">{engineModelMeta(m, tr)}</p>
              <ModelProvenance model={m} />
              <ModelStorageStatus model={m} storageFiles={storageFiles} />
              {/* 🔴 The one thing wrong with this row that nothing else on it shows. Every other
                  field is filled in, the toggle works, the id appears in generate_image's model
                  list — and the request fails, because the provider will not guess a workflow
                  from a name. A seeded row is always in this state: the seed cannot know. */}
              {!readOnly && m.base_model_missing && (
                <>
                  <p className="form-err">{tr("admin.engines_model_no_family")}</p>
                  {/* The fix, right where the problem is stated. Before this the only way to
                      give a row a family was to register the WHOLE row again — which for a
                      split model means re-typing three S3 keys to change one word, and lands
                      it disabled. The VRAM guard does not apply: declaring a family puts
                      nothing on the card. */}
                  {families.length > 0 && (
                    <label className="engines-model-family">
                      <span>{tr("admin.engines_model_add_family")}</span>
                      <select
                        value={m.base_model || ""}
                        disabled={pending}
                        onChange={(ev) => onChange(m.id, { base_model: ev.currentTarget.value })}
                      >
                        <option value="">{tr("admin.engines_model_add_family_pick")}</option>
                        {families.map((f) => (
                          <option key={f} value={f}>
                            {f}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                </>
              )}
              {/* 🔴 The other half of "this row cannot generate", and the one declaring a
                  family used to hide. The parts are named because taking them in and attaching
                  them to this row is exactly what has to happen next; enabling is refused
                  until then, so the sentence is not advice. */}
              {!readOnly && !!m.files_missing?.length && (
                <p className="form-err">
                  {(tr("admin.engines_model_files_missing") as string)
                    .replace("{n}", m.base_model || "")
                    .replace(
                      "{f}",
                      m.files_missing
                        .map((f) => (f === "" ? (tr("admin.engines_model_add_part_whole") as string) : f))
                        .join(", "),
                    )}
                </p>
              )}
              {/* 🔴 And the fault no declaration can express: the checkpoint file itself carries
                no VAE, so the family's workflow has nothing to decode with. Stated with the fix
                attached rather than as advice — by hand it is a search, an ingest under the
                right role and a retyped id, which is the road the person who owns this
                deployment walked once before this button existed. */}
            {!readOnly && (m.vae_missing || !!vaeUnreadable?.[m.id]) && (
              <ModelVaeFix
                engineKey={row.key}
                model={m}
                pending={pending}
                unreadable={vaeUnreadable?.[m.id]}
                onDone={onReload}
              />
            )}
            {/* What this row asks to be RUN at, and the way to change it. Read-only it is one
                  line; the operator gets the same six fields the ingest form filled in, because
                  "what the author suggested" and "what this deployment decided" have to be the
                  same six questions or the second cannot correct the first. */}
              {isImage && (
                <EngineModelParams
                  model={m}
                  pending={pending}
                  readOnly={!!readOnly}
                  onSave={(p) => onChange(m.id, { params: p })}
                />
              )}
              {/* What THIS checkpoint should never draw, which its publisher usually states and
                  nothing else here could know. Its own control rather than a seventh field in
                  the parameters editor above: it is a different column, saved on its own, and
                  the two answer different questions — how to run the model, and what not to ask
                  it for. Image rows only, and never a LoRA: a request names a checkpoint. */}
              {!readOnly && isImage && m.kind !== "lora" && (
                <ModelNegative model={m} pending={pending} onChange={onChange} />
              )}
              {/* The words this adapter answers to (ADR 0081 decision 5). The exact mirror of
                  the negative prompt above and on the opposite rows: a checkpoint has no
                  trigger, and an adapter loaded without one changes nothing visible — which is
                  indistinguishable from an ingest that failed. Ingest fills it from what
                  Civitai published, and plenty of publishers write a word their files do not
                  actually use, so correcting it has to be possible here. */}
              {!readOnly && isImage && m.kind === "lora" && (
                <ModelTriggerWords model={m} pending={pending} onChange={onChange} />
              )}
              {/* 🔴 The window, and the measurement the VRAM answer is compared against. This is
                  the one pair on the row whose wrong value is paid for in cash: measured on a
                  borrowed llm engine (ADR 0079, 2026-09-13), a row still declaring 262144 tokens
                  made llama.cpp ask for a 16 GiB KV cache on top of 17 GB of weights and the L4
                  that had just been bought answered `cudaMalloc failed: out of memory` — four
                  minutes into the cold start, with no field anywhere to correct it.

                  Never a LoRA (an adapter is not loaded with a window of its own), and the
                  context fields only where a window means something. Absent read-only for the
                  reason the rest of the controls are: every write route answers 400 for a
                  borrowed row, and a button that can only produce an error is worse than none. */}
              {!readOnly && !isLora && (
                <ModelWindow
                  model={m}
                  pending={pending}
                  showContext={!isImage}
                  onSave={(patch) => saveField(m, patch)}
                />
              )}
              {/* 🔴 The two acts, told apart. Forgetting alone leaves the bytes in the bucket
                  with nothing able to reach them (measured: a 491 MB file outlived its row);
                  purging starts the MODE=delete task ADR 0072 decision 7 exists for, because
                  the CP has no s3:DeleteObject and is not getting one. */}
              {/* ⚠️ A warning, not a refusal — quantisation, --offload-to-cpu and things this
                  deployment has not measured are real, so the panel points and the person
                  decides (the position ADR 0072 decision 10 takes on licences). */}
              {vramAsk?.id === m.id && (
                <div className="engines-model-confirm">
                  {/* The CP's own sentence when the CP is the one that asked. It names the demand
                      of the row as EDITED, which the numbers on this row cannot: they describe
                      the window that is still stored. */}
                  <p className="form-err">
                    {vramAsk.message ||
                      (tr("admin.engines_vram_confirm" as never) as string)
                        .replace("{id}", m.id)
                        .replace("{n}", String(m.vram_need_mib || 0))
                        .replace("{m}", String(cardMiB))
                        .replace(
                          "{src}",
                          tr(("admin.engines_vram_src_" + (m.vram_need_source || "unknown")) as never) as string,
                        )}
                  </p>
                  <span className="engines-model-actions">
                    <button
                      type="button"
                      className="primary sm"
                      disabled={pending}
                      onClick={() => {
                        const ask = vramAsk;
                        setVramAsk(null);
                        onChange(ask.id, { ...ask.patch, confirm_vram: true });
                      }}
                    >
                      {tr("admin.engines_vram_confirm_go")}
                    </button>
                    <button type="button" className="sm" onClick={() => setVramAsk(null)}>
                      {tr("common.cancel")}
                    </button>
                  </span>
                </div>
              )}
              {confirming === m.id && (
                <div className="engines-model-confirm">
                  <label>
                    <input
                      type="checkbox"
                      checked={purge}
                      onChange={(ev) => setPurge(ev.currentTarget.checked)}
                    />
                    <span>{tr("admin.engines_model_forget_purge")}</span>
                  </label>
                  <p className="muted">
                    {tr(purge ? "admin.engines_model_forget_purge_note" : "admin.engines_model_forget_note")}
                  </p>
                  {/* 🔴 The keys, HERE and nowhere else on the row. This is the one moment they
                      are needed: since P6 the catalogue is the only declaration there is, so
                      forgetting a row is the last time anyone can read what it was — and with
                      purge ticked, this is also the list of objects the delete task is handed.
                      A row's meta line stays base names; a split model is four paths and would
                      push every row to four lines for a value nobody reads while choosing. */}
                  {!!m.file_rows?.length && (
                    <ul className="engines-model-keys mono">
                      {m.file_rows.map((f) => (
                        <li key={f.s3Key}>
                          {f.flag ? f.flag + " " : ""}
                          {f.s3Key}
                        </li>
                      ))}
                    </ul>
                  )}
                  <span className="engines-model-actions">
                    <button
                      type="button"
                      className={purge ? "primary sm" : "sm"}
                      disabled={pending}
                      onClick={() => {
                        setConfirming("");
                        onForget(m.id, purge);
                      }}
                    >
                      {tr("admin.engines_model_forget_go")}
                    </button>
                    <button type="button" className="sm" onClick={() => setConfirming("")}>
                      {tr("common.cancel")}
                    </button>
                  </span>
                </div>
              )}
            </li>
          );
        })}
      </ul>
      {/* "Re-selecting takes effect at the next start" is about a control this panel only
          offers the operator. Rendered read-only it describes a button that is not there.
          🔴 Found by rendering the reduced panel, not by a test — every assertion was about
          what must be ABSENT, and a leftover sentence is neither a control nor a field. The
          same reasoning excludes the LoRA tab: an engine is never STARTED with an adapter, so
          the button this sentence explains is not offered there either. */}
      {!readOnly && !wantLora && <p className="muted">{tr("admin.engines_model_next_start")}</p>}
      {/* Registering a file that is ALREADY in the bucket. It needs an S3 key somebody put
          there by hand, which is an operator act on an operator's bucket — the tenant route
          into the catalogue is the ingest below, which fetches. */}
      {!readOnly && (
        <EngineModelAdd
          busy={busy === row.key + "/+"}
          isImage={isImage}
          isLora={wantLora}
          baseModels={row.base_models}
          fileFlags={row.file_flags}
          modelIds={(row.model_rows || []).filter((m) => m.kind !== "lora").map((m) => m.id)}
          prefill={prefill}
          onAdd={onAdd}
        />
      )}
    </div>
  );
}

/** One row's generation parameters: a line when there is something to say, and an editor behind
 *  a button for the operator.
 *
 * 🔴 Saving `{}` is how a declaration is REMOVED. Without a way back, a number typed once (or
 * read out of an author's prose once) is what that model runs at for ever, and the only escape
 * would be forgetting the row and taking the file in again. */
/** One model row's own negative prompt (ADR 0072 follow-up, negative prompts). A draft with an
 *  explicit save, like the engine-wide box on the machine panel, and empty is a REAL value: it
 *  means "stop declaring one", which the Agent answers with its own measured default rather than
 *  with nothing excluded. */
function ModelNegative({
  model,
  pending,
  onChange,
}: {
  model: EngineModel;
  pending: boolean;
  onChange: (id: string, patch: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const saved = model.negative_prompt || "";
  const [draft, setDraft] = useState(saved);
  // The server's value wins when it changes under us (another admin, a reload) and only then:
  // re-running this on every render would delete what is being typed.
  useEffect(() => setDraft(saved), [saved]);
  return (
    // Its own class, not the family picker's: a test counts the family rows to prove the panel
    // offers exactly one fix for exactly one broken row, and a second element wearing that name
    // would make that check pass for the wrong reason.
    <div className="engines-model-negative">
      <span>{tr("admin.engines_model_negative")}</span>
      <input
        type="text"
        value={draft}
        placeholder={tr("admin.engines_model_negative_placeholder") as string}
        onChange={(ev) => setDraft(ev.currentTarget.value)}
      />
      <button
        type="button"
        className="btn-secondary"
        disabled={pending || draft === saved}
        onClick={() => onChange(model.id, { negative_prompt: draft.trim() })}
      >
        {tr("admin.engines_negative_save")}
      </button>
    </div>
  );
}

/** One adapter's trigger words (ADR 0081 decision 5). A draft with an explicit save, like
 *  ModelNegative next door.
 *
 *  Comma-separated in the box and a LIST on the wire: the column is an array because Civitai
 *  publishes an array and the image generation pane offers one chip per word, but a person
 *  editing three short words wants one field and not three. The split happens here, once —
 *  blank entries are dropped on both sides, because a trailing comma is how every such box is
 *  typed and an empty chip is a trigger nobody can remove.
 *
 *  An empty box is a REAL value: "this adapter has no trigger", which is the only way back from
 *  a word the publisher recorded and the files do not use. */
function ModelTriggerWords({
  model,
  pending,
  onChange,
}: {
  model: EngineModel;
  pending: boolean;
  onChange: (id: string, patch: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const saved = (model.trained_words || []).join(", ");
  const [draft, setDraft] = useState(saved);
  // The server's value wins when it changes under us (another admin, a reload) and only then:
  // re-running this on every render would delete what is being typed.
  useEffect(() => setDraft(saved), [saved]);
  return (
    <div className="engines-model-trigger">
      <span>{tr("admin.engines_model_trigger")}</span>
      <input
        type="text"
        value={draft}
        placeholder={tr("admin.engines_model_trigger_placeholder") as string}
        onChange={(ev) => setDraft(ev.currentTarget.value)}
      />
      <button
        type="button"
        className="btn-secondary"
        disabled={pending || draft === saved}
        onClick={() => onChange(model.id, { trained_words: splitTriggerWords(draft) })}
      >
        {tr("admin.engines_negative_save")}
      </button>
    </div>
  );
}

/** The box's text as the list the wire carries. Exported for its own test: "a, b," and "a,b"
 *  and " a , b " all have to become the same two words, and that is the whole of what the box
 *  promises. */
export function splitTriggerWords(s: string): string[] {
  return s
    .split(",")
    .map((w) => w.trim())
    .filter((w) => w !== "");
}

/** A non-negative whole number, or null for "this box does not hold one". The empty box IS a
 *  number — 0, i.e. undeclared — because that is the only way back from a value typed once. */
function engineWholeNumber(s: string): number | null {
  const t = s.trim();
  if (t === "") return 0;
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return Number.isSafeInteger(n) ? n : null;
}

/** The window this row asks to be run at, and the VRAM somebody measured it using.
 *
 * A draft with an explicit save, like ModelNegative next door, and two saves rather than one:
 * they are two columns the CP writes separately, and the window's own two boxes go TOGETHER
 * because the row only ever answers a cap alongside a window.
 *
 * 🔴 What the demand line shows comes from the server (`vram_need_mib` / `vram_need_source`) and
 * is never recomputed here. The KV geometry it is derived from is not on the wire at all, and a
 * second copy of that arithmetic in TypeScript would disagree with the CP's the first time
 * either moved — on the one number a person is looking at while deciding. */
function ModelWindow({
  model,
  pending,
  showContext,
  onSave,
}: {
  model: EngineModel;
  pending: boolean;
  showContext: boolean;
  onSave: (patch: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const savedCtx = String(model.context_tokens || 0);
  const savedOut = String(model.max_output_tokens || 0);
  const savedVram = String(model.vram_mib || 0);
  const [ctxDraft, setCtxDraft] = useState(savedCtx);
  const [outDraft, setOutDraft] = useState(savedOut);
  const [vramDraft, setVramDraft] = useState(savedVram);
  // The server's values win when they change under us — the save answered with the whole engine,
  // another admin, a reload — and only then: re-running this on every render would delete what is
  // being typed.
  useEffect(() => {
    setCtxDraft(savedCtx);
    setOutDraft(savedOut);
  }, [savedCtx, savedOut]);
  useEffect(() => setVramDraft(savedVram), [savedVram]);
  const ctxNum = engineWholeNumber(ctxDraft);
  const outNum = engineWholeNumber(outDraft);
  const vramNum = engineWholeNumber(vramDraft);
  const windowDirty = ctxDraft !== savedCtx || outDraft !== savedOut;
  const need = model.vram_need_mib || 0;
  return (
    <div className="engines-model-window">
      {showContext && (
        <>
          <label>
            <span>{tr("admin.engines_model_window_context")}</span>
            <input
              type="text"
              inputMode="numeric"
              value={ctxDraft}
              onChange={(ev) => setCtxDraft(ev.currentTarget.value)}
            />
          </label>
          <label>
            <span>{tr("admin.engines_model_window_output")}</span>
            <input
              type="text"
              inputMode="numeric"
              value={outDraft}
              onChange={(ev) => setOutDraft(ev.currentTarget.value)}
            />
          </label>
          <button
            type="button"
            className="btn-secondary"
            disabled={pending || !windowDirty || ctxNum === null || outNum === null}
            onClick={() => onSave({ context_tokens: ctxNum, max_output_tokens: outNum })}
          >
            {tr("admin.engines_model_window_save")}
          </button>
        </>
      )}
      <label>
        <span>{tr("admin.engines_model_vram_edit")}</span>
        <input
          type="text"
          inputMode="numeric"
          value={vramDraft}
          onChange={(ev) => setVramDraft(ev.currentTarget.value)}
        />
      </label>
      <button
        type="button"
        className="btn-secondary"
        disabled={pending || vramDraft === savedVram || vramNum === null}
        onClick={() => onSave({ vram_mib: vramNum })}
      >
        {tr("admin.engines_model_window_save")}
      </button>
      {/* What the edit above moves, and where the figure comes from. Both halves are needed: the
          number alone would read as a measurement on a row where nobody measured anything. */}
      <span className="muted engines-model-need">
        {need
          ? (tr("admin.engines_model_need_now" as never) as string)
              .replace("{n}", String(need))
              .replace(
                "{src}",
                tr(("admin.engines_vram_src_" + (model.vram_need_source || "unknown")) as never) as string,
              )
          : tr("admin.engines_model_need_unknown")}
      </span>
    </div>
  );
}

function EngineModelParams({
  model,
  pending,
  readOnly,
  onSave,
}: {
  model: EngineModel;
  pending: boolean;
  readOnly: boolean;
  onSave: (p: EngineParams) => void;
}) {
  const tr = useT();
  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState<EngineParamsForm>(() => engineParamsToForm(model.params));
  const summary = engineParamsSummary(model.params, tr);
  if (!editing) {
    return (
      <p className="muted engines-model-params">
        {summary}
        {!readOnly && (
          <button
            type="button"
            className="sm ghost"
            disabled={pending}
            onClick={() => {
              // Re-read the row on every open: the last save answered with the whole engine,
              // and an editor holding what was typed two saves ago would quietly put it back.
              setForm(engineParamsToForm(model.params));
              setEditing(true);
            }}
          >
            {tr("admin.engines_params_edit")}
          </button>
        )}
      </p>
    );
  }
  return (
    <div className="engines-model-confirm">
      <EngineParamsFields
        value={form}
        onChange={setForm}
        isLora={model.kind === "lora"}
        family={model.base_model}
      />
      <span className="engines-model-actions">
        <button
          type="button"
          className="primary sm"
          disabled={pending}
          onClick={() => {
            setEditing(false);
            onSave(engineParamsBody(form));
          }}
        >
          {tr("admin.engines_params_save")}
        </button>
        <button
          type="button"
          className="sm"
          disabled={pending}
          onClick={() => {
            setEditing(false);
            setForm(engineParamsBlank);
            onSave({});
          }}
        >
          {tr("admin.engines_params_clear")}
        </button>
        <button type="button" className="sm" onClick={() => setEditing(false)}>
          {tr("common.cancel")}
        </button>
      </span>
    </div>
  );
}

/** The declared parameters as one line, or the sentence that says there are none. Never an empty
 *  string: this line is also where the button to add some lives, and a row with nothing at all
 *  beside it reads as a row that cannot have any. */
function engineParamsSummary(p: EngineParams | undefined, tr: ReturnType<typeof useT>): string {
  const bits: string[] = [];
  if (p?.steps) bits.push(tr("admin.engines_params_steps") + " " + p.steps);
  if (p?.cfg) bits.push(tr("admin.engines_params_cfg") + " " + p.cfg);
  if (p?.sampler) bits.push(p.sampler);
  if (p?.scheduler) bits.push(p.scheduler);
  if (p?.clip_skip) bits.push(tr("admin.engines_params_clip_skip") + " " + p.clip_skip);
  if (p?.weight) bits.push(tr("admin.engines_params_weight") + " " + p.weight);
  // 🔴 Its own sentence, not the form's help text. "Empty fields keep the family's recipe"
  //  is an instruction to somebody typing; on a row it reads as a caption for fields that are
  //  not there.
  if (bits.length === 0) return tr("admin.engines_params_none");
  return tr("admin.engines_params") + ": " + bits.join(" · ");
}

/** The answers a finished ingest job can give the form below, and the ones it must not.
 *
 * 🔴 There is no licence and no acceptance in here, and there must not be. The acceptance is the
 * record of a human act (ADR 0072 decision 10) and it belongs to the row that job created — the
 * person registering the key again may be somebody else, years later. The CP leaves
 * `license_accepted_by` empty for everything this route writes, and the row then says "licence
 * not recorded", which is the true state. */
export type ModelPrefill = {
  id: string;
  s3Key: string;
  /** What the file is within the model (`--vae`, `--t5xxl`), "" for a whole checkpoint. */
  flag: string;
  /** Where the bytes came from, as the job recorded it. Carried through to the new row rather
   *  than shown as an editable field: it is a fact about the file, not a preference. */
  source: string;
  /** The catalogue row that ALREADY points at this key, if any. Not a refusal — a shared key is
   *  normal, since SD3.5 and FLUX.1 read the same text encoders — but the form says who has it,
   *  because the likeliest reason to be here twice is not knowing that. */
  usedBy: string;
};

/** Registering a file that is ALREADY in the models bucket.
 *
 * This is NOT the ingest — that fetches from Hugging Face, needs `ecs:RunTask` on the Control
 * Plane, and is phase P4. It is the other half of what P4 will do for itself: write down what a
 * staged file is. Without it the only catalogue row that ever exists is the seeded one, and
 * "choose another checkpoint without touching CloudFormation" has nothing to choose.
 *
 * Few fields on purpose — most of what the catalogue can hold (the sizes, the licence) is better
 * filled in by P4's ingest, which reads it from the model card instead of asking a person to
 * retype it. Two exceptions earn their place, and both are about what happens AFTER the row
 * exists:
 *
 *   - the WINDOW, for a chat engine. Since ADR 0072 P1 the window is per model, and a model
 *     registered without one reaches opencode as context 0 — which switches auto-compaction off,
 *     the exact failure the field exists to prevent. Both halves or neither (an output cap of 0
 *     is read as 32,000, so a context alone leaves 768 usable tokens);
 *   - the SIZE, because the control plane cannot look in S3 (ADR 0072 review R3) and this is the
 *     only place the "sync +N s" estimate can come from. Optional: no size, no estimate. */
export function EngineModelAdd({
  busy,
  isImage,
  isLora,
  baseModels,
  fileFlags,
  modelIds,
  prefill,
  onAdd,
}: {
  busy: boolean;
  isImage: boolean;
  /** Whether this row is an adapter rather than a model (ADR 0072 decision 5). It decides three
   *  things at once — the kind, where the file belongs in the bucket, and what `base_model`
   *  means — and it is the TAB above rather than a field here, so the form and the list beside
   *  it can never disagree about which of the two is being registered. */
  isLora: boolean;
  /** The checkpoint families this provider dispatches on, served by the CP so the panel and
   *  the engine cannot disagree about the spelling. Empty = this provider has no opinion. */
  baseModels?: string[];
  /** The labels a split model's files may carry, same source and same rule. Empty = a row is
   *  always one unlabelled file. */
  fileFlags?: string[];
  /** The ids already in this catalogue. A LoRA on the llm role names one of them as its base:
   *  that is the model whose preset section it is pinned into, so it is a CHOICE and never a
   *  typed name — a base nothing matches is an adapter that silently does nothing. */
  modelIds?: string[];
  /** A key picked out of the ingest history, or null. A new object per press, which is what
   *  re-opens the form on a second press of the same job. */
  prefill?: ModelPrefill | null;
  onAdd: (body: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [id, setId] = useState("");
  const [desc, setDesc] = useState("");
  const [ctx, setCtx] = useState("");
  const [out, setOut] = useState("");
  const [baseModel, setBaseModel] = useState("");
  const [scale, setScale] = useState("");
  /** The licence, in the words of whoever staged the file. OPTIONAL, and deliberately so: this
   *  route registers a file already in the bucket, and there is no API to read a licence off —
   *  the ingest road is the one that records it from the source (ADR 0072 decision 6). Left
   *  empty, the row says "licence not recorded" rather than nothing, which is the honest state.
   *  Given, the CP derives the commercial-use verdict from it exactly as the ingest does, so
   *  the same model does not lose its "non-commercial" mark by coming in through this door. */
  const [licence, setLicence] = useState("");
  const [licenceURL, setLicenceURL] = useState("");
  // One row PER FILE, always — a single-file checkpoint is this list with one entry, so the
  // common case is not a second code path. Until this existed the form held one key, which made
  // a split model impossible to register at all: FLUX.2 klein is a diffusion model, a text
  // encoder and a VAE, and each has to be labelled with the flag its loader reads.
  const blank = () => [{ flag: "", s3Key: "", bytes: "" }];
  const [files, setFiles] = useState(blank);
  /** Where the file came from, when it is being registered from the ingest history. Held rather
   *  than shown as a field: nothing about it is a choice, and the CP stores it because an id is
   *  short and readable and does not say which vendor published the model (migration 0060). */
  const [source, setSource] = useState("");
  const [fromJob, setFromJob] = useState<ModelPrefill | null>(null);
  const families = baseModels || [];
  const flags = fileFlags || [];
  const reset = () => {
    setOpen(false);
    setId("");
    setDesc("");
    setCtx("");
    setOut("");
    setBaseModel("");
    setScale("");
    setLicence("");
    setLicenceURL("");
    setFiles(blank);
    setSource("");
    setFromJob(null);
  };

  /** A key picked out of the ingest history opens the form on it.
   *
   * 🔴 What is filled in is what the job KNOWS — the key, what the file is within the model,
   * where it came from, and the id that job used. What is not filled in is the FAMILY, which is
   * the one field a wrong answer fails silently on (a display name like "Flux.1 D" produced rows
   * that looked complete and refused to generate, ADR 0072 P2 実機検証), so the button below
   * stays disabled until a person picks one. The bytes are left empty too: the CP cannot look in
   * the bucket, so a number carried over from a job row would be a size nobody re-measured.
   *
   * Keyed on the prefill OBJECT, so a second press of the same job re-opens the form, and a
   * remount (the tab above changes this component's key) applies it once. */
  useEffect(() => {
    if (!prefill) return;
    setOpen(true);
    setId(prefill.id);
    setFiles([{ flag: prefill.flag, s3Key: prefill.s3Key, bytes: "" }]);
    setSource(prefill.source);
    setFromJob(prefill);
  }, [prefill]);

  if (!open) {
    return (
      <Button small className="engines-open" onClick={() => setOpen(true)}>
        {tr("admin.engines_model_add")}
      </Button>
    );
  }
  const rows = files.filter((f) => f.s3Key.trim());
  /** An llm adapter names a MODEL ID as its base; an image one names a family, as a checkpoint
   *  does. One column, two vocabularies, because "what this belongs to" is the same question
   *  and the engine that reads it is different (ADR 0072 decisions 2 and 5). */
  const basePicksAModel = isLora && !isImage;
  const baseOptions = basePicksAModel ? modelIds || [] : families;
  // The base is required exactly when there is a vocabulary to pick from, and the button says so
  // by being disabled rather than by letting the CP refuse after the press.
  const incomplete = !id.trim() || rows.length === 0 || (basePicksAModel
    ? !baseOptions.includes(baseModel)
    : baseOptions.length > 0 && !baseModel);
  const submit = () => {
    if (incomplete) return;
    const n = (v: string) => {
      const parsed = Number(v.trim().replace(/[_,]/g, ""));
      return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : 0;
    };
    // Both halves of the window or neither: a context with no output cap leaves opencode 768
    // usable tokens, which is worse than the no-limit default it would otherwise get.
    const c = n(ctx);
    const o = n(out);
    // The strength travels as an argument rather than a column: it is meaningful for exactly
    // one kind of row, and `args` is already where a row says something only its engine reads.
    // Left empty it is not sent at all, and the engine's own default (1) applies.
    const w = Number(scale.trim());
    const args = isLora && scale.trim() && Number.isFinite(w) ? ["--scale", scale.trim()] : [];
    onAdd({
      id: id.trim(),
      kind: isLora ? "lora" : isImage ? "checkpoint" : "gguf",
      files: rows.map((f) => ({ flag: f.flag, s3Key: f.s3Key.trim(), bytes: n(f.bytes) })),
      description: desc.trim(),
      base_model: baseModel,
      ...(args.length > 0 ? { args } : {}),
      // An adapter has no window of its own: it is loaded with the model that has one.
      context_tokens: isLora ? 0 : c && o ? c : 0,
      max_output_tokens: isLora ? 0 : c && o ? o : 0,
      // 🔴 `license_name`, not `license`: the panel reads `license_name || license`, and the
      // pair exists because Hugging Face answers `other` for both non-commercial models in ADR
      // 0072's table. What a person types here is the terms, so it goes in the field that holds
      // them. Empty stays empty — an unrecorded licence is a state the row states.
      license_name: licence.trim(),
      license_url: licenceURL.trim(),
      // Where the bytes came from, when this row is being rebuilt on a key the ingest history
      // still holds. 🔴 The licence ACCEPTANCE does not travel with it: that is the record of a
      // human act on the row the job created (ADR 0072 decision 10), and the CP leaves this
      // row's `license_accepted_by` empty. Registering a key again is not accepting anything.
      ...(source ? { source } : {}),
    });
    reset();
  };
  const setFile = (i: number, patch: Partial<{ flag: string; s3Key: string; bytes: string }>) =>
    setFiles((prev) => prev.map((f, j) => (j === i ? { ...f, ...patch } : f)));
  // One field per ROW, each with its own label. The fields are not interchangeable — an S3 key,
  // a token count and a byte count look nothing alike and mistyping one into another is silent —
  // so they are not laid out as a strip of look-alike boxes with placeholder text that vanishes
  // the moment somebody types.
  const field = (
    label: string,
    value: string,
    set: (v: string) => void,
    placeholder = "",
    numeric = false,
  ) => (
    <label className="engines-model-add-row">
      <span>{label}</span>
      <input
        value={value}
        placeholder={placeholder}
        inputMode={numeric ? "numeric" : undefined}
        onChange={(ev) => set(ev.currentTarget.value)}
      />
    </label>
  );
  return (
    <div className="engines-model-add">
      {/* Where these answers came from, when they were not typed. Three things are said and one
          is deliberately not:
            - the SOURCE, which is what travels onto the row and is otherwise invisible here;
            - that the family still has to be picked, because that is why the button is off;
            - who else already points at this key, when somebody does. A shared key is normal
              (SD3.5 and FLUX.1 read the same text encoders), so it is not a refusal — but a
              second row for a file that already has one is usually a mistake, and this is the
              only moment it can be noticed.
          🔴 What is NOT said is that the file is in the bucket. The CP has no S3 permission at
          all (ADR 0072 review R3) and a `done` job proves only that the fetch once succeeded —
          `deleteModel?purge=1` deletes the bytes and leaves the job `done`. A typo, or a purged
          file, surfaces in the fetch sidecar's log at the next cold start, which is what the
          note at the bottom of this form has always said. */}
      {fromJob && (
        <div className="engines-model-add-from-job">
          <p className="muted">
            {(tr("admin.engines_model_add_from_job") as string).replace("{s}", fromJob.source)}
          </p>
          {!!fromJob.usedBy && (
            <p className="muted">
              {(tr("admin.engines_model_add_from_job_used") as string).replace(
                "{who}",
                fromJob.usedBy,
              )}
            </p>
          )}
        </div>
      )}
      {field(tr("admin.engines_model_add_id"), id, setId,
        isLora ? "house-style" : isImage ? "juggernaut-xl-v9" : "qwen2.5-coder-1.5b")}
      {/* ⚠️ A CHOICE, never a text box. What the repository calls a model — "SDXL 1.0",
          "Flux.1 D" — is a display name, and typing one here produced rows that looked complete
          and refused to generate (ADR 0072 P2 実機検証). The list comes from the CP, which
          validates against the same one. An llm adapter picks from the catalogue's own ids for
          the same reason: a base nothing matches is an adapter that loads nowhere. */}
      {(baseOptions.length > 0 || basePicksAModel) && (
        <label className="engines-model-add-row">
          <span>
            {tr(basePicksAModel ? "admin.engines_model_add_lora_base" : "admin.engines_model_add_family")}
          </span>
          <select value={baseModel} disabled={basePicksAModel && baseOptions.length === 0} onChange={(ev) => setBaseModel(ev.currentTarget.value)}>
            <option value="">
              {tr(
                basePicksAModel
                  ? "admin.engines_model_add_lora_base_pick"
                  : "admin.engines_model_add_family_pick",
              )}
            </option>
            {baseOptions.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>
        </label>
      )}
      {files.map((f, i) => (
        <div className="engines-model-add-file" key={i}>
          {flags.length > 0 && (
            <label className="engines-model-add-row">
              <span>{tr("admin.engines_model_add_part")}</span>
              <select value={f.flag} onChange={(ev) => setFile(i, { flag: ev.currentTarget.value })}>
                {flags.map((fl) => (
                  <option key={fl} value={fl}>
                    {fl === "" ? (tr("admin.engines_model_add_part_whole") as string) : fl}
                  </option>
                ))}
              </select>
            </label>
          )}
          {field(tr("admin.engines_model_add_key"), f.s3Key, (v) => setFile(i, { s3Key: v }),
            engineIngestPrefix(isImage, f.flag, isLora) +
              (isImage ? "name.safetensors" : "name.gguf"))}
          {field(tr("admin.engines_model_add_bytes"), f.bytes, (v) => setFile(i, { bytes: v }),
            "1117320768", true)}
          {files.length > 1 && (
            <Button
              variant="ghost"
              small
              onClick={() => setFiles((prev) => prev.filter((_, j) => j !== i))}
            >
              {tr("admin.engines_model_add_part_drop")}
            </Button>
          )}
        </div>
      ))}
      {/* Offered only where a split model is a thing this provider can load. */}
      {flags.length > 0 && (
        <Button
          variant="ghost"
          small
          onClick={() => setFiles((prev) => [...prev, { flag: "", s3Key: "", bytes: "" }])}
        >
          {tr("admin.engines_model_add_part_more")}
        </Button>
      )}
      {field(tr("admin.engines_model_add_desc"), desc, setDesc)}
      {/* Optional, and the only route where a licence has to be TYPED — there is no source here
          to read one from. Left blank the row says "licence not recorded"; filled, the CP reads
          the commercial-use verdict off it the same way the ingest does. */}
      {field(tr("admin.engines_model_add_license"), licence, setLicence, "apache-2.0")}
      {field(tr("admin.engines_model_add_license_url"), licenceURL, setLicenceURL)}
      {/* The window is a chat engine's business: sd-server holds one checkpoint and has no
          context at all, so offering the field there would ask for a number nothing reads. An
          adapter has none either — it is loaded with the model whose window applies. */}
      {!isImage && !isLora && field(tr("admin.engines_model_add_ctx"), ctx, setCtx, "32768", true)}
      {!isImage && !isLora && field(tr("admin.engines_model_add_out"), out, setOut, "4096", true)}
      {/* Optional, and only for the pinned kind: the image role's adapters take their strength
          per request, from the agent's own call (decision 5's image half). */}
      {isLora && !isImage &&
        field(tr("admin.engines_model_add_lora_scale"), scale, setScale, "1", true)}
      <div className="engines-model-add-actions">
        <Button variant="primary" small disabled={busy || incomplete} onClick={submit}>
          {tr("admin.engines_model_add_go")}
        </Button>
        <Button small onClick={() => setOpen(false)}>
          {tr("common.cancel")}
        </Button>
      </div>
      {/* ⚠️ The CP never checks that the key exists: it has no S3 permission at all and none is
          being added (ADR 0072 review R3). A typo surfaces in the fetch sidecar's log at the
          next cold start, so the panel says so rather than implying a check happened. */}
      <p className="muted">
        {isLora
          ? (tr("admin.engines_model_add_lora_note") as string).replace(
              "{p}",
              engineIngestPrefix(isImage, "", true),
            )
          : tr("admin.engines_model_add_note")}
      </p>
    </div>
  );
}

/** What the CP knows about the operator's Hugging Face token. Never the value: the CP cannot
 *  read the secret it writes, and it does not offer to unseal the stored copy for a screen. */
type HfTokenStatus = {
  /** false on a stack that predates P5, where the token was a CloudFormation parameter and
   *  there is nowhere for the CP to put one. */
  available?: boolean;
  configured?: boolean;
  /** That older stack HAS a token: nothing to register, and gated repositories work. Without
   *  this the same screen would have to read as "no token" and send somebody to fix what is
   *  not broken. */
  stack_token?: boolean;
  updated_by?: string;
  updated_at?: string;
};

/** Registering the operator's Hugging Face token (ADR 0072 decision 6 as revised, phase P5).
 *
 * One token for the whole deployment, so this sits below the engines rather than inside one:
 * a single ingest task serves both roles, and a per-engine field would suggest a choice that
 * does not exist.
 *
 * The field is write-only, and that is not a UI convention here — it is what the deployment
 * can actually do. The CP holds `PutSecretValue` on one secret and never `GetSecretValue`, so
 * "show the current token" is not something it could offer even if a screen wanted it. What
 * can be shown is that one is registered, by whom and when. */
export function HfTokenPanel() {
  const tr = useT();
  const [st, setSt] = useState<HfTokenStatus | null>(null);
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    const d = await api("api/admin/engines/hf-token");
    if (d?.error) {
      setErr(errDetail(d.error));
      return;
    }
    setSt(d || {});
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/engines/hf-token", "PUT", { token });
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      // Cleared on success only: a token that was refused is still in the box to be corrected,
      // and retyping 40 characters because the deployment answered 502 is its own small insult.
      setToken("");
      setSt(d || {});
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/engines/hf-token", "DELETE");
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setSt(d || {});
    } finally {
      setBusy(false);
    }
  };

  if (!st) return null;
  return (
    <section className="admin-panel">
      <div className="usage-toolbar">
        <span>{tr("admin.engines_hf_token")}</span>
      </div>
      {st.available === false ? (
        <p className="muted">
          {tr(st.stack_token ? "admin.engines_hf_token_stack" : "admin.engines_hf_token_unsupported")}
        </p>
      ) : (
        <>
          <p className="muted">
            {st.configured
              ? tr("admin.engines_hf_token_set")
                  .replace("{who}", st.updated_by || "-")
                  .replace("{when}", st.updated_at ? fmtDateTime(st.updated_at) : "-")
              : tr("admin.engines_hf_token_unset")}
          </p>
          <label className="engines-hf-row">
            <span>{tr("admin.engines_hf_token_field")}</span>
            <input
              type="password"
              autoComplete="off"
              value={token}
              placeholder="hf_..."
              onChange={(ev) => setToken(ev.currentTarget.value)}
            />
          </label>
          <div className="engines-model-add-actions">
            <Button variant="primary" small disabled={busy || !token.trim()} onClick={save}>
              {tr("admin.engines_hf_token_save")}
            </Button>
            {st.configured && (
              <Button small disabled={busy} onClick={remove}>
                {tr("admin.engines_hf_token_remove")}
              </Button>
            )}
          </div>
          <p className="muted">{tr("admin.engines_hf_token_note")}</p>
        </>
      )}
      {err && <p className="form-err">{err}</p>}
    </section>
  );
}

/** Taking a model IN from Hugging Face, Civitai or a URL (ADR 0072 decision 6, phase P4).
 *
 * Two steps, and the split is the point: RESOLVE first (what is this file, what does it weigh,
 * what licence does it carry, is the repository gated), then INGEST. An acceptance offered
 * before the terms are on screen is not an acceptance, and a gated repository on a deployment
 * with no Hugging Face token is refused here rather than nine minutes into a Fargate task. */
/** The generation parameters as a FORM holds them: six strings, because every one of them is
 *  typed into and "empty" has to stay distinguishable from "zero".
 *
 * 🔴 That distinction is the whole reason this is not just EngineParams. An empty field means
 * "this row says nothing, use the family's recipe"; a 0 would mean "run this at zero steps".
 * The two are one keystroke apart on screen and worlds apart at the GPU. */
type EngineParamsForm = {
  steps: string;
  cfg: string;
  sampler: string;
  scheduler: string;
  clip_skip: string;
  weight: string;
};

const engineParamsBlank: EngineParamsForm = {
  steps: "",
  cfg: "",
  sampler: "",
  scheduler: "",
  clip_skip: "",
  weight: "",
};

/** A row's stored parameters, as the form edits them. */
function engineParamsToForm(p?: EngineParams): EngineParamsForm {
  return {
    steps: p?.steps ? String(p.steps) : "",
    cfg: p?.cfg ? String(p.cfg) : "",
    sampler: p?.sampler || "",
    scheduler: p?.scheduler || "",
    clip_skip: p?.clip_skip ? String(p.clip_skip) : "",
    weight: p?.weight ? String(p.weight) : "",
  };
}

/** The form as the API takes it. Empty fields are LEFT OUT rather than sent as 0: the CP reads a
 *  zero as undeclared too, but sending one would mean this panel had an opinion it does not
 *  have — and `{}` is how a row says "go back to the family's recipe". */
function engineParamsBody(f: EngineParamsForm): EngineParams {
  const num = (v: string) => {
    const n = Number(v.trim());
    return Number.isFinite(n) && n > 0 ? n : undefined;
  };
  const out: EngineParams = {};
  if (num(f.steps)) out.steps = Math.floor(num(f.steps) as number);
  if (num(f.cfg)) out.cfg = num(f.cfg);
  if (f.sampler.trim()) out.sampler = f.sampler.trim();
  if (f.scheduler.trim()) out.scheduler = f.scheduler.trim();
  if (num(f.clip_skip)) out.clip_skip = Math.floor(num(f.clip_skip) as number);
  if (num(f.weight)) out.weight = num(f.weight);
  return out;
}

/** What the author's description said, into the fields — but only into the EMPTY ones.
 *
 * Same rule the context window has followed since P4, and for the same reason: a number
 * somebody typed deliberately outranks one a regular expression found in a stranger's
 * paragraph. Picking a second search result therefore does not overwrite what was corrected
 * after the first. */
function engineParamsMerge(cur: EngineParamsForm, hint: EngineParams): EngineParamsForm {
  const h = engineParamsToForm(hint);
  const out = { ...cur };
  for (const k of Object.keys(out) as (keyof EngineParamsForm)[]) {
    if (!out[k].trim() && h[k]) out[k] = h[k];
  }
  return out;
}

/** The six fields, as one block. Used by the ingest form and by the row editor, which is what
 *  keeps "what the author suggested" and "what this deployment decided" the same six questions.
 *
 * The two notes at the bottom are per family and are the honest part: `cfg` reaches no graph on
 * flux1 or klein (their guidance is a different input), and `clip_skip` reaches none at all
 * today. A field that is stored and ignored has to say so, or the next person spends an
 * afternoon wondering why the picture did not change. */
function EngineParamsFields({
  value,
  onChange,
  isLora,
  family,
}: {
  value: EngineParamsForm;
  onChange: (v: EngineParamsForm) => void;
  isLora: boolean;
  family?: string;
}) {
  const tr = useT();
  const set = (k: keyof EngineParamsForm) => (v: string) => onChange({ ...value, [k]: v });
  const field = (k: keyof EngineParamsForm, label: string, placeholder = "") => (
    <label className="engines-model-add-row engines-param">
      <span>{label}</span>
      <input
        value={value[k]}
        placeholder={placeholder}
        onChange={(ev) => set(k)(ev.currentTarget.value)}
      />
    </label>
  );
  // 🔴 A LoRA has ONE of these questions — how strongly to apply it — and none of the others.
  // Steps and cfg belong to the checkpoint the adapter is chained onto, and offering them here
  // would be six fields where five do nothing.
  if (isLora) {
    return (
      <div className="engines-params">
        {field("weight", tr("admin.engines_params_weight"), "0.8")}
      </div>
    );
  }
  const cfgIgnored = family === "flux1" || family === "flux2-klein";
  return (
    <div className="engines-params">
      {field("steps", tr("admin.engines_params_steps"), "30")}
      {!cfgIgnored && field("cfg", tr("admin.engines_params_cfg"), "7")}
      {field("sampler", tr("admin.engines_params_sampler"), tr("admin.engines_params_name_ph"))}
      {field("scheduler", tr("admin.engines_params_scheduler"), "karras")}
      {field("clip_skip", tr("admin.engines_params_clip_skip"), "2")}
      <p className="muted">{tr("admin.engines_params_note")}</p>
      {cfgIgnored && <p className="muted">{tr("admin.engines_params_cfg_ignored")}</p>}
      {value.clip_skip.trim() && <p className="muted">{tr("admin.engines_params_clip_skip_note")}</p>}
    </div>
  );
}

/** The four questions, in order. They exist as a list because the rail, the "which step am I
 *  on" arithmetic and the Back button are all the same sequence, and a second spelling of it is
 *  how a rail comes to disagree with what is on screen. */
export const engineIngestSteps = ["act", "find", "file", "confirm"] as const;
export type EngineIngestStep = (typeof engineIngestSteps)[number];

/** What somebody came here to do, ASKED rather than inferred.
 *
 * 🔴 This is the fix for the defect that cost an operator of this deployment an evening. The
 * three acts used to be told apart by whether the id they typed happened to collide with a row
 * that already existed — so "add this VAE to that model" was something you discovered by typing
 * a name you had to already know, and the checkbox that offered it stayed disabled until a file
 * role three fields below it was set, with nothing on screen saying so. */
export const engineIngestActs = ["new", "attach", "replace"] as const;
export type EngineIngestAct = (typeof engineIngestActs)[number];

export function EngineIngest({
  engineKey,
  isImage,
  isLora,
  baseModels,
  fileFlags,
  models,
  cardMiB,
  busy,
  onStarted,
  open,
  setOpen,
  onJob,
}: {
  engineKey: string;
  isImage: boolean;
  /** Taking in an ADAPTER rather than a model (ADR 0072 decision 5). It moves the key into
   *  `<role>/loras/`, makes `base_model` a model id on the llm role, takes the window away — a
   *  LoRA has none of its own — and narrows the SEARCH above the form, which is the half that
   *  used to be missing: the selector said "LoRA" while the list under it went on answering
   *  checkpoints. It is the tab now, so the two cannot disagree. */
  isLora: boolean;
  /** As on the register form: the families this provider dispatches on, from the CP. The
   *  repository's OWN answer is a display name and is shown as a hint, never submitted — the
   *  CP refuses an ingest whose family is not one of these. */
  baseModels?: string[];
  /** What this file IS within the model, same vocabulary and same source as the register
   *  form's. Empty = this provider loads one whole checkpoint and has no parts to name. */
  fileFlags?: string[];
  /** The catalogue this engine already holds. Used to tell whether the typed id names a row:
   *  the CP refuses a plain ingest onto one, and without the offer to join it the only way to
   *  build a split model is three throwaway rows (ADR 0072 P2 欠落 6). The rows rather than
   *  their ids, because the other question asked of them is which of that row's SLOTS are
   *  already filled — which is what tells "add this part" apart from "replace the part that is
   *  there", and the two have opposite preconditions. */
  models?: EngineModel[];
  /** The VRAM of the rung this engine is set to buy, when the deployment declared a ladder at
   *  all. 🔴 Absent is the normal state of a deployment whose box CloudFormation bought, and
   *  then there is NOTHING to compare a file against — so the panel says what a file weighs and
   *  draws no verdict. "It fits" with no card to fit into is a lie. */
  cardMiB?: number;
  busy: boolean;
  onStarted: () => void;
  /** 🔴 Held by the PANEL, because opening this is a change of screen: the catalogue below is
   *  hidden while it is open, so that what somebody is answering is the only thing in front of
   *  them. A wizard drawn under a list of models is a form again. */
  open: boolean;
  setOpen: (v: boolean) => void;
  /** Hand the started job to whoever is hosting this. The PANE uses it to stop being a form and
   *  become the progress of the download it started — and to end at the enable press. */
  onJob?: (job: IngestJob) => void;
}) {
  const tr = useT();
  const [repo, setRepo] = useState("");
  /** The revision a pasted `/blob/<rev>/…` URL named. Held here because splitting the URL into
   *  the fields leaves nowhere else for it, and dropping it would silently resolve `main`. */
  const [rev, setRev] = useState("");
  const [file, setFile] = useState("");
  const [id, setId] = useState("");
  const [desc, setDesc] = useState("");
  const [ctx, setCtx] = useState("");
  const [out, setOut] = useState("");
  const [found, setFound] = useState<ResolvedSource | null>(null);
  const [files, setFiles] = useState<IngestCandidate[] | null>(null);
  const [accepted, setAccepted] = useState(false);
  const [err, setErr] = useState("");
  const [q, setQ] = useState("");
  const [hits, setHits] = useState<IngestHit[] | null>(null);
  /** The result the form was filled from, kept after the list it came from is gone.
   *
   * 🔴 The card is the only place the upstream page, the trigger words and the licence are on
   * screen at all, and the pick used to drop the whole list — so the fields below were filled
   * in with no way back to what they describe, and checking one meant searching again. The form
   * stores none of it: `repo` is `civitai:<versionId>`, which is not a link and is not a name.
   *
   * Cleared the moment the repository is typed over, because then it describes something else. */
  const [picked, setPicked] = useState<IngestHit | null>(null);
  const [searchSource, setSearchSource] = useState("hf");
  const [sort, setSort] = useState("downloads");
  const [baseModel, setBaseModel] = useState("");
  /** What this file is within the model, and — when the id names a row that already exists —
   *  whether it JOINS that row instead of making a new one. */
  const [fileFlag, setFileFlag] = useState("");
  /** Which of the four questions is on screen, and which act is being performed. `attach` and
   *  `replace` are derived from the act rather than held: they used to be two checkboxes that
   *  disabled each other, which is a state a screen can be in and a person cannot read. */
  const [step, setStep] = useState<EngineIngestStep>("act");
  const [act, setAct] = useState<EngineIngestAct>("new");
  const attach = act === "attach";
  const replace = act === "replace";
  /** Take the family's VAE in with this checkpoint (ADR 0072 follow-up). Ticked by the RESOLVE,
   *  and only when the header said the file carries none — an answer to a fact, never a setting
   *  somebody has to know about. */
  const [withVae, setWithVae] = useState(false);
  /** 🔴 Whether a slot has been CHOSEN, which the flag itself cannot say: the empty string is a
   *  real answer — a row's own checkpoint — and a placeholder that also carried it would make
   *  "not answered yet" and "the checkpoint" the same value. That collision is how a picker ends
   *  up offering an option nothing can be done with. */
  const [slotChosen, setSlotChosen] = useState(false);
  const families = baseModels || [];
  const flags = fileFlags || [];
  /** What this model should be RUN at, as text — the fields are typed into, so they are strings
   *  until the moment they are sent. Empty means "not declared", which is the family's own
   *  recipe and is what every row said before this existed. */
  const [params, setParams] = useState<EngineParamsForm>(engineParamsBlank);
  /** True when the typed id is one this engine already has. The CP refuses a plain ingest onto
   *  it (409 model_id_exists), so this is where the second act — attaching a part — is
   *  offered rather than left as an error to read. */
  const modelIds = (models || []).map((m) => m.id);
  const target = (models || []).find((m) => m.id === id.trim());
  const known = !!target;
  /** Which of that row's slots are FILLED. It decides which of the two acts is even possible:
   *  a free slot can only be attached to, a filled one can only be replaced — the same pair of
   *  refusals the CP answers, said here so neither costs a download to discover.
   *
   *  The empty flag is a slot like any other here, and it is the one that matters: a row's own
   *  checkpoint is filled by definition, so replace is the only act it ever offers. */
  const taken = new Set((target?.file_rows || []).map((f) => (f.flag || "").trim()));
  /** Same two vocabularies as the register form: an llm adapter names a model id, everything
   *  else names a family. */
  const basePicksAModel = isLora && !isImage;
  const baseOptions = basePicksAModel ? modelIds.filter((m) => m !== id.trim()) : families;
  /** What is being taken in, in the CP's own vocabulary. Sent on the RESOLVE as well as on the
   *  start: the resolve reads the GGUF header for the KV geometry, and an adapter's file is not
   *  a model's — asking for one costs a range GET over somebody else's network for a number
   *  that would mean nothing. */
  const kind = isLora ? "lora" : isImage ? "checkpoint" : "gguf";
  /** Which read of a source is the current one. Picking a second result before the first has
   *  answered is one click, and the two answers come back in whatever order the two APIs feel
   *  like — so the older one is dropped rather than allowed to describe the row on screen. */
  const asked = useRef(0);

  /** 🔴 The repository is a PARAMETER here, not a read of `repo`.
   *
   * Picking a search result sets the field and resolves in the same handler, and React still
   * has the previous render's `repo` in scope at that point — so reading the state resolved the
   * repository somebody chose a moment ago, with the new name on screen and nothing to see. The
   * same is true of `file`, which the pick clears. Every caller that changes either one passes
   * it. */
  const source = (name = file, repoOverride?: string) => {
    const r = (repoOverride ?? repo).trim();
    // A pasted https://huggingface.co/<repo>/blob|resolve/<rev>/<file> is what a person
    // actually has in hand, so it is accepted as-is rather than asked for in pieces. Normally
    // splitPasted has already taken it apart into the fields; this stays for the URL that was
    // never blurred.
    const m = r.match(/^https?:\/\/huggingface\.co\/([^/]+\/[^/]+)(?:\/(?:blob|resolve)\/([^/]+)\/(.+))?$/);
    // 🔴 The NAMED file wins over the one in the URL. The other way round, a blob URL for one
    // file plus a pick of another out of the list resolved the first one while the picker
    // showed the second — silently, because nothing on screen carried the URL's own filename.
    if (m) return { hf: { repo: m[1], revision: m[2] || "", file: name.trim() || m[3] || "" } };
    const civ = r.match(/civitai\.(?:com|red)\/.*modelVersionId=(\d+)|^civitai:(\d+)$/);
    if (civ) return { civitai: { versionId: Number(civ[1] || civ[2]), file: name.trim() } };
    if (/^https?:\/\//.test(r)) return { url: r, sha256: name.trim() };
    return { hf: { repo: r, file: name.trim(), revision: rev } };
  };

  /** What an address names, or null when it is not one. The two shapes a person has in hand:
   *  a Hugging Face model page (optionally pointing straight at a file) and a Civitai page
   *  carrying `modelVersionId` — which is the id an ingest takes, unlike the model id in the
   *  path next to it. */
  const splitPasted = (raw: string): { repo: string; rev: string; file: string } | null => {
    const t = raw.trim();
    const hf = t.match(
      /^https?:\/\/huggingface\.co\/([^/?#]+\/[^/?#]+)(?:\/(?:blob|resolve)\/([^/?#]+)\/([^?#]+))?(?:[?#].*)?$/,
    );
    if (hf) return { repo: hf[1], rev: hf[2] || "", file: hf[3] || "" };
    const civ = t.match(/^https?:\/\/(?:[\w-]+\.)*civitai\.(?:com|red)\/\S*[?&]modelVersionId=(\d+)/);
    if (civ) return { repo: "civitai:" + civ[1], rev: "", file: "" };
    return null;
  };

  /** Take a pasted address apart into the fields it names, once the box is left.
   *
   * `source()` has always understood one, but only at the moment the request was built — so
   * what would actually be fetched was never on screen, the file the URL named was not the one
   * the picker showed, and the id was proposed from neither. Splitting it into the fields
   * leaves one source of truth and makes the rest of the form behave as if it had been typed.
   *
   * On blur rather than on every keystroke: `huggingface.co/Qwen/Q` is a legal `owner/name`
   * halfway through typing one, and rewriting the box under a cursor is worse than waiting. */
  const splitRepoField = () => {
    const s = splitPasted(repo);
    if (!s) return;
    setRepo(s.repo);
    setRev(s.rev);
    setFiles(null);
    setFound(null);
    // Fills an empty box, never overwrites a typed one — the same rule the id and the window
    // follow further down.
    if (s.file && !file.trim()) setFile(s.file);
  };

  /** A plain url addresses one file and has no listing; the field carries its sha256 there. */
  const listable = (repoOverride?: string) => {
    const r = (repoOverride ?? repo).trim();
    return !/^https?:\/\//.test(r) || /huggingface\.co|civitai\.(?:com|red)/.test(r);
  };

  const resolveFile = async (name: string, repoOverride?: string) => {
    const seq = ++asked.current;
    const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest/resolve`, "POST", {
      source: source(name, repoOverride),
      kind,
    });
    // A second pick while this one was in flight: its answer is the one on screen, and this
    // late one would overwrite the licence, the sha256 and the window of a different model.
    if (seq !== asked.current) return;
    if (d?.error) {
      setFound(null);
      setErr(errDetail(d.error));
      return;
    }
    const res = d as ResolvedSource;
    setFound(res);
    setAccepted(false);
    // Same rule as the window below: fill an empty field, never overwrite a typed one. The id
    // is what a member sees in the launch menu, and the CP refuses one the catalogue already
    // holds (409 model_id_exists), so this is a starting point rather than an answer.
    if (!id.trim()) setId(engineIdFromFile(name));
    // Offered, not applied — and only into a field nobody has typed in, because the number
    // somebody entered deliberately outranks the one off the model card. The cap follows at an
    // eighth, which is what both models here were already being run at; it is a select, so it
    // stays a choice rather than a number that appeared.
    if (res.context_length && !ctx.trim()) {
      setCtx(String(res.context_length));
      if (!out.trim()) setOut(String(Math.floor(res.context_length / 8)));
    }
    // The family, on the same rule: filled in when the picker is still empty, never over a
    // choice somebody made. 🔴 It is the CP's translation of the upstream name into the
    // vocabulary this provider dispatches on — never the upstream string itself, which is what
    // ADR 0072 decision 2 forbids storing — and it is absent whenever the CP would be guessing,
    // in which case the picker stays empty and the row keeps its `base_model_missing` mark.
    if (res.base_model_suggest && !baseModel && !basePicksAModel) setBaseModel(res.base_model_suggest);
    // And what the author says about running it. Into the FIELDS, so the numbers are on screen
    // next to the sentence they were read out of and can be corrected or cleared before
    // anything is stored (engine_params_hint.go: this is a regular expression over prose).
    if (res.params_hint) setParams((cur) => engineParamsMerge(cur, res.params_hint as EngineParams));
    // 🔴 Ticked by the ANSWER. The offer appears only where the header was read and said "no
    // VAE in this file", so the default is not a preference — it is the only way the row that
    // is about to be created can ever generate a picture.
    setWithVae(res.vae_bundled === "no" && !!res.family_vae && !res.family_vae.unreachable);
  };

  /** 「調べる」. With no file named yet this ASKS WHAT THERE IS, because a filename retyped from
   *  another window is where the mistakes are. One candidate resolves straight through — a
   *  picker over a single option is a question with one answer.
   *
   * `over` is how a caller that has just changed the repository or the file says so; see
   * `source`. Called from an onClick as `() => resolve()`, never bare — the click event would
   * arrive as the override. */
  const resolve = async (over?: { repo?: string; file?: string }) => {
    setErr("");
    const named = (over?.file ?? file).trim();
    if (!named && listable(over?.repo)) {
      const seq = ++asked.current;
      const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest/files`, "POST", {
        source: source("", over?.repo),
      });
      if (seq !== asked.current) return;
      if (d?.error) {
        setFiles(null);
        setErr(errDetail(d.error));
        return;
      }
      const list = (Array.isArray(d?.files) ? d.files : []) as IngestCandidate[];
      setFiles(list);
      if (list.length === 1) {
        setFile(list[0].name);
        await resolveFile(list[0].name, over?.repo);
      }
      return;
    }
    await resolveFile(named, over?.repo);
  };

  /** 「探す」 — for somebody who does not already know `owner/name`. Reads only: it starts
   *  nothing, writes nothing and needs no token (both APIs answer anonymously). */
  const search = async (opts: { source?: string; sort?: string } = {}) => {
    setErr("");
    const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest/search`, "POST", {
      q,
      source: opts.source ?? searchSource,
      // With no words this IS the request: a ranking of what this engine can load, which is the
      // only way in for somebody who does not know what to type.
      sort: opts.sort ?? sort,
      // 🔴 The half that used to be missing. The form knew it was registering an adapter and the
      // search above it went on asking for checkpoints, so looking for a LoRA returned twenty
      // models — and the only way to reach an adapter was to paste its id from another window.
      lora: isLora,
    });
    if (d?.error) {
      setHits(null);
      setErr(errDetail(d.error));
      return;
    }
    setHits((Array.isArray(d?.hits) ? d.hits : []) as IngestHit[]);
  };

  /** Choosing a result fills the repository field — the same field somebody would have typed
   *  into — so everything downstream is the road that was already there. Civitai goes in as
   *  `civitai:<versionId>`, which is the form the source parser above already reads.
   *
   * And then it asks what is in there, which is what pressing 「調べる」 did by hand: the pick
   * has already said which model this is, so the button was a second confirmation of a decision
   * already taken. A repository with several loadable files still lands on the picker — the
   * same list, one press earlier — and one with none says so at the moment of choosing rather
   * than after another click. The search is still only a way IN: the field stays typeable and a
   * deployment with no egress loses the search and keeps the ingest. */
  const pickHit = async (h: IngestHit) => {
    const ref = h.source === "civitai" ? "civitai:" + h.ref : h.ref;
    setRepo(ref);
    setRev("");
    setFiles(null);
    setFile("");
    setFound(null);
    setHits(null);
    setPicked(h);
    await resolve({ repo: ref, file: "" });
  };

  const pick = async (name: string) => {
    setErr("");
    setFile(name);
    setFound(null);
    if (name) await resolveFile(name);
  };

  /** Back to the first question. 🔴 Closing has to do this, not only a successful start: the
   *  wizard is a dialog now, so its state outlives the press that dismissed it — and re-opening
   *  「モデルを追加」 into the middle of an act somebody chose minutes ago, against a row they no
   *  longer remember picking, is the same "state the screen does not show" defect this screen
   *  was built to end. Caught by rendering two scenes in a row (headless, 2026-09-13). */
  const reset = () => {
    setFound(null);
    setAccepted(false);
    setRepo("");
    setRev("");
    setFile("");
    setFiles(null);
    setHits(null);
    setPicked(null);
    setId("");
    setFileFlag("");
    setDesc("");
    setStep("act");
    setAct("new");
    setSlotChosen(false);
    setWithVae(false);
    setErr("");
    setParams(engineParamsBlank);
  };
  const close = () => {
    setOpen(false);
    reset();
  };

  const start = async () => {
    setErr("");
    const c = engineNumField(ctx);
    const o = engineNumField(out);
    const key = engineIngestPrefix(isImage, fileFlag, isLora) + (file.trim() || id.trim());
    const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest`, "POST", {
      id: id.trim(),
      kind,
      s3Key: key,
      source: source(),
      description: desc.trim(),
      base_model: baseModel,
      file_flag: fileFlag,
      attach: attach && known,
      // One file changes and the row keeps everything the ingest knows nothing about — the
      // licence acceptance, the family, the params, whether it is on. Only ever against a row
      // that is there: anywhere else the CP would have to guess which of two acts was meant.
      replace: replace && known,
      context_tokens: isLora ? 0 : c && o ? c : 0,
      max_output_tokens: isLora ? 0 : c && o ? o : 0,
      params: engineParamsBody(params),
      license_accepted: true,
      // The second file, asked for in the same press and under the same acceptance — its
      // licence was on screen beside the checkpoint's own (`family_vae` on the resolve).
      with_family_vae: withVae,
    });
    if (d?.error) {
      setErr(errDetail(d.error));
      return;
    }
    const started = d as IngestJob;
    reset();
    if (onJob && started?.id) onJob(started);
    onStarted();
  };

  if (!open) {
    return (
      <button type="button" className="sm engines-open" onClick={() => setOpen(true)}>
        {tr("admin.engines_ingest_open")}
      </button>
    );
  }
  void close;
  const field = (
    label: string,
    value: string,
    set: (v: string) => void,
    placeholder = "",
    onBlur?: () => void,
  ) => (
    <label className="engines-model-add-row">
      <span>{label}</span>
      <input
        value={value}
        placeholder={placeholder}
        onChange={(ev) => set(ev.currentTarget.value)}
        onBlur={onBlur}
      />
    </label>
  );
  const at = engineIngestSteps.indexOf(step);
  /** Which of that row's slots this act can even address. It is the whole of the old
   *  "why is the checkbox grey" problem, answered by not offering the impossible: attaching
   *  needs a slot that is FREE and can never take the unlabelled one (a row's own checkpoint is
   *  filled by definition), and replacing needs one that is TAKEN. */
  const slots = flags.filter((fl) => (act === "replace" ? taken.has(fl) : fl !== "" && !taken.has(fl)));
  /** What is still missing before this step can be left, as the sentence that says so. A
   *  disabled button that does not say why is the defect this whole screen was rebuilt for. */
  const blocking = (): string => {
    if (step === "act") {
      if (act === "new") return "";
      if (!(models || []).length) return tr("admin.engines_wizard_no_rows") as string;
      if (!id.trim()) return tr("admin.engines_wizard_need_target") as string;
      if (!slots.length) return tr(("admin.engines_wizard_no_slots_" + act) as never) as string;
      if (!slotChosen) return tr("admin.engines_wizard_need_role") as string;
      return "";
    }
    if (step === "find") return repo.trim() ? "" : (tr("admin.engines_wizard_need_repo") as string);
    if (step === "file") {
      if (!found) return tr("admin.engines_wizard_need_file") as string;
      if (found.can_ingest === false) return tr("admin.engines_wizard_cannot") as string;
      if (!id.trim()) return tr("admin.engines_wizard_need_id") as string;
      if (act === "new" && !basePicksAModel && families.length > 0 && !baseModel) {
        return tr("admin.engines_wizard_need_family") as string;
      }
      return "";
    }
    return "";
  };
  const stop = blocking();
  const next = async () => {
    // Leaving 「どこから」 IS the resolve: the button that used to have to be found and pressed
    // ("調べる") was a second confirmation of a decision already made by naming the repository.
    if (step === "find") {
      await resolve();
      setStep("file");
      return;
    }
    setStep(engineIngestSteps[at + 1]);
  };
  const partLabel = fileFlag || (tr("admin.engines_model_add_part_whole") as string);
  // The four questions. The SURFACE is the pane that hosts this (adminEngineAdd.tsx) — not a
  // dialog: what this starts runs for minutes and ends at an enable press, and a dialog is
  // dismissed long before either.
  return (
    <div className="engines-wizard engines-ingest">
      <div className="engines-wizard-head">
        {/* Where this is in the four questions. A rail rather than a scrollbar: the form it
            replaced was twelve fields in one column, of which the ones that applied depended on
            state nothing on screen showed. */}
        <ol className="engines-wizard-rail">
          {engineIngestSteps.map((s, i) => (
            <li key={s} className={i === at ? "on" : i < at ? "done" : ""}>
              <span className="engines-wizard-dot" aria-hidden="true" />
              {tr(("admin.engines_wizard_step_" + s) as never)}
            </li>
          ))}
        </ol>
      </div>

      {/* ① The ACT, asked instead of inferred. It used to be decided by whether the id somebody
          typed happened to collide with an existing row — so "add this file to that model" was
          an act you discovered by accident, and the checkbox that offered it was disabled until
          a file role was chosen three fields further down, with nothing saying so. */}
      {step === "act" && (
        <div className="engines-wizard-step">
          <ul className="engines-wizard-acts">
            {engineIngestActs.map((a) => (
              <li key={a}>
                <label>
                  <input
                    type="radio"
                    name="engines-wizard-act"
                    checked={act === a}
                    onChange={() => {
                      setAct(a);
                      // The id means opposite things in the two directions: a NEW row's name is
                      // proposed from the file, and the other two acts address a row that is
                      // already there. Carrying one into the other is how an ingest lands on a
                      // working row.
                      setId("");
                      setFileFlag("");
                      setSlotChosen(false);
                    }}
                  />
                  <span className="engines-wizard-act-name">
                    {tr(("admin.engines_wizard_act_" + a) as never)}
                  </span>
                </label>
                <p className="muted">{tr(("admin.engines_wizard_act_" + a + "_why") as never)}</p>
              </li>
            ))}
          </ul>
          {/* The target is CHOSEN, never typed. Typing it was the only way to reach the other
              two acts, and it had to match an existing id exactly before the panel would admit
              they existed. */}
          {act !== "new" && !!(models || []).length && (
            <label className="engines-model-add-row">
              <span>{tr("admin.engines_wizard_target")}</span>
              <select
                value={id}
                onChange={(ev) => {
                  setId(ev.currentTarget.value);
                  setFileFlag("");
                  setSlotChosen(false);
                }}
              >
                <option value="">{tr("admin.engines_wizard_target_pick")}</option>
                {(models || []).map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.id}
                  </option>
                ))}
              </select>
            </label>
          )}
          {/* And the slot, offered as the ones this act can actually use. Nothing here can be
              grey for a reason the screen does not state. */}
          {act !== "new" && !!id.trim() && (
            <div className="engines-wizard-slots">
              <span className="muted">{tr("admin.engines_wizard_role")}</span>
              {/* Radios and not a select, because one of the answers IS the empty string (a
                  row's own checkpoint) and a select needs a placeholder that would carry the
                  same value. Every option here is one this act can perform. */}
              <ul>
                {slots.map((fl) => (
                  <li key={fl}>
                    <label>
                      <input
                        type="radio"
                        name="engines-wizard-slot"
                        checked={slotChosen && fileFlag === fl}
                        onChange={() => {
                          setFileFlag(fl);
                          setSlotChosen(true);
                        }}
                      />
                      <span>{fl === "" ? (tr("admin.engines_model_add_part_whole") as string) : fl}</span>
                    </label>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {act === "replace" && <p className="muted">{tr("admin.engines_ingest_replace_keeps_bytes")}</p>}
        </div>
      )}

      {/* ② WHERE FROM. The search and the field it fills, in that order and on one screen —
          and the field stays typeable, because a deployment with closed egress loses the search
          and keeps the ingest (ADR 0072 decision 11). */}
      {step === "find" && (
        <div className="engines-wizard-step">
          <label className="engines-search-row">
            <span>{tr("admin.engines_ingest_search")}</span>
            <input
              value={q}
              placeholder={isImage ? "sdxl" : "qwen2.5 coder"}
              onChange={(ev) => setQ(ev.currentTarget.value)}
              onKeyDown={(ev) => {
                if (ev.key === "Enter" && q.trim()) {
                  ev.preventDefault();
                  search();
                }
              }}
            />
          </label>
          <div className="engines-model-add-actions">
            {/* Civitai is only offered to the image role: it hosts image models, and the CP
                answers the llm role nothing at all rather than checkpoints llama.cpp cannot
                load. */}
            {isImage && (
              <span className="seg sm">
                {(["hf", "civitai", "civitai-red"] as const).map((sr) => (
                  <button
                    key={sr}
                    type="button"
                    className={"seg-btn" + (searchSource === sr ? " active" : "")}
                    onClick={() => {
                      setSearchSource(sr);
                      setHits(null);
                    }}
                  >
                    {tr(("admin.engines_ingest_source_" + sr) as never)}
                  </button>
                ))}
              </span>
            )}
            <span className="seg sm">
              {(["downloads", "trending", "likes"] as const).map((sr) => (
                <button
                  key={sr}
                  type="button"
                  className={"seg-btn" + (sort === sr ? " active" : "")}
                  onClick={() => {
                    setSort(sr);
                    search({ sort: sr });
                  }}
                >
                  {tr(("admin.engines_ingest_sort_" + sr) as never)}
                </button>
              ))}
            </span>
            {/* Enabled with an empty box on purpose — that is the ranking. */}
            <button type="button" className="primary sm" onClick={() => search()} disabled={busy}>
              {q.trim() ? tr("admin.engines_ingest_search_go") : tr("admin.engines_ingest_browse_go")}
            </button>
          </div>
          {hits && hits.length === 0 && <p className="muted">{tr("admin.engines_ingest_search_none")}</p>}
          {hits && hits.length > 0 && (
            <ul className="engines-search-hits">
              {hits.map((h) => (
                <HitCard key={h.source + ":" + h.ref} hit={h} onPick={() => pickHit(h)} />
              ))}
            </ul>
          )}
          {/* What was chosen, still on screen after the list it came from is gone: `repo` below
              is `civitai:1759168` — an id, not a link and not a name — and the upstream page,
              the trigger words, the gate and the licence are published nowhere else here. */}
          {picked && (
            <div className="engines-picked">
              <span className="muted engines-picked-head">{tr("admin.engines_ingest_picked")}</span>
              <ul className="engines-picked-hit">
                <HitCard hit={picked} />
              </ul>
            </div>
          )}
          {field(
            tr("admin.engines_ingest_repo"),
            repo,
            (v) => {
              setRepo(v);
              setRev("");
              setFiles(null);
              setFile("");
              setFound(null);
              setPicked(null);
            },
            searchSource === "civitai" || searchSource === "civitai-red"
              ? "civitai:782002"
              : isImage
                ? "stabilityai/stable-diffusion-xl-base-1.0"
                : "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF",
            splitRepoField,
          )}
          {/* What a pasted address turned out to name. The URL is taken apart into the fields
              the moment the box is left, and those fields are on the NEXT question — so without
              this line the screen would look as if nothing had happened to what was pasted. */}
          {!!file.trim() && (
            <p className="muted">
              {(tr("admin.engines_wizard_named_file") as string).replace("{f}", file.trim())}
            </p>
          )}
          <p className="muted">{tr("admin.engines_wizard_repo_note")}</p>
        </div>
      )}

      {/* ③ WHICH FILE, and everything this deployment could learn about it before spending
          anything: the licence, the size, what it would ask of the card, the family, the
          author's own settings — and whether the checkpoint can decode a picture at all. */}
      {step === "file" && (
        <div className="engines-wizard-step">
          {files && files.length > 0 && (
            <label className="engines-model-add-row">
              <span>{tr("admin.engines_ingest_file")}</span>
              <select value={file} onChange={(ev) => pick(ev.currentTarget.value)}>
                <option value="">{tr("admin.engines_ingest_pick")}</option>
                {/* 🔴 The mark is one-directional: WEIGHTS ALONE over the card is a definite no
                    and the only verdict this list can reach — the KV cache is not known until
                    the file is resolved. A candidate with no mark is "not ruled out here". */}
                {files.map((f) => (
                  <option key={f.name} value={f.name}>
                    {f.name}
                    {f.bytes ? " · " + fmtBytes(f.bytes) : ""}
                    {engineFitsCard(engineWeightsMiB(f.bytes), cardMiB) === false
                      ? " · " + tr("admin.engines_ingest_over_card")
                      : ""}
                  </option>
                ))}
              </select>
            </label>
          )}
          {/* 🔴 A listing with nothing in it is not a dead end. The repository may hold the file
              under a name this filter did not recognise, and the text box below is the way
              through — the screen that printed the sentence and no field left somebody with a
              repository they could see and no way to name a file in it. */}
          {files && files.length === 0 && <p className="form-err">{tr("admin.engines_ingest_no_files")}</p>}
          {(!files || files.length === 0) &&
            (listable()
              ? field(
                  tr("admin.engines_ingest_file"),
                  file,
                  (v) => setFile(v),
                  isImage ? "name.safetensors" : "name.gguf",
                  () => {
                    if (file.trim()) resolve();
                  },
                )
              : field(
                  tr("admin.engines_ingest_sha256"),
                  file,
                  setFile,
                  tr("admin.engines_ingest_sha256_ph"),
                  () => {
                    if (file.trim()) resolve();
                  },
                ))}
          {found && <ResolvedNote found={found} />}
          {found && (
            <IngestFit
              bytes={found.bytes}
              kvPerThousand={found.kv_mib_per_1k_tokens}
              contextTokens={act !== "new" ? target?.context_tokens || 0 : engineNumField(ctx)}
              cardMiB={cardMiB}
              wantsKV={!isImage && !isLora}
            />
          )}
          {/* 🔴 The one fault the file itself can be asked about (ADR 0072 follow-up). An SDXL
              checkpoint published with no VAE tensors passes every check this deployment has and
              then fails every request inside the engine, after a 1-2.5 minute checkpoint switch.
              Offered here, already ticked, because this is the moment it costs one more download
              instead of a support thread. */}
          {found?.vae_bundled === "no" && found.family_vae && (
            <label className="engines-ingest-accept">
              <input
                type="checkbox"
                checked={withVae}
                disabled={!!found.family_vae.unreachable}
                onChange={(ev) => setWithVae(ev.currentTarget.checked)}
              />
              <span>
                {(tr(
                  found.family_vae.staged
                    ? "admin.engines_wizard_vae_staged"
                    : "admin.engines_wizard_vae_take",
                ) as string)
                  .replace("{f}", found.family_vae.repo + "/" + found.family_vae.file)
                  .replace("{n}", found.family_vae.bytes ? fmtBytes(found.family_vae.bytes) : "?")
                  .replace("{l}", found.family_vae.license || "?")}
              </span>
            </label>
          )}
          {found?.vae_bundled === "no" && !found.family_vae && (
            <p className="form-err">{tr("admin.engines_wizard_vae_none")}</p>
          )}
          {/* What this file IS within the model, for a row being CREATED — it decides the bucket
              directory, which is what makes the file visible to the right loader at all. For the
              other two acts it was answered in ①, as a slot of the row it joins. */}
          {act === "new" && flags.length > 0 && (
            <label className="engines-model-add-row">
              <span>{tr("admin.engines_model_add_part")}</span>
              <select value={fileFlag} onChange={(ev) => setFileFlag(ev.currentTarget.value)}>
                {flags.map((fl) => (
                  <option key={fl} value={fl}>
                    {fl === "" ? (tr("admin.engines_model_add_part_whole") as string) : fl}
                  </option>
                ))}
              </select>
            </label>
          )}
          {/* The id is what a member sees in the launch menu. Only ever asked for a NEW row:
              the other two acts address a row that has one. */}
          {act === "new" &&
            field(
              tr("admin.engines_model_add_id"),
              id,
              setId,
              isImage ? "sdxl-base-1.0" : "qwen2.5-coder-1.5b",
            )}
          {/* ⚠️ Declared by the OPERATOR (ADR 0072 decision 2): "SDXL 1.0" and "Flux.1 D" are
              display names, and storing one as the family produced rows that looked complete and
              refused to generate. The CP's translation fills the picker; the upstream string
              rides beside it as the provenance of that choice. */}
          {act === "new" && baseOptions.length > 0 && (
            <label className="engines-model-add-row">
              <span>
                {tr(basePicksAModel ? "admin.engines_model_add_lora_base" : "admin.engines_model_add_family")}
              </span>
              <select value={baseModel} onChange={(ev) => setBaseModel(ev.currentTarget.value)}>
                <option value="">
                  {tr(
                    basePicksAModel
                      ? "admin.engines_model_add_lora_base_pick"
                      : "admin.engines_model_add_family_pick",
                  )}
                </option>
                {baseOptions.map((f) => (
                  <option key={f} value={f}>
                    {f}
                  </option>
                ))}
              </select>
            </label>
          )}
          {act === "new" && !isLora && families.length > 0 && found?.base_model && (
            <p className="muted">
              {(tr(
                found.base_model_suggest && baseModel === found.base_model_suggest
                  ? "admin.engines_family_suggested"
                  : "admin.engines_ingest_family_hint",
              ) as string).replace("{n}", found.base_model)}
            </p>
          )}
          {act === "new" && field(tr("admin.engines_model_add_desc"), desc, setDesc)}
          {act === "new" && isImage && found?.params_hint && found.params_hint_quote && (
            <p className="muted engines-param-quote">
              {tr("admin.engines_params_hint_found")} <q>{found.params_hint_quote}</q>
            </p>
          )}
          {act === "new" && isImage && (
            <EngineParamsFields
              value={params}
              onChange={setParams}
              isLora={isLora}
              family={isLora ? undefined : baseModel}
            />
          )}
          {act === "new" && !isImage && !isLora && field(tr("admin.engines_model_add_ctx"), ctx, setCtx, "32768")}
          {act === "new" && !isImage && !isLora && <OutputCapField ctx={ctx} value={out} onChange={setOut} />}
        </div>
      )}

      {/* ④ WHAT WILL HAPPEN, in sentences, with the licence beside the box that accepts it —
          and then what the row will still need before anybody can use it. "Taken in" and
          "usable" are two different states, and the screen that ends at the first one is the
          screen somebody has to be told how to finish. */}
      {step === "confirm" && (
        <div className="engines-wizard-step">
          <ul className="engines-wizard-plan">
            <li>
              {(tr(("admin.engines_wizard_plan_" + act) as never) as string)
                .replace("{id}", id.trim())
                .replace("{part}", partLabel)}
            </li>
            <li>
              {(tr("admin.engines_wizard_plan_from") as string)
                .replace("{r}", repo.trim())
                .replace("{f}", file.trim() || "?")
                .replace("{n}", found?.bytes ? fmtBytes(found.bytes) : "?")}
            </li>
            {withVae && found?.family_vae && (
              <li>
                {(tr(
                  found.family_vae.staged
                    ? "admin.engines_wizard_vae_staged"
                    : "admin.engines_wizard_vae_take",
                ) as string)
                  .replace("{f}", found.family_vae.repo + "/" + found.family_vae.file)
                  .replace("{n}", found.family_vae.bytes ? fmtBytes(found.family_vae.bytes) : "?")
                  .replace("{l}", found.family_vae.license || "?")}
              </li>
            )}
            <li>{tr("admin.engines_wizard_plan_after")}</li>
          </ul>
          {found && <ResolvedNote found={found} />}
          {found && (
            <label className="engines-ingest-accept">
              <input
                type="checkbox"
                checked={accepted}
                disabled={found.can_ingest === false}
                onChange={(ev) => setAccepted(ev.currentTarget.checked)}
              />
              <span>{tr("admin.engines_ingest_accept")}</span>
            </label>
          )}
        </div>
      )}

      {err && <p className="form-err">{err}</p>}
      <div className="engines-wizard-nav">
        {at > 0 && (
          <button type="button" className="sm" onClick={() => setStep(engineIngestSteps[at - 1])}>
            {tr("admin.engines_wizard_back")}
          </button>
        )}
        {step === "confirm" ? (
          <button
            type="button"
            className="primary sm"
            disabled={busy || !accepted || !id.trim() || found?.can_ingest === false}
            onClick={start}
          >
            {tr("admin.engines_ingest_go")}
          </button>
        ) : (
          <button type="button" className="primary sm" disabled={busy || !!stop} onClick={next}>
            {tr("admin.engines_wizard_next")}
          </button>
        )}
        {/* 🔴 The reason the button is grey, beside the button. Not a tooltip and not an error
            after the press: the form this replaced had three controls that disabled each other
            and said nothing, which is where an operator of this deployment lost an evening. */}
        {!!stop && step !== "confirm" && <span className="muted engines-wizard-stop">{stop}</span>}
      </div>
      {/* What this route is and is not — the CP touches neither S3 nor the token, and the row it
          creates lands disabled. It belongs where a source is being named or accepted, not over
          the question about which act to perform. */}
      {(step === "find" || step === "confirm") && <p className="muted">{tr("admin.engines_ingest_note")}</p>}
    </div>
  );
}

/** A number out of one of this form's text fields. 0 means "nothing usable typed", which every
 *  caller draws as "not declared" rather than as zero. */
export function engineNumField(v: string): number {
  const n = Number(v.trim().replace(/[_,]/g, ""));
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0;
}

/** What a file's WEIGHTS would take on the card, in the unit the GPU ladder is declared in.
 *  The same conversion the CP makes in engineModelVramNeed: bytes as whoever staged them
 *  declared, MiB as a card is measured. */
export function engineWeightsMiB(bytes?: number): number {
  return bytes && bytes > 0 ? Math.round(bytes / (1024 * 1024)) : 0;
}

/** What the KV cache costs at this window, from the CP's per-1024-token number.
 *
 * 🔴 A MULTIPLICATION and nothing else. The cache is linear in the context length, which is why
 * one number crosses the wire at all; the formula (`n_layer × n_head_kv × (k+v) × ctx × 2`)
 * stays in engine_gguf.go, because a second copy here would be a second thing to fix the day a
 * model declares different key and value widths.
 *
 * 0 is "no answer": either the header was never read (the field is absent, never zero) or no
 * window has been typed yet. Both mean the caller draws no cache, not a free one. */
export function engineKVMiB(perThousand?: number, contextTokens?: number): number {
  if (!perThousand || !contextTokens || contextTokens <= 0) return 0;
  return Math.round((perThousand * contextTokens) / 1024);
}

/** Does this need fit the card — and is the question answerable at all?
 *
 * 🔴 `undefined` is the answer on a deployment that declared no GPU ladder: its box was bought
 * by CloudFormation, `row.class` is absent, and there is nothing to compare against. The caller
 * must then draw NO verdict — "it fits" said with no card to fit into is a lie, and this panel
 * is read by somebody deciding whether to spend four minutes and a GPU on a download. */
export function engineFitsCard(needMiB: number, cardMiB?: number): boolean | undefined {
  if (!cardMiB || cardMiB <= 0 || needMiB <= 0) return undefined;
  return needMiB <= cardMiB;
}

/** What pressing 「取り込む」 would ask the card to hold, before it is pressed.
 *
 * 🔴 Measured on a borrowed llm engine: an L4 (24 GB) loaded 17 GB of weights and then died on
 * `cudaMalloc failed: out of memory … failed to allocate buffer for kv cache` for the 16 GB the
 * window wanted — four minutes and one purchased GPU after the button. The weights were never
 * the question, and the panel showed nothing but a filename and a size.
 *
 * Two things it must not do. It must not compare against a card that does not exist (see
 * engineFitsCard), and it must not let an UNREAD KV cache pass as a small one: for a GGUF the
 * cache is most of the answer, so when the header could not be read the line says so and the
 * total is labelled as the weights alone. The CP's read is best-effort and silent by design
 * (engine_gguf.go), so "absent" is a state this screen meets in normal use. */
function IngestFit({
  bytes,
  kvPerThousand,
  contextTokens,
  cardMiB,
  wantsKV,
}: {
  bytes?: number;
  kvPerThousand?: number;
  contextTokens: number;
  cardMiB?: number;
  /** Whether a KV cache is part of this model's answer at all. A diffusion checkpoint has none
   *  — ADR 0074's first measurement put the image role's memory in the compute buffers — and an
   *  adapter is not loaded on its own, so for those the silence is correct rather than missing. */
  wantsKV: boolean;
}) {
  const tr = useT();
  const weights = engineWeightsMiB(bytes);
  if (!weights) return null;
  const kv = engineKVMiB(kvPerThousand, contextTokens);
  const need = weights + kv;
  const fits = engineFitsCard(need, cardMiB);
  const bits = [(tr("admin.engines_ingest_fit_weights") as string).replace("{n}", String(weights))];
  if (wantsKV) {
    bits.push(
      kv > 0
        ? (tr("admin.engines_ingest_fit_kv") as string)
            .replace("{n}", String(kv))
            .replace("{c}", String(contextTokens))
        : (tr("admin.engines_ingest_fit_kv_unread") as string),
    );
  }
  if (fits !== undefined) {
    bits.push(
      (tr("admin.engines_ingest_fit_card") as string)
        .replace("{n}", String(need))
        .replace("{c}", String(cardMiB)),
    );
  }
  return (
    <>
      <p className={"engines-ingest-fit " + (fits === false ? "form-err" : "muted")}>
        {bits.join(" · ")}
      </p>
      {fits === false && <p className="form-err">{tr("admin.engines_ingest_fit_over")}</p>}
    </>
  );
}

/** Where in the bucket a taken-in file goes, from what it IS within the model.
 *
 * 🔴 Not cosmetic, and not a place a wrong answer is ever reported: the box mirrors the bucket
 * and ComfyUI builds each loader's menu from its own directory, so a text encoder staged under
 * `image/checkpoints/` is one `UNETLoader` list it can never appear in. Measured on af-sandbox
 * (ADR 0072 P2 残作業 5): a 22.2 GiB FLUX.1 checkpoint sat in `image/checkpoints/` and no flux1
 * template could reach it, which read as "the model does not work".
 *
 * The layout is ADR 0072 decision 2's, which is ComfyUI's own convention (ADR 0071 decision 6). */
export function engineIngestPrefix(isImage: boolean, flag: string, isLora = false): string {
  // Adapters are flat inside one directory per role, which is what both engines scan and what
  // the llm preset points at file by file (ADR 0072 decision 5).
  if (isLora) return isImage ? "image/loras/" : "llm/loras/";
  if (!isImage) return "llm/";
  switch (flag) {
    case "--diffusion-model":
      return "image/diffusion_models/";
    // All three encoders live in one directory — that IS what TripleCLIPLoader enumerates.
    case "--clip_l":
    case "--clip_g":
    case "--t5xxl":
      return "image/text_encoders/";
    case "--vae":
      return "image/vae/";
    default:
      return "image/checkpoints/";
  }
}

/** A catalogue id proposed from the filename that was picked.
 *
 * The id is what a member sees in the launch menu, so it wants to be short — but it is also a
 * key, and the person choosing it has just read the filename and nothing else. Proposing the
 * stem gets `flux1-dev.safetensors` to `flux1-dev` exactly, and gets a GGUF most of the way
 * there once the quantisation tag comes off, which is a detail of the FILE and not of the model
 * (the same model at q4 and q8 is one model with two files).
 *
 * A proposal, never a decision: it fills an empty field and the field stays editable, for the
 * same reason the context window does. Deriving `sdxl-base-1.0` from `sd_xl_base_1.0` is where
 * this stops being derivation and starts being guessing, and it is left to the person. */
export function engineIdFromFile(file: string): string {
  const stem = (file.split("/").pop() || "")
    .replace(/\.(safetensors|gguf|ckpt|pt|sft|bin)$/i, "")
    .toLowerCase();
  // The quantisation / precision tag, which names the file rather than the model.
  return stem.replace(/[-.](q\d+(_[a-z0-9]+)*|iq\d+(_[a-z0-9]+)*|f16|fp16|bf16|f32|fp32|fp8(_[a-z0-9]+)*|int8)$/, "");
}

/** The output cap, as a fraction of the context window.
 *
 * A free number here is a question nobody can answer from a model card: how much of the window
 * one reply may consume is a deployment's choice, not a property Hugging Face publishes. The
 * fractions are computed from whatever window is in the field beside it, so the two can never
 * disagree — and 1/8 of the 32,768 both models in this deployment declare is 4,096, which is
 * what they were being run at by hand. Falls back to a plain number while no window is known,
 * because a select with nothing to compute from would offer nothing at all. */
function OutputCapField({
  ctx,
  value,
  onChange,
}: {
  ctx: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const tr = useT();
  const window = Math.floor(Number(ctx.trim().replace(/[_,]/g, "")));
  if (!Number.isFinite(window) || window <= 0) {
    return (
      <label className="engines-model-add-row">
        <span>{tr("admin.engines_model_add_out")}</span>
        <input value={value} placeholder="4096" onChange={(ev) => onChange(ev.currentTarget.value)} />
      </label>
    );
  }
  const options = [4, 8, 16].map((d) => ({ d, n: Math.floor(window / d) })).filter((o) => o.n > 0);
  return (
    <label className="engines-model-add-row">
      <span>{tr("admin.engines_model_add_out")}</span>
      <select value={value} onChange={(ev) => onChange(ev.currentTarget.value)}>
        <option value="">{tr("admin.engines_ingest_pick")}</option>
        {options.map((o) => (
          <option key={o.d} value={String(o.n)}>
            {`1/${o.d}（${o.n}）`}
          </option>
        ))}
      </select>
    </label>
  );
}

/** What the source turned out to be. Every line is a fact the control plane read from the
 *  source's own API — nothing here is guessed, and the two licence fields are both shown
 *  because Hugging Face answers `other` for the non-commercial ones. */
function ResolvedNote({ found }: { found: ResolvedSource }) {
  const tr = useT();
  const bits: string[] = [];
  if (found.bytes) bits.push(fmtBytes(found.bytes));
  if (found.license_name || found.license) bits.push(found.license_name || found.license || "");
  if (found.base_model) bits.push(found.base_model);
  // Attributed, always: it is the architecture's ceiling, not a window this deployment has
  // decided it can afford, and the two differ by 8x on the model already running here.
  if (found.context_length) {
    bits.push((tr("admin.engines_ingest_ctx_max") as string).replace("{n}", String(found.context_length)));
  }
  return (
    <>
      <p className="muted engines-model-meta">
        {bits.join(" · ")}
        {found.sha256 ? <span className="mono"> {found.sha256.slice(0, 12)}…</span> : null}
      </p>
      {found.commercial_use === "no" && (
        <p className="form-err">{tr("admin.engines_ingest_noncommercial")}</p>
      )}
      {found.gated && (
        <p className={found.can_ingest === false ? "form-err" : "muted"}>
          {tr(found.can_ingest === false ? "admin.engines_ingest_gated_no_token" : "admin.engines_ingest_gated")}
        </p>
      )}
      {/* 🔴 A different wall from the one above, and there is no key to it here: Civitai's
          metadata answers 200 for everybody and the BYTES are per uploader, so this used to
          be found nine minutes into a Fargate task as a bare curl exit code. The sentence
          says what can be done instead, because registering a token is not it. */}
      {found.login_required && <p className="form-err">{tr("admin.engines_ingest_civitai_login")}</p>}
      {/* Gated WITH a token is not a yes: the terms also have to have been accepted by that
          token's own account, on this repository, and nothing here can check that. Said before
          the press because the alternative is finding out from a 403 minutes later. */}
      {found.gated_needs_acceptance && (
        <p className="muted">{tr("admin.engines_ingest_gated_accept_first")}</p>
      )}
    </>
  );
}

/** The jobs this engine has run, newest first. Shown only when there are any: an empty list is
 *  the normal state and a heading over nothing reads as something being broken.
 *
 * 🔴 This is a LOG OF EVENTS, not the catalogue, and it outlives the rows it created — a job
 *    stays after its model has been forgotten and its bytes deleted, because "this ingest ran
 *    and finished" goes on being true. Observed on the dev deployment (2026-09-09): two
 *    finished jobs sat under the form for a model that no longer existed anywhere.
 *
 *    So every row is DATED and the list is headed. Without a time, a green "done" beside a
 *    model id reads as the current state of that model — i.e. as "this one is ready to use" —
 *    which is exactly wrong for a row whose model has been deleted. */
/** Looking at what there is to stage, on a deployment with no engine to stage it INTO.
 *
 * The same read as the ingest form's picker (ADR 0072 decision 11) with no engine in the path:
 * the CP holds no token, reads no bucket and starts no task to answer it, so nothing about it
 * needs 60-engines to be deployed. What it cannot do is take anything in, and the note says so
 * — an administrator deciding whether self-hosted inference is worth standing up is exactly the
 * person who cannot see the catalogue today. */
function EngineBrowse() {
  const tr = useT();
  const [q, setQ] = useState("");
  const [kind, setKind] = useState("gguf");
  const [sort, setSort] = useState("downloads");
  const [source, setSource] = useState("hf");
  const [hits, setHits] = useState<IngestHit[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const run = async (opts: { kind?: string; sort?: string; source?: string } = {}) => {
    setBusy(true);
    setErr("");
    try {
      const k = opts.kind ?? kind;
      const d = await apiJSON(`api/admin/engines/search?kind=${encodeURIComponent(k)}`, "POST", {
        q,
        source: opts.source ?? source,
        sort: opts.sort ?? sort,
      });
      if (d?.error) {
        setHits(null);
        setErr(errDetail(d.error));
        return;
      }
      setHits((Array.isArray(d?.hits) ? d.hits : []) as IngestHit[]);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="engines-model-add engines-ingest">
      <label className="engines-search-row">
        <span>{tr("admin.engines_ingest_search")}</span>
        <input
          value={q}
          placeholder={kind === "gguf" ? "qwen2.5 coder" : "sdxl"}
          onChange={(ev) => setQ(ev.currentTarget.value)}
          onKeyDown={(ev) => {
            if (ev.key === "Enter") {
              ev.preventDefault();
              run();
            }
          }}
        />
      </label>
      <div className="engines-model-add-actions">
        {/* With no engine there is nothing to derive the kind from, so it is asked. */}
        <span className="seg sm">
          {(["gguf", "checkpoint"] as const).map((k) => (
            <button
              key={k}
              type="button"
              className={"seg-btn" + (kind === k ? " active" : "")}
              onClick={() => {
                setKind(k);
                setHits(null);
                // Civitai hosts image models only, so switching to GGUF has to take the source
                // back with it — otherwise the next search is Civitai-for-LLM, which the CP
                // correctly answers with nothing and which reads as a broken search.
                if (k === "gguf") setSource("hf");
              }}
            >
              {tr(("admin.engines_browse_kind_" + k) as never)}
            </button>
          ))}
        </span>
        {/* Civitai only for checkpoints — the same rule the ingest form follows. */}
        {kind === "checkpoint" && (
          <span className="seg sm">
            {(["hf", "civitai", "civitai-red"] as const).map((sr) => (
              <button
                key={sr}
                type="button"
                className={"seg-btn" + (source === sr ? " active" : "")}
                onClick={() => {
                  setSource(sr);
                  setHits(null);
                }}
              >
                {tr(("admin.engines_ingest_source_" + sr) as never)}
              </button>
            ))}
          </span>
        )}
        <span className="seg sm">
          {(["downloads", "trending", "likes"] as const).map((sr) => (
            <button
              key={sr}
              type="button"
              className={"seg-btn" + (sort === sr ? " active" : "")}
              onClick={() => {
                setSort(sr);
                run({ sort: sr });
              }}
            >
              {tr(("admin.engines_ingest_sort_" + sr) as never)}
            </button>
          ))}
        </span>
        <button type="button" className="primary sm" onClick={() => run()} disabled={busy}>
          {q.trim() ? tr("admin.engines_ingest_search_go") : tr("admin.engines_ingest_browse_go")}
        </button>
      </div>
      {hits && hits.length === 0 && <p className="muted">{tr("admin.engines_ingest_search_none")}</p>}
      {hits && hits.length > 0 && (
        <ul className="engines-search-hits">
          {/* No pick button: there is nowhere to put it. Picking one fills an ingest form, and
              this deployment has no role to ingest into. */}
          {hits.map((h) => (
            <HitCard key={h.source + ":" + h.ref} hit={h} />
          ))}
        </ul>
      )}
      {err && <p className="form-err">{err}</p>}
      <p className="muted">{tr("admin.engines_browse_note")}</p>
    </div>
  );
}

/** One search result, as a card.
 *
 * The three kinds of fact are told apart by KIND rather than by a separator character — what
 * it is called, the numbers a ranking is built on, the terms it comes with — because twenty
 * results as one "・"-joined line each are a wall of text with nothing to scan by.
 *
 * Every part is omitted rather than guessed: the two APIs answer different subsets, and a zero
 * download count reads as a fact.
 *
 * The gating flag and the licence stay on the card rather than moving behind a detail view.
 * They decide whether this row is takeable at all, and learning that from a refusal one step
 * later is the dead end the whole picker exists to avoid. */
/** Date only, with the year: a search result's dates are months or years old, and the default
 *  "M/D HH:MM" of fmtDateTime would print a 2024 model as if it were this year. */
const HIT_DATE: Intl.DateTimeFormatOptions = { year: "numeric", month: "numeric", day: "numeric" };

/** The word for one restriction code. The codes are the CP's closed set (engineRestrict* in
 *  engine_ingest_limits.go) and the words live only here, because the two sources spell the same
 *  restriction differently and this is where the locale catalogue is.
 *
 *  An unknown code is printed VERBATIM rather than dropped: a Control Plane newer than this
 *  bundle is exactly the case where a restriction nobody here has heard of matters most. */
function engineRestrictLabel(code: string): string {
  return tMaybe("admin.engines_limit_" + code) ?? code;
}

/** Which restrictions stop the download rather than limit what may be done with it. They are
 *  drawn in the warning colour: one of them means choosing this row is choosing a refusal, and
 *  the other kind is a decision for a human to make. */
const ENGINE_HARD_LIMITS = new Set([
  "gated_auto",
  "gated_manual",
  "paid",
  "early_access",
  "private",
  "generate_only",
  "unscanned",
  "pickle",
]);

function engineRestrictIsHard(code: string): boolean {
  return ENGINE_HARD_LIMITS.has(code);
}

function HitCard({ hit, onPick }: { hit: IngestHit; onPick?: () => void }) {
  const tr = useT();
  const stat = (n: number | undefined, key: string) =>
    n ? (
      <span className="engines-hit-stat">
        <b>{fmtCount(n)}</b>
        {(tr(key as never) as string).trim()}
      </span>
    ) : null;
  /** Published and last-updated, as ONE flex item rather than two.
   *
   * 🔴 Measured on the real bundle (ja, the modal's width): the busiest card's strip is 390px
   * and its three counts plus two separate dates come to 392 — two over, so the pair split
   * across a line break and the card grew 118px → 146px. Kept together they are 382 and the
   * line holds; when a locale's labels are wider they move down as a pair, which is the
   * legible way to lose the race. */
  const dates = [
    { k: "published", iso: hit.published_at, label: "admin.engines_ingest_hit_published" },
    { k: "updated", iso: hit.updated_at, label: "admin.engines_ingest_hit_updated" },
  ].filter((d) => !!d.iso);
  const lic = hit.license_name || hit.license;
  return (
    <li className="engines-hit">
      <div className="engines-hit-head">
        <span className="mono engines-hit-name">{hit.name}</span>
        {/* The page this row came from. The href is the CP's string as it stands — building it
            here would mean the panel learning both sources' spellings, and Civitai's needs an
            id this row does not carry. stopPropagation because the card is the "choose this
            result" surface: a link that also chose would send somebody two places at once. */}
        {hit.url && (
          <a
            className="engines-hit-link"
            href={hit.url}
            target="_blank"
            rel="noopener noreferrer"
            onClick={(ev) => ev.stopPropagation()}
          >
            {tr(hit.source === "civitai" ? "admin.engines_ingest_hit_open_civitai" : "admin.engines_ingest_hit_open_hf")}
          </a>
        )}
        {onPick && (
          <button type="button" className="sm engines-hit-pick" onClick={onPick}>
            {tr("admin.engines_ingest_hit_pick")}
          </button>
        )}
      </div>
      <div className="engines-hit-stats">
        {stat(hit.downloads, "admin.engines_ingest_hit_downloads")}
        {stat(hit.likes, "admin.engines_ingest_hit_likes")}
        {stat(hit.trending, "admin.engines_ingest_hit_trending")}
        {/* The pair, in the same row as the counts and each labelled: "published in 2024, last
            touched last week" and "published last week" are different models to choose
            between, and one date alone says neither. Absent is absent — Civitai publishes no
            update date at all, and an empty label would read as "never". */}
        {dates.length > 0 && (
          <span className="engines-hit-stat engines-hit-date">
            {dates.map((d) => (
              <span key={d.k}>
                {(tr(d.label as never) as string).trim()} <b>{fmtDateTime(d.iso as string, HIT_DATE)}</b>
              </span>
            ))}
          </span>
        )}
      </div>
      <div className="engines-hit-tags">
        {/* 🔴 What would REFUSE this row comes first and in its own colour, because it is the
            only kind of tag that turns a choice into a wasted nine-minute download.
            "Login required" leads even that: it is the one restriction no token on this
            deployment can satisfy, and measured 2026-09-12 it is true of 13 of the 20 rows
            Civitai's own ranking puts on the first screen. Absent means the CP could not tell
            — never "anyone may have it" — so nothing is drawn for it. */}
        {hit.login_required === "yes" && (
          <span className="engines-model-tag warn" title={tr("admin.engines_hit_login_note")}>
            {tr("admin.engines_hit_login_required")}
          </span>
        )}
        {/* Civitai's own content rating. Drawn on every Civitai hit, not only ones from the
            civitai-red tab — the plain tab's own default query still answers a nonzero level
            (measured), so a card without this would read as "safe" on a false premise. */}
        {!!hit.nsfw_level && (
          <span className="engines-model-tag">
            {(tr("admin.engines_ingest_hit_nsfw_level" as never) as string).replace("{n}", String(hit.nsfw_level))}
          </span>
        )}
        {/* The gate, in the two flavours Hugging Face publishes. The bare `gated` stays as the
            fallback for a CP too old to send which kind, so this panel never loses the warning
            it has had since P4 just because the field it now prefers is missing. */}
        {hit.restrictions?.length
          ? hit.restrictions.map((code) => (
              <span key={code} className={"engines-model-tag" + (engineRestrictIsHard(code) ? " warn" : "")}>
                {engineRestrictLabel(code)}
              </span>
            ))
          : hit.gated && (
              <span className="engines-model-tag warn">{tr("admin.engines_ingest_hit_gated")}</span>
            )}
        {lic && <span className="engines-model-tag">{lic}</span>}
        {hit.base_model && <span className="engines-model-tag">{hit.base_model}</span>}
        {/* A LoRA does nothing without its trigger, and this is the only place the words are
            published in a form that can be copied rather than read off a picture. */}
        {hit.trained_words?.length ? (
          <span className="engines-model-tag">
            {(tr("admin.engines_hit_trigger") as string) + ": " + hit.trained_words.join(", ")}
          </span>
        ) : null}
        {!!hit.bytes && <span className="engines-model-tag">{fmtBytes(hit.bytes)}</span>}
        {!!hit.context_length && (
          <span className="engines-model-tag">
            {(tr("admin.engines_ingest_ctx_max" as never) as string).replace("{n}", String(hit.context_length))}
          </span>
        )}
      </div>
    </li>
  );
}

/** 1,632,949 → 1.6M. The exact number is noise next to "is this the one everybody uses".
 *
 * 🔴 The small end is rounded because one of these numbers is a SCORE, not a count: Hugging
 * Face's `trendingScore` comes back fractional, and 0.7000000000000001 is what a raw
 * `String(n)` puts on the row. */
function fmtCount(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1).replace(/\.0$/, "") + "M";
  if (n >= 1_000) return Math.round(n / 1_000) + "k";
  return String(Math.round(n * 10) / 10);
}

/** The sentence that says what to do about a failed job, or "" when the CP could not tell.
 *
 * A table rather than one key per code so that the two gated refusals cannot end up sharing a
 * sentence: 401 sends somebody to the token field and 403 sends them to the model page, and
 * they arrive one character apart in the task's log. */
export function engineJobAdvice(code?: string): string {
  switch (code) {
    case "gated_not_accepted":
      return "admin.engines_ingest_job_not_accepted";
    case "gated_no_token":
      return "admin.engines_ingest_job_no_token";
    case "civitai_login_required":
      return "admin.engines_ingest_civitai_login";
    default:
      return "";
  }
}

export function EngineIngestJobs({
  jobs,
  busy,
  readOnly,
  onForget,
  onReuse,
}: {
  jobs: IngestJob[];
  /** The id of the job a request is in flight for, so one press disables one row's buttons
   *  rather than the whole list. */
  busy: string;
  /** A borrowed engine's screen (ADR 0079 decision 7). The CP would in fact accept the delete —
   *  the job ledger is THIS deployment's, not the mirror's — but this whole panel is read-only
   *  for a borrowed role, and one live button among absent ones reads as "the rest are broken". */
  readOnly: boolean;
  onForget: (id: string) => void;
  /** Open the registration form on this job's key, or undefined for a caller who may not
   *  register anything (a granted tenant_admin: the bucket is the operator's). */
  onReuse?: (j: IngestJob) => void;
}) {
  const tr = useT();
  const [confirming, setConfirming] = useState("");
  /** The extra press a job with nothing pointing at its file needs. Reset with the confirmation
   *  it belongs to, so it cannot be carried from one row to the next. */
  const [ack, setAck] = useState(false);
  const openConfirm = (id: string) => {
    setConfirming(id);
    setAck(false);
  };
  if (jobs.length === 0) return null;
  return (
    <>
    <p className="muted engines-ingest-jobs-head">{tr("admin.engines_ingest_jobs_head")}</p>
    <ul className="engines-model-list engines-ingest-jobs">
      {jobs.map((j) => {
        // 🔴 Only a job that has STOPPED may be forgotten. The row is not the task: deleting it
        // leaves the ECS task downloading, and it still writes its catalogue row minutes later
        // with nothing on screen that explains where the model came from. The CP refuses this
        // too (409 ingest_job_live) — the button is absent here so that the refusal is read
        // before the press rather than after it.
        const live = j.state === "running" || j.state === "pending";
        // Nothing in the catalogue points at this file, so this row is the last written record
        // of its key: forgetting it also forgets the address of bytes that are still being paid
        // for, and with it the only place that key can be picked from to register it again.
        const last = !j.key_used_by;
        return (
        <li key={j.id} className="engines-model">
          <div className="engines-model-head">
            <span className="mono engines-model-id">{j.model_id}</span>
            {/* The outcome carries a colour, because that is what the list is scanned for: a
                row that failed and a row that finished look identical in a neutral pill. */}
            <span className={"engines-model-tag " + engineJobTone(j.state)}>
              {tr(("admin.engines_ingest_state_" + j.state) as never)}
            </span>
            {j.created_at && (
              <span className="muted engines-ingest-when">{fmtDateTime(j.created_at)}</span>
            )}
          </div>
          <p className="muted engines-model-meta">
            {j.source}
            {j.bytes ? " · " + fmtBytes(j.bytes) : ""}
          </p>
          {/* The task's own words, not an exit code: "sha256 mismatch" and "401 on a gated
              repository" need different things from the person reading them. */}
          {j.message && <p className="form-err engines-model-meta">{j.message}</p>}
          {/* And what to do about it, when the status says. The curl line above is the task's
              own words and stays; this is the action, in the reader's language. */}
          {!!engineJobAdvice(j.code) && (
            <p className="form-err engines-model-meta">{tr(engineJobAdvice(j.code) as never)}</p>
          )}
          {/* The refusal, in place of the button rather than behind it. "Why can I not delete
              this one" is answered before the press, which is the only place it helps. */}
          {!readOnly && live && (
            <p className="muted engines-model-meta engines-ingest-job-live">
              {tr("admin.engines_ingest_job_forget_live")}
            </p>
          )}
          {!readOnly && !live && confirming !== j.id && (
            <span className="engines-model-actions">
              {/* 🔴 "Take that file in again" is not an ingest: the bytes are in the bucket
                  already (forgetting a row without `?purge=1` leaves them — measured on the dev
                  deployment, 2026-09-09: a 491 MB object outlived its row), so this opens the
                  REGISTRATION form on the key. What was missing was never the route — it was
                  the list of keys, which lives only here and had to be retyped by hand.

                  Only for a job that FINISHED, and even then only as an offer: a `done` job is
                  not proof the file is there. A purge deletes the bytes and leaves the job
                  `done` for ever, and the CP cannot look in the bucket to check (review R3). A
                  failed job is not offered at all — whatever it left behind is a part of a
                  file, and registering that would produce a row that fails at load. */}
              {j.state === "done" && !!j.s3_key && onReuse && (
                <Button
                  variant="ghost"
                  small
                  className="engines-ingest-job-reuse"
                  onClick={() => onReuse(j)}
                >
                  {tr("admin.engines_ingest_job_reuse")}
                </Button>
              )}
              <Button
                variant="ghost"
                small
                className="engines-ingest-job-forget"
                disabled={busy === j.id}
                onClick={() => openConfirm(j.id)}
              >
                {tr("admin.engines_ingest_job_forget")}
              </Button>
            </span>
          )}
          {confirming === j.id && !live && (
            <div className="engines-model-confirm engines-ingest-job-confirm">
              {/* The KEY, here and nowhere else on the row. This is the moment it matters: the
                  row is about to stop being a record of it, and with nothing in the catalogue
                  pointing at it this is the last time anyone can read it off a screen. */}
              {j.s3_key && <p className="mono engines-model-keys">{j.s3_key}</p>}
              <p className={last ? "form-err" : "muted"}>
                {last
                  ? tr("admin.engines_ingest_job_forget_last")
                  : (tr("admin.engines_ingest_job_forget_used") as string).replace(
                      "{who}",
                      j.key_used_by || "",
                    )}
              </p>
              {/* 🔴 A second, explicit act for the row whose key nothing else holds — and only
                  for that one. Forgetting a job whose file a catalogue row already names loses
                  nothing (the key is written down in that row), so asking twice there would
                  train the tick out of meaning anything. */}
              {last && (
                <label className="engines-ingest-job-ack">
                  <input
                    type="checkbox"
                    checked={ack}
                    onChange={(ev) => setAck(ev.currentTarget.checked)}
                  />
                  <span>{tr("admin.engines_ingest_job_forget_ack")}</span>
                </label>
              )}
              <span className="engines-model-actions">
                <Button
                  small
                  disabled={busy === j.id || (last && !ack)}
                  onClick={() => {
                    setConfirming("");
                    onForget(j.id);
                  }}
                >
                  {tr("admin.engines_ingest_job_forget_go")}
                </Button>
                <Button small onClick={() => setConfirming("")}>
                  {tr("common.cancel")}
                </Button>
              </span>
            </div>
          )}
        </li>
        );
      })}
    </ul>
    </>
  );
}

/** Which badge colour an ingest job's state earns. An unknown state gets the neutral pill
 *  rather than a guess: the CP may grow one, and drawing it green would be a claim. */
function engineJobTone(state: string): string {
  if (state === "done") return "on";
  if (state === "failed") return "bad";
  if (state === "pending" || state === "running") return "lead";
  return "";
}

function fmtBytes(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(1) + " GB";
  if (n >= 1e6) return Math.round(n / 1e6) + " MB";
  return n + " B";
}

/** Where this row and each of its files came from.
 *
 * Its own line rather than two more items in the meta string, because these are the only facts on
 * the row that are LINKS. The id is short and unique only inside this deployment (it is what a
 * member reads in the launch menu), so the source is the one thing that says WHICH vendor's model
 * of that name this is — and the value was already stored, just never shown as anything but text.
 *
 * 🔴 Three states, drawn differently on purpose:
 *
 *   - a source with a `source_url` is a link. The URL is the CP's, verbatim — composing it here
 *     would mean the panel learning both vendors' spellings, and `civitai:<id>` is a model
 *     VERSION id whose `/models/<id>` opens a DIFFERENT model;
 *   - a source WITHOUT one stays text. That is a plain `url:` source (the direct download of the
 *     weights, which no "where this came from" line should start) or a prefix this deployment's
 *     CP does not know. A broken link in an operator's console is worse than a string;
 *   - no source at all draws NOTHING. "Nobody recorded one" and "recorded, and unreadable" are
 *     different facts and this panel draws them apart everywhere else (the licence line above
 *     says so out loud) — a seeded row and every row from before migration 0060 is in the first
 *     state, and labelling those "unknown" would assert that somebody looked.
 *
 * A FILE whose source is the row's own is not repeated: for a single-file model the two are the
 * same string. What is left are the parts that came from somewhere else, which is exactly the
 * question a split model could not answer — a four-file FLUX.1 row carried one line about its
 * diffusion model and nothing at all about the three text encoders beside it. */
function ModelProvenance({ model }: { model: EngineModel }) {
  // Read from `file_rows` when it is there, and from `files` otherwise. NOT matched by position
  // against `files`: the two are emitted from the same loop today, and a rule that silently
  // mislabels every part the day one of them starts skipping an entry is not worth the base
  // names it saves. The base name is the last segment, which is the CP's own rule (path.Base).
  const parts = model.file_rows?.length
    ? model.file_rows.map((f) => ({
        name: f.s3Key.split("/").pop() || f.s3Key,
        source: f.source,
        url: f.source_url,
      }))
    : (model.files || []).map((name) => ({ name, source: undefined, url: undefined }));
  if (!model.source && parts.length === 0) return null;
  const bits = [
    ...(model.source ? [<SourceText text={model.source} url={model.source_url} />] : []),
    ...parts.map((p) =>
      p.source && p.source !== model.source ? (
        <SourceText text={p.name} url={p.url} title={p.source} />
      ) : (
        <span className="engines-model-file">{p.name}</span>
      ),
    ),
  ];
  return (
    <p className="muted engines-model-meta engines-model-provenance">
      {/* Keyed by position: two parts of one model can share a base name (two directories hold
          `model.safetensors`), and a name key would drop one of them. */}
      {bits.map((b, i) => (
        <Fragment key={i}>
          {i > 0 && " · "}
          {b}
        </Fragment>
      ))}
    </p>
  );
}

/** Aggregate a multipart row without hiding partial loss. The dedicated storage response is
 * the only authority here; a finished ingest job is deliberately not consulted. */
function ModelStorageStatus({ model, storageFiles }: {
  model: EngineModel;
  storageFiles?: EngineStorageFile[] | null;
}) {
  const tr = useT();
  const keys = (model.file_rows || []).map((file) => file.s3Key).filter(Boolean);
  if (!keys.length) return null;
  if (!storageFiles) {
    return <p className="engines-model-storage unknown">{tr("admin.catalog_registered_unknown" as never)}</p>;
  }
  const states = keys.map((key) => storageFiles.find((file) => file.s3_key === key)?.state || "unknown");
  const present = states.filter((state) => state === "present").length;
  const missing = states.filter((state) => state === "missing").length;
  const status = present === keys.length ? "present"
    : missing === keys.length ? "missing"
      : present > 0 || missing > 0 ? "partial" : "unknown";
  return <p className={`engines-model-storage ${status}`}>
    {tr((`admin.catalog_registered_${status}`) as never, { present, total: keys.length } as never)}
  </p>;
}

/** One provenance value: a link when the CP could compose one, the same text when it could not. */
function SourceText({ text, url, title }: { text: string; url?: string; title?: string }) {
  if (!url) {
    return (
      <span className="engines-model-source" title={title}>
        {text}
      </span>
    );
  }
  return (
    <a
      className="engines-model-source"
      href={url}
      title={title}
      target="_blank"
      rel="noopener noreferrer"
    >
      {text}
    </a>
  );
}

/** Which sentence a derived VRAM figure gets. Shared by the meta line and the window editor so
 *  the two cannot drift: a floor drawn as a measurement is the defect this exists to prevent. */
function engineVramNeedKey(source: EngineModel["vram_need_source"]): string {
  if (source === "floor") return "admin.engines_model_vram_floor";
  if (source === "weights_kv") return "admin.engines_model_vram_weights_kv";
  return "admin.engines_model_vram";
}

/** The one-line facts under a model, each omitted when it is not known — the same rule the
 *  status block follows. The licence is two fields on purpose: Hugging Face answers `other` for
 *  both non-commercial models in ADR 0072's table, and showing only that says nothing. */
function engineModelMeta(m: EngineModel, tr: (k: never) => string): string {
  const bits: string[] = [];
  if (m.context_tokens) {
    bits.push(
      (tr("admin.engines_model_window" as never) as string)
        .replace("{c}", String(m.context_tokens))
        .replace("{o}", String(m.max_output_tokens ?? 0)),
    );
  }
  if (m.sizes?.length) bits.push(m.sizes.join(" "));
  if (m.base_model) bits.push(m.base_model);
  if (m.precision) bits.push(m.precision);
  if (m.vram_mib) {
    bits.push((tr("admin.engines_model_vram" as never) as string).replace("{n}", String(m.vram_mib)));
  } else if (m.vram_need_mib) {
    // Nobody measured this one, but its files say it cannot be smaller than this. The wording
    // follows the SOURCE rather than the absence of a measurement — the confirmation dialog
    // quotes the same number, and a row that mentioned none would make it appear from nowhere.
    //
    // 🔴 BOTH floors get floor wording. `weights_kv` used to fall through to the measured
    // sentence, so a number the CP derived from a GGUF header read as one an operator stood
    // behind — on exactly the rows where the derivation is the only thing anyone has.
    bits.push((tr(engineVramNeedKey(m.vram_need_source) as never) as string).replace("{n}", String(m.vram_need_mib)));
  }
  // What this model costs the next cold start. Stated as an estimate because it is one: S3 to
  // the box ran at 104–147 MB/s over four measured starts, and this uses the slow end.
  if (m.sync_secs) {
    bits.push((tr("admin.engines_model_sync" as never) as string).replace("{n}", String(m.sync_secs)));
  }
  const licence = m.license_name || m.license;
  if (licence) {
    bits.push(licence);
    // Who took those terms on for every member, and when. Only an INGESTED row has it: the
    // acceptance is a human act the ingest form recorded, and a row that never passed through
    // it says nothing rather than implying somebody agreed to something.
    if (m.license_accepted_by) {
      bits.push(
        (tr("admin.engines_model_license_by" as never) as string)
          .replace("{who}", m.license_accepted_by)
          .replace("{when}", m.license_accepted_at ? fmtDateTime(m.license_accepted_at) : "—"),
      );
    }
  } else if (m.kind !== "lora") {
    // 🔴 Said, not left blank. Nothing recorded a licence for this row — a seed cannot know one
    // and the hand-registration form does not ask — and an empty space where every ingested row
    // carries a name reads as "no restrictions", which is not what it means. Same rule ADR 0074
    // decision 6 applies to an unmeasured VRAM demand.
    bits.push(tr("admin.engines_model_license_unknown" as never) as string);
  }
  // 🔴 The source and the file names are NOT here. They are the only facts on this row that are
  // links, and a "·"-joined string cannot hold one — see ModelProvenance, which draws them on
  // their own line directly below this one.
  return bits.join(" · ");
}
