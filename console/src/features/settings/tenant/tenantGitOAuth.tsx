// テナントの git プロバイダ OAuth アプリ（docs/log/71・ADR 0052）。
//
// メンバーの「接続」タブに出る GitHub / Bitbucket の **OAuth で接続** ボタンが、
// どの OAuth アプリを叩くかをここで決める。以前はデプロイの env
// （GITHUB_OAUTH_CLIENT_ID / BITBUCKET_OAUTH_KEY・SECRET）だったので、テナントごとに
// 変えられず、登録も運用者頼みだった。アプリが置かれるのは各社の GitHub org /
// Bitbucket ワークスペースなので、持ち主はテナント管理者である。
//
// ★ 承認の段は無い（決定 3）。サインイン方法（tenantLogin）は super_admin の承認が
// 要るが、あれは「誰であるか」を宣言する権限だから。ここは clone するためのアプリで
// identity を増やさないし、redirect_uri は CP 固定・トークンは本人のワークスペースに
// しか渡らない。保存した瞬間に効く。
//
// ★ client_secret は書き込み専用。保存済みの値は二度と返らないので、空のまま保存
// したら「変えない」の意味にする（サーバも同じ契約）。
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText, raw } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

interface GitOAuthApp {
  provider: string;
  client_id?: string;
  has_secret?: boolean;
  needs_secret?: boolean;
  updated_at?: string;
  redirect_uri?: string;
}

const PROVIDER_LABEL: Record<string, string> = { github: "GitHub", bitbucket: "Bitbucket", jira: "Jira" };

// 登録の入口。「client_id をどこで取るのか」が分からないと詰まるので、行き先を出す。
// ★ Bitbucket の OAuth コンシューマはワークスペース配下（/{workspace}/workspace/
// settings/api）で、ワークスペース名はこちらが知らない。当てずっぽうの URL を出すと
// 404 に送ることになるので、手順のドキュメントを指す。
const REGISTER_URL: Record<string, string> = {
  github: "https://github.com/settings/developers",
  bitbucket: "https://support.atlassian.com/bitbucket-cloud/docs/use-oauth-on-bitbucket-cloud/",
  // ⚠️ Jira は Bitbucket と同じ Atlassian でも登録先が別（3LO アプリは Developer
  // Console）。Bitbucket のコンシューマを流用することはできない（docs/log/80 §80.17）。
  jira: "https://developer.atlassian.com/console/myapps/",
};

export function TenantGitOAuthView({ slug }: { slug: string }) {
  const tr = useT();
  const [apps, setApps] = useState<GitOAuthApp[] | null>(null);

  const load = useCallback(async () => {
    try {
      const d = await api(`api/admin/tenants/${encodeURIComponent(slug)}/git-oauth`);
      if (d && !d.error) setApps(d.providers || []);
    } catch {
      /* transient; 直前の値のまま置く */
    }
  }, [slug]);
  useEffect(() => {
    load();
  }, [load]);

  if (!apps) return <p className="muted pad">{tr("common.loading")}</p>;
  return (
    <section className="admin-panel">
      <p className="admin-hint">{tr("tenant.git_oauth_intro")}</p>
      <p className="admin-hint">{tr("tenant.git_oauth_optional")}</p>
      {apps.map((app) => (
        <GitOAuthCard key={app.provider} slug={slug} app={app} onChanged={load} />
      ))}
    </section>
  );
}

function GitOAuthCard({ slug, app, onChanged }: { slug: string; app: GitOAuthApp; onChanged: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [clientID, setClientID] = useState(app.client_id || "");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    setClientID(app.client_id || "");
    setSecret("");
  }, [app.client_id, app.provider]);

  const base = `api/admin/tenants/${encodeURIComponent(slug)}/git-oauth/${encodeURIComponent(app.provider)}`;
  const registered = !!app.client_id;

  const save = async () => {
    setBusy(true);
    try {
      const res = await apiJSON(base, "PUT", { client_id: clientID.trim(), client_secret: secret.trim() });
      if (res?.error) {
        toast(errText(res.error), { kind: "warn" });
        return;
      }
      setSecret("");
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
      onChanged();
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    try {
      await raw(base, { method: "DELETE" });
      onChanged();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="admin-fgroup">
      <h4>
        {PROVIDER_LABEL[app.provider] || app.provider}
        <span className="af-note">{registered ? tr("tenant.git_oauth_on") : tr("tenant.git_oauth_off")}</span>
      </h4>
      <div className="admin-fgrid">
        <label className="admin-fld wide">
          <span className="af-cap">{tr("tenant.git_oauth_client_id")}</span>
          <input type="text" value={clientID} onChange={(e) => setClientID(e.target.value)} />
        </label>
        {/* GitHub は device flow なので secret を持たない。持たない欄を灰色で出すより
            項目ごと作らない（何を入れる欄なのか分からないものを置かない）。 */}
        {app.needs_secret && (
          <label className="admin-fld wide">
            <span className="af-cap">{tr("tenant.git_oauth_client_secret")}</span>
            <input
              type="password"
              placeholder={app.has_secret ? tr("tenant.git_oauth_secret_kept") : ""}
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
            />
            <span className="af-unit">{tr("tenant.git_oauth_secret_unit")}</span>
          </label>
        )}
      </div>
      {/* コールバックは provider 側の登録画面に貼るもの。出さないと詰む。
          ★ コールバックを持つプロバイダなのに URL が空なら PUBLIC_BASE_URL 未設定で、
          直せるのは運用者。ここで言わないと「登録したのに使えない」で止まる。 */}
      {app.needs_secret &&
        (app.redirect_uri ? (
          <p className="admin-hint">
            {tr("tenant.git_oauth_redirect")} <code>{app.redirect_uri}</code>
          </p>
        ) : (
          <p className="admin-hint warn">{tr("tenant.git_oauth_no_base_url")}</p>
        ))}
      {app.provider === "github" && <p className="admin-hint">{tr("tenant.git_oauth_gh_device")}</p>}
      {/* ★ Bitbucket は認可 URL に scope を載せない —— コンシューマの Permissions で
          チェックした物がそのまま渡る。だから「どれを入れるか」はここで言うしかなく、
          Pull requests: Read を後から足した場合はメンバーの再接続が要る
          （既存トークンには古い権限が焼かれている・docs/log/80 §80.19.3）。 */}
      {app.provider === "bitbucket" && <p className="admin-hint">{tr("tenant.git_oauth_bb_scopes")}</p>}
      {/* ★ 3LO アプリは既定で「開発中」＝作成者本人しか認可できない。作った管理者が試すと
          通ってしまうので登録時のテストをすり抜け、他のメンバー全員が Atlassian 側の
          「You don't have access to this app」で止まる —— しかも認可画面より手前なので
          af には何も返ってこず、無言で未接続のままになる。 */}
      {app.provider === "jira" && (
        <>
          <p className="admin-hint">{tr("tenant.git_oauth_jira_access")}</p>
          <p className="admin-hint">{tr("tenant.git_oauth_jira_scopes")}</p>
          <p className="admin-hint">{tr("tenant.git_oauth_jira_sharing")}</p>
        </>
      )}
      <p className="admin-hint">
        {tr("tenant.git_oauth_where")}{" "}
        <a href={REGISTER_URL[app.provider]} target="_blank" rel="noopener noreferrer">
          {REGISTER_URL[app.provider]}
        </a>
      </p>
      <div className="le-actions">
        <button className="primary" disabled={busy || !clientID.trim()} onClick={save}>
          {tr("common.save")}
        </button>
        {registered && (
          <button className="ghost" disabled={busy} onClick={remove}>
            {tr("tenant.git_oauth_remove")}
          </button>
        )}
        {saved && (
          <span className="saved-note">
            <Icon name="check" /> {tr("admin.saved")}
          </span>
        )}
      </div>
    </div>
  );
}
