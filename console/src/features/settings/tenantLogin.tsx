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

// useDeploymentProviders — このデプロイの方式（＝既定テナントの方式・docs/61 §61.17）。
//
// ★ 3 状態を分ける。以前は `res?.providers || []` で**エラーも 0 件に潰していた**ので、
// 読めなかった相手に「設定されていません」と嘘を表示していた（§61.17.9 ②）。
// null=読み込み中 / "error"=読めなかった / 配列=読めた（空配列は本当に 0 件）。
//
// ★ 判定は `Array.isArray(res?.providers)` — `res.error` の有無ではなく**欲しい形が
// 来たかどうか**で見る。error の形が将来変わっても、0 件と混ざらない。
//
// ★ テナント自身の方法（`t:<slug>:<name>`）はここには出ない。あれは実行時に増減し、
// 全部並べるとグループ会社の名簿になる（決定 32-4）。自テナントの分は別に取る。
function useDeploymentProviders(): DeployProvider[] | "error" | null {
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
  return rows;
}

// --- 「受け入れる」／「ボタンに出す」の代数（docs/61 §61.17.5）------------------
//
// DB 表現は CSV 2 本のまま（`allowed_providers` / `hidden_providers`）。画面がそれを
// 行ごとの 2 トグルとして見せるだけで、スキーマは変わらない。ここに置いた関数だけが
// CSV を読み書きし、画面は真偽値しか触らない。
//
// ★ 罠は 3 つとも「空＝全部」という既存の意味と、既存の安全弁から出る:
//   1. `allowed_providers` は空＝全部受け入れ（§61.9.4）。全部 OFF にすると保存結果は
//      「全部 ON」＝**絞ったつもりで全開**になる。
//   2. `hidden_providers` にも「全部隠したら無視する」弁がある（`oauth.go` の
//      loginButtons）。全行の「出す」を OFF にする操作は**保存できて、そして効かない**
//      ＝画面が嘘をつく。
//   3. 「空」は *デプロイに追従する* という意味を持つ。最初の操作で明示リストに固めると、
//      以後 env に足した方式をこのテナントだけが黙って拒否する。だから固めない —
//      正規化は「**全部 ON なら空で保存**」。

/** CSV を id の配列に。CP 側が小文字化して保存する（splitCSVLower）ので合わせる。 */
export const splitIds = (csv?: string): string[] =>
  (csv || "")
    .split(",")
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean);

/**
 * 「空＝全部」を展開した受け入れ集合。knownIds の順序を保ち、knownIds に無い id は
 * 落とす（消された方式が CSV に残っていても、画面の状態には影響させない）。
 */
export function acceptedIds(knownIds: string[], allowedCSV?: string): string[] {
  const a = splitIds(allowedCSV);
  if (a.length === 0) return [...knownIds];
  const set = new Set(a);
  return knownIds.filter((id) => set.has(id));
}

/** 1 行の 2 トグルの状態。shown は accepted の従属（受け入れていないものは出ない）。 */
export function ruleStateFor(
  knownIds: string[],
  allowedCSV: string | undefined,
  hiddenCSV: string | undefined,
  id: string,
): { accepted: boolean; shown: boolean } {
  const acc = new Set(acceptedIds(knownIds, allowedCSV));
  const hid = new Set(splitIds(hiddenCSV));
  return { accepted: acc.has(id), shown: acc.has(id) && !hid.has(id) };
}

/**
 * OFF にできない行を返す。usableIds は「いま実際に人が入れる方式」＝デプロイの方式と、
 * **active かつ usable な自前の行**だけ。
 *
 * ★ これ 1 本で 2 つの規則を兼ねる: 「最後の 1 つは OFF にできない」と、
 * §61.17.5 の**順序**（先に絞ってからテナント管理者を招くとその人が入れない＝自前の行が
 * まだ動いていないうちは、デプロイの方式を外せない）。承認前の行は usable でないので、
 * 自動的に後者になる。
 */
export function ruleLocks(
  knownIds: string[],
  usableIds: string[],
  allowedCSV: string | undefined,
  hiddenCSV: string | undefined,
  id: string,
): { acceptOffLocked: boolean; showOffLocked: boolean } {
  const usable = new Set(usableIds);
  const hid = new Set(splitIds(hiddenCSV));
  const accUsable = acceptedIds(knownIds, allowedCSV).filter((x) => usable.has(x));
  const shownUsable = accUsable.filter((x) => !hid.has(x));
  return {
    acceptOffLocked: accUsable.length <= 1 && accUsable.includes(id),
    showOffLocked: shownUsable.length <= 1 && shownUsable.includes(id),
  };
}

/** トグル 1 回を CSV 2 本に畳む。返すのは保存する値そのもの。 */
export function toggleRule(
  knownIds: string[],
  allowedCSV: string | undefined,
  hiddenCSV: string | undefined,
  id: string,
  field: "accepted" | "shown",
  value: boolean,
): { allowed_providers: string; hidden_providers: string } {
  const known = new Set(knownIds);
  const acc = new Set(acceptedIds(knownIds, allowedCSV));
  const hid = new Set(splitIds(hiddenCSV).filter((x) => known.has(x)));
  if (field === "accepted") {
    if (value) {
      acc.add(id);
    } else {
      acc.delete(id);
      // 受け入れないなら「出さない」指定は意味を持たない（描画側は hidden の判定の
      // 中でも allowed を要求する）。残すと、後で受け入れ直したときに「出ない」が
      // 説明なく復活する。
      hid.delete(id);
    }
  } else if (value) {
    hid.delete(id);
  } else {
    hid.add(id);
  }
  const accList = knownIds.filter((x) => acc.has(x));
  return {
    // ★ 全部 ON なら空で保存（罠 3）。ここが「固めない」の実体。
    allowed_providers: accList.length === knownIds.length ? "" : accList.join(","),
    hidden_providers: knownIds.filter((x) => hid.has(x)).join(","),
  };
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
  const [autoJoin, setAutoJoin] = useState(tenant?.auto_join_domains || "");
  const [domains, setDomains] = useState(tenant?.allowed_domains || "");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setAutoJoin(tenant?.auto_join_domains || "");
    setDomains(tenant?.allowed_domains || "");
  }, [slug, tenant]);

  const save = async () => {
    const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/login`, "PUT", {
      // ★ この PUT は 4 列を丸ごと置き換える。方式の 2 列はもう別の面（サインイン方法の
      // 2 トグル）が持っているので、ここでは**読んだ値をそのまま返す** — 送らないと
      // 空で上書きされ、絞っていたテナントが黙って全開になる。
      allowed_providers: (tenant?.allowed_providers || "").trim(),
      hidden_providers: (tenant?.hidden_providers || "").trim(),
      auto_join_domains: autoJoin.trim(),
      allowed_domains: domains.trim(),
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
            <span className="af-cap">{tr("admin.auto_join_domains")}</span>
            <input type="text" placeholder="@sales.acme.co.jp" value={autoJoin} onChange={(e) => setAutoJoin(e.target.value)} />
            <span className="af-unit">{tr("admin.auto_join_domains_unit")}</span>
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.invite_domains")}</span>
            <input type="text" placeholder="@acme.co.jp" value={domains} onChange={(e) => setDomains(e.target.value)} />
            <span className="af-unit">{tr("admin.invite_domains_unit")}</span>
          </label>
        </div>
        {/* ★ 方式の 2 列（受け入れる／ボタンに出す）は P7-0 でこの欄から出た
            （docs/61 §61.17.5）。自由入力に id を打つ代わりに、「サインイン方法」の面で
            行ごとのトグルを倒す — 打てる id の一覧を別に用意する必要も、
            400 unknown_provider も無くなる。 */}
        <p className="admin-hint">{tr("admin.login_rules_methods_moved")}</p>
        <p className="admin-hint">{tr("admin.login_rules_hint")}</p>
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
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
          {row(tr("admin.auto_join_domains"), (tenant?.auto_join_domains || "").trim(), tr("tenant.rules_autojoin_note"))}
          {row(tr("admin.invite_domains"), (tenant?.allowed_domains || "").trim(), tr("tenant.rules_invite_note"))}
        </div>
        {/* ★ 方式の 2 列はこの面から出た（§61.17.5）。CSV を読み取り専用で見せても
            「うちは何で入れるのか」には答えないので、行として並ぶ「サインイン方法」の
            面を指す。あちらは tenant_admin にも読める（§61.17.9 ①）。 */}
        <p className="admin-hint">{tr("admin.login_rules_methods_moved")}</p>
        {/* 管理モーダル側の同名ヒント（admin.login_rules_hint）はこの面に合わない —
            「下のメンバー詳細から外す」は、この画面には無い操作を指してしまう。 */}
        <p className="admin-hint">{tr("tenant.rules_hint")}</p>
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
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

// TenantSignInMethods — このテナントで使えるサインイン方法**ぜんぶ**（docs/61 §61.17.5）。
//
// P7-0 でここが 1 本のリストになった。自前の行（作成・編集可・承認が要る）と、
// デプロイの方式＝既定テナントの方式（バッジ「デプロイ共通」・編集不可）が同じ並びに出て、
// 各行に 2 つのトグルが付く: **受け入れる** と **ボタンに出す**。
// ★ これで「その画面が門の全体を映す」ようになる — 以前はデプロイの方式がどこにも
// 出ず、Google で毎日入っている会社でもこの面が空だった（§61.17 の出発点）。
//
// ★ ［方式を追加］は「新しく作る」だけを指す。既定テナントの方式を*参照する*操作は
// 作らない — それは「受け入れる」トグルと**同じ 1 ビット**で、同じことに 2 つの名前を
// 与えると「参照行を編集できると思った」を生む（§61.17.5）。だから未参照の行も
// 最初から並べ、トグルが OFF なだけにする。
//
// ★ この画面が必ず伝えないといけないのは 2 つ。どちらを外しても子会社のオンボードが
// 止まる:
//
//  1. 新しい方法は、デプロイ管理者が承認するまで動かない。状態チップがそう言い、
//     サインイン URL は承認後にしか出さない（ボタンの無い URL を配ると問い合わせに
//     なるだけ・docs/61 §61.14 の 2 つ目）。
//  2. 受け入れドメインは任意項目ではない。承認は「その範囲でこの issuer を信じてよい」
//     に対して与えるものなので、範囲こそが承認の対象。
//
// ★ トグルを**倒せる**のは super_admin だけ（PUT .../login は withSuperAdmin 固定＝
// 決定 19 は変えていない）。テナント管理者には同じ状態を静的なチップで見せる —
// 押せないトグルを出すのは「できる」と言って断ることで、能力が無い面には操作要素を
// 置かない。
export function TenantSignInMethods({
  slug,
  isSuper,
  tenant,
  onChanged,
}: {
  slug: string;
  isSuper: boolean;
  /** ログイン規則の 4 列。方式の 2 列をこの面が読み書きする。 */
  tenant?: TenantLoginFields | null;
  onChanged?: () => void;
}) {
  const tr = useT();
  const locale = useLocale();
  const toast = useToast();
  const [rows, setRows] = useState<TenantIdP[] | null>(null);
  const [form, setForm] = useState<TenantIdP | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDel, setConfirmDel] = useState<TenantIdP | null>(null);
  const base = `api/admin/tenants/${encodeURIComponent(slug)}/idp`;
  const deployment = useDeploymentProviders();

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

  // --- 統合リスト（docs/61 §61.17.5）-----------------------------------------
  //
  // 並びはデプロイの方式 → 自前の行。id の集合はこの順序で固定して、CSV への
  // 書き出しにもそのまま使う（保存のたびに順番が入れ替わると監査ログが読めない）。
  const deployRows = Array.isArray(deployment) ? deployment : [];
  const ownRows = rows || [];
  const knownIds = [...deployRows.map((p) => p.id), ...ownRows.map((r) => r.provider_id || "").filter(Boolean)];
  // usable は「いま実際に人が入れる方式」。デプロイの方式は常に、自前の行は承認済みで
  // 壊れていないものだけ。ここが §61.17.5 の**順序**の規則になる。
  const usableIds = [
    ...deployRows.map((p) => p.id),
    ...ownRows.filter((r) => r.status === "active" && r.usable).map((r) => r.provider_id || ""),
  ].filter(Boolean);

  // ★ トグルを触らせてよいのは、両方の一覧が揃っていて（＝knownIds が本物で）、
  // かつ規則そのものを読めているときだけ。デプロイの方式が読めていないのに保存すると、
  // 「全部 ON なら空」の正規化が**知らない id を落とした結果**で走り、絞ったつもりの
  // ないテナントを絞ってしまう。
  const rulesReady = isSuper && deployment !== null && deployment !== "error" && rows !== null && !!tenant;

  const toggle = async (id: string, field: "accepted" | "shown", value: boolean) => {
    if (!rulesReady || !tenant) return;
    const next = toggleRule(knownIds, tenant.allowed_providers, tenant.hidden_providers, id, field, value);
    setBusy(true);
    try {
      const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/login`, "PUT", {
        ...next,
        // ★ この PUT は 4 列を丸ごと置き換える。ドメインの 2 列はこの面が持っていないので、
        // 読んだ値をそのまま返す — 送らないと空で上書きされ、招待の上限が消える。
        auto_join_domains: (tenant.auto_join_domains || "").trim(),
        allowed_domains: (tenant.allowed_domains || "").trim(),
      });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      onChanged?.();
    } finally {
      setBusy(false);
    }
  };

  // 1 行分の 2 トグル。super_admin には操作できるチェックボックス、それ以外には
  // 同じ状態の静的なチップ（押せないトグルは「できる」と言って断ることになる）。
  const toggles = (id: string) => {
    // provider_id は CP が必ず組んで返す（tenant_idp_api.go）。それでも空の行が来たら
    // トグルは出さない — 押せるのに何も起きない要素は、壊れているより分かりにくい。
    if (!id) return null;
    const { accepted, shown } = ruleStateFor(knownIds, tenant?.allowed_providers, tenant?.hidden_providers, id);
    const locks = ruleLocks(knownIds, usableIds, tenant?.allowed_providers, tenant?.hidden_providers, id);
    if (!isSuper) {
      return (
        <span className="idp-flags">
          <span className={"idp-flag" + (accepted ? " on" : "")}>{tr("admin.idp_accept")}</span>
          <span className={"idp-flag" + (shown ? " on" : "")}>{tr("admin.idp_show")}</span>
        </span>
      );
    }
    // ★ OFF にできない場合が 2 つある。どちらも「絞ったつもりで全開／設定ごと無視」に
    // なるので、保存させてから謝るのではなく最初から倒せなくする（§61.17.5）。
    const acceptLocked = accepted && locks.acceptOffLocked;
    const showLocked = shown && locks.showOffLocked;
    return (
      <span className="idp-flags">
        <label className={"idp-flag" + (accepted ? " on" : "")} title={acceptLocked ? tr("admin.idp_accept_last") : undefined}>
          <input
            type="checkbox"
            checked={accepted}
            disabled={busy || !rulesReady || acceptLocked}
            onChange={(e) => toggle(id, "accepted", e.target.checked)}
          />
          <span>{tr("admin.idp_accept")}</span>
        </label>
        {/* ★ 「出す」は「受け入れる」の従属。描画側は hidden の判定の中でも allowed を
            要求するので、受け入れていない行の「出す」は ON にしても何も起きない。 */}
        <label
          className={"idp-flag" + (shown ? " on" : "")}
          title={!accepted ? tr("admin.idp_show_needs_accept") : showLocked ? tr("admin.idp_show_last") : undefined}
        >
          <input
            type="checkbox"
            checked={shown}
            disabled={busy || !rulesReady || !accepted || showLocked}
            onChange={(e) => toggle(id, "shown", e.target.checked)}
          />
          <span>{tr("admin.idp_show")}</span>
        </label>
      </span>
    );
  };

  const deployLabel = (p: DeployProvider) =>
    (locale === "en" ? p.label_en : p.label_ja) || p.label_ja || p.label_en || p.id;

  // 「受け入れているが出さない」行が 1 つでもあるか（＝素の /login の注記を出すか）。
  const anyHidden = knownIds.some((id) => {
    const s = ruleStateFor(knownIds, tenant?.allowed_providers, tenant?.hidden_providers, id);
    return s.accepted && !s.shown;
  });

  return (
    <section className="admin-panel">
      <h4>
        {tr("admin.idp_title")}
        <span className="af-note">{tr("admin.idp_note")}</span>
      </h4>
      <p className="admin-hint">{tr("admin.idp_hint")}</p>
      {/* デプロイの方式＝既定テナントの方式（§61.17）。編集はできない（issuer を
          握っているのはオペレーターで、テナント管理者ではない）が、受け入れるか／
          ボタンに出すかはこのテナントの選択なので、同じ行にトグルが付く。 */}
      {deployment === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : deployment === "error" ? (
        <p className="admin-hint">{tr("admin.providers_unreadable")}</p>
      ) : deployment.length === 0 ? (
        // 本当に 0 件（AUTH=dev / proxy、または env に 1 つも書かれていない）。
        // 「読めなかった」とは別の文言でなければならない（§61.17.9 ②）。
        <p className="admin-hint">{tr("admin.providers_none")}</p>
      ) : (
        deployment.map((p) => (
          <div key={p.id} className="adm-mcp-row">
            {toggles(p.id)}
            <span className="as-name">{deployLabel(p)}</span>
            <code>{p.id}</code>
            {/* issuer は super_admin にしか返らない（§61.17.9 ①）。無いときは列ごと
                出さない — 空セルは設定漏れに見える。 */}
            {p.issuer && (
              <span className="as-repo muted" title={p.issuer}>
                {p.issuer}
              </span>
            )}
            <span className="idp-state">{tr("admin.idp_deployment_wide")}</span>
          </div>
        ))
      )}
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
              {toggles(row.provider_id || "")}
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
      {/* ★ 絞り込みの副作用は、絞る人の画面にしか書く場所が無い。兼務の人は他テナントの
          方式で入っていることがあり、その方式の「受け入れる」を外すと切り替えが
          provider_required で止まる（docs/61 §61.15 の運用上の注意）。 */}
      {isSuper && <p className="admin-hint">{tr("admin.allowed_providers_shared_note")}</p>}
      {/* サインイン URL は、その上で何かが動くようになって初めて出す。それ以前は
          ボタンの無いページで、早く配られた URL は配らないより悪い。 */}
      {anyActive && (
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
      )}
      {/* ★ 「出さない」にした人にだけ出す。以前はここに「素の /login には効かないので
          上の URL を配れ」という運用回避を置いていたが、P7-1（docs/61 §61.17.6）で素の
          /login が既定テナントのページになり、効くようになった。残っているのは
          **受け入れは続く**という一点だけ — 「隠した＝もう使えない」と読む人が居るため。 */}
      {anyHidden && <p className="admin-hint">{tr("admin.hidden_still_accepted_note")}</p>}
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
