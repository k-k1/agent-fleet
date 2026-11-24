import { NO_AUTOFILL } from "../../../ui/noAutofill.ts";
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { getTts, putTtsDict, putTtsMode, type TtsStatus } from "./adminApi.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { setTenantDict } from "../../chat/ttsDict.ts";
import { useReadGuard } from "../../../lib/readGuard.ts";
import { useInterval } from "../../../lib/useInterval.ts";

export function TtsAdminView() {
  const tr = useT();
  const [data, setData] = useState<TtsStatus | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  // The tenant-wide reading dictionary, applied to every user's speech; a user's own dictionary
  // overrides the same spelling. dict = the value being edited (null = not loaded), savedDict =
  // the server's value, used to detect dirty.
  const [dict, setDict] = useState<string | null>(null);
  const [savedDict, setSavedDict] = useState("");
  const [dictBusy, setDictBusy] = useState(false);

  // The poll's period no longer restarts with every answer, so a read can still be in flight when
  // a mode change or a dictionary save answers: those claim their lanes ("data" for the status,
  // "dict" for the saved dictionary), and a read begun before that answer must not put the
  // previous mode or dictionary back on screen.
  const guard = useReadGuard();
  const load = useCallback(async () => {
    const tk = guard.begin();
    try {
      const d = await getTts();
      if (!isOk(d)) {
        if (d) {
          if (tk.land("err")) setErr(errText(d.error));
        } else {
          if (tk.land("data")) setData(null);
          if (tk.land("err")) setErr(tr("admin.load_error"));
        }
        return;
      }
      if (tk.land("err")) setErr("");
      if (tk.land("data")) setData(d);
      // Its own lane: only a dictionary save claims it, so a mode change answering first does
      // not also drop the newer dictionary another administrator saved.
      if (tk.land("dict")) {
        const dv = typeof d.dict === "string" ? d.dict : "";
        setSavedDict(dv);
        setDict((cur) => (cur === null ? dv : cur)); // the poll must not clobber an in-progress edit
      }
    } catch {
      if (tk.land("err")) setErr(tr("admin.load_error"));
    }
  }, [guard, tr]);
  useEffect(() => {
    load();
  }, [load]);
  // Poll while anything is still moving: the engine is coming up, it is being stopped
  // (the mode said off and the desired count has not caught up yet), or the control is
  // pinned off because no engine exists — that pin has to lift the moment one appears.
  // On-demand always polls: the desired count moves without anyone touching this screen.
  const settled = !!(data?.engine?.ready || data?.engine?.state === "stopped");
  const polling =
    !!data &&
    !(settled && data.mode !== "ondemand") &&
    !(data.mode === "off" && data.managed && data.engine?.state !== "stopping");
  useInterval(() => void load(), polling ? 5000 : null);

  const setMode = async (mode: string) => {
    setBusy(true);
    try {
      const d = await putTtsMode(mode);
      if (isOk(d) || !d) {
        guard.claim("data");
        setData(isOk(d) ? d : null);
      } else {
        guard.claim("err");
        setErr(errText(d.error));
      }
    } finally {
      setBusy(false);
    }
  };

  const saveDict = async () => {
    if (dict === null) return;
    setDictBusy(true);
    try {
      const d = await putTtsDict(dict);
      if (d && !isOk(d)) {
        guard.claim("err");
        setErr(errText(d.error));
        return;
      }
      guard.claim("err", "data", "dict");
      setErr("");
      setData(d); // null stays a success, as it always did
      setSavedDict(dict);
      setTenantDict(dict); // applies to this browser's speech at once; other users on next load
    } finally {
      setDictBusy(false);
    }
  };

  const engine = data?.engine || {};
  // "No engine": not ECS-managed (so this screen cannot start one) and its URL is unreachable.
  // Enabling would send nothing to VOICEVOX and auto would fall back to Polly even for Japanese,
  // so the effective state is off. Pin only the display and the control to off, never the stored
  // setting: when an engine appears the poll above lifts the pin and the recorded intent returns.
  const noEngine = !!data && !data.managed && !engine.ready;
  // Mode is what the administrator chose and state is what the deployment is doing about
  // it — two different questions (ADR 0070 decision 7). They disagree on purpose right
  // after "off" is pressed: the mode is off immediately, the state says stopping, and the
  // desired count only moves once the undo window has run out.
  const mode: string = noEngine ? "off" : (data?.mode ?? (data?.enabled === false ? "off" : "on"));
  const engineLabel = !data
    ? "…"
    : engine.state === "stopping"
      ? tr("admin.tts_stopping")
      : engine.ready
        ? tr("admin.tts_running")
        : engine.state === "starting"
          ? tr("admin.tts_starting")
          : engine.state === "running"
            ? tr("admin.tts_running_waiting")
            : mode !== "off" && data.managed
              ? tr("admin.tts_stopped")
              : tr("admin.tts_stopped_or_off");

  return (
    <div className="admin-stage">
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.tts_engine_label")}</span>
          <span className="seg sm">
            <button
              type="button"
              className={"seg-btn" + (mode === "off" ? " seg-btn--active" : "")}
              disabled={busy || data === null || noEngine}
              onClick={() => setMode("off")}
            >
              {tr("admin.tts_mode_off")}
            </button>
            {/* On-demand only exists where this screen can actually start a service. */}
            {data?.managed && (
              <button
                type="button"
                className={"seg-btn" + (mode === "ondemand" ? " seg-btn--active" : "")}
                disabled={busy || data === null}
                onClick={() => setMode("ondemand")}
              >
                {tr("admin.tts_mode_ondemand")}
              </button>
            )}
            <button
              type="button"
              className={"seg-btn" + (mode === "on" ? " seg-btn--active" : "")}
              disabled={busy || data === null || noEngine}
              onClick={() => setMode("on")}
            >
              {tr("admin.tts_mode_on")}
            </button>
          </span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        {data && (
          <>
            <p className={engine.ready || mode === "off" ? "muted" : "form-err"}>
              {tr("admin.tts_engine_prefix")}{engineLabel}
              {data.managed ? tr("admin.tts_managed") : tr("admin.tts_external")}
              {tr("admin.tts_polly_sep")}{data.polly?.ready ? tr("admin.tts_polly_ready") : tr("admin.tts_polly_unset")}
            </p>
            {engine.state === "starting" && data.managed && (
              <p className="muted">{tr("admin.tts_starting_note")}</p>
            )}
            {engine.state === "stopping" && <p className="muted">{tr("admin.tts_stopping_note")}</p>}
            {mode === "ondemand" && <p className="muted">{tr("admin.tts_ondemand_note")}</p>}
            {noEngine && <p className="muted">{tr("admin.tts_no_engine")}</p>}
            {engine.error && <p className="form-err">{engine.error}</p>}
          </>
        )}
        {err && <p className="form-err">{err}</p>}
        <p className="muted">{tr("admin.tts_disable_note")}</p>
      </section>
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.tts_dict_title")}</span>
          <button
            type="button"
            className="btn primary"
            disabled={dictBusy || dict === null || dict === savedDict}
            onClick={saveDict}
          >
            {dictBusy ? tr("admin.saving") : tr("common.save")}
          </button>
        </div>
        <textarea {...NO_AUTOFILL}
          className="ds-userdict"
          value={dict ?? ""}
          onChange={(e) => setDict(e.target.value)}
          rows={8}
          spellCheck={false}
          disabled={dict === null}
          placeholder={tr("admin.tts_dict_ph")}
        />
        <p className="muted">{tr("admin.tts_dict_note")}</p>
      </section>
    </div>
  );
}

// --- Tenant list (the root entry point) ------------------------------------
// Opening a card swaps the whole rail for that tenant's surface (drill-down level 1).
