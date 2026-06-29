import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, raw, rel } from "../api.js";

// TokensTab issues and revokes Personal Access Tokens for the MCP endpoint
// (docs/decisions/0006, P3-6). A token carries the issuer's identity+membership;
// role is resolved live at call time, scope is fixed here and capped at the
// issuer's ceiling. The secret is shown exactly once.
export default function TokensTab() {
  const [tokens, setTokens] = useState(null);
  const [ceiling, setCeiling] = useState("write");
  const [err, setErr] = useState("");
  const [name, setName] = useState("");
  const [scope, setScope] = useState("write");
  const [ttl, setTtl] = useState("90");
  const [issued, setIssued] = useState(null); // the once-shown secret
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    setErr("");
    api("api/pat")
      .then((res) => {
        if (res && res.error) {
          setErr(res.error.message || "取得に失敗しました");
          return;
        }
        setTokens(res.tokens || []);
        if (res.ceiling) setCeiling(res.ceiling);
      })
      .catch(() => setErr("読み込みに失敗しました"));
  }, []);
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
      alert("発行に失敗: " + (res.error.message || ""));
      return;
    }
    setIssued(res);
    setName("");
    load();
  };

  const revoke = async (id) => {
    if (!confirm("このトークンを失効しますか？ 使用中の接続は次回から 401 になります。")) return;
    await raw(`api/pat/${encodeURIComponent(id)}`, { method: "DELETE" });
    load();
  };

  const scopeOptions = ["read", "write"];
  if (ceiling === "admin:dangerous") scopeOptions.push("admin:dangerous");

  const mcpURL = rel("mcp");

  return (
    <div className="display-settings">
      <p className="muted ds-note">
        手元の Claude（Claude Code / Desktop）から MCP で Workspace のセッションを操作するための
        トークンです。発行者の権限を継承し、スコープはここで選びます。
      </p>

      {issued && (
        <div className="pat-issued">
          <div className="pat-issued-head">
            ✅ トークンを発行しました（この画面を閉じると再表示できません）
          </div>
          <code className="pat-secret">{issued.token}</code>
          <div className="pat-issued-actions">
            <button
              type="button"
              onClick={() => navigator.clipboard?.writeText(issued.token)}
            >
              コピー
            </button>
            <button type="button" className="btn-ghost" onClick={() => setIssued(null)}>
              閉じる
            </button>
          </div>
        </div>
      )}

      <div className="pat-form">
        <Row label="名前">
          <input
            type="text"
            value={name}
            placeholder="例: laptop-claude"
            onChange={(e) => setName(e.target.value)}
          />
        </Row>
        <Row label="スコープ">
          <select value={scope} onChange={(e) => setScope(e.target.value)}>
            {scopeOptions.map((s) => (
              <option key={s} value={s}>
                {s === "read"
                  ? "read（閲覧のみ）"
                  : s === "write"
                    ? "write（セッション駆動）"
                    : "admin:dangerous（強権・管理）"}
              </option>
            ))}
          </select>
        </Row>
        <Row label="有効期限">
          <select value={ttl} onChange={(e) => setTtl(e.target.value)}>
            <option value="90">90 日（既定）</option>
            <option value="30">30 日</option>
            <option value="365">365 日</option>
            <option value="-1">無期限</option>
          </select>
        </Row>
        <div className="ds-row">
          <span className="ds-label" />
          <button type="button" onClick={create} disabled={busy}>
            {busy ? "発行中…" : "トークンを発行"}
          </button>
        </div>
      </div>

      <p className="muted ds-note">
        MCP エンドポイント: <code>{mcpURL}</code>
        （Streamable HTTP。クライアントに <code>Authorization: Bearer &lt;token&gt;</code> で設定）
      </p>

      {err && <p className="muted pad">{err}</p>}
      {tokens && tokens.length > 0 && (
        <table className="pat-table">
          <thead>
            <tr>
              <th>名前</th>
              <th>スコープ</th>
              <th>期限</th>
              <th>最終使用</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {tokens.map((t) => (
              <tr key={t.id} className={t.revoked ? "pat-revoked" : ""}>
                <td>{t.name || "(無名)"}</td>
                <td>{t.scope}</td>
                <td>{fmtDate(t.expires_at) || "無期限"}</td>
                <td>{fmtDate(t.last_used_at) || "—"}</td>
                <td>
                  {t.revoked ? (
                    <span className="muted">失効済</span>
                  ) : (
                    <button type="button" className="btn-ghost" onClick={() => revoke(t.id)}>
                      失効
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

function fmtDate(s) {
  if (!s) return "";
  const d = new Date(s);
  if (isNaN(d)) return "";
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

function Row({ label, children }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}
