import { useCallback, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { api, apiJSON, errDetail } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { EngineUptimePanel, Sep, useDuration } from "./EngineUptime.tsx";
import { secsUntil, windowIsPartial } from "./engineUptime.ts";
import { fmtDateTime } from "../../../lib/intl.ts";

// The self-hosted inference engines (ADR 0071): one row per engine, each with the same
// off / on-demand / always-on control the VOICEVOX panel has, plus what that engine is
// actually doing right now.
//
// Until this existed the only way to switch one off was a CloudFormation parameter
// (`LlmMode` / `ImageMode`), which is not a control anyone reaches for when a GPU is
// misbehaving. The mode was always read from a stored setting — the gateway, the catalogue
// and the controller all consult it — so this panel is the missing half rather than a new
// mechanism.
//
// Mode is what the administrator chose; state is what ECS is doing about it. They are shown
// separately and they disagree on purpose for the minute after "off" (the task is still going
// away), because a panel that echoed ECS back would report the opposite of the button that was
// just pressed.
//
// The status block is written to a rule worth restating whenever it is edited: EVERY LINE IS
// OMITTED RATHER THAN GUESSED. A GPU that costs $1.26/hour is asleep most of the time and the
// CP genuinely does not know some of these things — when a box started before this CP existed,
// how many requests arrived before it was deployed, when an engine pinned "on" will stop (it
// will not). A blank reads as "unknown"; a plausible-looking zero or a fallback date reads as
// fact and gets acted on.

type EngineBox = {
  id?: string;
  status?: string;
  /** registeredAt: when the EC2 INSTANCE joined the cluster, which is a different fact from
   *  when the service last changed. See engineBox in control-plane/engine_ecs.go. */
  since?: string;
};

/** One entry of the model catalogue (ADR 0072). What the engine may load is a declaration an
 *  administrator edits here, not a CloudFormation parameter — which is the whole point of the
 *  ADR: swapping a checkpoint is an operational act, performed while the GPU is asleep. */
type EngineModel = {
  id: string;
  kind?: string;
  enabled: boolean;
  /** The image role's ONE checkpoint (sd-server holds one, chosen at startup) and the llm
   *  role's answer to a request that named no model. Exclusive within an engine. */
  selected?: boolean;
  default?: boolean;
  description?: string;
  context_tokens?: number;
  max_output_tokens?: number;
  vram_mib?: number;
  /** BOTH are kept and both are shown: Hugging Face reports `other` for the two
   *  non-commercial models in ADR 0072's table, with the real terms in license_name. */
  license?: string;
  license_name?: string;
  license_url?: string;
  base_model?: string;
  /** Where the bytes came from (`hf:<repo>/<file>`, `civitai:<id>`, a URL). The id is short and
   *  unique only inside this deployment, so this is the only thing that says WHICH vendor's
   *  model of that name this row is. Absent for a seeded row. */
  source?: string;
  precision?: string;
  sizes?: string[];
  files?: string[];
  /** What enabling this model adds to the next cold start, in seconds, from the file sizes
   *  whoever staged them declared. Absent when nobody declared one — the CP cannot look in S3
   *  (ADR 0072 review R3), so this is an estimate and is labelled as one. */
  sync_secs?: number;
};

/** What POST …/ingest/resolve answered: what the file IS, before anything is started. */
type ResolvedSource = {
  sha256?: string;
  bytes?: number;
  gated?: boolean;
  license?: string;
  license_name?: string;
  license_url?: string;
  base_model?: string;
  commercial_use?: string;
  /** false when the repository is gated and this deployment has no Hugging Face token. The
   *  button is disabled on it rather than letting a task run nine minutes into a 401. */
  can_ingest?: boolean;
  /** The model's OWN maximum, off the GGUF header. 🔴 A ceiling, not a setting: the 30B in
   *  this deployment publishes 262144 and is run at 32768, because what the architecture
   *  allows and what fits in the GPU are different questions. Offered, never applied. */
  context_length?: number;
};

/** One file a repository offers (POST …/ingest/files), already filtered to the ones this
 *  engine could load and that carry a sha256. */
type IngestCandidate = { name: string; bytes?: number; sha256?: string };

type IngestJob = {
  id: string;
  model_id: string;
  s3_key?: string;
  source?: string;
  state: string;
  message?: string;
  bytes?: number;
  created_at?: string;
};

type EngineRow = {
  key: string;
  api?: string;
  provider?: string;
  models?: string[];
  /** Every row of the catalogue, enabled or not — this panel is where one is turned ON, so a
   *  list filtered to the enabled ones would have no way to reach the others. */
  model_rows?: EngineModel[];
  /** Whether anything is enabled at all. Stated by the CP rather than inferred from the list,
   *  because it is the reason the controller refuses to start the engine. */
  has_models?: boolean;
  mode: string;
  enabled: boolean;
  managed: boolean;
  state?: string;
  desired?: number;
  /** The engine answered a real request since it came up. Different from `state:"running"`:
   *  llama-server binds its port 267 seconds before the weights are in VRAM (measured), so a
   *  panel showing only the ECS state reports an engine as up through its whole cold start. */
  warm?: boolean;
  /** The model whose weights are actually in VRAM, and how many times that changed. Both are
   *  in-memory facts of the CURRENT control-plane process (as window_units is), and the swap
   *  count is the visible price of `--models-max 1`: every change cost an unload plus a load. */
  warm_model?: string;
  model_swaps?: number;
  /** Service events — the only place ECS writes down why a start failed. */
  events?: string[];
  service_since?: string;
  box?: EngineBox;
  /** When the controller will stop it by itself. ABSENT is meaningful: pinned on, switched
   *  off, already stopped, or no demand mark yet — see engineStopETA. Never render a fallback. */
  stop_eta?: string;
  idle_secs?: number;
  window_secs?: number;
  window_units?: number;
  window_counted_secs?: number;
  last_demand?: string;
  error?: string;
};

export function EnginesAdminView() {
  const tr = useT();
  const [rows, setRows] = useState<EngineRow[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");
  const [note, setNote] = useState("");
  const [jobs, setJobs] = useState<Record<string, IngestJob[]>>({});

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/engines");
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setRows(Array.isArray(d?.engines) ? d.engines : []);
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);

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
  // only the job list, and the engine poll below is off because an on-demand engine parked at
  // "stopped" is a settled state. Measured on the dev deployment: the job reached "done" and the
  // panel went on showing the two models it already had, so the row an administrator has to
  // enable was reachable only by pressing refresh.
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

  // Poll only while something is actually moving. An engine parked at "off", or stopped under
  // on-demand with nobody asking, is a settled state, and a GPU panel that polls forever is a
  // request per five seconds for a screen nobody is watching.
  useEffect(() => {
    if (!rows) return;
    const moving = rows.some(
      (e) =>
        e.state === "starting" ||
        e.state === "stopping" ||
        (e.mode === "ondemand" && e.state === "running"),
    );
    if (!moving) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [rows, load]);

  const setMode = async (key: string, mode: string) => {
    setBusy(key);
    try {
      const d = await apiJSON("api/admin/engines/" + encodeURIComponent(key), "PUT", { mode });
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setRows((cur) => (cur || []).map((e) => (e.key === key ? { ...e, ...d } : e)));
    } finally {
      setBusy("");
    }
  };

  /** Enable / disable a model, or make it the one the engine starts with. The CP answers with
   *  the whole engine row, so the panel takes its new state from the server rather than
   *  guessing at the exclusivity rule — selecting one model clears another, and reproducing
   *  that here would be a second copy of a rule that has to be enforced in a transaction. */
  const setModel = async (key: string, id: string, patch: Record<string, boolean>) => {
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

  return (
    <div className="admin-stage">
      {rows.length === 0 && <p className="muted pad">{tr("admin.engines_none")}</p>}
      {rows.map((e) => (
        <section className="admin-panel" key={e.key}>
          <div className="usage-toolbar">
            <span>{engineTitle(e)}</span>
            <span className="seg sm">
              {(["off", "ondemand", "on"] as const).map((m) => (
                <button
                  key={m}
                  type="button"
                  className={"seg-btn" + (e.mode === m ? " active" : "")}
                  disabled={busy === e.key}
                  onClick={() => setMode(e.key, m)}
                >
                  {tr(("admin.tts_mode_" + m) as never)}
                </button>
              ))}
            </span>
            <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
              <Icon name="refresh" />
            </button>
          </div>
          <p className="muted">
            {tr("admin.engines_state_prefix")}
            {engineStateLabel(e, tr)}
            {e.models?.length ? tr("admin.engines_models_sep") + e.models.join(", ") : ""}
            {e.models?.length ? " " + tr(e.warm ? "admin.engines_model_loaded" : "admin.engines_model_declared") : ""}
          </p>
          <EngineStatus row={e} />
          <EngineModels
            row={e}
            busy={busy}
            onChange={(id, patch) => setModel(e.key, id, patch)}
            onForget={(id, purge) => forgetModel(e.key, id, purge)}
            onAdd={(body) => addModel(e.key, body)}
          />
          <EngineIngest
            engineKey={e.key}
            isImage={e.api === "images"}
            busy={busy === e.key + "/ingest"}
            onStarted={() => loadJobs(e.key)}
          />
          <EngineIngestJobs jobs={jobs[e.key] || []} />
          {e.mode === "on" && <p className="form-err">{tr("admin.engines_always_on_note")}</p>}
          {e.error && <p className="form-err">{e.error}</p>}
          {/* The events are the only place ECS says why a start failed ("no container
              instances met the placement constraints", a pull failure). An engine stuck in
              `starting` is exactly when somebody needs them, and the alternative is a trip to
              the AWS console for a string the CP already has. */}
          {e.state === "starting" && e.events?.length ? (
            <p className="muted mono engines-events">{e.events.join(" | ")}</p>
          ) : null}
          <EngineHistory engineKey={e.key} />
        </section>
      ))}
      {rows.length > 0 && <HfTokenPanel />}
      {err && <p className="form-err pad">{err}</p>}
      {note && <p className="muted pad">{note}</p>}
      <p className="muted pad">{tr("admin.engines_note")}</p>
    </div>
  );
}

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
function EngineModels({
  row,
  busy,
  onChange,
  onForget,
  onAdd,
}: {
  row: EngineRow;
  busy: string;
  onChange: (id: string, patch: Record<string, boolean>) => void;
  onForget: (id: string, purge: boolean) => void;
  onAdd: (body: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const models = row.model_rows || [];
  const isImage = row.api === "images";
  // Which row is mid-confirm, and whether the bytes go too. Deleting gigabytes is not something
  // a single click should do, and "forget the row" and "delete the file" have to be told apart
  // BEFORE the press rather than explained afterwards.
  const [confirming, setConfirming] = useState("");
  const [purge, setPurge] = useState(false);
  return (
    <div className="engines-models">
      {/* An engine with no catalogue at all is the interesting case, not an empty section: the
          controller refuses to start it and every request is refused, so it gets a sentence
          rather than a blank area that reads as "still loading". */}
      {models.length === 0 && <p className="form-err">{tr("admin.engines_catalog_empty")}</p>}
      {models.length > 0 && !row.has_models && (
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
                <span className="mono">{m.id}</span>
                {started && <span className="engines-model-tag">{tr("admin.engines_model_started")}</span>}
                {isLora && <span className="engines-model-tag">LoRA</span>}
                <span className="engines-model-actions">
                  <button
                    type="button"
                    className="ghost sm"
                    disabled={pending}
                    onClick={() => onChange(m.id, { enabled: !m.enabled })}
                  >
                    {tr(m.enabled ? "admin.engines_model_disable" : "admin.engines_model_enable")}
                  </button>
                  {/* A LoRA is never something an engine is started with, so the control that
                      would say so is not offered for one. */}
                  {!isLora && !started && (
                    <button
                      type="button"
                      className="ghost sm"
                      disabled={pending}
                      onClick={() => onChange(m.id, isImage ? { selected: true } : { default: true })}
                    >
                      {tr("admin.engines_model_select")}
                    </button>
                  )}
                  {/* Forgetting the ROW. The file stays in the bucket — the CP has no
                      s3:DeleteObject and is not getting one (ADR 0072 decision 7) — so the
                      label says "forget", not "delete", and the note below says why. */}
                  <button
                    type="button"
                    className="ghost sm"
                    disabled={pending || started}
                    onClick={() => {
                      setConfirming(m.id);
                      setPurge(false);
                    }}
                  >
                    {tr("admin.engines_model_forget")}
                  </button>
                </span>
              </div>
              {m.description && <p className="muted engines-model-desc">{m.description}</p>}
              <p className="muted engines-model-meta">{engineModelMeta(m, tr)}</p>
              {/* 🔴 The two acts, told apart. Forgetting alone leaves the bytes in the bucket
                  with nothing able to reach them (measured: a 491 MB file outlived its row);
                  purging starts the MODE=delete task ADR 0072 decision 7 exists for, because
                  the CP has no s3:DeleteObject and is not getting one. */}
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
                  <span className="engines-model-actions">
                    <button
                      type="button"
                      className={purge ? "primary sm" : "ghost sm"}
                      disabled={pending}
                      onClick={() => {
                        setConfirming("");
                        onForget(m.id, purge);
                      }}
                    >
                      {tr("admin.engines_model_forget_go")}
                    </button>
                    <button type="button" className="ghost sm" onClick={() => setConfirming("")}>
                      {tr("common.cancel")}
                    </button>
                  </span>
                </div>
              )}
            </li>
          );
        })}
      </ul>
      <p className="muted">{tr("admin.engines_model_next_start")}</p>
      <EngineModelAdd busy={busy === row.key + "/+"} isImage={isImage} onAdd={onAdd} />
    </div>
  );
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
  onAdd,
}: {
  busy: boolean;
  isImage: boolean;
  onAdd: (body: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [id, setId] = useState("");
  const [s3Key, setS3Key] = useState("");
  const [desc, setDesc] = useState("");
  const [ctx, setCtx] = useState("");
  const [out, setOut] = useState("");
  const [bytes, setBytes] = useState("");

  if (!open) {
    return (
      <button type="button" className="ghost sm" onClick={() => setOpen(true)}>
        {tr("admin.engines_model_add")}
      </button>
    );
  }
  const submit = () => {
    if (!id.trim() || !s3Key.trim()) return;
    const n = (v: string) => {
      const parsed = Number(v.trim().replace(/[_,]/g, ""));
      return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : 0;
    };
    // Both halves of the window or neither: a context with no output cap leaves opencode 768
    // usable tokens, which is worse than the no-limit default it would otherwise get.
    const c = n(ctx);
    const o = n(out);
    onAdd({
      id: id.trim(),
      kind: isImage ? "checkpoint" : "gguf",
      files: [{ s3Key: s3Key.trim(), bytes: n(bytes) }],
      description: desc.trim(),
      context_tokens: c && o ? c : 0,
      max_output_tokens: c && o ? o : 0,
    });
    setOpen(false);
    setId("");
    setS3Key("");
    setDesc("");
    setCtx("");
    setOut("");
    setBytes("");
  };
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
        isImage ? "juggernaut-xl-v9" : "qwen2.5-coder-1.5b")}
      {field(tr("admin.engines_model_add_key"), s3Key, setS3Key,
        isImage ? "image/checkpoints/name.safetensors" : "llm/name.gguf")}
      {field(tr("admin.engines_model_add_desc"), desc, setDesc)}
      {/* The window is a chat engine's business: sd-server holds one checkpoint and has no
          context at all, so offering the field there would ask for a number nothing reads. */}
      {!isImage && field(tr("admin.engines_model_add_ctx"), ctx, setCtx, "32768", true)}
      {!isImage && field(tr("admin.engines_model_add_out"), out, setOut, "4096", true)}
      {field(tr("admin.engines_model_add_bytes"), bytes, setBytes, "1117320768", true)}
      <div className="engines-model-add-actions">
        <button type="button" className="primary sm" disabled={busy} onClick={submit}>
          {tr("admin.engines_model_add_go")}
        </button>
        <button type="button" className="ghost sm" onClick={() => setOpen(false)}>
          {tr("common.cancel")}
        </button>
      </div>
      {/* ⚠️ The CP never checks that the key exists: it has no S3 permission at all and none is
          being added (ADR 0072 review R3). A typo surfaces in the fetch sidecar's log at the
          next cold start, so the panel says so rather than implying a check happened. */}
      <p className="muted">{tr("admin.engines_model_add_note")}</p>
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
              <button type="button" className="ghost sm" disabled={busy} onClick={remove}>
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
function EngineIngest({
  engineKey,
  isImage,
  busy,
  onStarted,
}: {
  engineKey: string;
  isImage: boolean;
  busy: boolean;
  onStarted: () => void;
}) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [repo, setRepo] = useState("");
  const [file, setFile] = useState("");
  const [id, setId] = useState("");
  const [desc, setDesc] = useState("");
  const [ctx, setCtx] = useState("");
  const [out, setOut] = useState("");
  const [found, setFound] = useState<ResolvedSource | null>(null);
  const [files, setFiles] = useState<IngestCandidate[] | null>(null);
  const [accepted, setAccepted] = useState(false);
  const [err, setErr] = useState("");

  const source = (name = file) => {
    const r = repo.trim();
    // A pasted https://huggingface.co/<repo>/blob|resolve/<rev>/<file> is what a person
    // actually has in hand, so it is accepted as-is rather than asked for in pieces.
    const m = r.match(/^https?:\/\/huggingface\.co\/([^/]+\/[^/]+)(?:\/(?:blob|resolve)\/([^/]+)\/(.+))?$/);
    if (m) return { hf: { repo: m[1], revision: m[2] || "", file: m[3] || name.trim() } };
    const civ = r.match(/civitai\.com\/.*modelVersionId=(\d+)|^civitai:(\d+)$/);
    if (civ) return { civitai: { versionId: Number(civ[1] || civ[2]), file: name.trim() } };
    if (/^https?:\/\//.test(r)) return { url: r, sha256: name.trim() };
    return { hf: { repo: r, file: name.trim(), revision: "" } };
  };

  /** A plain url addresses one file and has no listing; the field carries its sha256 there. */
  const listable = () => !/^https?:\/\//.test(repo.trim()) || /huggingface\.co|civitai\.com/.test(repo.trim());

  const resolveFile = async (name: string) => {
    const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest/resolve`, "POST", {
      source: source(name),
    });
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
  };

  /** 「調べる」. With no file named yet this ASKS WHAT THERE IS, because a filename retyped from
   *  another window is where the mistakes are. One candidate resolves straight through — a
   *  picker over a single option is a question with one answer. */
  const resolve = async () => {
    setErr("");
    if (!file.trim() && listable()) {
      const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest/files`, "POST", {
        source: source(""),
      });
      if (d?.error) {
        setFiles(null);
        setErr(errDetail(d.error));
        return;
      }
      const list = (Array.isArray(d?.files) ? d.files : []) as IngestCandidate[];
      setFiles(list);
      if (list.length === 1) {
        setFile(list[0].name);
        await resolveFile(list[0].name);
      }
      return;
    }
    await resolveFile(file);
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
    const key = (isImage ? "image/checkpoints/" : "llm/") + (file.trim() || id.trim());
    const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest`, "POST", {
      id: id.trim(),
      kind: isImage ? "checkpoint" : "gguf",
      s3Key: key,
      source: source(),
      description: desc.trim(),
      context_tokens: c && o ? c : 0,
      max_output_tokens: c && o ? o : 0,
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
    setFile("");
    setFiles(null);
    setId("");
    onStarted();
  };

  if (!open) {
    return (
      <button type="button" className="ghost sm" onClick={() => setOpen(true)}>
        {tr("admin.engines_ingest_open")}
      </button>
    );
  }
  const field = (label: string, value: string, set: (v: string) => void, placeholder = "") => (
    <label className="engines-model-add-row">
      <span>{label}</span>
      <input value={value} placeholder={placeholder} onChange={(ev) => set(ev.currentTarget.value)} />
    </label>
  );
  return (
    <div className="engines-model-add engines-ingest">
      {/* Editing the repository drops the list and the verdict with it: a filename picked out
          of the previous repository's answer would resolve against the new one. */}
      {field(tr("admin.engines_ingest_repo"), repo, (v) => {
        setRepo(v);
        setFiles(null);
        setFile("");
        setFound(null);
      }, "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF")}
      {/* The filename is a picker as soon as the repository has been asked what it holds. The
          text field stays underneath it: a plain url has no listing, and there the field
          carries the sha256 instead. */}
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
      {!files && field(tr("admin.engines_ingest_file"), file, setFile, isImage ? "name.safetensors" : "name.gguf")}
      {field(tr("admin.engines_model_add_id"), id, setId, "qwen2.5-coder-1.5b")}
      {field(tr("admin.engines_model_add_desc"), desc, setDesc)}
      {!isImage && field(tr("admin.engines_model_add_ctx"), ctx, setCtx, "32768")}
      {/* The output cap is a FRACTION of the window, never a free number. It is not published
          anywhere — it is a deployment's policy for how much of the window one reply may eat —
          and 🔴 ADR 0072 decision 3: left at 0 opencode reads it as 32,000 and a 32k model ends
          up with 768 usable tokens. Offering computed values makes the pair impossible to
          half-fill. */}
      {!isImage && <OutputCapField ctx={ctx} value={out} onChange={setOut} />}
      <div className="engines-model-add-actions">
        <button type="button" className="ghost sm" onClick={resolve} disabled={busy || !repo.trim()}>
          {tr("admin.engines_ingest_resolve")}
        </button>
        <button type="button" className="ghost sm" onClick={() => setOpen(false)}>
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
            disabled={busy || !accepted || !id.trim() || found.can_ingest === false}
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
function EngineIngestJobs({ jobs }: { jobs: IngestJob[] }) {
  const tr = useT();
  if (jobs.length === 0) return null;
  return (
    <>
    <p className="muted engines-ingest-jobs-head">{tr("admin.engines_ingest_jobs_head")}</p>
    <ul className="engines-model-list engines-ingest-jobs">
      {jobs.map((j) => (
        <li key={j.id} className="engines-model on">
          <div className="engines-model-head">
            <span className="mono">{j.model_id}</span>
            <span className="engines-model-tag">{tr(("admin.engines_ingest_state_" + j.state) as never)}</span>
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
        </li>
      ))}
    </ul>
    </>
  );
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
  }
  // What this model costs the next cold start. Stated as an estimate because it is one: S3 to
  // the box ran at 104–147 MB/s over four measured starts, and this uses the slow end.
  if (m.sync_secs) {
    bits.push((tr("admin.engines_model_sync" as never) as string).replace("{n}", String(m.sync_secs)));
  }
  const licence = m.license_name || m.license;
  if (licence) bits.push(licence);
  // Next to the licence, because they are the same kind of fact: both were true of that
  // repository at the moment somebody accepted its terms.
  if (m.source) bits.push(m.source);
  if (m.files?.length) bits.push(m.files.join(" "));
  return bits.join(" · ");
}

/** The collapsed history, one 14-day query per engine.
 *
 * ⚠️ The heatmap is mounted from an `open` flag rather than left inside a closed `<details>`.
 * `<details>` hides its children, it does not unmount them: React renders them and the fetch
 * fires anyway, so every engine on the panel would spend a 336-bucket query on load for a
 * section nobody opened. The visual result is identical and the request is not made. */
function EngineHistory({ engineKey }: { engineKey: string }) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  return (
    <details
      className="engines-history"
      onToggle={(ev) => setOpen((ev.currentTarget as HTMLDetailsElement).open)}
    >
      <summary>{tr("admin.engines_history")}</summary>
      {open && <EngineUptimePanel engineKey={engineKey} />}
    </details>
  );
}

/** A clock that ticks only while there is something to count.
 *
 * "Up for" and the stop countdown are live figures, and a status panel that needs a manual
 * refresh to stop being wrong is worse than one that shows nothing. Two things bound the cost:
 * the interval is torn down as soon as there is nothing to count (a deployment whose engines are
 * all parked runs no timer at all), and it ticks every 15 seconds rather than every second —
 * every figure it feeds is rounded to whole minutes, so a one-second tick would be 59 re-renders
 * producing identical text. */
const ENGINE_TICK_MS = 15_000;

function useSecondHand(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), ENGINE_TICK_MS);
    return () => clearInterval(t);
  }, [active]);
  return now;
}

/** The live half of an engine row: when it started, when it will stop, and what has been asked
 *  of it lately. */
function EngineStatus({ row }: { row: EngineRow }) {
  const tr = useT();
  const dur = useDuration();
  // The box's own registeredAt when there is one, and only then the service's deployment time.
  // They are different facts: `service_since` moves when a stack update or a replaced task
  // changes the deployment, without a new box being bought, and an operator looking at a GPU
  // bill wants to know when THE BOX started.
  const startedAt = row.box?.since || row.service_since;
  const upKind = row.box?.since ? "admin.engines_since_box" : "admin.engines_since_service";
  const stopIn = row.stop_eta;
  const now = useSecondHand(!!startedAt || !!stopIn);
  const upSecs = startedAt ? -(secsUntil(startedAt, now) ?? 0) : null;
  const leftSecs = secsUntil(stopIn, now);

  const lines: { key: string; body: ReactNode; warn?: boolean }[] = [];

  if (startedAt && upSecs !== null && upSecs >= 0) {
    lines.push({
      key: "since",
      body: (
        <>
          {tr(upKind as never)}
          <span className="mono">{localStamp(startedAt)}</span>
          <Sep />
          {tr("admin.engines_up_for").replace("{d}", dur(upSecs))}
          {row.box?.id ? <span className="mono engines-boxid"> {row.box.id}</span> : null}
          {/* DRAINING is stopped-but-still-billing: the task is gone, the instance is not.
              Measured 427-477 s on a GPU box, and it is money already spent — which is why
              shortening the idle window below it buys nothing (ADR 0071 決定 7). */}
          {row.box?.status && row.box.status !== "ACTIVE" ? (
            <span className="mono"> ({row.box.status})</span>
          ) : null}
        </>
      ),
    });
  }

  // ABSENT means "no answer", and the CP omits it in exactly the cases where a countdown would
  // be a lie: pinned on (it never stops), switched off, already stopped, or nothing has ever
  // stamped the demand mark. Do not invent a fallback here — the honesty lives in the omission.
  if (leftSecs !== null) {
    lines.push({
      key: "stop",
      body:
        leftSecs > 0 ? (
          <>
            {tr("admin.engines_stops_at")}
            <span className="mono">{localStamp(stopIn!)}</span>
            <Sep />
            {tr("admin.engines_stops_in").replace("{d}", dur(leftSecs))}
          </>
        ) : (
          // The window has elapsed but the controller has not ticked yet (up to 30 seconds).
          // Saying "in -4 seconds" or silently flipping to "stopped" both misdescribe it.
          <>{tr("admin.engines_stops_due")}</>
        ),
    });
  } else if (row.mode === "ondemand" && row.idle_secs) {
    // No live countdown — the engine is not up, so there is nothing to stop. The POLICY is
    // still worth stating, and it is a different claim from a time: "it will go quiet after
    // 30 minutes" rather than "it goes at 10:47". Without it, the operator of a stopped
    // engine has no way to see the window at all.
    lines.push({
      key: "policy",
      body: tr("admin.engines_idle_policy").replace("{d}", dur(row.idle_secs)),
    });
  }

  if (row.window_secs) {
    const partial = windowIsPartial(row);
    lines.push({
      key: "demand",
      warn: partial,
      body: (
        <>
          {tr("admin.engines_recent")
            .replace("{m}", String(Math.round(row.window_secs / 60)))
            .replace("{n}", String(row.window_units ?? 0))}
          {/* ⚠️ The count lives in the CP's memory and nowhere else, so a control plane
              replaced two minutes ago reports 0 while somebody is mid-conversation. Saying so
              is the whole point: an unqualified 0 here is the one number on this panel that
              can be confidently wrong. */}
          {partial && (
            <>
              {" "}
              {tr("admin.engines_recent_partial").replace(
                "{d}",
                dur(row.window_counted_secs ?? 0),
              )}
            </>
          )}
          {/* Persisted (engine_<key>_demand_at), so it survives the restart the count does
              not — which is what makes a zero above readable rather than alarming. */}
          {row.last_demand && (
            <>
              <Sep />
              {tr("admin.engines_last_demand")}
              <span className="mono">{localStamp(row.last_demand)}</span>
            </>
          )}
        </>
      ),
    });
  }

  // Which model is in VRAM right now, and what the taking of turns has cost. A router holding
  // one model at a time (LlmModelsMax=1) reloads on every change of model — 267 s of weights,
  // measured — and an engine that only ever says "warm" hides that entirely (ADR 0072
  // decision 3). Both numbers are this CP process's own, like the demand count above.
  if (row.warm_model || row.model_swaps) {
    lines.push({
      key: "warm-model",
      body: (
        <>
          {row.warm_model && (
            <>
              {tr("admin.engines_warm_model")}
              <span className="mono">{row.warm_model}</span>
            </>
          )}
          {!!row.model_swaps && (
            <>
              {row.warm_model && <Sep />}
              {tr("admin.engines_model_swaps").replace("{n}", String(row.model_swaps))}
            </>
          )}
        </>
      ),
    });
  }

  if (lines.length === 0) return null;
  return (
    <ul className="engines-status muted">
      {lines.map((l) => (
        <li key={l.key} className={l.warn ? "engines-partial" : undefined}>
          {l.body}
        </li>
      ))}
    </ul>
  );
}

/** An instant in the reader's own timezone. The CP speaks UTC throughout (the buckets are cut
 *  there), and an operator deciding whether to stop a GPU should not have to do the arithmetic. */
function localStamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// The heading names the engine by what it does, not by its key: "llm" and "image" are the
// stack's words, and `provider` is what a session sees in its launch menu.
function engineTitle(e: EngineRow): string {
  const provider = e.provider || e.key;
  return e.api === "images" ? `${provider} (${e.key}) — image` : `${provider} (${e.key})`;
}

function engineStateLabel(e: EngineRow, tr: (k: never) => string): string {
  if (!e.managed) return tr("admin.tts_external" as never);
  switch (e.state) {
    case "stopping":
      return tr("admin.tts_stopping" as never);
    case "starting":
      return tr("admin.tts_starting" as never);
    case "running":
      return tr("admin.tts_running" as never);
    default:
      return tr("admin.tts_stopped" as never);
  }
}
