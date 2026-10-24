// The trusted client hosts of the MCP OAuth authorization server (control-plane/mcp_oauth_trusted.go,
// ADR 0118): deployment-wide, super admin only.
//
// The server's answer is the only source of what is shown. A saved empty list ("none", loopback
// clients only) and nothing saved are different states and the screen says which one is in
// effect; "reset" deletes the saved value so the environment variable, then the built-in
// default, applies again.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

interface TrustedView {
  hosts: string[];
  source: "admin" | "env" | "default";
  fallback: string[];
  fallback_source: "env" | "default";
  served: boolean;
  unreadable?: boolean;
}

const SOURCE_KEY = {
  admin: "admin.mcptrust_source_admin",
  env: "admin.mcptrust_source_env",
  default: "admin.mcptrust_source_default",
} as const;
const FALLBACK_KEY = {
  env: "admin.mcptrust_fallback_env",
  default: "admin.mcptrust_fallback_default",
} as const;

const PATH = "api/admin/mcp-oauth/trusted-hosts";

export function McpTrustedHostsView() {
  const tr = useT();
  const [data, setData] = useState<TrustedView | null>(null);
  const [text, setText] = useState("");
  const [err, setErr] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  const show = (d: TrustedView) => {
    setData(d);
    setText(d.hosts.join("\n"));
  };
  const load = useCallback(async () => {
    try {
      const d = await api(PATH);
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      show(d as TrustedView);
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);

  const send = async (method: "PUT" | "DELETE", body?: unknown) => {
    setBusy(true);
    setSaved(false);
    try {
      const d = await apiJSON(PATH, method, body);
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      show(d as TrustedView);
      setSaved(true);
    } catch {
      setErr(tr("admin.load_error"));
    } finally {
      setBusy(false);
    }
  };
  // Commas are accepted too, so a pasted env-var value works.
  const hostsFromText = () =>
    text
      .split(/[\s,]+/)
      .map((h) => h.trim())
      .filter(Boolean);

  const fallbackLabel = data
    ? data.fallback.length
      ? data.fallback.join(", ")
      : tr("admin.mcptrust_fallback_none")
    : "";
  return (
    <div className="admin-stage">
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.mcptrust_title")}</span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        <p className="muted">{tr("admin.mcptrust_note")}</p>
        {data?.unreadable && <p className="form-err">{tr("admin.mcptrust_unreadable")}</p>}
        {data && !data.served && <p className="form-err">{tr("admin.mcptrust_not_served")}</p>}
        <label className="brand-field">
          {tr("admin.mcptrust_hosts")}
          <textarea
            id="mcptrust-hosts"
            className="mono"
            rows={6}
            spellCheck={false}
            value={text}
            disabled={busy || !data}
            onChange={(e) => {
              setText(e.target.value);
              setSaved(false);
            }}
          />
        </label>
        {data && <p className="muted">{tr(SOURCE_KEY[data.source])}</p>}
        {data && data.hosts.length === 0 && <p className="muted">{tr("admin.mcptrust_none")}</p>}
        <div className="usage-toolbar">
          <button
            type="button"
            className="primary"
            disabled={busy || !data}
            onClick={() => send("PUT", { hosts: hostsFromText() })}
          >
            {tr("common.save")}
          </button>
          <button
            type="button"
            className="ghost"
            disabled={busy || !data || data.source !== "admin"}
            onClick={() => send("DELETE")}
          >
            {tr("admin.mcptrust_reset")}
          </button>
        </div>
        {data && data.source === "admin" && (
          <p className="muted">
            {tr("admin.mcptrust_reset_note", {
              hosts: fallbackLabel,
              source: tr(FALLBACK_KEY[data.fallback_source]),
            })}
          </p>
        )}
        {saved && <p className="muted">{tr("admin.mcptrust_saved")}</p>}
        {err && <p className="form-err">{err}</p>}
      </section>
    </div>
  );
}
