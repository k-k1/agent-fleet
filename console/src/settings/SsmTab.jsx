import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, raw } from "../api.js";

// SsmTab manages the member's own AWS SSM login config (docs/history/p3-ssm-session.md):
// SSO sessions (the access-portal start URL + region) and SSM host bookmarks (which
// instance, run-as document, account/role). Personal scope. NO AWS secrets are stored
// or entered here — at session start the in-container aws CLI runs `aws sso login`
// (device-code URL surfaced in the terminal) and caches the short-lived token in the
// workspace home. These records are just the non-secret coordinates.
export default function SsmTab() {
  const [ssos, setSsos] = useState(null);
  const [hosts, setHosts] = useState(null);

  const reload = useCallback(() => {
    api("api/ssm/sso-sessions").then((d) => setSsos(Array.isArray(d) ? d : [])).catch(() => setSsos([]));
    api("api/ssm/hosts").then((d) => setHosts(Array.isArray(d) ? d : [])).catch(() => setHosts([]));
  }, []);
  useEffect(reload, [reload]);

  return (
    <div className="ssm-tab">
      <p className="field-help">
        自社 AWS の EC2 に SSM Session Manager でログインするための設定です。ここには <b>AWS の秘密情報は保存しません</b>
        （短命の資格情報はセッション起動時に <code>aws sso login</code> でブラウザ認証し、ワークスペース内にのみ保持されます）。
      </p>
      <SsoSection ssos={ssos} reload={reload} />
      <HostSection hosts={hosts} ssos={ssos} reload={reload} />
    </div>
  );
}

// --- SSO sessions ---------------------------------------------------------------

function SsoSection({ ssos, reload }) {
  const [label, setLabel] = useState("");
  const [startUrl, setStartUrl] = useState("");
  const [ssoRegion, setSsoRegion] = useState("");
  const [busy, setBusy] = useState(false);

  const add = async () => {
    if (!/^https:\/\//.test(startUrl.trim()) || !ssoRegion.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/ssm/sso-sessions", "POST", {
        label: label.trim(),
        startUrl: startUrl.trim(),
        ssoRegion: ssoRegion.trim(),
      });
      if (res && res.error) {
        alert("保存に失敗: " + (res.error.message || res.error));
        return;
      }
      setLabel("");
      setStartUrl("");
      setSsoRegion("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id) => {
    if (!confirm("この SSO セッションを削除しますか？")) return;
    await raw(`api/ssm/sso-sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
    reload();
  };

  return (
    <section className="ssm-section">
      <div className="conn-cat">SSO セッション</div>
      <div className="field-help">
        AWS アクセスポータル（IAM Identity Center）の開始 URL とリージョン。1 つの SSO で配下の複数アカウントを賄えます。
      </div>
      {ssos === null ? (
        <p className="muted pad">読み込み中…</p>
      ) : ssos.length === 0 ? (
        <p className="muted">まだ登録がありません。</p>
      ) : (
        <ul className="ssm-list">
          {ssos.map((s) => (
            <li key={s.id}>
              <span className="ssm-alias">{s.label || s.startUrl}</span>
              <span className="muted">{s.startUrl} · {s.ssoRegion}</span>
              <button className="icon danger" title="削除" onClick={() => remove(s.id)}>✕</button>
            </li>
          ))}
        </ul>
      )}
      <div className="flow ssm-form">
        <input className="cinput" placeholder="ラベル（任意）" value={label} onChange={(e) => setLabel(e.target.value)} />
        <input
          className="cinput"
          placeholder="start URL (https://xxx.awsapps.com/start)"
          value={startUrl}
          onChange={(e) => setStartUrl(e.target.value)}
        />
        <input
          className="cinput"
          placeholder="SSO リージョン (例 ap-northeast-1)"
          value={ssoRegion}
          onChange={(e) => setSsoRegion(e.target.value)}
        />
        <button disabled={busy || !/^https:\/\//.test(startUrl.trim()) || !ssoRegion.trim()} onClick={add}>
          追加
        </button>
      </div>
    </section>
  );
}

// --- SSM hosts ------------------------------------------------------------------

const emptyHost = { alias: "", ssoSessionId: "", accountId: "", roleName: "", region: "", instanceId: "", documentName: "" };

function HostSection({ hosts, ssos, reload }) {
  const [f, setF] = useState(emptyHost);
  const [busy, setBusy] = useState(false);
  const set = (k) => (e) => setF((p) => ({ ...p, [k]: e.target.value }));

  const add = async () => {
    if (!f.alias.trim() || !f.instanceId.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/ssm/hosts", "POST", {
        alias: f.alias.trim(),
        ssoSessionId: f.ssoSessionId,
        accountId: f.accountId.trim(),
        roleName: f.roleName.trim(),
        region: f.region.trim(),
        instanceId: f.instanceId.trim(),
        documentName: f.documentName.trim(),
      });
      if (res && res.error) {
        alert("保存に失敗: " + (res.error.message || res.error));
        return;
      }
      setF(emptyHost);
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id) => {
    if (!confirm("このホストを削除しますか？")) return;
    await raw(`api/ssm/hosts/${encodeURIComponent(id)}`, { method: "DELETE" });
    reload();
  };

  return (
    <section className="ssm-section">
      <div className="conn-cat">SSM ホスト</div>
      <div className="field-help">
        ログイン先の別名 → インスタンス ID・run-as ドキュメント・アカウント/ロールの対応。
        <code>aws ssm start-session --target &lt;instance&gt; --document-name &lt;document&gt;</code> に対応します。
      </div>
      {hosts === null ? (
        <p className="muted pad">読み込み中…</p>
      ) : hosts.length === 0 ? (
        <p className="muted">まだ登録がありません。</p>
      ) : (
        <ul className="ssm-list">
          {hosts.map((h) => (
            <li key={h.id}>
              <span className="ssm-alias">{h.alias}</span>
              <span className="muted">
                {h.instanceId}
                {h.documentName ? ` · ${h.documentName}` : ""}
                {h.region ? ` · ${h.region}` : ""}
                {h.accountId ? ` · acct ${h.accountId}` : ""}
              </span>
              <button className="icon danger" title="削除" onClick={() => remove(h.id)}>✕</button>
            </li>
          ))}
        </ul>
      )}
      <div className="ssm-form-grid">
        <input className="cinput" placeholder="別名 (例 mng@g3prod-mon01)" value={f.alias} onChange={set("alias")} />
        <input className="cinput" placeholder="インスタンス ID (i-...)" value={f.instanceId} onChange={set("instanceId")} />
        <input className="cinput" placeholder="run-as ドキュメント（任意）" value={f.documentName} onChange={set("documentName")} />
        <input className="cinput" placeholder="リージョン（任意）" value={f.region} onChange={set("region")} />
        <input className="cinput" placeholder="アカウント ID（任意）" value={f.accountId} onChange={set("accountId")} />
        <input className="cinput" placeholder="ロール名（任意）" value={f.roleName} onChange={set("roleName")} />
        <select className="cinput" value={f.ssoSessionId} onChange={set("ssoSessionId")}>
          <option value="">SSO セッション（任意）</option>
          {(ssos || []).map((s) => (
            <option key={s.id} value={s.id}>{s.label || s.startUrl}</option>
          ))}
        </select>
        <button disabled={busy || !f.alias.trim() || !f.instanceId.trim()} onClick={add}>
          追加
        </button>
      </div>
    </section>
  );
}
