import { useCallback, useEffect, useRef, useState } from "react";
import type { ChangeEvent, ReactNode, RefObject } from "react";
import { api, raw, rawJSON } from "../api.js";
import Icon from "../components/Icon.jsx";
import { useConfirm } from "../components/ConfirmProvider.jsx";
import { useToast } from "../components/ToastProvider.jsx";
import { useSettings, setSetting } from "../lib/settings.js";
import { SSM_HOST_COLORS, hostColorBase, termBackground } from "../lib/termcolor.js";

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
async function postJSON(path: string, method: string, body: unknown, toast: (msg: string) => void): Promise<boolean> {
  let res;
  try {
    res = await rawJSON(path, method, body);
  } catch (e: any) {
    toast("通信に失敗しました: " + (e?.message || e));
    return false;
  }
  if (!res.ok) {
    const j = await res.json().catch(() => null);
    toast(
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

// FieldGroup / Field build the labeled add-form: a bordered group holding fields that
// each carry a label, an optional required * marker, and a hint (取得元・形式). Required
// vs optional is conveyed per-field (the * marker + hint), so one group suffices — no
// separate 必須 / 詳細 boxes. `title` is optional (omitted for the single merged group).
function FieldGroup({ title, optional, children }: { title?: ReactNode; optional?: ReactNode; children: ReactNode }) {
  return (
    <div className="ssm-fgroup">
      {title && (
        <div className="ssm-fg-title">
          {title}
          {optional && <span className="opt"> {optional}</span>}
        </div>
      )}
      <div className="ssm-fgrid">{children}</div>
    </div>
  );
}

function Field({
  label,
  req,
  hint,
  wide,
  children,
}: {
  label: ReactNode;
  req?: boolean;
  hint?: ReactNode;
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <div className={"ssm-fld" + (wide ? " wide" : "")}>
      <label>
        {label}
        {req && <span className="req">*</span>}
      </label>
      {children}
      {hint && <div className="hint">{hint}</div>}
    </div>
  );
}

export default function SsmTab() {
  const [profiles, setProfiles] = useState<any[] | null>(null);
  const [hosts, setHosts] = useState<any[] | null>(null);
  const profileLabelRef = useRef<HTMLInputElement>(null);
  // The profile add-form open state is lifted here so the host form's
  // "プロファイルを追加" CTA can expand it (and scroll to it) when none exists yet.
  const [profileOpen, setProfileOpen] = useState(false);

  const reload = useCallback(() => {
    api("api/ssm/profiles").then((d) => setProfiles(Array.isArray(d) ? d : [])).catch(() => setProfiles([]));
    api("api/ssm/hosts").then((d) => setHosts(Array.isArray(d) ? d : [])).catch(() => setHosts([]));
  }, []);
  useEffect(reload, [reload]);

  const focusProfile = () => {
    setProfileOpen(true);
    requestAnimationFrame(() => profileLabelRef.current?.scrollIntoView({ block: "center" }));
  };

  return (
    <div className="ssm-tab">
      <p className="field-help">
        自社 AWS の EC2 に SSM Session Manager でログインするための設定です。ここには <b>AWS の秘密情報は保存しません</b>
        （短命の資格情報はセッション起動時に <code>aws sso login</code> でブラウザ認証し、ワークスペース内にのみ保持されます）。
      </p>
      <ProfileSection
        profiles={profiles}
        reload={reload}
        labelRef={profileLabelRef}
        open={profileOpen}
        setOpen={setProfileOpen}
      />
      <HostSection hosts={hosts} profiles={profiles} reload={reload} onNeedProfile={focusProfile} />
    </div>
  );
}

// --- profiles (common) ----------------------------------------------------------

const emptyProfile: Record<string, string> = { label: "", startUrl: "", ssoRegion: "", accountId: "", roleName: "", region: "" };

type FieldEvent = ChangeEvent<HTMLInputElement | HTMLSelectElement>;

function ProfileSection({
  profiles,
  reload,
  labelRef,
  open,
  setOpen,
}: {
  profiles: any[] | null;
  reload: () => void;
  labelRef: RefObject<HTMLInputElement | null>;
  open: boolean;
  setOpen: (v: boolean) => void;
}) {
  const askConfirm = useConfirm();
  const toast = useToast();
  const [f, setF] = useState<Record<string, string>>(emptyProfile);
  const [busy, setBusy] = useState(false);
  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));
  const valid = f.label.trim() && /^https:\/\//.test(f.startUrl.trim()) && f.ssoRegion.trim();

  const add = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const ok = await postJSON("api/ssm/profiles", "POST", {
        label: f.label.trim(),
        startUrl: f.startUrl.trim(),
        ssoRegion: f.ssoRegion.trim(),
        accountId: f.accountId.trim(),
        roleName: f.roleName.trim(),
        region: f.region.trim(),
      }, toast);
      if (!ok) return;
      setF(emptyProfile);
      setOpen(false);
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    const ok = await askConfirm({
      title: "プロファイルを削除",
      body: "このプロファイルを削除します。参照中のホストはログインできなくなります。",
      confirmLabel: "削除する",
      danger: true,
    });
    if (!ok) return;
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
                <button className="ghost danger ssm-del" title="削除" onClick={() => remove(p.id)}>
                  削除
                </button>
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
      {open ? (
        <div className="ssm-frm">
          <FieldGroup>
            <Field label="ラベル" req hint="一覧・ホストの選択に出る表示名。">
              <input
                ref={labelRef}
                className="cinput"
                placeholder="my-profile"
                value={f.label}
                onChange={set("label")}
                autoFocus
              />
            </Field>
            <Field label="SSO リージョン" req hint="アクセスポータルのリージョン。">
              <input className="cinput" placeholder="ap-northeast-1" value={f.ssoRegion} onChange={set("ssoRegion")} />
            </Field>
            <Field
              label="start URL"
              req
              wide
              hint={
                <>
                  IAM Identity Center のアクセスポータル URL（<code>https://…awsapps.com/start</code>）。
                </>
              }
            >
              <input
                className="cinput"
                placeholder="https://my-company.awsapps.com/start"
                value={f.startUrl}
                onChange={set("startUrl")}
              />
            </Field>
            <Field label="アカウント ID" hint="任意。未入力ならログイン時に選択。">
              <input className="cinput" placeholder="123456789012" value={f.accountId} onChange={set("accountId")} />
            </Field>
            <Field label="ロール名" hint="任意。未入力ならログイン時に選択。">
              <input className="cinput" placeholder="AdministratorAccess" value={f.roleName} onChange={set("roleName")} />
            </Field>
            <Field label="既定リージョン" hint="任意。セッションを開くリージョン。">
              <input className="cinput" placeholder="ap-northeast-1" value={f.region} onChange={set("region")} />
            </Field>
          </FieldGroup>
          <div className="ssm-frm-foot">
            <button className="primary" disabled={busy || !valid} onClick={add}>
              プロファイルを追加
            </button>
            <button
              className="ghost"
              onClick={() => {
                setOpen(false);
                setF(emptyProfile);
              }}
            >
              キャンセル
            </button>
            <span className="req-note">
              <b>*</b> は必須
            </span>
          </div>
        </div>
      ) : (
        <button className="ghost ssm-add-toggle" onClick={() => setOpen(true)}>
          <Icon name="add" /> プロファイルを追加
        </button>
      )}
    </section>
  );
}

// --- hosts (per-instance) -------------------------------------------------------

// HostColorPicker chooses the terminal background hue for a host's sessions. The
// choice is a per-user setting (synced), applied to a session's terminal when it is
// created (new sessions to this host pick it up). Swatches show the vivid hue; the
// terminal itself renders a subtle dark tint of it.
function HostColorPicker({ hostId }: { hostId: string }) {
  const settings = useSettings();
  const cur = settings.ssmHostColors?.[hostId] || "auto";
  const setColor = (id: string) =>
    setSetting("ssmHostColors", { ...(settings.ssmHostColors || {}), [hostId]: id });
  return (
    <div className="ssm-host-color">
      <span className="ssm-meta-k">端末の色</span>
      <div className="ssm-swatches">
        {SSM_HOST_COLORS.map((c) => {
          const base = c.base || hostColorBase("auto", hostId); // vivid identity color
          return (
            <button
              key={c.id}
              type="button"
              className={"ssm-swatch" + (cur === c.id ? " active" : "")}
              title={c.id === "auto" ? "自動（ホスト名から決定）" : c.label}
              style={{ background: base }}
              onClick={() => setColor(c.id)}
            >
              {c.id === "auto" ? "A" : ""}
            </button>
          );
        })}
        <span className="ssm-swatch-preview" title="端末での見え方" style={{ background: termBackground("ssm", hostColorBase(cur, hostId)) }}>
          端末
        </span>
      </div>
    </div>
  );
}

const emptyHost: Record<string, string> = { alias: "", profileId: "", instanceId: "", documentName: "", region: "" };

function HostSection({
  hosts,
  profiles,
  reload,
  onNeedProfile,
}: {
  hosts: any[] | null;
  profiles: any[] | null;
  reload: () => void;
  onNeedProfile: () => void;
}) {
  const askConfirm = useConfirm();
  const toast = useToast();
  const [f, setF] = useState<Record<string, string>>(emptyHost);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  const set = (k: string) => (e: FieldEvent) => setF((p) => ({ ...p, [k]: e.target.value }));
  const profileLabel = (id: string) => (profiles || []).find((p) => p.id === id)?.label || "?";
  const noProfiles = profiles !== null && profiles.length === 0;
  const valid = f.alias.trim() && f.instanceId.trim() && f.profileId;

  const add = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const ok = await postJSON("api/ssm/hosts", "POST", {
        alias: f.alias.trim(),
        profileId: f.profileId,
        instanceId: f.instanceId.trim(),
        documentName: f.documentName.trim(),
        region: f.region.trim(),
      }, toast);
      if (!ok) return;
      setF(emptyHost);
      setOpen(false);
      reload();
    } finally {
      setBusy(false);
    }
  };
  const remove = async (id: string) => {
    const ok = await askConfirm({
      title: "ホストを削除",
      body: "このホストを削除します。",
      confirmLabel: "削除する",
      danger: true,
    });
    if (!ok) return;
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
                <button className="ghost danger ssm-del" title="削除" onClick={() => remove(h.id)}>
                  削除
                </button>
              </div>
              <div className="ssm-meta">
                <Meta k="インスタンス" v={h.instanceId} />
                <Meta k="ドキュメント" v={h.documentName} />
                <Meta k="リージョン" v={h.region} />
                <Meta k="プロファイル" v={profileLabel(h.profileId)} mono={false} />
              </div>
              <HostColorPicker hostId={h.id} />
            </li>
          ))}
        </ul>
      )}
      {noProfiles ? (
        <div className="ssm-dep">
          <span className="i">
            <Icon name="info" />
          </span>
          <span className="t">ホストの登録には共通プロファイルが必要です。まず 1 つ作成してください。</span>
          <button className="primary" onClick={onNeedProfile}>
            プロファイルを追加
          </button>
        </div>
      ) : open ? (
        <div className="ssm-frm">
          <FieldGroup>
            <Field label="使用するプロファイル" req wide hint="このホストの認証に使う共通プロファイル。まず選びます。">
              <select className="cinput" value={f.profileId} onChange={set("profileId")} autoFocus>
                <option value="">プロファイルを選択</option>
                {(profiles || []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.label}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="別名" req hint="起動メニューに出るログイン先の名前。">
              <input className="cinput" placeholder="admin@web-01" value={f.alias} onChange={set("alias")} />
            </Field>
            <Field label="インスタンス ID" req hint={<>EC2 コンソール / <code>aws ec2 describe-instances</code>。</>}>
              <input className="cinput" placeholder="i-0123456789abcdef0" value={f.instanceId} onChange={set("instanceId")} />
            </Field>
            <Field label="run-as ドキュメント" hint="任意。既定でよければ空のまま。">
              <input className="cinput" placeholder="SSM-SessionManagerRunShell" value={f.documentName} onChange={set("documentName")} />
            </Field>
            <Field label="リージョン" hint="任意。プロファイル既定を上書きしたい時だけ。">
              <input className="cinput" placeholder="プロファイル既定を使用" value={f.region} onChange={set("region")} />
            </Field>
          </FieldGroup>
          <div className="ssm-frm-foot">
            <button className="primary" disabled={busy || !valid} onClick={add}>
              ホストを追加
            </button>
            <button
              className="ghost"
              onClick={() => {
                setOpen(false);
                setF(emptyHost);
              }}
            >
              キャンセル
            </button>
            <span className="req-note">
              <b>*</b> は必須
            </span>
          </div>
        </div>
      ) : (
        <button className="ghost ssm-add-toggle" onClick={() => setOpen(true)}>
          <Icon name="add" /> ホストを追加
        </button>
      )}
    </section>
  );
}
