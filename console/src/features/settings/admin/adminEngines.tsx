import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// The self-hosted inference engines (ADR 0071): one row per engine, each with the same
// off / on-demand / always-on control the VOICEVOX panel has.
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

type EngineRow = {
  key: string;
  api?: string;
  provider?: string;
  models?: string[];
  mode: string;
  enabled: boolean;
  managed: boolean;
  state?: string;
  desired?: number;
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
          </p>
          {e.mode === "on" && <p className="form-err">{tr("admin.engines_always_on_note")}</p>}
          {e.error && <p className="form-err">{e.error}</p>}
        </section>
      ))}
      {err && <p className="form-err pad">{err}</p>}
      <p className="muted pad">{tr("admin.engines_note")}</p>
    </div>
  );
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
