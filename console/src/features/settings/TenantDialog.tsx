// TenantDialog — テナント設定（テナント管理者の面）。
//
// モーダルは 3 つある。個人設定＝自分、管理＝デプロイ全体（デプロイ管理者）、
// そしてここ＝自分が管理しているテナント。管理モーダルの中で isSuper 分岐に
// 埋もれていたテナント管理者向けの面を、読み手ごとに面を分ける形へ移す第一歩で、
// まずログイン面（docs/61）を扱う。
//
// ★ 面を分けても権限はサーバが持つ。サインイン方法の「承認して有効化」は CP の
// setStatus が super_admin を見ており（ADR0043 決定 30）、ログイン規則の PUT は
// withSuperAdmin 固定（決定 19）。この画面の出し分けは案内でしかない。
//
// レイアウトは個人設定と同じ二枚看板（左レール＋本文）で、CSS も settings-* を
// そのまま使う（クラスの再定義はしない — 同名クラスの二重定義は import 順で合成
// されるため）。tenant-modal は色味の微調整だけを足す。
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useSettingsUI } from "./store.ts";
import { mobileMatches } from "../../lib/device.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { Modal } from "../../ui/Modal.tsx";
import { TenantLoginRulesView, TenantSignInMethods } from "./tenantLogin.tsx";

// 管理 API のテナント表現のうち、この画面が読む分だけ。ログイン規則の 3 列は
// GET /api/admin/tenants に載っており、テナント管理者にも自分のテナント分だけ返る。
interface AdminTenant {
  slug: string;
  name?: string;
  allowed_providers?: string;
  auto_join_domains?: string;
  allowed_domains?: string;
}

// レールの並び。項目 = [セクションキー, i18n キー]。ここの順がそのまま表示順。
const GROUPS: { key: string; label: string; items: [string, string][] }[] = [
  {
    key: "login",
    label: "tenant.group_login",
    items: [
      ["signin", "tenant.tab_signin"],
      ["rules", "tenant.tab_rules"],
    ],
  },
];
const ALL_SECTIONS = GROUPS.flatMap((g) => g.items.map(([k]) => k));

export function TenantDialog() {
  const tr = useT();
  const closeTenantSettings = useSettingsUI((s) => s.closeTenantSettings);
  const tenantSection = useSettingsUI((s) => s.tenantSection);
  const [section, setSection] = useState(
    ALL_SECTIONS.includes(tenantSection) ? tenantSection : "signin",
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
    if (ALL_SECTIONS.includes(tenantSection)) {
      setSection(tenantSection);
      setEntered(true);
    }
  }, [tenantSection]);

  // ★ isSuper は管理モーダルと同じ GET /api/admin/tenants のレスポンスの super_admin。
  // 面を分けても取得元を変えない（テナント管理者だけの人には false が返り、承認ボタン
  // は出ない）。tenants は super_admin なら全テナント、そうでなければ自分が
  // tenant_admin のテナントだけがサーバ側で絞られて返る。
  const [tenants, setTenants] = useState<AdminTenant[] | null>(null);
  const [isSuper, setIsSuper] = useState(false);
  const [forbidden, setForbidden] = useState(false);
  const [slug, setSlug] = useState("");

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/tenants");
      if (d && d.error) {
        setForbidden(true);
        return;
      }
      const list: AdminTenant[] = d.tenants || [];
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
    (GROUPS.flatMap((g) => g.items).find(([k]) => k === section)?.[1] ??
      "tenant.title") as Parameters<typeof tr>[0],
  );

  const body = () => {
    if (forbidden) return <p className="muted pad">{tr("tenant.forbidden")}</p>;
    if (tenants === null) return <p className="muted pad">{tr("common.loading")}</p>;
    if (!tenant) return <p className="muted pad">{tr("tenant.none")}</p>;
    if (section === "rules") return <TenantLoginRulesView slug={tenant.slug} tenant={tenant} />;
    return <TenantSignInMethods slug={tenant.slug} isSuper={isSuper} />;
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
                  onChange={(e) => setSlug(e.target.value)}
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
            {GROUPS.map((g) => (
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
