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
import { Icon } from "../../ui/Icon.tsx";
import { Modal } from "../../ui/Modal.tsx";
import { TenantLoginRulesView, TenantSignInMethods } from "./tenantLogin.tsx";
import { MembersPanel, MemberView } from "./tenantMembers.tsx";
import { AllSessionsView, AuditView, UsageView } from "./tenantOps.tsx";
import { McpAdminView } from "./mcpAdmin.tsx";
import type { Member, Tenant } from "./adminShared.ts";

// レールの並び。項目 = [セクションキー, i18n キー]。ここの順がそのまま表示順。
//
// 「運用」に並ぶ 4 つは、CP が tenant_admin に自テナント分を返す面（GET
// /api/admin/{sessions,usage,audit} と /api/admin/mcp-servers）。管理モーダルにしか
// 置き場が無かったので、デプロイ管理者と同じ画面を通らないと自分の部署のことすら
// 見られなかった。
const GROUPS: { key: string; label: string; items: [string, string][] }[] = [
  {
    key: "login",
    label: "tenant.group_login",
    items: [
      ["signin", "tenant.tab_signin"],
      ["rules", "tenant.tab_rules"],
    ],
  },
  {
    key: "manage",
    label: "tenant.group_manage",
    items: [
      ["members", "tenant.tab_members"],
      ["sessions", "tenant.tab_sessions"],
      ["usage", "tenant.tab_usage"],
      ["audit", "tenant.tab_audit"],
      ["mcp", "tenant.tab_mcp"],
    ],
  },
];
const ALL_SECTIONS = GROUPS.flatMap((g) => g.items.map(([k]) => k));

// TenantSummary — テナントそのものの数字（人数・起動中のワークスペース・テナント
// 全体の上限）。管理モーダルではテナントカードに出ていて、テナント管理者もそこで
// 読めた。入口を閉じるとこれだけが行き場を失うので、名簿の頭に読み取り専用で残す。
// 決めるのはデプロイ管理者（PUT .../limits は withSuperAdmin 固定）。
function TenantSummary({ tenant }: { tenant: Tenant }) {
  const tr = useT();
  return (
    <section className="admin-panel tenant-summary">
      <h4>
        {tenant.name || tenant.slug}
        <span className="af-note">{tr("tenant.summary_note")}</span>
      </h4>
      <div className="tc-stats">
        <span>
          <Icon name="person" /> {tr("admin.person_count", { n: tenant.users ?? 0 })}
        </span>
        <span className={(tenant.running || 0) > 0 ? "tc-run on" : "tc-run"}>
          <Icon name="vm-running" /> {tr("admin.running_ws", { n: tenant.running ?? 0 })}
        </span>
      </div>
      <p className="admin-hint">
        {tr("admin.tenant_limits", { ws: tenant.max_workspaces || "∞", ss: tenant.max_sessions || "∞" })}
      </p>
    </section>
  );
}

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
    (GROUPS.flatMap((g) => g.items).find(([k]) => k === section)?.[1] ??
      "tenant.title") as Parameters<typeof tr>[0],
  );

  const body = () => {
    if (forbidden) return <p className="muted pad">{tr("tenant.forbidden")}</p>;
    if (tenants === null) return <p className="muted pad">{tr("common.loading")}</p>;
    if (!tenant) return <p className="muted pad">{tr("tenant.none")}</p>;
    if (section === "rules") return <TenantLoginRulesView slug={tenant.slug} tenant={tenant} />;
    if (section === "members") {
      if (member) {
        return (
          <>
            <div className="admin-nav tenant-drill">
              <button type="button" className="admin-back" onClick={() => setMember(null)}>
                <Icon name="arrow-left" /> {tr("common.back")}
              </button>
              <nav className="admin-crumbs">
                <button type="button" className="crumb" onClick={() => setMember(null)}>
                  {tr("tenant.tab_members")}
                </button>
                <Icon name="chevron-right" className="crumb-sep" />
                <span className="crumb here">{member.user_key}</span>
              </nav>
            </div>
            <MemberView
              slug={tenant.slug}
              member={member}
              isSuper={isSuper}
              onChanged={load}
              // 外した人の詳細を開いたままにしない — 一覧へ戻して読み直す。
              onRemoved={() => {
                setMember(null);
                load();
              }}
            />
          </>
        );
      }
      return (
        <>
          <TenantSummary tenant={tenant} />
          <MembersPanel slug={tenant.slug} isSuper={isSuper} onOpenMember={setMember} />
        </>
      );
    }
    // ★ 運用の 3 面には、この画面が見ているテナント 1 つだけを渡す。isSuper={false}
    // は「あなたはデプロイ管理者ではない」ではなく「この画面はテナントを跨がない」
    // の意味で、テナント選択欄を出さないための指定（誰の分が返るかはサーバが決める）。
    // slug を key にしているのは、テナントを切り替えたらポーリングごと張り直すため。
    if (section === "sessions") return <AllSessionsView key={tenant.slug} tenants={[tenant]} isSuper={false} />;
    if (section === "usage") return <UsageView key={tenant.slug} tenants={[tenant]} isSuper={false} />;
    if (section === "audit") return <AuditView key={tenant.slug} tenants={[tenant]} isSuper={false} />;
    if (section === "mcp") return <McpAdminView key={tenant.slug} tenants={[tenant]} />;
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
