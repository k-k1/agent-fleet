import { useCallback, useEffect, useState } from "react";
import { api, raw, errText } from "../../../core/api/client.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

interface Grant {
  id: string;
  client_name: string;
  redirect_host: string;
  tenant: string;
  scope: string;
  created_at: number;
  last_used_at: number;
}

export function ConnectedApps() {
  const tr = useT();
  const confirm = useConfirm();
  const toast = useToast();
  const [view, setView] = useState<{ enabled: boolean; grants: Grant[] } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const res = await api("api/mcp-oauth/grants");
      if (res?.error) {
        setError(errText(res.error));
        return;
      }
      setView(res);
      setError("");
    } catch {
      setError(tr("tokens.apps_load_failed"));
    }
  }, [tr]);
  useEffect(() => {
    void load();
  }, [load]);
  const revoke = async (g: Grant) => {
    const ok = await confirm({
      title: tr("tokens.apps_revoke_title"),
      body: tr("tokens.apps_revoke_body", { name: g.client_name || tr("tokens.unnamed") }),
      confirmLabel: tr("tokens.revoke"),
      danger: true,
    });
    if (!ok) return;
    setBusy(g.id);
    try {
      const res = await raw(`api/mcp-oauth/grants/${encodeURIComponent(g.id)}`, { method: "DELETE" });
      if (!res.ok) {
        toast(tr("tokens.apps_revoke_failed"));
        return;
      }
      await load();
    } catch {
      toast(tr("tokens.apps_revoke_failed"));
    } finally {
      setBusy(null);
    }
  };
  if (view && !view.enabled) return null;
  if (!view) return error ? <p role="alert" className="muted">{error}</p> : null;
  const date = (ts: number) => ts ? new Date(ts * 1000).toLocaleDateString() : tr("tokens.apps_never_used");
  return (
    <section>
      <h3>{tr("tokens.apps_title")}</h3>
      <p className="muted ds-note">{tr("tokens.apps_intro")}</p>
      {error && <p role="alert">{error}</p>}
      {view.grants.length === 0 ? <p className="muted">{tr("tokens.apps_empty")}</p> : (
        <div style={{ overflowX: "auto" }}>
          <table className="pat-table">
            <thead><tr>
              <th>{tr("tokens.apps_client")}</th><th>{tr("tokens.apps_redirect")}</th>
              <th>{tr("tokens.apps_tenant")}</th><th>{tr("tokens.scope")}</th>
              <th>{tr("tokens.apps_created")}</th><th>{tr("tokens.th_last_used")}</th><th />
            </tr></thead>
            <tbody>{view.grants.map((g) => <tr key={g.id}>
              <td>{g.client_name || tr("tokens.unnamed")}<small className="muted" style={{ display: "block" }}>{tr("tokens.apps_self_declared")}</small></td>
              <td>{g.redirect_host}</td><td>{g.tenant}</td><td>{g.scope}</td>
              <td>{date(g.created_at)}</td><td>{date(g.last_used_at)}</td>
              <td><button type="button" className="btn-ghost" disabled={busy !== null} onClick={() => revoke(g)}>{tr("tokens.revoke")}</button></td>
            </tr>)}</tbody>
          </table>
        </div>
      )}
    </section>
  );
}
