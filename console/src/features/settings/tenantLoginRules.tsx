import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { TenantLoginFields } from "./tenantLoginTypes.ts";

// このデプロイが env で有効にしているサインイン方法（GET /api/admin/providers）。
// 秘密は載らない — id・表示名・issuer だけ。
export interface DeployProvider {
  id: string;
  label_ja?: string;
  label_en?: string;
  issuer?: string;
}

// useDeploymentProviders — このデプロイの方式（＝既定テナントの方式・docs/log/61 §61.17）。
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
export function useDeploymentProviders(): DeployProvider[] | "error" | null {
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

// --- 「受け入れる」／「ボタンに出す」の代数（docs/log/61 §61.17.5）------------------
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

// TenantLoginRules — docs/log/61 §61.9.7 の CSV 3 列のエディタ。
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
            （docs/log/61 §61.17.5）。自由入力に id を打つ代わりに、「サインイン方法」の面で
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

// --- テナント定義のサインイン方法（docs/log/61 §61.11 / ADR0043 決定 29-33）------
