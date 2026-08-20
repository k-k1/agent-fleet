// TenantDialog — テナント設定（テナント管理者の面）。
//
// モーダルは 3 つある。個人設定＝自分、管理＝デプロイ全体（デプロイ管理者）、
// そしてここ＝自分が管理しているテナント。
//
// ★ 面を分けても権限はサーバが持つ。サインイン方法の「承認して有効化」は CP の
// setStatus が super_admin を見ており（ADR0043 決定 30）、ログイン規則の PUT は
// withSuperAdmin 固定（決定 19）。この画面の出し分けは案内でしかない。
//
// レイアウトは個人設定と同じ二枚看板（左レール＋本文）で、CSS も settings-* を
// そのまま使う（クラスの再定義はしない — 同名クラスの二重定義は import 順で合成
// されるため）。tenant-modal は色味の微調整だけを足す。
//
// レールの並びと本文は tenantScope に置いて管理モーダルと共有する。同じテナントを
// 別の入口から見るだけなので、IA が入口ごとに分かれないようにするため。
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useSettingsUI } from "./store.ts";
import { mobileMatches } from "../../lib/device.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { Modal } from "../../ui/Modal.tsx";
import { useCostProfile } from "../cost/CloudCostView.tsx";
import { TENANT_SCOPE_SECTIONS, TenantScopeBody, tenantScopeGroups } from "./tenantScope.tsx";
import type { Member, Tenant } from "./adminShared.ts";

export function TenantDialog() {
  const tr = useT();
  // クラウド費用の面があるかはランタイムが申告する。docker / native には AWS の
  // 請求が無いので、項目ごと出さない（ADR 0048 決定 9）。
  const costProfile = useCostProfile();
  const groups = tenantScopeGroups({ cost: !!costProfile?.available });
  const closeTenantSettings = useSettingsUI((s) => s.closeTenantSettings);
  const tenantSection = useSettingsUI((s) => s.tenantSection);
  const [section, setSection] = useState(
    TENANT_SCOPE_SECTIONS.includes(tenantSection) ? tenantSection : "signin",
  );
  // モバイルは個人設定と同じドリルダウン（レール → 本文、戻るでレールへ）。
  const [entered, setEntered] = useState(false);
  useBackClose(() => setEntered(false), mobileMatches() && entered);

  // 開いたまま openTenantSettings(section) が呼ばれたとき（他画面からの deep-link）
  // レールを飛ばす。マウント時は素通しして、スマホでは一覧から始める。
  const mounted = useRef(false);
  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true;
      return;
    }
    if (TENANT_SCOPE_SECTIONS.includes(tenantSection)) {
      setSection(tenantSection);
      setMember(null);
      setEntered(true);
    }
  }, [tenantSection]);

  // ★ isSuper は管理モーダルと同じ GET /api/admin/tenants のレスポンスの super_admin。
  // 面を分けても取得元を変えない（テナント管理者だけの人には false が返り、承認ボタン
  // は出ない）。tenants は super_admin なら全テナント、そうでなければ自分が
  // tenant_admin のテナントだけがサーバ側で絞られて返る。
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  const [isSuper, setIsSuper] = useState(false);
  const [forbidden, setForbidden] = useState(false);
  const [slug, setSlug] = useState("");

  // メンバーは「一覧 → 詳細」の 2 段。レールを人の数だけ伸ばすと、40 人いる部署で
  // 破綻するし、外した／追加した瞬間にレールが動く。段はここ（本文）で積む。
  const [member, setMember] = useState<Member | null>(null);
  // 端末の戻るは、まず詳細を閉じる（モーダルごと閉じない）。レールより後に
  // 積まれるので、こちらが先に剥がれる。
  useBackClose(() => setMember(null), !!member);

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/tenants");
      if (d && d.error) {
        setForbidden(true);
        return;
      }
      const list: Tenant[] = d.tenants || [];
      setTenants(list);
      setIsSuper(!!d.super_admin);
      setSlug((cur) => (cur && list.some((t) => t.slug === cur) ? cur : list[0]?.slug || ""));
    } catch {
      setForbidden(true);
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const tenant = tenants?.find((t) => t.slug === slug) || null;
  const currentLabel = tr(
    (groups.flatMap((g) => g.items).find(([k]) => k === section)?.[1] ??
      "tenant.title") as Parameters<typeof tr>[0],
  );

  const body = () => {
    if (forbidden) return <p className="muted pad">{tr("tenant.forbidden")}</p>;
    if (tenants === null) return <p className="muted pad">{tr("common.loading")}</p>;
    if (!tenant) return <p className="muted pad">{tr("tenant.none")}</p>;
    return (
      <TenantScopeBody
        slug={tenant.slug}
        tenant={tenant}
        section={section}
        isSuper={isSuper}
        member={member}
        onOpenMember={setMember}
        onCloseMember={() => setMember(null)}
        onChanged={load}
      />
    );
  };

  return (
    <Modal title={tr("tenant.title")} onClose={closeTenantSettings} className="settings-modal tenant-modal">
      <div className="ui-modal-body">
        <div className={"settings-layout" + (entered ? " entered" : "")}>
          <nav className="settings-rail" aria-label={tr("tenant.title")}>
            {/* 複数のテナントを管理している人のための切り替え。1 つしか無いなら
                選ぶものが無いので出さない（個人設定に倣い、余計な操作を置かない）。 */}
            {(tenants?.length || 0) > 1 && (
              <div className="settings-rail-group">
                <div className="settings-rail-head">{tr("tenant.picker")}</div>
                <select
                  className="tenant-picker"
                  value={slug}
                  onChange={(e) => {
                    setSlug(e.target.value);
                    setMember(null); // 別のテナントの名簿に、前のテナントの人が残らないように
                  }}
                  aria-label={tr("tenant.picker")}
                >
                  {tenants?.map((t) => (
                    <option key={t.slug} value={t.slug}>
                      {t.name || t.slug}
                    </option>
                  ))}
                </select>
              </div>
            )}
            {groups.map((g) => (
              <div key={g.key} className="settings-rail-group">
                <div className="settings-rail-head">{tr(g.label as Parameters<typeof tr>[0])}</div>
                {g.items.map(([key, label]) => (
                  <button
                    key={key}
                    type="button"
                    className={"settings-rail-item" + (section === key ? " active" : "")}
                    aria-current={section === key ? "page" : undefined}
                    onClick={() => {
                      setSection(key);
                      setMember(null);
                      setEntered(true);
                    }}
                  >
                    {tr(label as Parameters<typeof tr>[0])}
                  </button>
                ))}
              </div>
            ))}
          </nav>
          <div className="settings-content">
            <div className="settings-crumb">
              <button type="button" className="settings-back" onClick={() => setEntered(false)}>
                ‹ {tr("tenant.back")}
              </button>
              <span className="settings-current" aria-current="page">
                {currentLabel}
              </span>
            </div>
            {body()}
          </div>
        </div>
      </div>
    </Modal>
  );
}
