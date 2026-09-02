import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, raw, rel } from "../../../core/api/client.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { Row } from "../parts/controls.tsx";

// TokensTab issues and revokes Personal Access Tokens for the MCP endpoint
// (docs/decisions/0006, P3-6). A token carries the issuer's identity+membership;
// role is resolved live at call time, scope is fixed here and capped at the
// issuer's ceiling. The secret is shown exactly once.

// A Personal Access Token row from GET /api/pat.
interface PatToken {
  id: string;
  name?: string;
  scope?: string;
  expires_at?: string;
  last_used_at?: string;
  revoked?: boolean;
}

export function TokensTab() {
  const askConfirm = useConfirm();
  const toast = useToast();
  const tr = useT();
  const [tokens, setTokens] = useState<PatToken[] | null>(null);
  const [ceiling, setCeiling] = useState("write");
  const [err, setErr] = useState("");
  const [name, setName] = useState("");
  const [scope, setScope] = useState("write");
  const [ttl, setTtl] = useState("90");
  const [issued, setIssued] = useState<{ token: string } | null>(null); // the once-shown secret
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    setErr("");
    api("api/pat")
      .then((res) => {
        if (res && res.error) {
          setErr(res.error.message || tr("tokens.fetch_failed"));
          return;
        }
        setTokens(res.tokens || []);
        if (res.ceiling) setCeiling(res.ceiling);
      })
      .catch(() => setErr(tr("tokens.load_failed")));
  }, [tr]);
  useEffect(load, [load]);

  const create = async () => {
    setBusy(true);
    const res = await apiJSON("api/pat", "POST", {
      name: name.trim(),
      scope,
      ttl_days: parseInt(ttl, 10),
    });
    setBusy(false);
    if (res && res.error) {
      toast(tr("tokens.issue_failed", { msg: res.error.message || "" }));
      return;
    }
    setIssued(res);
    setName("");
    load();
  };

  const revoke = async (id: string) => {
    const ok = await askConfirm({
      title: tr("tokens.revoke_title"),
      body: tr("tokens.revoke_body"),
      confirmLabel: tr("tokens.revoke_confirm"),
      danger: true,
    });
    if (!ok) return;
    await raw(`api/pat/${encodeURIComponent(id)}`, { method: "DELETE" });
    load();
  };

  const scopeOptions = ["read", "write"];
  if (ceiling === "admin:dangerous") scopeOptions.push("admin:dangerous");

  const mcpURL = rel("mcp");

  // A ready-to-paste .mcp.json for Claude Code (Streamable HTTP server). The PAT
  // already carries the issuer's identity+membership, so no tenant header is needed
  // — just the Bearer. Built only while the once-shown secret is in hand.
  const mcpJson = issued
    ? JSON.stringify(
        { mcpServers: { "agent-fleet": { type: "http", url: mcpURL, headers: { Authorization: `Bearer ${issued.token}` } } } },
        null,
        2,
      )
    : "";

  return (
    <div className="display-settings">
      <p className="muted ds-note">{tr("tokens.intro")}</p>

      {issued && (
        <div className="pat-issued">
          <div className="pat-issued-head">✅ {tr("tokens.issued_head")}</div>
          <code className="pat-secret">{issued.token}</code>
          <div className="pat-issued-actions">
            <button
              type="button"
              onClick={() => navigator.clipboard?.writeText(issued.token)}
            >
              {tr("tokens.copy_token")}
            </button>
            <button type="button" className="btn-ghost" onClick={() => setIssued(null)}>
              {tr("common.close")}
            </button>
          </div>

          <div className="pat-tmpl-head">
            {tr("tokens.mcp_json_head_1")}
            <code>.mcp.json</code>
            {tr("tokens.mcp_json_head_2")}
            <code>agent-fleet</code>
            {tr("tokens.mcp_json_head_3")}
          </div>
          <pre className="pat-tmpl">{mcpJson}</pre>
          <div className="pat-issued-actions">
            <button type="button" onClick={() => navigator.clipboard?.writeText(mcpJson)}>
              {tr("tokens.copy_mcp_json")}
            </button>
          </div>
        </div>
      )}

      <div className="pat-form">
        <Row label={tr("tokens.name")}>
          <input
            type="text"
            value={name}
            placeholder={tr("tokens.name_placeholder")}
            onChange={(e) => setName(e.target.value)}
          />
        </Row>
        <Row label={tr("tokens.scope")}>
          <select value={scope} onChange={(e) => setScope(e.target.value)}>
            {scopeOptions.map((s) => (
              <option key={s} value={s}>
                {s === "read"
                  ? tr("tokens.scope_read")
                  : s === "write"
                    ? tr("tokens.scope_write")
                    : tr("tokens.scope_admin")}
              </option>
            ))}
          </select>
        </Row>
        <Row label={tr("tokens.expiry")}>
          <select value={ttl} onChange={(e) => setTtl(e.target.value)}>
            <option value="90">{tr("tokens.ttl_90")}</option>
            <option value="30">{tr("tokens.ttl_30")}</option>
            <option value="365">{tr("tokens.ttl_365")}</option>
            <option value="-1">{tr("tokens.ttl_never")}</option>
          </select>
        </Row>
        <div className="ds-row">
          <span className="ds-label" />
          <button type="button" onClick={create} disabled={busy}>
            {busy ? tr("tokens.issuing") : tr("tokens.issue")}
          </button>
        </div>
      </div>

      <p className="muted ds-note">
        {tr("tokens.mcp_endpoint_pre")}
        <code>{mcpURL}</code>
        {tr("tokens.mcp_endpoint_mid1")}
        <code>Authorization: Bearer &lt;token&gt;</code>
        {tr("tokens.mcp_endpoint_mid2")}
      </p>

      {err && <p className="muted pad">{err}</p>}
      {tokens && tokens.length > 0 && (
        <table className="pat-table">
          <thead>
            <tr>
              <th>{tr("tokens.name")}</th>
              <th>{tr("tokens.scope")}</th>
              <th>{tr("tokens.th_expiry")}</th>
              <th>{tr("tokens.th_last_used")}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {tokens.map((t) => (
              <tr key={t.id} className={t.revoked ? "pat-revoked" : ""}>
                <td>{t.name || tr("tokens.unnamed")}</td>
                <td>{t.scope}</td>
                <td>{fmtDate(t.expires_at) || tr("tokens.ttl_never")}</td>
                <td>{fmtDate(t.last_used_at) || "—"}</td>
                <td>
                  {t.revoked ? (
                    <span className="muted">{tr("tokens.revoked")}</span>
                  ) : (
                    <button type="button" className="btn-ghost" onClick={() => revoke(t.id)}>
                      {tr("tokens.revoke")}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function fmtDate(s: string | undefined): string {
  if (!s) return "";
  const d = new Date(s);
  if (isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

