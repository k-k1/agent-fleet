// テナントのログイン面（docs/61 §61.9 / §61.11・ADR0043 決定 19 / 29-33）。
//
// AdminTab.tsx から切り出した。P3/P4 では「IA 刷新のときにまとめて移す」と決めて
// 管理モーダルの中に暫定で置いていたが、置き場が 2 つ（デプロイ管理者の管理モーダル /
// テナント管理者のテナント設定モーダル）に分かれたため、両方から同じ実装を差せる
// 場所が要る。読み手ごとの出し分けは props（isSuper / 読み取り専用）だけで、
// ★ 権限そのものは常にサーバ側が持つ:
//   - ログイン規則の PUT は withSuperAdmin 固定（決定 19）
//   - サインイン方法の「承認して有効化」は CP の setStatus が super_admin を見る（決定 30）
// UI の出し分けは案内であって、権限の実装ではない。
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ConfirmDialog } from "../../ui/ConfirmDialog.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { fmtDateTime, DATETIME_FULL } from "../../lib/intl.ts";
import { useT, useLocale } from "../../lib/i18n/index.ts";
// メンバー側の MCP フォームと同じ部品立て（1 つのデザインに保つ）。
import { Field } from "./mcpForm.tsx";

// テナント行のうち、この画面が読む 3 列だけ（docs/61 §61.9.7）。管理 API の
// テナント表現の部分集合なので、呼び出し側の型をそのまま渡せる。
export interface TenantLoginFields {
  allowed_providers?: string;
  auto_join_domains?: string;
  allowed_domains?: string;
  // 受け入れるが、このテナントのログイン画面には出さない方式（docs/61 §61.15.9）。
  // ★ 表示だけの欄で、門ではない。
  hidden_providers?: string;
}

// テナントが定義したサインイン方法（docs/61 §61.11）。client_secret は書き込み専用 —
// レスポンスには決して載らず、保存済みかどうかは has_secret で分かる。
export interface TenantIdP {
  id: string;
  name: string;
  label_ja?: string;
  label_en?: string;
  // kind は「どのアダプタで動くか」＝出す欄そのものを変える（docs/61 §61.15）。
  // 既定は oidc（P4 の行はこの列より古い）。
  kind?: string;
  issuer: string;
  client_id: string;
  client_secret?: string;
  trust: string;
  allowed_tids?: string;
  allowed_domains?: string;
  allowed_orgs?: string;
  // 規則 1.5 を当てるための安定クレーム名（docs/61 §61.15.10）。値ではなく「名前」だけ
  // が設定で、値は必ずトークンから読む。書けるのは CP が許した名前だけ。
  link_claim?: string;
  provider_id?: string;
  tenant_slug?: string;
  status?: string;
  has_secret?: boolean;
  approved_by?: string;
  approved_at?: string;
  usable?: boolean;
}

// このデプロイが env で有効にしているサインイン方法（GET /api/admin/providers）。
// 秘密は載らない — id・表示名・issuer だけ。
interface DeployProvider {
  id: string;
  label_ja?: string;
  label_en?: string;
  issuer?: string;
}

// DeploymentSignInMethods — 「使えるサインイン方法」欄に何が書けるかの一覧。
//
// ★ この欄は自由入力で、書けるものはデプロイの env（AF_OIDC_PROVIDERS ほか）にしか
// 無い。今までは画面から知る手段が無く、間違えると保存が 400 unknown_provider で
// 弾かれるだけだった — 弾かれた人が次に何を打てばいいかは、やはり画面のどこにも
// 書いていない。だから欄のすぐ下に置く。
//
// ★ 出すのは表示名（ログイン画面のボタンと同じ文言）を主にし、id は打ち込む値なので
// <code> で添える。provider id はドメイン概念ではなく技術識別子で、それを主役にすると
// 「entra とは何か」を別の場所で聞くことになる。
//
// ★ テナント自身の方法（t:<slug>:<name>）はここには出ない。あれは実行時に増減し、
// 全部並べるとグループ会社の名簿になる（決定 32-4）。自テナントの分は下の
// 「このテナントのサインイン方法」に出ているので、ヒントでそちらを指す。
function DeploymentSignInMethods() {
  const tr = useT();
  const locale = useLocale();
  // ★ 3 状態を分ける。以前は `res?.providers || []` で**エラーも 0 件に潰していた**ので、
  // 読めなかった相手に「設定されていません」と嘘を表示していた（docs/61 §61.17.9 ②）。
  // P7 で読める相手が super_admin から各テナントの管理者へ広がるまでは、この部品が
  // super_admin の編集フォームの中にしか無かったため表に出ていなかっただけ。
  // null=読み込み中 / "error"=読めなかった / 配列=読めた（空配列は本当に 0 件）。
  const [rows, setRows] = useState<DeployProvider[] | "error" | null>(null);

  useEffect(() => {
    let live = true;
    api("api/admin/providers")
      .then((res) => {
        if (!live) return;
        setRows(Array.isArray(res?.providers) ? res.providers : "error");
      })
      // 通信断は reject で来る（api() が合成するのは非 JSON 応答のときだけ）。
      .catch(() => {
        if (live) setRows("error");
      });
    return () => {
      live = false;
    };
  }, []);

  // 読み込み中は何も出さない（編集フォームの下で行が生えるより静かなほうがよい）。
  if (rows === null) return null;
  const label = (p: DeployProvider) => (locale === "en" ? p.label_en : p.label_ja) || p.label_ja || p.label_en || p.id;
  return (
    <div className="idp-known">
      <span className="af-cap">{tr("admin.providers_title")}</span>
      {rows === "error" ? (
        <p className="admin-hint">{tr("admin.providers_unreadable")}</p>
      ) : rows.length === 0 ? (
        <p className="admin-hint">{tr("admin.providers_none")}</p>
      ) : (
        rows.map((p) => (
          <div key={p.id} className="adm-mcp-row">
            <span className="as-name">{label(p)}</span>
            <code>{p.id}</code>
            {/* issuer は super_admin にしか返らない（§61.17.9 ①）。無いときは列ごと出さない
                — 空セルは設定漏れに見える。 */}
            {p.issuer && <span className="as-repo muted">{p.issuer}</span>}
          </div>
        ))
      )}
      {rows !== "error" && <p className="admin-hint">{tr("admin.providers_hint")}</p>}
    </div>
  );
}

// TenantLoginRules — docs/61 §61.9.7 の CSV 3 列のエディタ。
//
// 3 つはわざと似せていないし、ヒントもそう書いてある。高くつく誤読は
// allowed_domains を「このテナントを使ってよい人」と読むこと。あれは「招待して
// よい人」の上限でしかない。使い続けられるかは在籍（メンバーシップ）の話で、
// ドメインをリクエスト毎の条件にすると、意図して招いた業務委託の人を締め出す
// （§61.9.5）。
export function TenantLoginRules({
  slug,
  tenant,
  onChanged,
}: {
  slug: string;
  tenant: TenantLoginFields | null | undefined;
  onChanged: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [providers, setProviders] = useState(tenant?.allowed_providers || "");
  const [autoJoin, setAutoJoin] = useState(tenant?.auto_join_domains || "");
  const [domains, setDomains] = useState(tenant?.allowed_domains || "");
  const [hidden, setHidden] = useState(tenant?.hidden_providers || "");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setProviders(tenant?.allowed_providers || "");
    setAutoJoin(tenant?.auto_join_domains || "");
    setDomains(tenant?.allowed_domains || "");
    setHidden(tenant?.hidden_providers || "");
  }, [slug, tenant]);

  const save = async () => {
    const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/login`, "PUT", {
      allowed_providers: providers.trim(),
      auto_join_domains: autoJoin.trim(),
      allowed_domains: domains.trim(),
      hidden_providers: hidden.trim(),
    });
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    setSaved(true);
    setTimeout(() => setSaved(false), 1500);
    onChanged();
  };

  // テナント管理者が新しい同僚に渡す URL（§61.10.4）。通知経路が無いので人が渡す —
  // これは意図的（決定 28: Control Plane に SMTP を持たない）。
  const loginURL = new URL("login/" + encodeURIComponent(slug), document.baseURI).toString();

  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>{tr("admin.login_rules")}<span className="af-note">{tr("admin.login_rules_note")}</span></h4>
        <div className="admin-fgrid">
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.allowed_providers")}</span>
            <input type="text" placeholder="entra, google" value={providers} onChange={(e) => setProviders(e.target.value)} />
            <span className="af-unit">{tr("admin.allowed_providers_unit")}</span>
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.auto_join_domains")}</span>
            <input type="text" placeholder="@sales.acme.co.jp" value={autoJoin} onChange={(e) => setAutoJoin(e.target.value)} />
            <span className="af-unit">{tr("admin.auto_join_domains_unit")}</span>
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.invite_domains")}</span>
            <input type="text" placeholder="@acme.co.jp" value={domains} onChange={(e) => setDomains(e.target.value)} />
            <span className="af-unit">{tr("admin.invite_domains_unit")}</span>
          </label>
          {/* ★ 受け入れる方式と、ボタンに出す方式は別のこと。兼務の人のために本社の
              方式を受け入れたままにしつつ、そのテナントの画面には出さない、が書ける
              （docs/61 §61.15.9）。門は resolver 側のままで、ここは表示だけ。 */}
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.hidden_providers")}</span>
            <input type="text" placeholder="google" value={hidden} onChange={(e) => setHidden(e.target.value)} />
            <span className="af-unit">{tr("admin.hidden_providers_unit")}</span>
          </label>
        </div>
        {/* 「使えるサインイン方法」の答えは欄の隣にしか置けない — 別の面に置くと、
            打ち間違えて 400 で弾かれた人がそこへ辿り着けない。 */}
        <DeploymentSignInMethods />
        {/* ★ 絞り込みの副作用は、絞る人の画面にしか書く場所が無い。兼務の人は
            他テナントの方式で入っていることがあり、その方式を外すと切り替えが
            provider_required で止まる（docs/61 §61.15 の運用上の注意）。 */}
        <p className="admin-hint">{tr("admin.allowed_providers_shared_note")}</p>
        <p className="admin-hint">{tr("admin.login_rules_hint")}</p>
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
        {/* ★ 隠す指定をした人にだけ出す。素の /login はテナントを知らないので、
            デプロイ共通の方式はそこに出続ける（出さないと誰も入れない・docs/61
            §61.15.13）。つまり「隠した」が効くのはこの URL を配ったときだけで、
            それが読めるのは隠す欄と同じ画面しかない。 */}
        {hidden.trim() && <p className="admin-hint">{tr("admin.hidden_providers_url_note")}</p>}
      </div>
      <div className="admin-actions">
        <button onClick={save} className="primary">{tr("common.save")}</button>
        {saved && <span className="saved-note"><Icon name="check" /> {tr("admin.saved")}</span>}
      </div>
    </section>
  );
}

// TenantLoginRulesView — 同じ 3 列の読み取り専用版（テナント設定モーダル用）。
//
// 編集フォームを出さないのは権限の実装ではなく、事実の反映: PUT は withSuperAdmin
// 固定（決定 19）で、3 つのうち 2 つはこのテナントの外まで届く — 自動参加ドメインは
// デプロイの入口そのものを開け、使えるサインイン方法は「誰であるか」を名乗ってよい
// IdP の選定だから。それでもテナント管理者に見せるのは、招待が弾かれた理由や、
// 自分のテナントに何が効いているかを人に聞かずに読めるようにするため。
export function TenantLoginRulesView({
  slug,
  tenant,
}: {
  slug: string;
  tenant: TenantLoginFields | null | undefined;
}) {
  const tr = useT();
  const loginURL = new URL("login/" + encodeURIComponent(slug), document.baseURI).toString();
  const row = (cap: string, value: string, note: string) => (
    <div className="admin-fld">
      <span className="af-cap">{cap}</span>
      <span className={"af-val" + (value ? "" : " unset")}>{value || tr("tenant.rules_unset")}</span>
      <span className="af-unit">{note}</span>
    </div>
  );
  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>
          {tr("admin.login_rules")}
          <span className="af-note">{tr("tenant.rules_readonly_note")}</span>
        </h4>
        <div className="admin-fgrid">
          {row(tr("admin.allowed_providers"), (tenant?.allowed_providers || "").trim(), tr("tenant.rules_providers_note"))}
          {row(tr("admin.auto_join_domains"), (tenant?.auto_join_domains || "").trim(), tr("tenant.rules_autojoin_note"))}
          {row(tr("admin.invite_domains"), (tenant?.allowed_domains || "").trim(), tr("tenant.rules_invite_note"))}
          {row(tr("admin.hidden_providers"), (tenant?.hidden_providers || "").trim(), tr("admin.hidden_providers_unit"))}
        </div>
        {/* 管理モーダル側の同名ヒント（admin.login_rules_hint）はこの面に合わない —
            「下のメンバー詳細から外す」は、この画面には無い操作を指してしまう。 */}
        <p className="admin-hint">{tr("admin.allowed_providers_shared_note")}</p>
        <p className="admin-hint">{tr("tenant.rules_hint")}</p>
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
        {/* 読み取り専用の面にも同じ一文を出す。設定した人と、それを読む人が別なのが
            この面の前提なので、「なぜこの URL を配る必要があるのか」はここにも要る。 */}
        {(tenant?.hidden_providers || "").trim() && <p className="admin-hint">{tr("admin.hidden_providers_url_note")}</p>}
      </div>
    </section>
  );
}

// --- テナント定義のサインイン方法（docs/61 §61.11 / ADR0043 決定 29-33）------

// idpStatusLabel は行の status を「読み手が知りたいこと」に写す。状態名ではなく、
// 今この方法で誰かがサインインできるのかどうか。
type IdPStatusKey =
  | "admin.idp_state_active"
  | "admin.idp_state_broken"
  | "admin.idp_state_suspended"
  | "admin.idp_state_pending";

// idpSource は行の「身元の出どころ」を 1 行で言う。OIDC は issuer がその答えだが、
// GitHub の issuer は全テナント共通の github.com なので、それを出しても何も区別が
// つかない — 実際に効いているのは組織の方（docs/61 §61.15）。
function idpSource(row: TenantIdP): string {
  if (row.kind === "github") return "GitHub: " + (row.allowed_orgs || "");
  return row.issuer;
}

function idpStatusKey(row: TenantIdP): IdPStatusKey {
  if (row.status === "active") return row.usable ? "admin.idp_state_active" : "admin.idp_state_broken";
  if (row.status === "suspended") return "admin.idp_state_suspended";
  return "admin.idp_state_pending";
}

const emptyIdP = (): TenantIdP => ({
  id: "",
  name: "",
  kind: "oidc",
  issuer: "",
  client_id: "",
  client_secret: "",
  trust: "issuer",
  allowed_domains: "",
  allowed_tids: "",
  allowed_orgs: "",
});

// TenantSignInMethods — そのテナント自身の IdP 定義。
//
// ★ この画面が必ず伝えないといけないのは 2 つ。どちらを外しても子会社のオンボードが
// 止まる:
//
//  1. 新しい方法は、デプロイ管理者が承認するまで動かない。状態チップがそう言い、
//     サインイン URL は承認後にしか出さない（ボタンの無い URL を配ると問い合わせに
//     なるだけ・docs/61 §61.14 の 2 つ目）。
//  2. 受け入れドメインは任意項目ではない。承認は「その範囲でこの issuer を信じてよい」
//     に対して与えるものなので、範囲こそが承認の対象。
export function TenantSignInMethods({ slug, isSuper }: { slug: string; isSuper: boolean }) {
  const tr = useT();
  const toast = useToast();
  const [rows, setRows] = useState<TenantIdP[] | null>(null);
  const [form, setForm] = useState<TenantIdP | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDel, setConfirmDel] = useState<TenantIdP | null>(null);
  const base = `api/admin/tenants/${encodeURIComponent(slug)}/idp`;

  const load = useCallback(async () => {
    const res = await api(base);
    setRows(res?.providers || []);
  }, [base]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    if (!form) return;
    setBusy(true);
    try {
      const body = {
        name: form.name.trim(),
        label_ja: (form.label_ja || "").trim(),
        label_en: (form.label_en || "").trim(),
        kind: form.kind || "oidc",
        issuer: form.issuer.trim(),
        client_id: form.client_id.trim(),
        // 編集時に空のままなら保存済みの秘密をそのまま使う（サーバがマージする）。
        // だからこの欄はマスクを流し込まず空にしてある。
        client_secret: (form.client_secret || "").trim(),
        trust: form.trust,
        allowed_tids: (form.allowed_tids || "").trim(),
        allowed_domains: (form.allowed_domains || "").trim(),
        allowed_orgs: (form.allowed_orgs || "").trim(),
        link_claim: (form.link_claim || "").trim(),
      };
      const res = form.id
        ? await apiJSON(`${base}/${encodeURIComponent(form.id)}`, "PUT", body)
        : await apiJSON(base, "POST", body);
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setForm(null);
      load();
    } finally {
      setBusy(false);
    }
  };

  const setStatus = async (row: TenantIdP, status: string) => {
    setBusy(true);
    try {
      const res = await apiJSON(`${base}/${encodeURIComponent(row.id)}/status`, "POST", { status });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      load();
    } finally {
      setBusy(false);
    }
  };

  const remove = async (row: TenantIdP) => {
    setBusy(true);
    try {
      const res = await apiJSON(`${base}/${encodeURIComponent(row.id)}`, "DELETE");
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setConfirmDel(null);
      load();
    } finally {
      setBusy(false);
    }
  };

  const loginURL = new URL("login/" + encodeURIComponent(slug), document.baseURI).toString();
  const anyActive = (rows || []).some((r) => r.status === "active" && r.usable);

  return (
    <section className="admin-panel">
      <h4>
        {tr("admin.idp_title")}
        <span className="af-note">{tr("admin.idp_note")}</span>
      </h4>
      <p className="admin-hint">{tr("admin.idp_hint")}</p>
      {rows === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : rows.length === 0 ? (
        <p className="muted">{tr("admin.idp_none")}</p>
      ) : (
        rows.map((row) =>
          form && form.id === row.id ? (
            <IdPForm key={row.id} form={form} setForm={setForm} busy={busy} onSave={save} onCancel={() => setForm(null)} />
          ) : (
            <div key={row.id} className={"adm-mcp-row" + (row.status === "active" && row.usable ? "" : " off")}>
              <span className="as-name mono" title={row.provider_id}>
                {row.name}
              </span>
              <span className="as-repo muted" title={idpSource(row)}>
                {idpSource(row)}
              </span>
              <span className={"idp-state idp-" + (row.status || "pending")}>{tr(idpStatusKey(row))}</span>
              <span className="allow-acts">
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setForm({ ...row, client_secret: "" })}>
                  {tr("mcp.edit")}
                </button>
                {/* ★ 有効化はデプロイ管理者の一手であって、他の誰のものでもない —
                    この非対称ひとつが、テナント管理者が自分をデプロイ管理者に
                    格上げできない理由になっている（決定 30）。ボタンを出さない
                    のは案内で、実体は CP の setStatus が見ている。 */}
                {isSuper && row.status !== "active" && (
                  <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "active")}>
                    {tr("admin.idp_approve")}
                  </button>
                )}
                {row.status === "active" ? (
                  <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "suspended")}>
                    {tr("admin.idp_suspend")}
                  </button>
                ) : (
                  row.status === "suspended" && (
                    <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "pending")}>
                      {tr("admin.idp_reapply")}
                    </button>
                  )
                )}
                <button type="button" className="ghost xs danger" disabled={busy} onClick={() => setConfirmDel(row)}>
                  {tr("common.delete")}
                </button>
              </span>
            </div>
          ),
        )
      )}
      {form && form.id === "" ? (
        <IdPForm form={form} setForm={setForm} busy={busy} onSave={save} onCancel={() => setForm(null)} />
      ) : (
        !form && (
          <button type="button" className="ghost" onClick={() => setForm(emptyIdP())}>
            <Icon name="add" /> {tr("admin.idp_add")}
          </button>
        )
      )}
      {/* サインイン URL は、その上で何かが動くようになって初めて出す。それ以前は
          ボタンの無いページで、早く配られた URL は配らないより悪い。 */}
      {anyActive && (
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
      )}
      {confirmDel && (
        <ConfirmDialog
          title={tr("admin.idp_delete_title", { name: confirmDel.name })}
          confirmLabel={tr("common.delete")}
          danger
          busy={busy}
          onCancel={() => setConfirmDel(null)}
          onConfirm={() => remove(confirmDel)}
        >
          <p>{tr("admin.idp_delete_body")}</p>
        </ConfirmDialog>
      )}
    </section>
  );
}

function IdPForm({
  form,
  setForm,
  busy,
  onSave,
  onCancel,
}: {
  form: TenantIdP;
  setForm: (f: TenantIdP) => void;
  busy: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const tr = useT();
  const set = (patch: Partial<TenantIdP>) => setForm({ ...form, ...patch });
  // ★ 種類で「何を訊くか」が変わる。GitHub には issuer も tid も無く（発行元は
  // github.com 1 つ）、代わりに組織が要る — 空欄のまま出すと、埋めようのない欄を
  // 見せて 400 で弾くことになる（docs/61 §61.15）。
  const isGitHub = form.kind === "github";
  const callbackURL = new URL("oauth2/callback", document.baseURI).toString();
  const valid =
    form.name.trim() &&
    form.client_id.trim() &&
    (form.allowed_domains || "").trim() &&
    (isGitHub ? (form.allowed_orgs || "").trim() : form.issuer.trim()) &&
    (form.id || (form.client_secret || "").trim());
  return (
    <div className="ssm-frm adm-mcp-form">
      <div className="ssm-fgrid">
        <Field label={tr("admin.idp_name")} req hint={tr("admin.idp_name_hint")}>
          <input value={form.name} placeholder={isGitHub ? "github" : "entra"} onChange={(e) => set({ name: e.target.value })} />
        </Field>
        <Field label={tr("admin.idp_kind")} req hint={tr("admin.idp_kind_hint")}>
          {/* ★ 種類を変えたら、もう一方の欄は捨てる。持ち越すと「issuer が
              https://github.com の OIDC 行」のような、保存はできるのに動かない行が
              作れてしまう（github 行の issuer はサーバが入れた定数なので、なおさら
              残す意味が無い）。 */}
          <select
            value={form.kind || "oidc"}
            onChange={(e) =>
              set(
                e.target.value === "github"
                  ? { kind: "github", issuer: "", allowed_tids: "" }
                  : { kind: "oidc", allowed_orgs: "", issuer: form.issuer === "https://github.com" ? "" : form.issuer },
              )
            }
          >
            <option value="oidc">{tr("admin.idp_kind_oidc")}</option>
            <option value="github">{tr("admin.idp_kind_github")}</option>
          </select>
        </Field>
        {isGitHub ? (
          <Field label={tr("admin.idp_orgs")} req wide hint={tr("admin.idp_orgs_hint")}>
            <input
              value={form.allowed_orgs || ""}
              placeholder="acme-sub"
              onChange={(e) => set({ allowed_orgs: e.target.value })}
            />
          </Field>
        ) : (
          <>
            <Field label={tr("admin.idp_trust")} req hint={tr("admin.idp_trust_hint")}>
              <select value={form.trust} onChange={(e) => set({ trust: e.target.value })}>
                <option value="issuer">{tr("admin.idp_trust_issuer")}</option>
                <option value="email_verified">{tr("admin.idp_trust_email")}</option>
              </select>
            </Field>
            <Field label={tr("admin.idp_issuer")} req wide hint={tr("admin.idp_issuer_hint")}>
              <input
                value={form.issuer}
                placeholder="https://login.microsoftonline.com/<tenant-guid>/v2.0"
                onChange={(e) => set({ issuer: e.target.value })}
              />
            </Field>
          </>
        )}
        <Field label={tr("admin.idp_client_id")} req hint={isGitHub ? tr("admin.idp_github_app_hint", { url: callbackURL }) : undefined}>
          <input value={form.client_id} onChange={(e) => set({ client_id: e.target.value })} />
        </Field>
        <Field
          label={tr("admin.idp_client_secret")}
          req={!form.id}
          hint={form.id && form.has_secret ? tr("admin.idp_secret_kept") : tr("admin.idp_secret_hint")}
        >
          <input
            type="password"
            autoComplete="new-password"
            value={form.client_secret || ""}
            placeholder={form.id && form.has_secret ? "***" : ""}
            onChange={(e) => set({ client_secret: e.target.value })}
          />
        </Field>
        <Field
          label={tr("admin.idp_domains")}
          req
          wide
          hint={isGitHub ? tr("admin.idp_github_domains_note") : tr("admin.idp_domains_hint")}
        >
          <input
            value={form.allowed_domains || ""}
            placeholder="@sub.co.jp"
            onChange={(e) => set({ allowed_domains: e.target.value })}
          />
        </Field>
        {!isGitHub && (
          <Field label={tr("admin.idp_tids")} wide hint={tr("admin.idp_tids_hint")}>
            <input value={form.allowed_tids || ""} onChange={(e) => set({ allowed_tids: e.target.value })} />
          </Field>
        )}
        {/* ★ 自由入力ではなく選択にしてある。ここに書けるのは「IdP が割り当てる、
            本人にも選べないクレーム」だけで、主張されるクレーム（email・upn …）を
            書けると、同じ発行元を共有する方式の間で email 結合ができてしまう
            （docs/61 §61.15.10）。選択肢は CP のホワイトリストの写しで、判断は
            サーバが持つ（保存時に弾かれる）。 */}
        {!isGitHub && (
          <Field label={tr("admin.idp_link_claim")} wide hint={tr("admin.idp_link_claim_hint")}>
            <select value={form.link_claim || ""} onChange={(e) => set({ link_claim: e.target.value })}>
              <option value="">{tr("admin.idp_link_claim_none")}</option>
              <option value="oid">oid</option>
            </select>
          </Field>
        )}
        <Field label={tr("admin.idp_label_ja")}>
          <input value={form.label_ja || ""} onChange={(e) => set({ label_ja: e.target.value })} />
        </Field>
        <Field label={tr("admin.idp_label_en")}>
          <input value={form.label_en || ""} onChange={(e) => set({ label_en: e.target.value })} />
        </Field>
      </div>
      <p className="admin-hint">{tr("admin.idp_repend_hint")}</p>
      <div className="admin-actions">
        <button className="primary" disabled={busy || !valid} onClick={onSave}>
          {tr("common.save")}
        </button>
        <button className="ghost" disabled={busy} onClick={onCancel}>
          {tr("common.cancel")}
        </button>
      </div>
    </div>
  );
}

// SignInMethodRegister — テナント定義のサインイン方法をデプロイ全体で見る台帳
// （docs/61 §61.11.6）。デプロイ管理者専用。
//
// ★ これは「捌けて空になるキュー」ではなく、わざと「台帳」にしてある。承認は一度
// きりの点検だが、その先の IdP は他人の管理下にあり続け、設定は後から変わり得る
// （セルフサインアップが有効化される、が典型）。だから承認済みの行も、誰がいつ
// 承認したかと一緒に残り、その一覧が定期点検の対象になる。承認待ちを先に並べるの
// は、そこで誰かが待っているから。
export function SignInMethodRegister() {
  const tr = useT();
  const toast = useToast();
  const [rows, setRows] = useState<TenantIdP[] | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const res = await api("api/admin/idp");
    setRows(res?.providers || []);
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  // ★ 承認はこの台帳から直接できる。件数だけ出して「承認はテナントの詳細画面で」と
  // 案内していたが、待っている人が見えている場所と、待たせている人が動ける場所が
  // 違うのは、ただの遠回りだった。叩く先はテナント側と同じ 1 本
  // （POST /api/admin/tenants/{slug}/idp/{id}/status）で、行が持つ tenant_slug から
  // 組み立てる。★ 権限は変わらない — CP の setStatus が super_admin を見ている
  // （決定 30）。ここが super_admin にしか見えないのは、その事実の案内でしかない。
  const setStatus = async (row: TenantIdP, status: string) => {
    if (!row.tenant_slug) return;
    setBusy(true);
    try {
      const res = await apiJSON(
        `api/admin/tenants/${encodeURIComponent(row.tenant_slug)}/idp/${encodeURIComponent(row.id)}/status`,
        "POST",
        { status },
      );
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      // 承認したら状態も承認者・承認日時も変わる。1 回きりの fetch のままだと
      // 押した本人にだけ結果が見えない画面になる。
      await load();
    } finally {
      setBusy(false);
    }
  };

  if (rows === null) return <p className="muted pad">{tr("common.loading")}</p>;
  // ★ 以前はテナント一覧の下に積んでいたので「0 件なら出さない」で良かったが、
  // 今はレールの 1 項目＝それだけが本文なので、空でも空だと分かる必要がある。
  if (rows.length === 0) {
    return (
      <section className="admin-panel">
        <h4>{tr("admin.idp_register")}</h4>
        <p className="muted">{tr("admin.idp_register_none")}</p>
      </section>
    );
  }
  const pending = rows.filter((r) => r.status === "pending").length;
  return (
    <section className="admin-panel">
      <h4>
        {tr("admin.idp_register")}
        {pending > 0 && <span className="af-note">{tr("admin.idp_pending_count", { n: pending })}</span>}
      </h4>
      <p className="admin-hint">{tr("admin.idp_register_hint")}</p>
      {rows.map((row) => (
        <div key={row.id} className={"adm-mcp-row" + (row.status === "active" && row.usable ? "" : " off")}>
          <span className="as-name mono" title={row.provider_id}>
            {row.tenant_slug}
          </span>
          <span className="as-repo muted" title={idpSource(row)}>
            {idpSource(row)}
          </span>
          <span className="muted">{row.allowed_domains}</span>
          <span className={"idp-state idp-" + (row.status || "pending")}>{tr(idpStatusKey(row))}</span>
          {row.approved_at && <span className="muted">{fmtDateTime(row.approved_at, DATETIME_FULL)}</span>}
          {row.tenant_slug && (
            <span className="allow-acts">
              {row.status !== "active" && (
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "active")}>
                  {tr("admin.idp_approve")}
                </button>
              )}
              {row.status === "active" && (
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "suspended")}>
                  {tr("admin.idp_suspend")}
                </button>
              )}
            </span>
          )}
        </div>
      ))}
    </section>
  );
}
