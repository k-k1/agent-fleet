import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { setTenantDict } from "../../chat/ttsDict.ts";

export function TtsAdminView() {
  const tr = useT();
  const [data, setData] = useState<any | null>(null); // { managed, enabled, engine, polly, dict }
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  // The tenant-wide reading dictionary, applied to every user's speech; a user's own dictionary
  // overrides the same spelling. dict = the value being edited (null = not loaded), savedDict =
  // the server's value, used to detect dirty.
  const [dict, setDict] = useState<string | null>(null);
  const [savedDict, setSavedDict] = useState("");
  const [dictBusy, setDictBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/tts");
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      setData(d);
      const dv = typeof d.dict === "string" ? d.dict : "";
      setSavedDict(dv);
      setDict((cur) => (cur === null ? dv : cur)); // the poll must not clobber an in-progress edit
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);
  // Poll for readiness while enabled but not ready (ECS still starting), and also while the
  // toggle is pinned off because no engine exists — the pin has to lift the moment one appears.
  // Polling stops only when it is usable now, or deliberately stopped under management.
  useEffect(() => {
    if (!data || data.engine?.ready) return;
    if (!data.enabled && data.managed) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [data, load]);

  const setEnabled = async (enabled: boolean) => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/tts", "PUT", { enabled });
      if (d?.error) setErr(errText(d.error));
      else setData(d);
    } finally {
      setBusy(false);
    }
  };

  const saveDict = async () => {
    if (dict === null) return;
    setDictBusy(true);
    try {
      const d = await apiJSON("api/admin/tts/dict", "PUT", { dict });
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      setData(d);
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
  const enabled = !!data?.enabled && !noEngine;
  const engineLabel = !data
    ? "…"
    : engine.ready
      ? tr("admin.tts_running")
      : engine.state === "starting"
        ? tr("admin.tts_starting")
        : engine.state === "running"
          ? tr("admin.tts_running_waiting")
          : enabled && data.managed
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
              className={"seg-btn" + (enabled ? " active" : "")}
              disabled={busy || data === null || noEngine}
              onClick={() => setEnabled(true)}
            >
              {tr("admin.enable")}
            </button>
            <button
              type="button"
              className={"seg-btn" + (!enabled ? " active" : "")}
              disabled={busy || data === null || noEngine}
              onClick={() => setEnabled(false)}
            >
              {tr("admin.disable")}
            </button>
          </span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        {data && (
          <>
            <p className={engine.ready ? "muted" : enabled ? "form-err" : "muted"}>
              {tr("admin.tts_engine_prefix")}{engineLabel}
              {data.managed ? tr("admin.tts_managed") : tr("admin.tts_external")}
              {tr("admin.tts_polly_sep")}{data.polly?.ready ? tr("admin.tts_polly_ready") : tr("admin.tts_polly_unset")}
            </p>
            {enabled && !engine.ready && data.managed && (
              <p className="muted">{tr("admin.tts_starting_note")}</p>
            )}
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
        <textarea
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
