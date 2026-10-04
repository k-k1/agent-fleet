import { useEffect, useState } from "react";
import { apiJSON, errDetail } from "../../../core/api/client.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Button } from "../../../ui/Button.tsx";

/** What the CP says about the LAN ComfyUI behind the image role (#957). Never the key: only
 *  whether the panel holds one. */
export type ComfyLanStatus = {
  /** false where the panel can change nothing — a managed engine-table row holds the role. */
  available?: boolean;
  /** Where the image row in effect comes from. */
  source?: "panel" | "env" | "table" | "remote" | "";
  url?: string;
  panel_url?: string;
  panel_key_set?: boolean;
  env_url?: string;
  env_key_set?: boolean;
  updated_by?: string;
  updated_at?: string;
};

// PUT saves, DELETE forgets; both answer the new ComfyLanStatus. There is no GET of its own.
const PATH = "api/admin/engines/comfy-lan";

/** The LAN ComfyUI's URL and bearer, set from the panel instead of AF_COMFY_URL /
 *  AF_COMFY_API_KEY (ADR 0076 decision 2 and its 2026-10-04 addendum). The panel value wins over
 *  the environment, a managed engine-table row wins over both, and the key is write-only: the
 *  field is always empty, and "set / not set" is all the CP answers. `onChanged` reloads the
 *  engine list, whose image row the save has just replaced. */
export function ComfyLanPanel({ status, onChanged }: { status: ComfyLanStatus; onChanged?: () => void }) {
  const tr = useT();
  // Drawn from the engine list's `comfy_lan` (no read of its own, so the screen stays one
  // request), then from each save's answer until the list is read again.
  const [st, setSt] = useState<ComfyLanStatus>(status);
  const [url, setUrl] = useState(status.panel_url || "");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  useEffect(() => {
    setSt(status);
  }, [status]);
  // Only when the saved URL itself moves: the engine list is re-read on a poll while a box
  // starts, and that must not wipe a URL somebody is in the middle of typing.
  useEffect(() => {
    setUrl(status.panel_url || "");
  }, [status.panel_url]);

  const send = async (method: "PUT" | "DELETE", body?: Record<string, unknown>) => {
    setBusy(true);
    try {
      const d = await apiJSON(PATH, method, body);
      if (d?.error) {
        setErr(errDetail(d.error));
        return;
      }
      setErr("");
      // Cleared on success only, like the token panels: a refused save keeps what was typed.
      setKey("");
      setSt(d || {});
      setUrl(d?.panel_url || "");
      onChanged?.();
    } finally {
      setBusy(false);
    }
  };

  const source = st.source || "";
  return (
    <section className="admin-panel" data-testid="comfy-lan-panel">
      <div className="usage-toolbar">
        <span>{tr("admin.engines_comfy_lan")}</span>
      </div>
      <p className="muted" data-testid="comfy-lan-source">
        {source
          ? (tr(`admin.engines_comfy_lan_source_${source}` as never) as string).replace("{url}", st.url || "-")
          : tr("admin.engines_comfy_lan_source_none")}
      </p>
      {st.available === false ? (
        <p className="muted">{tr("admin.engines_comfy_lan_unavailable")}</p>
      ) : (
        <>
          <label className="engines-hf-row">
            <span>{tr("admin.engines_comfy_lan_url")}</span>
            <input
              type="url"
              autoComplete="off"
              value={url}
              placeholder="http://192.168.1.20:8188"
              onChange={(ev) => setUrl(ev.currentTarget.value)}
            />
          </label>
          <label className="engines-hf-row">
            <span>{tr("admin.engines_comfy_lan_key")}</span>
            <input
              type="password"
              autoComplete="off"
              value={key}
              placeholder={st.panel_key_set ? tr("admin.engines_comfy_lan_key_keep") : ""}
              onChange={(ev) => setKey(ev.currentTarget.value)}
            />
          </label>
          <p className="muted" data-testid="comfy-lan-key-state">
            {st.panel_url
              ? (st.panel_key_set ? tr("admin.engines_comfy_lan_key_set") : tr("admin.engines_comfy_lan_key_unset"))
                  .replace("{who}", st.updated_by || "-")
                  .replace("{when}", st.updated_at ? fmtDateTime(st.updated_at) : "-")
              : tr("admin.engines_comfy_lan_not_saved")}
          </p>
          <div className="engines-model-add-actions">
            <Button
              variant="primary"
              small
              disabled={busy || !url.trim()}
              onClick={() => send("PUT", key.trim() ? { url, key } : { url })}
            >
              {tr("admin.engines_comfy_lan_save")}
            </Button>
            {st.panel_key_set && (
              <Button small disabled={busy} onClick={() => send("PUT", { url: st.panel_url, clear_key: true })}>
                {tr("admin.engines_comfy_lan_clear_key")}
              </Button>
            )}
            {st.panel_url && (
              <Button small disabled={busy} onClick={() => send("DELETE")}>
                {tr("admin.engines_comfy_lan_remove")}
              </Button>
            )}
          </div>
          <p className="muted">
            {st.env_url
              ? tr("admin.engines_comfy_lan_env").replace("{url}", st.env_url)
              : tr("admin.engines_comfy_lan_env_none")}
          </p>
          <p className="muted">{tr("admin.engines_comfy_lan_note")}</p>
        </>
      )}
      {err && <p className="form-err">{err}</p>}
    </section>
  );
}
