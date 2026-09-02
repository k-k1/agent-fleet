import { useCallback, useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { mobileMatches } from "../../lib/device.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { Member, Tenant } from "./adminShared.ts";
import { AllSessionsView, AuditView, UsageView } from "./tenantOps.tsx";
import { CloudCostAdminView, useCostProfile } from "../cost/CloudCostView.tsx";
import { McpAdminView } from "./mcpAdmin.tsx";
import { PoolView } from "./ec2Pool.tsx";
import { SignInMethodRegister } from "./tenantSignInMethods.tsx";
// テナント 1 つ分の面（レールの並びも本文も）はテナント設定モーダルと共有する。
// 同じテナントを別の入口から見るだけなので、IA を入口ごとに分けない。
import { TenantScopeBody, tenantScopeGroups } from "./tenantScope.tsx";
import { EgressView } from "./adminEgress.tsx";
import { TtsAdminView } from "./adminTts.tsx";
import { TenantsList } from "./adminTenants.tsx";

// AdminTab（super_admin の面）— 個人設定・テナント設定と同じ左レール＋本文の二枚看板。
//
// レールは 2 段になっている:
//   ルート  … テナント一覧 / 登録簿・デプロイ全体（通信・読み上げ・スロット）・
//              横断で見る（セッション・稼働時間・費用・監査・MCP 配布）
//   テナント… 一覧からテナントを開くとレールごと切り替わり、そのテナントの
//              上限 / ログイン / 運用の節が並ぶ（テナント設定モーダルと同じ並び）。
//              メンバーは本文の中でもう 1 段ドリルする。
//
// ★ 旧実装は横一列のセグメントタブ 9 個＋その中だけパンくずドリルダウン、という
// 二重のナビだった。設定モーダルが「タブ 6 超で破綻する」として捨てた形がここに
// 残っていたもので、スマホでは横スクロールとスワイプの回避策まで要っていた。
// レール化でその 2 つは不要になり、戻る（端末/ブラウザ）も ui/Modal と同じ
// useBackClose の層に一本化した（独自 history エントリは撤去）。

interface RailGroup {
  key: string;
  label: string;
  items: [string, string][];
}

function rootGroups(opts: { pool: boolean; cost: boolean }): RailGroup[] {
  return [
    {
      key: "tenants",
      label: "admin.group_tenants",
      items: [
        ["tenants", "admin.tenants_list"],
        // テナントが定義したサインイン方法の登録簿（docs/log/61 §61.11.6）。承認できるのは
        // デプロイ管理者だけなので、デプロイ管理者にしか無い面。
        ["register", "admin.tab_register"],
      ],
    },
    {
      key: "deployment",
      label: "admin.group_deployment",
      items: [
        ["egress", "admin.mode_egress"],
        ["tts", "admin.mode_tts"],
        // スロットプールは 1 つのランタイムにしか無い。Fargate のデプロイに空の
        // 「スロット」が出ると「自分のスロットが消えた」と読める。
        ...(opts.pool ? ([["pool", "admin.mode_pool"]] as [string, string][]) : []),
      ],
    },
    {
      key: "across",
      label: "admin.group_across",
      items: [
        ["sessions", "admin.mode_sessions"],
        ["usage", "admin.mode_usage"],
        // docker / native には AWS の請求が無い。0 が並ぶ費用の面は「無料」と読める
        // ので、項目ごと作らない（docs/log/67 §67.8・ADR 0048 決定 9）。
        ...(opts.cost ? ([["cost", "admin.mode_cost"]] as [string, string][]) : []),
        ["audit", "admin.mode_audit"],
        ["mcp", "admin.mode_mcp"],
      ],
    },
  ];
}

export function AdminTab() {
  const tr = useT();
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  const [forbidden, setForbidden] = useState(false);
  const [isSuper, setIsSuper] = useState(false); // super_admin: unlocks deployment-wide controls
  // Whether this deployment HAS a slot pool. One cheap probe at mount; the endpoint
  // answers {"runtime":"other"} everywhere else.
  const [hasPool, setHasPool] = useState(false);
  // Whether this deployment HAS an AWS bill. Runtime-declared, not configured.
  const costProfile = useCostProfile();

  // scope = 開いているテナントの slug（null＝ルート）。section はレールの現在地で、
  // ルートとテナントで別々に覚える（テナントを閉じても一覧に戻ってくるだけ）。
  const [scope, setScope] = useState<string | null>(null);
  const [rootSection, setRootSection] = useState("tenants");
  const [scopeSection, setScopeSection] = useState("limits");
  const [member, setMember] = useState<Member | null>(null);
  // スマホは個人設定と同じドリルダウン（レール → 本文、戻るでレールへ）。
  const [entered, setEntered] = useState(false);

  // 戻るの層は積んだ順に剥がれる（useBackClose の共有スタック）。宣言順＝
  // レール ↓ テナント ↓ メンバーなので、端末の戻るは メンバー → テナント →
  // レール → モーダルを閉じる、の順で 1 段ずつ戻る。
  useBackClose(() => setEntered(false), mobileMatches() && entered);
  const leaveScope = useCallback(() => {
    setScope(null);
    setMember(null);
    // スマホではテナント一覧（本文）へ戻す。レールに戻してしまうと、どのテナントから
    // 出てきたのか分からなくなる。
    if (mobileMatches()) setEntered(true);
  }, []);
  useBackClose(leaveScope, !!scope);
  useBackClose(() => setMember(null), !!member);

  const loadTenants = useCallback(async () => {
    try {
      const d = await api("api/admin/tenants");
      if (d && d.error) {
        setForbidden(true);
        return;
      }
      setTenants(d.tenants || []);
      setIsSuper(!!d.super_admin);
    } catch {
      setForbidden(true);
    }
  }, []);
  useEffect(() => {
    loadTenants();
  }, [loadTenants]);
  useEffect(() => {
    // super_admin only, so a tenant_admin simply never sees the tab (the endpoint 403s
    // and this stays false).
    api("api/admin/ec2-pool")
      .then((d) => setHasPool(d?.runtime === "ecs-ec2"))
      .catch(() => setHasPool(false));
  }, []);

  if (forbidden) return <p className="muted pad">{tr("admin.forbidden")}</p>;
  if (tenants === null) return <p className="muted pad">{tr("common.loading")}</p>;

  const cost = !!costProfile?.available;
  const groups = scope ? tenantScopeGroups({ cost }) : rootGroups({ pool: hasPool, cost });
  const section = scope ? scopeSection : rootSection;
  const scopeTenant = scope ? tenants.find((t) => t.slug === scope) || null : null;
  const currentLabel = tr(
    (groups.flatMap((g) => g.items).find(([k]) => k === section)?.[1] ??
      "admin.title") as Parameters<typeof tr>[0],
  );

  const openTenant = (slug: string) => {
    setScope(slug);
    setScopeSection("limits");
    setMember(null);
    // スマホ: テナントを開いたらそのテナントのレールを見せる（次に選ぶのは節）。
    if (mobileMatches()) setEntered(false);
  };

  const pick = (key: string) => {
    if (scope) setScopeSection(key);
    else setRootSection(key);
    setMember(null);
    setEntered(true);
  };

  const body = () => {
    if (scope) {
      return (
        <TenantScopeBody
          slug={scope}
          tenant={scopeTenant}
          section={scopeSection}
          isSuper={isSuper}
          hasPool={hasPool}
          member={member}
          onOpenMember={setMember}
          onCloseMember={() => setMember(null)}
          onChanged={loadTenants}
          // テナントが消えたら、その中の面に留まる意味が無い——一覧へ戻して読み直す。
          onDeleted={() => {
            leaveScope();
            loadTenants();
          }}
        />
      );
    }
    if (rootSection === "register") return <SignInMethodRegister />;
    if (rootSection === "egress") return <EgressView />;
    if (rootSection === "tts") return <TtsAdminView />;
    if (rootSection === "pool" && hasPool) return <PoolView />;
    if (rootSection === "sessions") return <AllSessionsView tenants={tenants} isSuper={isSuper} />;
    if (rootSection === "usage") return <UsageView tenants={tenants} isSuper={isSuper} />;
    if (rootSection === "cost" && cost) return <CloudCostAdminView tenants={tenants} isSuper={isSuper} />;
    if (rootSection === "audit") return <AuditView tenants={tenants} isSuper={isSuper} />;
    if (rootSection === "mcp") return <McpAdminView tenants={tenants} />;
    return <TenantsList tenants={tenants} isSuper={isSuper} onReload={loadTenants} onOpen={openTenant} />;
  };

  return (
    <div className={"settings-layout" + (entered ? " entered" : "")}>
      <nav className="settings-rail" aria-label={tr("admin.title")}>
        {/* テナントを開いている間はレールごとそのテナントの中身に入れ替わる。
            見出しは今どこに居るか（テナント名）、その上が出口。 */}
        {scope && (
          <div className="settings-rail-group admin-scope-head">
            <button type="button" className="admin-rail-back" onClick={leaveScope}>
              <Icon name="arrow-left" /> {tr("admin.all_tenants_back")}
            </button>
            <div className="admin-scope-name">
              <Icon name="organization" /> {scopeTenant?.name || scope}
            </div>
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
                onClick={() => pick(key)}
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
            ‹ {scope ? scopeTenant?.name || scope : tr("admin.title")}
          </button>
          <span className="settings-current" aria-current="page">
            {currentLabel}
          </span>
        </div>
        <div className="admin">{body()}</div>
      </div>
    </div>
  );
}

// --- Egress: allowlist + mode + observations (docs/log/20 M2/M3) -----------------
// Deployment-wide egress control (super_admin). Manages the versioned allowlist
// (approve agent-proposed entries, add/retire), toggles log-only vs enforce, and
// shows destination stats from the forward proxy (would-allow / would-block).
