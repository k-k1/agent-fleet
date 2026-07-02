import { useCallback, useEffect, useState } from "react";
import type { ChangeEvent, ReactNode } from "react";
import { api, raw, rawJSON } from "../api.js";

// SsmTab manages the member's own AWS SSM login config (docs/history/p3-ssm-session.md)
// in two tiers so the form isn't cluttered:
//   プロファイル (共通) = the shared auth bundle (SSO portal + account/role/region);
//                         many hosts reuse one. Maps to a ~/.aws named profile.
//   ホスト (個別)       = per-instance only (alias, instance id, run-as document,
//                         optional region override) + which profile to use.
// NO AWS secrets are stored or entered here — at session start the in-container aws CLI
// runs `aws sso login` (device-code URL surfaced in the terminal) and caches the
// short-lived token in the workspace home.

// postJSON POSTs/PUTs and surfaces failures loudly (a stale CP without the routes
// returns 404 non-JSON, which would otherwise be swallowed). Returns true on success.
async function postJSON(path: string, method: string, body: unknown): Promise<boolean> {
  let res;
  try {
    res = await rawJSON(path, method, body);
  } catch (e: any) {
    alert("通信に失敗しました: " + (e?.message || e));
    return false;
  }
  if (!res.ok) {
    const j = await res.json().catch(() => null);
    alert(
      "保存に失敗: HTTP " +
        res.status +
        (j?.error?.message ? " — " + j.error.message : res.status === 404 ? "（/api/ssm 未提供。CP の再起動が必要かもしれません）" : ""),
    );
    return false;
  }
  return true;
}

// Meta renders one labeled key/value row inside a list card. Empty values show "—".
// `wide` spans the full grid width (for long values like a start URL).
function Meta({ k, v, mono = true, wide = false }: { k: ReactNode; v?: ReactNode; mono?: boolean; wide?: boolean }) {
  return (
    <div className={"ssm-meta-row" + (wide ? " wide" : "")}>
      <span className="ssm-meta-k">{k}</span>
      <span className={"ssm-meta-v" + (mono ? " mono" : "")}>{v || "—"}</span>
    </div>
  );
}

export default function SsmTab() {
  const [profiles, setProfiles] = useState<any[] | null>(null);
  const [hosts, setHosts] = useState<any[] | null>(null);

  const reload = useCallback(() => {
    api("api/ssm/profiles").then((d) => setProfiles(Array.isArray(d) ? d : [])).catch(() => setProfiles([]));
    api("api/ssm/hosts").then((d) => setHosts(Array.isArray(d) ? d : [])).catch(() => setHosts([]));
  }, []);
  useEffect(reload, [reload]);

  return (
    <div className="ssm-tab">
      <p className="field-help">
        自社 AWS の EC2 に SSM Session Manager でログインするための設定です。ここには <b>AWS の秘密情報は保存しません</b>
        （短命の資格情報はセッション起動時に <code>aws sso login</code> でブラウザ認証し、ワークスペース内にのみ保持されます）。
      </p>
      <ProfileSection profiles={profiles} reload={reload} />
      <HostSection hosts={hosts} profiles={profiles} reload={reload} />
    </div>
  );
}

// --- profiles (common) ----------------------------------------------------------

const emptyProfile: Record<string, string> = { label: "", startUrl: "", ssoRegion: "", accountId: "", roleName: "", region: "" };

type FieldEvent = ChangeEvent<HTMLInputElement | HTMLSelectElement>;

function ProfileSection({ profiles, reload }: { profiles: any[] | null; reload: () => void }) {
  const [f, setF] = useState<Record<string, string>>(emptyProfile);
  const [busy, setBusy] = useState(false);
  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));

  const add = async () => {
    if (!f.label.trim() || !/^https:\/\//.test(f.startUrl.trim()) || !f.ssoRegion.trim()) return;
    setBusy(true);
    try {
      const ok = await postJSON("api/ssm/profiles", "POST", {
        label: f.label.trim(),
        startUrl: f.startUrl.trim(),
        ssoRegion: f.ssoRegion.trim(),
        accountId: f.accountId.trim(),
        roleName: f.roleName.trim(),
        region: f.region.trim(),
      });
      if (!ok) return;
      setF(emptyProfile);
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    if (!confirm("このプロファイルを削除しますか？（参照中のホストはログインできなくなります）")) return;
    await raw(`api/ssm/profiles/${encodeURIComponent(id)}`, { method: "DELETE" });
    reload();
  };

  return (
    <section className="ssm-section">
      <div className="conn-cat">プロファイル（共通設定）</div>
      <div className="field-help">
        AWS アクセスポータル（IAM Identity Center）と、その中のアカウント／ロール。複数のホストで使い回します。
        1 つのプロファイル＝1 つの <code>~/.aws</code> プロファイル。
      </div>
      {profiles === null ? (
        <p className="muted pad">読み込み中…</p>
      ) : profiles.length === 0 ? (
        <p className="muted">まだ登録がありません。まずここを 1 つ作成してください。</p>
      ) : (
        <ul className="ssm-list">
          {profiles.map((p) => (
            <li key={p.id} className="ssm-item">
              <div className="ssm-item-head">
                <span className="ssm-alias">{p.label}</span>
                <button className="icon danger" title="削除" onClick={() => remove(p.id)}>✕</button>
              </div>
              <div className="ssm-meta">
                <Meta k="アカウント" v={p.accountId} />
                <Meta k="ロール" v={p.roleName} />
                <Meta k="既定リージョン" v={p.region} />
                <Meta k="SSO リージョン" v={p.ssoRegion} />
                <Meta k="start URL" v={p.startUrl} wide />
              </div>
            </li>
          ))}
        </ul>
      )}
      <div className="ssm-form-grid">
        <input className="cinput" placeholder="ラベル (例 my-profile)" value={f.label} onChange={set("label")} />
        <input className="cinput" placeholder="start URL (https://xxx.awsapps.com/start)" value={f.startUrl} onChange={set("startUrl")} />
        <input className="cinput" placeholder="SSO リージョン (例 ap-northeast-1)" value={f.ssoRegion} onChange={set("ssoRegion")} />
        <input className="cinput" placeholder="既定リージョン（任意）" value={f.region} onChange={set("region")} />
        <input className="cinput" placeholder="アカウント ID（任意）" value={f.accountId} onChange={set("accountId")} />
        <input className="cinput" placeholder="ロール名（任意）" value={f.roleName} onChange={set("roleName")} />
        <button
          disabled={busy || !f.label.trim() || !/^https:\/\//.test(f.startUrl.trim()) || !f.ssoRegion.trim()}
          onClick={add}
        >
          追加
        </button>
      </div>
    </section>
  );
}

// --- hosts (per-instance) -------------------------------------------------------

const emptyHost: Record<string, string> = { alias: "", profileId: "", instanceId: "", documentName: "", region: "" };

function HostSection({ hosts, profiles, reload }: { hosts: any[] | null; profiles: any[] | null; reload: () => void }) {
  const [f, setF] = useState<Record<string, string>>(emptyHost);
  const [busy, setBusy] = useState(false);
  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));
  const profileLabel = (id: string) => (profiles || []).find((p) => p.id === id)?.label || "?";
  const noProfiles = profiles !== null && profiles.length === 0;

  const add = async () => {
    if (!f.alias.trim() || !f.instanceId.trim() || !f.profileId) return;
    setBusy(true);
    try {
      const ok = await postJSON("api/ssm/hosts", "POST", {
        alias: f.alias.trim(),
        profileId: f.profileId,
        instanceId: f.instanceId.trim(),
        documentName: f.documentName.trim(),
        region: f.region.trim(),
      });
      if (!ok) return;
      setF(emptyHost);
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    if (!confirm("このホストを削除しますか？")) return;
    await raw(`api/ssm/hosts/${encodeURIComponent(id)}`, { method: "DELETE" });
    reload();
  };

  return (
    <section className="ssm-section">
      <div className="conn-cat">SSM ホスト（個別）</div>
      <div className="field-help">
        ログイン先の別名 → インスタンス ID・run-as ドキュメント。認証はプロファイルを選ぶだけ。
        <code>aws ssm start-session --target &lt;instance&gt; --document-name &lt;document&gt;</code> に対応します。
      </div>
      {hosts === null ? (
        <p className="muted pad">読み込み中…</p>
      ) : hosts.length === 0 ? (
        <p className="muted">まだ登録がありません。</p>
      ) : (
        <ul className="ssm-list">
          {hosts.map((h) => (
            <li key={h.id} className="ssm-item">
              <div className="ssm-item-head">
                <span className="ssm-alias">{h.alias}</span>
                <button className="icon danger" title="削除" onClick={() => remove(h.id)}>✕</button>
              </div>
              <div className="ssm-meta">
                <Meta k="インスタンス" v={h.instanceId} />
                <Meta k="ドキュメント" v={h.documentName} />
                <Meta k="リージョン" v={h.region} />
                <Meta k="プロファイル" v={profileLabel(h.profileId)} mono={false} />
              </div>
            </li>
          ))}
        </ul>
      )}
      {noProfiles ? (
        <p className="muted">先にプロファイルを 1 つ作成してください。</p>
      ) : (
        <div className="ssm-form-grid">
          <input className="cinput" placeholder="別名 (例 admin@web-01)" value={f.alias} onChange={set("alias")} />
          <input className="cinput" placeholder="インスタンス ID (i-...)" value={f.instanceId} onChange={set("instanceId")} />
          <input className="cinput" placeholder="run-as ドキュメント（任意）" value={f.documentName} onChange={set("documentName")} />
          <input className="cinput" placeholder="リージョン上書き（任意）" value={f.region} onChange={set("region")} />
          <select className="cinput" value={f.profileId} onChange={set("profileId")}>
            <option value="">プロファイルを選択</option>
            {(profiles || []).map((p) => (
              <option key={p.id} value={p.id}>{p.label}</option>
            ))}
          </select>
          <button disabled={busy || !f.alias.trim() || !f.instanceId.trim() || !f.profileId} onClick={add}>
            追加
          </button>
        </div>
      )}
    </section>
  );
}
