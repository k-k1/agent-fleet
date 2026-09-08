import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { EngineUptimePanel, Sep, useDuration } from "./EngineUptime.tsx";
import { secsUntil, windowIsPartial } from "./engineUptime.ts";

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
  precision?: string;
  sizes?: string[];
  files?: string[];
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

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/engines");
      if (d?.error) {
        setErr(errText(d.error));
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
        setErr(errText(d.error));
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
    setBusy(key + "/" + id);
    try {
      const d = await apiJSON(
        `api/admin/engines/${encodeURIComponent(key)}/models/${encodeURIComponent(id)}`,
        "PUT",
        patch,
      );
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
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
          />
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
      {err && <p className="form-err pad">{err}</p>}
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
}: {
  row: EngineRow;
  busy: string;
  onChange: (id: string, patch: Record<string, boolean>) => void;
}) {
  const tr = useT();
  const models = row.model_rows || [];
  // An engine with no catalogue at all is the interesting case, not an empty section: the
  // controller refuses to start it and every request is refused, so it needs a sentence rather
  // than a blank area that reads as "still loading".
  if (models.length === 0) {
    return <p className="form-err">{tr("admin.engines_catalog_empty")}</p>;
  }
  const isImage = row.api === "images";
  return (
    <div className="engines-models">
      {!row.has_models && <p className="form-err">{tr("admin.engines_catalog_none_enabled")}</p>}
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
                </span>
              </div>
              {m.description && <p className="muted engines-model-desc">{m.description}</p>}
              <p className="muted engines-model-meta">{engineModelMeta(m, tr)}</p>
            </li>
          );
        })}
      </ul>
      <p className="muted">{tr("admin.engines_model_next_start")}</p>
    </div>
  );
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
  const licence = m.license_name || m.license;
  if (licence) bits.push(licence);
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
