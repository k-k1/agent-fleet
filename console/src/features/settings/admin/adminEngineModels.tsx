import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import {
  engineIsImage,
  engineTitle,
  useEngineRows,
  type EngineModel,
  type EngineParams,
  type EngineRow,
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

export function EngineModelsAdminView() {
  const tr = useT();
  const { rows, isSuper, err, setErr, setRows, load } = useEngineRows();
  const [busy, setBusy] = useState("");
  const [note, setNote] = useState("");
  const [jobs, setJobs] = useState<Record<string, IngestJob[]>>({});
  /** Which engine's catalogue is open, by key. A key rather than an index so that a reload that
   *  reorders the list does not move somebody to another engine mid-ingest. */
  const [role, setRole] = useState("");
  /** Models or adapters. It drives the LIST and the ingest form together, which is the point:
   *  the form used to carry its own model/LoRA selector while the search above it always asked
   *  for checkpoints, so choosing "LoRA" changed what the row would be registered as and
   *  nothing about what was on offer. */
  const [kind, setKind] = useState<ModelKind>("model");

  /** The ingest jobs for one engine. The CP reconciles against ECS inside this call, so asking
   *  is also what moves a finished job to `done` while somebody is watching. */
  const loadJobs = useCallback(async (key: string) => {
    const d = await api(`api/admin/engines/${encodeURIComponent(key)}/ingest`);
    if (!d?.error) setJobs((cur) => ({ ...cur, [key]: Array.isArray(d?.jobs) ? d.jobs : [] }));
  }, []);
  useEffect(() => {
    (rows || []).forEach((e) => loadJobs(e.key));
  }, [rows, loadJobs]);
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
    await callModel(key + "/" + id, `api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(id)}`, "PUT", patch, key);
  };

  const addModel = async (key: string, body: Record<string, unknown>) => {
    await callModel(key + "/+", `api/admin/engines/${encodeURIComponent(key)}/models`, "POST", body, key);
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

  const callModel = async (
    busyKey: string,
    path: string,
    method: string,
    body: unknown,
    key: string,
  ) => {
    setBusy(busyKey);
    try {
      const d = await apiJSON(path, method, body);
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      // What the CP started, in its own words ("deleting llm/x.gguf", or why it could not).
      // Deleting the bytes is a task that has been LAUNCHED, and saying so beats a row that
      // simply vanishes while gigabytes stay behind.
      setNote(typeof d?.purge === "string" ? d.purge : "");
      setRows((cur) => (cur || []).map((e) => (e.key === key ? { ...e, ...d } : e)));
    } finally {
      setBusy("");
    }
  };

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
          {rows.length > 1 ? (
            <span className="seg sm">
              {rows.map((e) => (
                <button
                  key={e.key}
                  type="button"
                  className={"seg-btn" + (open.key === e.key ? " active" : "")}
                  onClick={() => setRole(e.key)}
                >
                  {tr(engineIsImage(e) ? "admin.engines_role_image" : "admin.engines_role_llm")}
                </button>
              ))}
            </span>
          ) : (
            <span>{engineTitle(open)}</span>
          )}
          {/* Models or adapters. Both roles have both: an image LoRA is chosen per request by
              family, and the llm role's is pinned to a model through a preset (ADR 0072
              decision 5). */}
          <span className="seg sm">
            {(["model", "lora"] as const).map((k) => (
              <button
                key={k}
                type="button"
                className={"seg-btn" + (kind === k ? " active" : "")}
                onClick={() => setKind(k)}
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
        <EngineModels
          key={open.key + "/" + kind}
          row={open}
          kind={kind}
          busy={busy}
          readOnly={!isSuper}
          onChange={(id, patch) => setModel(open.key, id, patch)}
          onForget={(id, purge) => forgetModel(open.key, id, purge)}
          onAdd={(body) => addModel(open.key, body)}
        />
        <EngineIngest
          key={"ingest/" + open.key + "/" + kind}
          engineKey={open.key}
          isImage={isImage}
          isLora={kind === "lora"}
          baseModels={open.base_models}
          fileFlags={open.file_flags}
          modelIds={(open.model_rows || []).map((m) => m.id)}
          busy={busy === open.key + "/ingest"}
          onStarted={() => loadJobs(open.key)}
        />
        <EngineIngestJobs jobs={jobs[open.key] || []} />
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
  onChange,
  onForget,
  onAdd,
}: {
  row: EngineRow;
  kind: ModelKind;
  busy: string;
  readOnly?: boolean;
  onChange: (id: string, patch: Record<string, unknown>) => void;
  onForget: (id: string, purge: boolean) => void;
  onAdd: (body: Record<string, unknown>) => void;
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
  const [vramAsk, setVramAsk] = useState<{ id: string; patch: Record<string, boolean | string> } | null>(null);
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
              {/* 🔴 The two acts, told apart. Forgetting alone leaves the bytes in the bucket
                  with nothing able to reach them (measured: a 491 MB file outlived its row);
                  purging starts the MODE=delete task ADR 0072 decision 7 exists for, because
                  the CP has no s3:DeleteObject and is not getting one. */}
              {/* ⚠️ A warning, not a refusal — quantisation, --offload-to-cpu and things this
                  deployment has not measured are real, so the panel points and the person
                  decides (the position ADR 0072 decision 10 takes on licences). */}
              {vramAsk?.id === m.id && (
                <div className="engines-model-confirm">
                  <p className="form-err">
                    {(tr("admin.engines_vram_confirm" as never) as string)
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
function EngineModelAdd({
  busy,
  isImage,
  isLora,
  baseModels,
  fileFlags,
  modelIds,
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
  };

  if (!open) {
    return (
      <button type="button" className="sm engines-open" onClick={() => setOpen(true)}>
        {tr("admin.engines_model_add")}
      </button>
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
  const incomplete = !id.trim() || rows.length === 0 || (baseOptions.length > 0 && !baseModel);
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
      {field(tr("admin.engines_model_add_id"), id, setId,
        isLora ? "house-style" : isImage ? "juggernaut-xl-v9" : "qwen2.5-coder-1.5b")}
      {/* ⚠️ A CHOICE, never a text box. What the repository calls a model — "SDXL 1.0",
          "Flux.1 D" — is a display name, and typing one here produced rows that looked complete
          and refused to generate (ADR 0072 P2 実機検証). The list comes from the CP, which
          validates against the same one. An llm adapter picks from the catalogue's own ids for
          the same reason: a base nothing matches is an adapter that loads nowhere. */}
      {baseOptions.length > 0 && (
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
            <button
              type="button"
              className="ghost sm"
              onClick={() => setFiles((prev) => prev.filter((_, j) => j !== i))}
            >
              {tr("admin.engines_model_add_part_drop")}
            </button>
          )}
        </div>
      ))}
      {/* Offered only where a split model is a thing this provider can load. */}
      {flags.length > 0 && (
        <button
          type="button"
          className="ghost sm"
          onClick={() => setFiles((prev) => [...prev, { flag: "", s3Key: "", bytes: "" }])}
        >
          {tr("admin.engines_model_add_part_more")}
        </button>
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
        <button type="button" className="primary sm" disabled={busy || incomplete} onClick={submit}>
          {tr("admin.engines_model_add_go")}
        </button>
        <button type="button" className="sm" onClick={() => setOpen(false)}>
          {tr("common.cancel")}
        </button>
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
function HfTokenPanel() {
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
            <button type="button" className="primary sm" disabled={busy || !token.trim()} onClick={save}>
              {tr("admin.engines_hf_token_save")}
            </button>
            {st.configured && (
              <button type="button" className="sm" disabled={busy} onClick={remove}>
                {tr("admin.engines_hf_token_remove")}
              </button>
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

function EngineIngest({
  engineKey,
  isImage,
  isLora,
  baseModels,
  fileFlags,
  modelIds,
  busy,
  onStarted,
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
  /** The ids this engine's catalogue already holds. Only used to offer "add it to that row"
   *  when the id names one: the CP refuses a plain ingest onto an existing id, and without the
   *  offer the only way to build a split model is three throwaway rows (ADR 0072 P2 欠落 6). */
  modelIds?: string[];
  busy: boolean;
  onStarted: () => void;
}) {
  const tr = useT();
  const [open, setOpen] = useState(false);
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
  const [searchSource, setSearchSource] = useState("hf");
  const [sort, setSort] = useState("downloads");
  const [baseModel, setBaseModel] = useState("");
  /** What this file is within the model, and — when the id names a row that already exists —
   *  whether it JOINS that row instead of making a new one. */
  const [fileFlag, setFileFlag] = useState("");
  const [attach, setAttach] = useState(false);
  const families = baseModels || [];
  const flags = fileFlags || [];
  /** What this model should be RUN at, as text — the fields are typed into, so they are strings
   *  until the moment they are sent. Empty means "not declared", which is the family's own
   *  recipe and is what every row said before this existed. */
  const [params, setParams] = useState<EngineParamsForm>(engineParamsBlank);
  /** True when the typed id is one this engine already has. The CP refuses a plain ingest onto
   *  it (409 model_id_exists), so this is where the second act — attaching a part — is
   *  offered rather than left as an error to read. */
  const known = (modelIds || []).includes(id.trim());
  /** Same two vocabularies as the register form: an llm adapter names a model id, everything
   *  else names a family. */
  const basePicksAModel = isLora && !isImage;
  const baseOptions = basePicksAModel ? (modelIds || []).filter((m) => m !== id.trim()) : families;
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
    const civ = r.match(/civitai\.com\/.*modelVersionId=(\d+)|^civitai:(\d+)$/);
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
    const civ = t.match(/^https?:\/\/(?:[\w-]+\.)*civitai\.com\/\S*[?&]modelVersionId=(\d+)/);
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
    return !/^https?:\/\//.test(r) || /huggingface\.co|civitai\.com/.test(r);
  };

  const resolveFile = async (name: string, repoOverride?: string) => {
    const seq = ++asked.current;
    const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest/resolve`, "POST", {
      source: source(name, repoOverride),
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
    await resolve({ repo: ref, file: "" });
  };

  const pick = async (name: string) => {
    setErr("");
    setFile(name);
    setFound(null);
    if (name) await resolveFile(name);
  };

  const start = async () => {
    setErr("");
    const n = (v: string) => {
      const p = Number(v.trim().replace(/[_,]/g, ""));
      return Number.isFinite(p) && p > 0 ? Math.floor(p) : 0;
    };
    const c = n(ctx);
    const o = n(out);
    const key = engineIngestPrefix(isImage, fileFlag, isLora) + (file.trim() || id.trim());
    const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest`, "POST", {
      id: id.trim(),
      kind: isLora ? "lora" : isImage ? "checkpoint" : "gguf",
      s3Key: key,
      source: source(),
      description: desc.trim(),
      base_model: baseModel,
      file_flag: fileFlag,
      attach: attach && known,
      context_tokens: isLora ? 0 : c && o ? c : 0,
      max_output_tokens: isLora ? 0 : c && o ? o : 0,
      params: engineParamsBody(params),
      license_accepted: true,
    });
    if (d?.error) {
      setErr(errDetail(d.error));
      return;
    }
    setOpen(false);
    setFound(null);
    setAccepted(false);
    setRepo("");
    setRev("");
    setFile("");
    setFiles(null);
    setId("");
    setFileFlag("");
    setAttach(false);
    setParams(engineParamsBlank);
    onStarted();
  };

  if (!open) {
    return (
      <button type="button" className="sm engines-open" onClick={() => setOpen(true)}>
        {tr("admin.engines_ingest_open")}
      </button>
    );
  }
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
  return (
    <div className="engines-model-add engines-ingest">
      {/* The repository picker (ADR 0072 decision 11). It sits ABOVE the field it fills, and
          the field stays typeable: search is a way in, never a precondition — a deployment with
          closed egress loses the search and keeps the ingest. */}
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
        {/* Civitai is only offered to the image role: it hosts image models, and the CP answers
            the llm role nothing at all rather than checkpoints llama.cpp cannot load. */}
        {isImage && (
          <span className="seg sm">
            {(["hf", "civitai"] as const).map((sr) => (
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
        {/* The ranking, which is also what an empty box asks for. Pressing one searches
            immediately: a ranking that needed a second click on another button would read as a
            setting rather than as the question it is. */}
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
      {/* Editing the repository drops the list and the verdict with it: a filename picked out
          of the previous repository's answer would resolve against the new one.

          The example follows the ROLE and the source that is selected. A GGUF repository
          offered to the image engine is not a hint, it is a wrong answer: llama.cpp's files
          are not what sd-server loads, and following it costs a resolve and a refusal. */}
      {field(tr("admin.engines_ingest_repo"), repo, (v) => {
        setRepo(v);
        setRev("");
        setFiles(null);
        setFile("");
        setFound(null);
      }, searchSource === "civitai"
        ? "civitai:782002"
        : isImage
          ? "stabilityai/stable-diffusion-xl-base-1.0"
          : "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF",
      splitRepoField)}
      {/* The filename is a picker as soon as the repository has been asked what it holds. The
          text field stays underneath it: a plain url has no listing, and there the field
          carries the SHA256 instead — so it is labelled as one. Offering "name.safetensors"
          there asks for the one thing that field must not be given. */}
      {files && files.length > 0 && (
        <label className="engines-model-add-row">
          <span>{tr("admin.engines_ingest_file")}</span>
          <select value={file} onChange={(ev) => pick(ev.currentTarget.value)}>
            <option value="">{tr("admin.engines_ingest_pick")}</option>
            {files.map((f) => (
              <option key={f.name} value={f.name}>
                {f.name}
                {f.bytes ? " · " + fmtBytes(f.bytes) : ""}
              </option>
            ))}
          </select>
        </label>
      )}
      {files && files.length === 0 && <p className="form-err">{tr("admin.engines_ingest_no_files")}</p>}
      {!files &&
        (listable()
          ? field(
              tr("admin.engines_ingest_file"),
              file,
              setFile,
              isImage ? "name.safetensors" : "name.gguf",
            )
          : field(tr("admin.engines_ingest_sha256"), file, setFile, tr("admin.engines_ingest_sha256_ph")))}
      {field(
        tr("admin.engines_model_add_id"),
        id,
        setId,
        isImage ? "sdxl-base-1.0" : "qwen2.5-coder-1.5b",
      )}
      {/* What this file IS within the model. Until this existed every ingest wrote one
          unlabelled file, so a split model could not be assembled by taking its parts in — the
          three components of a FLUX.1 row had to be staged as throwaway rows and the real row
          re-typed key by key (ADR 0072 P2 欠落 6). It also decides the bucket directory, which
          is what makes the file visible to the right loader at all. */}
      {flags.length > 0 && (
        <label className="engines-model-add-row">
          <span>{tr("admin.engines_model_add_part")}</span>
          <select
            value={fileFlag}
            onChange={(ev) => {
              setFileFlag(ev.currentTarget.value);
              // A whole checkpoint is never a part of another row.
              if (!ev.currentTarget.value) setAttach(false);
            }}
          >
            {flags.map((fl) => (
              <option key={fl} value={fl}>
                {fl === "" ? (tr("admin.engines_model_add_part_whole") as string) : fl}
              </option>
            ))}
          </select>
        </label>
      )}
      {/* The id names a row that is already there. The CP refuses a plain ingest onto it — it
          would upsert that row's files, licence and enabled flag away — so the choice is made
          here, before the download: a new id, or this file as one more part of that row. */}
      {known && (
        <label className="engines-ingest-accept">
          <input
            type="checkbox"
            checked={attach}
            disabled={!fileFlag}
            onChange={(ev) => setAttach(ev.currentTarget.checked)}
          />
          <span>{(tr("admin.engines_ingest_attach") as string).replace("{id}", id.trim())}</span>
        </label>
      )}
      {known && !attach && <p className="form-err">{tr("admin.engines_ingest_id_taken")}</p>}
      {/* ⚠️ Declared by the OPERATOR (ADR 0072 decision 2), which is why the repository's own
          answer rides BESIDE the picker instead of into it: "SDXL 1.0" and "Flux.1 D" are what
          Hugging Face and Civitai publish, and storing one of those as the family produced rows
          that looked complete and refused to generate (P2 実機検証). */}
      {!attach && baseOptions.length > 0 && (
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
      {/* What the repository itself calls this, beside the picker rather than in it. When the CP
          could translate it the picker above is already filled in, and this line is then the
          PROVENANCE of that choice — which is what makes it correctable rather than magic. */}
      {!attach && !isLora && families.length > 0 && found?.base_model && (
        <p className="muted">
          {(tr(
            found.base_model_suggest && baseModel === found.base_model_suggest
              ? "admin.engines_family_suggested"
              : "admin.engines_ingest_family_hint",
          ) as string).replace("{n}", found.base_model)}
        </p>
      )}
      {field(tr("admin.engines_model_add_desc"), desc, setDesc)}
      {/* How to RUN it, filled in from the author's own description and editable before
          anything is stored. 🔴 The quote is not decoration: these numbers were found by a
          regular expression in somebody's paragraph, and the sentence is what lets a person
          tell "Steps: 30" from "trained for 30 epochs" without opening the model page. */}
      {!attach && isImage && found?.params_hint && found.params_hint_quote && (
        <p className="muted engines-param-quote">
          {tr("admin.engines_params_hint_found")} <q>{found.params_hint_quote}</q>
        </p>
      )}
      {/* 🔴 The image role only. Steps, cfg and a sampler are what a DIFFUSION graph takes; the
          llm role's equivalents are the window and the output cap two lines below, and an
          adapter's strength there is `--scale`, which the register form has always had. Five
          fields that reach nothing would be five fields somebody fills in. */}
      {!attach && isImage && (
        <EngineParamsFields
          value={params}
          onChange={setParams}
          isLora={isLora}
          family={isLora ? undefined : baseModel}
        />
      )}
      {!isImage && !isLora && field(tr("admin.engines_model_add_ctx"), ctx, setCtx, "32768")}
      {/* The output cap is a FRACTION of the window, never a free number. It is not published
          anywhere — it is a deployment's policy for how much of the window one reply may eat —
          and 🔴 ADR 0072 decision 3: left at 0 opencode reads it as 32,000 and a 32k model ends
          up with 768 usable tokens. Offering computed values makes the pair impossible to
          half-fill. */}
      {!isImage && !isLora && <OutputCapField ctx={ctx} value={out} onChange={setOut} />}
      <div className="engines-model-add-actions">
        <button type="button" className="sm" onClick={() => resolve()} disabled={busy || !repo.trim()}>
          {tr("admin.engines_ingest_resolve")}
        </button>
        <button type="button" className="sm" onClick={() => setOpen(false)}>
          {tr("common.cancel")}
        </button>
      </div>
      {/* Everything below appears only once the source has been read: the licence to accept,
          the size that becomes the cold start, and — for a gated repository — whether this
          deployment can take it in at all. */}
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
      {found && (
        <div className="engines-model-add-actions">
          <button
            type="button"
            className="primary sm"
            disabled={
              busy ||
              !accepted ||
              !id.trim() ||
              found.can_ingest === false ||
              // An id the catalogue already holds goes in as a PART or not at all; the CP
              // refuses both of these too, and a button that let the press happen would spend
              // a resolve and a refusal to say so.
              (known && !attach) ||
              (attach && !fileFlag)
            }
            onClick={start}
          >
            {tr("admin.engines_ingest_go")}
          </button>
        </div>
      )}
      {err && <p className="form-err">{err}</p>}
      <p className="muted">{tr("admin.engines_ingest_note")}</p>
    </div>
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
            {(["hf", "civitai"] as const).map((sr) => (
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

function EngineIngestJobs({ jobs }: { jobs: IngestJob[] }) {
  const tr = useT();
  if (jobs.length === 0) return null;
  return (
    <>
    <p className="muted engines-ingest-jobs-head">{tr("admin.engines_ingest_jobs_head")}</p>
    <ul className="engines-model-list engines-ingest-jobs">
      {jobs.map((j) => (
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
        </li>
      ))}
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
    const key =
      m.vram_need_source === "floor" ? "admin.engines_model_vram_floor" : "admin.engines_model_vram";
    bits.push((tr(key as never) as string).replace("{n}", String(m.vram_need_mib)));
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
  // Next to the licence, because they are the same kind of fact: both were true of that
  // repository at the moment somebody accepted its terms.
  if (m.source) bits.push(m.source);
  if (m.files?.length) bits.push(m.files.join(" "));
  return bits.join(" · ");
}