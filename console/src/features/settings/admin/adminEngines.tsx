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
  /** The EC2 type this box actually is. After a class change it is NOT what the capacity
   *  provider says: the change reaches the next box only (ADR 0074 decision 4). */
  instance_type?: string;
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
  /** What this model would want on the card, and how well that is known (ADR 0074 decision 6):
   *  "declared" = the operator measured it, "floor" = the weight files' size and nothing else
   *  (no KV cache, no context), "unknown" = nobody said. 🔴 `unknown` must never be drawn as a
   *  comfortable zero — it means the question was not answered. */
  vram_need_mib?: number;
  vram_need_source?: "declared" | "floor" | "unknown";
  /** BOTH are kept and both are shown: Hugging Face reports `other` for the two
   *  non-commercial models in ADR 0072's table, with the real terms in license_name. */
  license?: string;
  license_name?: string;
  license_url?: string;
  /** "yes" | "no" | "unknown", resolved from the licence at ingest (ADR 0072 decision 10). The
   *  row says what may be RESTRICTED and points at the licence; it does not decide, because
   *  which of "running the model" and "selling what it makes" a non-commercial licence forbids
   *  differs between them. */
  commercial_use?: string;
  /** Who took the licence on for every member of this deployment, and when (ADR 0072 decision
   *  10). A record of a HUMAN act, which is the question an audit asks and the model card
   *  cannot answer — so it belongs on the row and not only in the ingest form that recorded it.
   *  Absent for a seeded row and for one registered by hand. */
  license_accepted_by?: string;
  license_accepted_at?: string;
  base_model?: string;
  /** The provider dispatches on base_model and THIS row's is missing or names no workflow
   *  template (ADR 0072 decision 2). Stated by the CP, because the panel cannot know the
   *  vocabulary — and because the row looks complete without it and fails only at generation,
   *  after a cold start somebody waited through. */
  base_model_missing?: boolean;
  /** The row HAS a family, and the workflow that family names reads files this row does not
   *  declare (ADR 0072 P2 欠落 10) — listed as the roles that are missing. It is the mark that
   *  used to disappear the moment a family was chosen: `flux1-dev` was one unflagged 22.2 GiB
   *  checkpoint, the flux1 template reads four other files, and choosing any family at all
   *  made the panel go quiet about a row that still could not generate. The CP refuses to
   *  enable one of these. */
  files_missing?: string[];
  /** Where the bytes came from (`hf:<repo>/<file>`, `civitai:<id>`, a URL). The id is short and
   *  unique only inside this deployment, so this is the only thing that says WHICH vendor's
   *  model of that name this row is. Absent for a seeded row. */
  source?: string;
  precision?: string;
  sizes?: string[];
  files?: string[];
  /** The row as it was DECLARED — the S3 keys, the flags and the sizes, in the shape
   *  `POST …/models` reads back (ADR 0072 P6 R2). `files` above is base names to read; these
   *  are what a forgotten row is rebuilt from, and since P6 the catalogue is the only place
   *  the declaration exists at all. Super-admin only, like the rest of this row. */
  file_rows?: { s3Key: string; flag?: string; bytes?: number }[];
  args?: string[];
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
  /** false when the repository is gated and this deployment has no Hugging Face token, or when
   *  Civitai's uploader requires an account. The button is disabled on it rather than letting a
   *  task run nine minutes into a 401. */
  can_ingest?: boolean;
  /** The Civitai uploader requires a logged-in account to download this asset (ADR 0072 P2
   *  欠落 5). Its own field rather than `gated`, because the two have different answers: a
   *  registered Hugging Face token satisfies gating and cannot touch this one, so folding them
   *  would send somebody to the token field to fix what a token does not fix. */
  login_required?: boolean;
  /** Gated, and a token IS registered — which is still not a yes. The CP resolves anonymously
   *  and cannot ask whether that account accepted THIS repository's terms; when it has not,
   *  the answer is a 403 on the download minutes later. So this is a warning before the press,
   *  not a verdict (ADR 0072 P5 実機検証). */
  gated_needs_acceptance?: boolean;
  /** The model's OWN maximum, off the GGUF header. 🔴 A ceiling, not a setting: the 30B in
   *  this deployment publishes 262144 and is run at 32768, because what the architecture
   *  allows and what fits in the GPU are different questions. Offered, never applied. */
  context_length?: number;
};

/** One search result (POST …/ingest/search, ADR 0072 decision 11).
 *
 * A DESTINATION, not an ingest: picking one fills the repository field and the existing
 * resolve → accept → ingest road runs unchanged. The numbers here are the listing's, i.e. a
 * draft — the licence and the sha256 of record are what the resolve of the chosen FILE reads,
 * because a Hugging Face card can move between the two calls. */
type IngestHit = {
  /** "hf" or "civitai" — decides how `ref` is turned into a repository field. */
  source: string;
  /** The repository for HF; the VERSION id for Civitai (not the model id on the page's URL). */
  ref: string;
  name: string;
  /** The three numbers a ranking is built on. All three ride on every row, whichever one the
   *  list was ordered by — sorting by one and showing only that one leaves "why is this here"
   *  unanswerable. `trending` is Hugging Face's own score; Civitai publishes none. */
  downloads?: number;
  likes?: number;
  trending?: number;
  gated?: boolean;
  license?: string;
  license_name?: string;
  base_model?: string;
  /** When it first appeared, beside `updated_at`'s "when it last changed". Both, because for a
   *  quantisation repository they are a year apart and only the pair answers "is this
   *  maintained". 🔴 Display only — there is deliberately no "newest" ranking to sort by (the
   *  CP's engineSortHF says why: every date-ordered page is bulk automated re-quantisations). */
  published_at?: string;
  updated_at?: string;
  /** The upstream page, composed by the CP and used here VERBATIM. Not built in the panel: the
   *  two sources spell it differently and Civitai's needs the model id, which `ref` is not. */
  url?: string;
  bytes?: number;
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
  /** What the operator has to DO about a failure, read by the CP out of the status in the
   *  message. 🔴 `gated_no_token` and `gated_not_accepted` are one line of curl apart and need
   *  opposite screens: 401 is a token that never reached the task, 403 is a token that did and
   *  an account that has not accepted THAT repository (measured, ADR 0072 P5 実機検証 — one
   *  token, FLUX.1-dev through and SD3.5 Medium refused). */
  code?: string;
  bytes?: number;
  created_at?: string;
};

/** One rung of the GPU ladder the operator declared (ADR 0074 decision 1). The CP asks neither
 *  EC2 nor the Pricing API: every number here was written by whoever wrote the ladder, and
 *  `usd_per_hour` is absent — not zero — when they left it out. */
type EngineClass = {
  id: string;
  label: string;
  vram_mib: number;
  types: string[];
  usd_per_hour?: number;
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
  /** The checkpoint families this provider picks a workflow graph from, and the labels a split
   *  model's files may carry. BOTH absent for a provider with no opinion (sd.cpp holds one
   *  unlabelled checkpoint and never reads a family), which is what the panel reads as "do not
   *  ask" — offering a choice that changes nothing is worse than offering none. */
  base_models?: string[];
  file_flags?: string[];
  /** 🔴 Optional because the row a granted tenant_admin receives DOES NOT CARRY THEM (ADR 0072
   *  open question 11). That row is a strict subset of the operator's, so everything the
   *  reduced panel does not draw is simply absent — which is why the panel branches on the
   *  answer's `super_admin` flag and never on "did this field arrive". */
  mode?: string;
  enabled?: boolean;
  managed?: boolean;
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
  /** The GPU ladder, and where this role sits on it. All absent on a deployment that declares
   *  no ladder, which is what the panel reads as "this deployment does not choose its box". */
  classes?: EngineClass[];
  class?: EngineClass;
  class_default?: string;
  /** 🔴 Stated by the CP, not computed here: "you are not on the default" is the sentence that
   *  keeps a temporary experiment from becoming a permanent hourly bill (decision 7). */
  class_is_default?: boolean;
  /** A box of another rung is still up, so the saved choice has reached nothing yet. */
  class_replace_pending?: boolean;
  /** 🔴 Why the capacity provider does not hold the rung above. The CP saves the choice BEFORE
   *  it applies it, so a failed apply leaves this picker showing a rung nothing was written for
   *  — and picking that same rung again is no change, so nothing is sent. This field is what
   *  the retry hangs off. In-memory at the CP: absent means "no claim", never "it was applied". */
  class_apply_error?: string;
  /** The largest demand among the ENABLED models — a maximum, not a sum: one model is in VRAM
   *  at a time (`--models-max 1`, one checkpoint). */
  vram_need_mib?: number;
  vram_need_source?: "declared" | "floor" | "unknown";
  vram_need_model?: string;
  vram_fits?: boolean;
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
  /** Whether this caller is the deployment operator. 🔴 Taken from the answer's own flag, never
   *  inferred from which fields arrived: the tenant_admin row is a strict SUBSET of the
   *  operator's (ADR 0072 open question 11), so "mode is undefined" would work today and start
   *  drawing 403-ing buttons the day a field is renamed. Defaults to false, so a Control Plane
   *  too old to send the flag shows the safe half rather than controls that cannot work. */
  const [isSuper, setIsSuper] = useState(false);
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
      setIsSuper(!!d?.super_admin);
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

  /** Stop the box so the next one is bought on the rung that is now chosen. It does NOT touch
   *  the mode: an engine pinned `on` comes back by itself, an on-demand one with the next
   *  request, and the control plane holds that start until the old box has left the cluster. */
  const replaceBox = async (key: string) => {
    setBusy(key);
    try {
      const d = await apiJSON(`api/admin/engines/${encodeURIComponent(key)}/replace-box`, "POST", {});
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      setRows((cur) => (cur || []).map((x) => (x.key === key ? { ...x, ...d } : x)));
    } finally {
      setBusy("");
    }
  };

  /** Which GPU this role buys next (ADR 0074). It does NOT replace a running box — the API
   *  reaches new instances only — so the answer carries class_replace_pending and the card
   *  turns that into an explicit "replace it now", which costs a cold start. */
  const setClass = async (key: string, cls: string) => {
    setBusy(key);
    try {
      const d = await apiJSON(
        `api/admin/engines/${encodeURIComponent(key)}/class`,
        "PUT",
        { class: cls },
      );
      if (d?.error) {
        setErr(errDetail(d.error));
        // 🔴 A refusal here is not a request that did nothing: the CP stores the choice before
        // it applies it, so the rung on screen is already stale and the failure it should be
        // showing is only in the fresh row. Without this re-read the picker snaps back to the
        // OLD rung — which is neither what is stored nor what the provider holds — and the
        // retry below has nothing to appear beside.
        await load();
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
  const setModel = async (key: string, id: string, patch: Record<string, boolean | string>) => {
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
      {rows.length === 0 && (
        <section className="admin-panel">
          <p className="muted">{tr("admin.engines_none")}</p>
          {/* 🔴 "There is nothing here" is the worst possible answer to "what could I run?".
              Looking at what Hugging Face has needs no engine — no token, no bucket, no task —
              so the browse stays, and it says what it cannot do rather than pretending. */}
          <EngineBrowse />
        </section>
      )}
      {/* What a granted tenant_admin may do here, said once at the top (ADR 0072 open question
          11). Without it the reduced panel reads as a broken operator panel — the controls are
          not disabled, they are absent — and the one thing that has to be understood before
          taking a model in is that the catalogue is shared with every other tenant. */}
      {!isSuper && rows.length > 0 && <p className="admin-hint pad">{tr("admin.engines_tenant_scope")}</p>}
      {rows.map((e) => (
        <section className="admin-panel" key={e.key}>
          <div className="usage-toolbar">
            <span>{engineTitle(e)}</span>
            {/* The mode buys and stops a GPU for the WHOLE deployment, so it is the operator's
                and is not rendered at all for anyone else. Not disabled: a greyed-out row of
                buttons invites an email asking to have them enabled. */}
            {isSuper && (
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
            )}
            <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
              <Icon name="refresh" />
            </button>
          </div>
          {/* What ECS is doing, and what the engine would hold if it were up. A badge and chips
              rather than one sentence: the state is the fact the rest of this panel is read
              against, and inside "状態: 停止中 / モデル: a, b （宣言。…）" it was a phrase like
              any other. The model names carry no tone of their own — whether they are in VRAM
              is the note that follows them, and it is not the same claim. */}
          {/* The box: whether it is up, which models are in VRAM. Operator-only because it is
              the state of a machine only the operator starts, stops and pays for — and because
              the reduced row does not carry the fields it is drawn from. */}
          {isSuper && (
          <div className="engines-state">
            <span className={"engines-model-tag " + engineStateTone(e)}>{engineStateLabel(e, tr)}</span>
            {e.models?.length ? (
              <>
                <span className="engines-fact-label">{tr("admin.engines_models_label")}</span>
                {e.models.map((m) => (
                  <span key={m} className="mono engines-model-tag">
                    {m}
                  </span>
                ))}
                <span className="muted">
                  {tr(e.warm ? "admin.engines_model_loaded" : "admin.engines_model_declared")}
                </span>
              </>
            ) : null}
          </div>
          )}
          {isSuper && <EngineStatus row={e} />}
          {isSuper && (
            <EngineClassPicker
              row={e}
              busy={busy === e.key}
              onPick={(cls) => setClass(e.key, cls)}
              onReplace={() => replaceBox(e.key)}
            />
          )}
          <EngineModels
            row={e}
            busy={busy}
            readOnly={!isSuper}
            onChange={(id, patch) => setModel(e.key, id, patch)}
            onForget={(id, purge) => forgetModel(e.key, id, purge)}
            onAdd={(body) => addModel(e.key, body)}
          />
          <EngineIngest
            engineKey={e.key}
            isImage={e.api === "images"}
            baseModels={e.base_models}
            fileFlags={e.file_flags}
            modelIds={(e.model_rows || []).map((m) => m.id)}
            busy={busy === e.key + "/ingest"}
            onStarted={() => loadJobs(e.key)}
          />
          <EngineIngestJobs jobs={jobs[e.key] || []} />
          {isSuper && e.mode === "on" && <p className="form-err">{tr("admin.engines_always_on_note")}</p>}
          {isSuper && e.error && <p className="form-err">{e.error}</p>}
          {/* The events are the only place ECS says why a start failed ("no container
              instances met the placement constraints", a pull failure). An engine stuck in
              `starting` is exactly when somebody needs them, and the alternative is a trip to
              the AWS console for a string the CP already has. */}
          {isSuper && e.state === "starting" && e.events?.length ? (
            <p className="muted mono engines-events">{e.events.join(" | ")}</p>
          ) : null}
          {/* Uptime is billing history for a machine somebody else pays for, and the route
              behind it is super_admin anyway — rendering it would be a permanent spinner. */}
          {isSuper && <EngineHistory engineKey={e.key} />}
        </section>
      ))}
      {/* The deployment's Hugging Face token. One token serves every role and every tenant, so
          registering it is the operator's act; a tenant_admin who needs a gated repository asks
          for it (the ingest form already says so when a gated source is resolved). */}
      {isSuper && rows.length > 0 && <HfTokenPanel />}
      {err && <p className="form-err pad">{err}</p>}
      {note && <p className="muted pad">{note}</p>}
      {/* What the three modes do. Same reason as the sentence in EngineModels: it explains the
          segment above, and that segment is the operator's. */}
      {isSuper && <p className="muted pad">{tr("admin.engines_note")}</p>}
    </div>
  );
}

/** Which GPU this role buys (ADR 0074).
 *
 * Absent entirely on a deployment that declares no ladder — that is decision 3, and it has to
 * look like "there is no such choice here", not like a control that does nothing.
 *
 * Three things this section must keep saying, because each one is a bill somebody would
 * otherwise only find later:
 *
 *   - a saved rung reaches the NEXT box only. Reporting success while the old card keeps
 *     answering is the most expensive lie available here, so the replace step is separate,
 *     explicit, and priced;
 *   - running on something other than the default is stated permanently. "Temporarily try a
 *     bigger box" turns into a permanent hourly bill exactly when nobody is reminded;
 *   - a model that will not fit is pointed at, with the STRENGTH of the evidence attached:
 *     a measured number and a weights-only floor are different claims, and "nobody measured
 *     this" is never drawn as "it fits";
 *   - a rung that was saved but could not be APPLIED offers its own retry. The select cannot be
 *     one: the choice is stored before it is applied, so after a failure this picker already
 *     shows that rung and re-picking it fires no change event at all. Recovery was a detour
 *     through another rung until this button existed (ADR 0074). */
function EngineClassPicker({
  row,
  busy,
  onPick,
  onReplace,
}: {
  row: EngineRow;
  busy: boolean;
  onPick: (cls: string) => void;
  onReplace: () => void;
}) {
  const tr = useT();
  const classes = row.classes || [];
  if (classes.length === 0) return null;
  const current = row.class?.id || row.class_default || "";
  // The box that is answering right now, when it is not one this rung covers. It is read from
  // the container instance rather than from the capacity provider, because the provider
  // describes the NEXT box.
  const oldBox =
    row.box?.instance_type && row.class && !row.class.types.includes(row.box.instance_type)
      ? row.box.instance_type
      : "";
  return (
    <div className="engines-class">
      <div className="engines-class-head">
        <span className="muted">{tr("admin.engines_class")}</span>
        <select
          className="sm"
          value={current}
          disabled={busy}
          onChange={(ev) => onPick(ev.currentTarget.value)}
        >
          {classes.map((c) => (
            <option key={c.id} value={c.id}>
              {engineClassLabel(c, tr)}
            </option>
          ))}
        </select>
        {row.class_is_default === false && (
          <>
            <span className="engines-model-tag">{tr("admin.engines_class_not_default")}</span>
            <button
              type="button"
              className="sm"
              disabled={busy}
              onClick={() => onPick(row.class_default || "")}
            >
              {tr("admin.engines_class_reset")}
            </button>
          </>
        )}
      </div>
      {/* Saved, not applied. The provider's own words are quoted rather than summarised: a
          missing IAM grant and a throttle need different things from the person reading them.
          The retry re-sends the rung already selected, which is the one request the select can
          never produce. */}
      {row.class_apply_error && (
        <div className="engines-class-pending">
          <p className="form-err">
            {tr("admin.engines_class_apply_failed").replace("{m}", row.class_apply_error)}
          </p>
          <button type="button" className="sm" disabled={busy} onClick={() => onPick(current)}>
            {tr("admin.engines_class_apply_retry")}
          </button>
        </div>
      )}
      {/* The saved rung has reached nothing yet: a box of another type is still up. Both halves
          are said — that the change is pending, and that acting on it costs a cold start. */}
      {oldBox && (
        <div className="engines-class-pending">
          <p className="form-err">{tr("admin.engines_class_pending").replace("{t}", oldBox)}</p>
          {/* On its own line rather than trailing the sentence. Rendered headless it read as
              part of the paragraph — and this is the button that costs a cold start, so it has
              to look like one before somebody presses it by accident. */}
          <button type="button" className="sm" disabled={busy} onClick={onReplace}>
            {tr("admin.engines_class_replace")}
          </button>
        </div>
      )}
      <p className="muted">{engineClassVramNote(row, tr)}</p>
    </div>
  );
}

/** One rung as an option: the operator's label, its VRAM, and the price only when they declared
 *  one. A missing price prints nothing — an invented 0 would read as free. */
function engineClassLabel(c: EngineClass, tr: (k: never) => string): string {
  const bits = [c.label, (tr("admin.engines_model_vram" as never) as string).replace("{n}", String(c.vram_mib))];
  if (c.usd_per_hour) bits.push("$" + c.usd_per_hour + "/h");
  return bits.join(" · ");
}

/** What the enabled models want against what the card has. Three sentences, because the three
 *  cases are not the same claim (ADR 0074 decision 6). */
function engineClassVramNote(row: EngineRow, tr: (k: never) => string): string {
  const have = row.class?.vram_mib || 0;
  if (!have) return "";
  // ⚠️ What the comparison above is, on an engine that can hold more than one model at a time.
  // The number is a MAXIMUM and not a sum (ADR 0074 decision 6, `--models-max 1` and sd-server's
  // one checkpoint) — but comfy chooses a checkpoint per REQUEST, keeps what it loaded cached
  // and only evicts when it needs the room, so several can be resident. The rule is not changed
  // to a sum: comfy evicts rather than dies, and a sum would warn on every start of a deployment
  // with four enabled models, which is the warning nobody reads.
  const many = row.provider === "comfy" ? " " + (tr("admin.engines_class_vram_many" as never) as string) : "";
  if (row.vram_need_source === "unknown" || !row.vram_need_mib) {
    return (tr("admin.engines_class_vram_unknown" as never) as string) + many;
  }
  const key = row.vram_fits === false ? "admin.engines_class_vram_over" : "admin.engines_class_vram_ok";
  return (
    (tr(key as never) as string)
      .replace("{n}", String(row.vram_need_mib))
      .replace("{m}", String(have))
      .replace("{id}", row.vram_need_model || "")
      .replace("{src}", tr(("admin.engines_vram_src_" + (row.vram_need_source || "unknown")) as never) as string) +
    many
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
/** The catalogue list.
 *
 * `readOnly` is the reduced panel a granted tenant_admin sees (ADR 0072 open question 11): the
 * same rows, none of the controls. Enabling a model, choosing what the engine starts with and
 * forgetting a row all decide what EVERY OTHER TENANT runs off one shared box, so they stay the
 * operator's — and the CP refuses them anyway, which is the reason not to draw them disabled. */
function EngineModels({
  row,
  busy,
  readOnly,
  onChange,
  onForget,
  onAdd,
}: {
  row: EngineRow;
  busy: string;
  readOnly?: boolean;
  onChange: (id: string, patch: Record<string, boolean | string>) => void;
  onForget: (id: string, purge: boolean) => void;
  onAdd: (body: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const models = row.model_rows || [];
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
      {models.length === 0 && <p className="form-err">{tr("admin.engines_catalog_empty")}</p>}
      {/* "Nothing is enabled" is addressed to whoever can enable something. The reduced row does
          not carry has_models at all, so this is also the field that must not be read as false. */}
      {!readOnly && models.length > 0 && !row.has_models && (
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
                {isLora && <span className="engines-model-tag">LoRA</span>}
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
          what must be ABSENT, and a leftover sentence is neither a control nor a field. */}
      {!readOnly && <p className="muted">{tr("admin.engines_model_next_start")}</p>}
      {/* Registering a file that is ALREADY in the bucket. It needs an S3 key somebody put
          there by hand, which is an operator act on an operator's bucket — the tenant route
          into the catalogue is the ingest below, which fetches. */}
      {!readOnly && (
        <EngineModelAdd
          busy={busy === row.key + "/+"}
          isImage={isImage}
          baseModels={row.base_models}
          fileFlags={row.file_flags}
          onAdd={onAdd}
        />
      )}
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
  baseModels,
  fileFlags,
  onAdd,
}: {
  busy: boolean;
  isImage: boolean;
  /** The checkpoint families this provider dispatches on, served by the CP so the panel and
   *  the engine cannot disagree about the spelling. Empty = this provider has no opinion. */
  baseModels?: string[];
  /** The labels a split model's files may carry, same source and same rule. Empty = a row is
   *  always one unlabelled file. */
  fileFlags?: string[];
  onAdd: (body: Record<string, unknown>) => void;
}) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [id, setId] = useState("");
  const [desc, setDesc] = useState("");
  const [ctx, setCtx] = useState("");
  const [out, setOut] = useState("");
  const [baseModel, setBaseModel] = useState("");
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
  // The family is required exactly when the provider has one, and the button says so by being
  // disabled rather than by letting the CP refuse after the press.
  const incomplete = !id.trim() || rows.length === 0 || (families.length > 0 && !baseModel);
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
    onAdd({
      id: id.trim(),
      kind: isImage ? "checkpoint" : "gguf",
      files: rows.map((f) => ({ flag: f.flag, s3Key: f.s3Key.trim(), bytes: n(f.bytes) })),
      description: desc.trim(),
      base_model: baseModel,
      context_tokens: c && o ? c : 0,
      max_output_tokens: c && o ? o : 0,
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
        isImage ? "juggernaut-xl-v9" : "qwen2.5-coder-1.5b")}
      {/* ⚠️ A CHOICE, never a text box. What the repository calls a model — "SDXL 1.0",
          "Flux.1 D" — is a display name, and typing one here produced rows that looked complete
          and refused to generate (ADR 0072 P2 実機検証). The list comes from the CP, which
          validates against the same one. */}
      {families.length > 0 && (
        <label className="engines-model-add-row">
          <span>{tr("admin.engines_model_add_family")}</span>
          <select value={baseModel} onChange={(ev) => setBaseModel(ev.currentTarget.value)}>
            <option value="">{tr("admin.engines_model_add_family_pick")}</option>
            {families.map((f) => (
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
            isImage ? "image/checkpoints/name.safetensors" : "llm/name.gguf")}
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
          context at all, so offering the field there would ask for a number nothing reads. */}
      {!isImage && field(tr("admin.engines_model_add_ctx"), ctx, setCtx, "32768", true)}
      {!isImage && field(tr("admin.engines_model_add_out"), out, setOut, "4096", true)}
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
function EngineIngest({
  engineKey,
  isImage,
  baseModels,
  fileFlags,
  modelIds,
  busy,
  onStarted,
}: {
  engineKey: string;
  isImage: boolean;
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
  /** True when the typed id is one this engine already has. The CP refuses a plain ingest onto
   *  it (409 model_id_exists), so this is where the second act — attaching a part — is
   *  offered rather than left as an error to read. */
  const known = (modelIds || []).includes(id.trim());
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
    const key = engineIngestPrefix(isImage, fileFlag) + (file.trim() || id.trim());
    const d = await apiJSON(`api/admin/engines/${encodeURIComponent(engineKey)}/ingest`, "POST", {
      id: id.trim(),
      kind: isImage ? "checkpoint" : "gguf",
      s3Key: key,
      source: source(),
      description: desc.trim(),
      base_model: baseModel,
      file_flag: fileFlag,
      attach: attach && known,
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
    setRev("");
    setFile("");
    setFiles(null);
    setId("");
    setFileFlag("");
    setAttach(false);
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
      {!attach && families.length > 0 && (
        <label className="engines-model-add-row">
          <span>{tr("admin.engines_model_add_family")}</span>
          <select value={baseModel} onChange={(ev) => setBaseModel(ev.currentTarget.value)}>
            <option value="">{tr("admin.engines_model_add_family_pick")}</option>
            {families.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>
        </label>
      )}
      {!attach && families.length > 0 && found?.base_model && (
        <p className="muted">
          {(tr("admin.engines_ingest_family_hint") as string).replace("{n}", found.base_model)}
        </p>
      )}
      {field(tr("admin.engines_model_add_desc"), desc, setDesc)}
      {!isImage && field(tr("admin.engines_model_add_ctx"), ctx, setCtx, "32768")}
      {/* The output cap is a FRACTION of the window, never a free number. It is not published
          anywhere — it is a deployment's policy for how much of the window one reply may eat —
          and 🔴 ADR 0072 decision 3: left at 0 opencode reads it as 32,000 and a 32k model ends
          up with 768 usable tokens. Offering computed values makes the pair impossible to
          half-fill. */}
      {!isImage && <OutputCapField ctx={ctx} value={out} onChange={setOut} />}
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
export function engineIngestPrefix(isImage: boolean, flag: string): string {
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
        {/* Gated first and in its own colour: it is the one tag that can turn into a refusal. */}
        {hit.gated && (
          <span className="engines-model-tag warn">{tr("admin.engines_ingest_hit_gated")}</span>
        )}
        {lic && <span className="engines-model-tag">{lic}</span>}
        {hit.base_model && <span className="engines-model-tag">{hit.base_model}</span>}
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
          {/* WHICH card is answering. Once the class is selectable this is not derivable from
              the class shown above: that one describes the next box, and after a change the
              two disagree until this one is replaced (ADR 0074 decision 4). */}
          {row.box?.instance_type ? (
            <span className="mono engines-boxid"> {row.box.instance_type}</span>
          ) : null}
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

/** The badge colour for a state. `stopped` is the resting state of an on-demand GPU, not a
 *  fault, so it stays neutral — a red one there would cry wolf on every panel load. */
function engineStateTone(e: EngineRow): string {
  if (!e.managed) return "";
  switch (e.state) {
    case "running":
      return "on";
    case "starting":
      return "lead";
    case "stopping":
      return "warn";
    default:
      return "off";
  }
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
