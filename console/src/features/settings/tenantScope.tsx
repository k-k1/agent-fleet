// tenantScope — テナント 1 つ分の面（レールの並び＋本文）。
//
// 同じテナントを 2 人が別の入口から見る: テナント管理者はテナント設定モーダルで
// 自分のテナントを、デプロイ管理者は管理モーダルでどのテナントでも。中身が同じなのに
// IA が入口ごとに違うと、案内も操作も 2 通りになるので、レールの並びと本文の
// 差し分けをここ 1 つに置いて両方から差す（出し分けは props の isSuper だけ・
// サーバのゲートは触らない）。
//
// ★ 権限はサーバが持つ。上限の PUT も、サインイン方式の「承認して有効化」も
// withSuperAdmin / super_admin 固定（ADR0043 決定 19 / 決定 30）。ここの出し分けは
// 案内でしかない。
import { useEffect, useState } from "react";
import { apiJSON, errText } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { fmtGbHint } from "./adminShared.ts";
import type { Member, Tenant } from "./adminShared.ts";
import { TenantLoginRules, TenantLoginRulesView, TenantSignInMethods } from "./tenantLogin.tsx";
import { TenantNetworkView } from "./tenantNetwork.tsx";
import { MembersPanel, MemberView } from "./tenantMembers.tsx";
import { AllSessionsView, AuditView, UsageView } from "./tenantOps.tsx";
import { CloudCostAdminView } from "../cost/CloudCostView.tsx";
import { McpAdminView } from "./mcpAdmin.tsx";

export interface ScopeGroup {
  key: string;
  label: string;
  items: [string, string][];
}

// レールの並び。項目 = [セクションキー, i18n キー]。ここの順がそのまま表示順。
//
// 「運用」に並ぶ 5 つは、CP がテナント管理者に自テナント分を返す面（GET
// /api/admin/{sessions,usage,audit} と /api/admin/mcp-servers）。
export function tenantScopeGroups(opts: { cost: boolean }): ScopeGroup[] {
  const manage: [string, string][] = [
    ["members", "tenant.tab_members"],
    ["sessions", "tenant.tab_sessions"],
    ["usage", "tenant.tab_usage"],
    // クラウド費用は AWS の請求があるデプロイにしかない。レールから外すのでは
    // なく、そもそも項目を作らない（docs/67 §67.8）。
    ...(opts.cost ? ([["cost", "tenant.tab_cost"]] as [string, string][]) : []),
    ["audit", "tenant.tab_audit"],
    ["mcp", "tenant.tab_mcp"],
  ];
  return [
    {
      key: "tenant",
      label: "tenant.group_tenant",
      // 上限は「テナントそのもの」の話で、ログインにも運用にも属さない。決めるのは
      // デプロイ管理者だが、テナント管理者にも読める（読み取り専用で同じ場所）。
      items: [["limits", "tenant.tab_limits"]],
    },
    {
      key: "login",
      label: "tenant.group_login",
      items: [
        ["signin", "tenant.tab_signin"],
        ["rules", "tenant.tab_rules"],
        // 接続元の制限（docs/66）。ここだけテナント管理者が「書ける」面で、
        // 上の 2 つ（読み取り専用 / super_admin 承認）とは持ち主が違う。
        ["network", "tenant.tab_network"],
      ],
    },
    { key: "manage", label: "tenant.group_manage", items: manage },
  ];
}

export const TENANT_SCOPE_SECTIONS = tenantScopeGroups({ cost: true }).flatMap((g) =>
  g.items.map(([k]) => k),
);

// TenantSummary — テナントそのものの数字（人数・起動中のワークスペース・テナント
// 全体の上限）を読み取り専用で。決めるのはデプロイ管理者（PUT .../limits は
// withSuperAdmin 固定）なので、テナント管理者にはこちらを出す。
function TenantSummary({ tenant }: { tenant: Tenant | null }) {
  const tr = useT();
  if (!tenant) return <p className="muted pad">{tr("tenant.none")}</p>;
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

// TenantLimits — テナント全体の上限とアイドル自動停止（super_admin のみ）。
// 旧 AdminTab の TenantView からそのまま移した（1 枚の長いテナント詳細を、レールの
// 節に割った先の 1 つ）。
export function TenantLimits({
  slug,
  tenant,
  // home の休止 / バックアップは EC2 スロットプールのランタイムにしか無い。他では
  // 何もしない欄になるので、そもそも出さない。
  hasPool,
  onChanged,
}: {
  slug: string;
  tenant: Tenant | null | undefined;
  hasPool: boolean;
  onChanged: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [maxWs, setMaxWs] = useState<number | string>(tenant?.max_workspaces || 0);
  const [maxSs, setMaxSs] = useState<number | string>(tenant?.max_sessions || 0);
  const [maxRepos, setMaxRepos] = useState<number | string>(tenant?.max_git_repos || 0);
  // LFS cap is stored in bytes but edited in MB for usability.
  const [maxLfsMb, setMaxLfsMb] = useState<number | string>(Math.round((tenant?.max_lfs_bytes || 0) / 1048576));
  // Per-workspace RAM cap: stored in bytes, edited in MB.
  const [maxWsMemMb, setMaxWsMemMb] = useState<number | string>(Math.round((tenant?.max_workspace_mem || 0) / 1048576));
  const [sessIdle, setSessIdle] = useState(tenant?.session_idle_timeout || "");
  const [wsIdle, setWsIdle] = useState(tenant?.ws_idle_timeout || "");
  const [homeHib, setHomeHib] = useState(tenant?.home_hibernate_after || "");
  const [homeBackup, setHomeBackup] = useState(tenant?.home_backup_every || "");
  const [allowUpd, setAllowUpd] = useState(!!tenant?.allow_agent_self_update);
  const [termRetention, setTermRetention] = useState(tenant?.terminal_history_retention_days || 0);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setMaxWs(tenant?.max_workspaces || 0);
    setMaxSs(tenant?.max_sessions || 0);
    setMaxRepos(tenant?.max_git_repos || 0);
    setMaxLfsMb(Math.round((tenant?.max_lfs_bytes || 0) / 1048576));
    setMaxWsMemMb(Math.round((tenant?.max_workspace_mem || 0) / 1048576));
    setSessIdle(tenant?.session_idle_timeout || "");
    setWsIdle(tenant?.ws_idle_timeout || "");
    setHomeHib(tenant?.home_hibernate_after || "");
    setHomeBackup(tenant?.home_backup_every || "");
    setAllowUpd(!!tenant?.allow_agent_self_update);
    setTermRetention(tenant?.terminal_history_retention_days || 0);
  }, [slug, tenant]);

  const saveLimits = async () => {
    const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/limits`, "PUT", {
      max_workspaces: +maxWs || 0,
      max_sessions: +maxSs || 0,
      max_git_repos: +maxRepos || 0,
      max_lfs_bytes: Math.round(+maxLfsMb || 0) * 1048576,
      max_workspace_mem: Math.round(+maxWsMemMb || 0) * 1048576,
      session_idle_timeout: sessIdle.trim(),
      ws_idle_timeout: wsIdle.trim(),
      home_hibernate_after: homeHib.trim(),
      home_backup_every: homeBackup.trim(),
      allow_agent_self_update: allowUpd,
      terminal_history_retention_days: termRetention,
    });
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    setSaved(true);
    setTimeout(() => setSaved(false), 1500);
    onChanged();
  };

  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>{tr("admin.limits")}<span className="af-note">{tr("admin.zero_unlimited")}</span></h4>
        <div className="admin-fgrid">
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.max_workspace")}</span>
            <input type="number" min="0" value={maxWs} onChange={(e) => setMaxWs(e.target.value)} />
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.max_session")}</span>
            <input type="number" min="0" value={maxSs} onChange={(e) => setMaxSs(e.target.value)} />
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.max_repos")}</span>
            <input type="number" min="0" value={maxRepos} onChange={(e) => setMaxRepos(e.target.value)} />
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.max_lfs")}</span>
            <span className="af-inputwrap">
              <input type="number" min="0" value={maxLfsMb} onChange={(e) => setMaxLfsMb(e.target.value)} />
              <span className="af-suffix">MB</span>
            </span>
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.max_ws_mem")}</span>
            <span className="af-inputwrap">
              <input type="number" min="0" step="256" value={maxWsMemMb} onChange={(e) => setMaxWsMemMb(e.target.value)} />
              <span className="af-suffix">MB</span>
            </span>
            <span className="af-unit">{+maxWsMemMb > 0 ? tr("admin.per_container", { hint: fmtGbHint(+maxWsMemMb) }) : tr("admin.zero_no_tenant_cap")}</span>
          </label>
        </div>
        <p className="admin-hint">
          {tr("admin.ws_mem_hint_1")}<code>WS_MEMORY</code>{tr("admin.ws_mem_hint_2")}<code>AF_MAX_WORKSPACE_MEM</code>{tr("admin.ws_mem_hint_3")}<b>{tr("admin.ws_mem_hint_bold")}</b>{tr("admin.ws_mem_hint_4")}
        </p>
      </div>

      <div className="admin-fgroup">
        <h4>{tr("admin.idle_autostop")}<span className="af-note">{tr("admin.empty_deploy_default")}</span></h4>
        <div className="admin-fgrid">
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.session_halt")}</span>
            <input type="text" placeholder={tr("admin.idle_ph_30m")} value={sessIdle} onChange={(e) => setSessIdle(e.target.value)} />
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.ws_stop")}</span>
            <input type="text" placeholder={tr("admin.idle_ph_60m")} value={wsIdle} onChange={(e) => setWsIdle(e.target.value)} />
          </label>
        </div>
        <p className="admin-hint">
          {tr("admin.idle_hint_1")}<code>30m</code> / <code>2h</code> / <code>90s</code>{tr("admin.idle_hint_2")}<code>0</code>{tr("admin.idle_hint_3")}
        </p>
      </div>

      {/* Only the EC2 slot pool has somewhere cheaper to put a home; on the other
          runtimes this field would be a control that does nothing. */}
      {hasPool && (
        <div className="admin-fgroup">
          <h4>{tr("admin.hibernate_title")}<span className="af-note">{tr("admin.empty_deploy_default")}</span></h4>
          <div className="admin-fgrid">
            <label className="admin-fld">
              <span className="af-cap">{tr("admin.hibernate_after")}</span>
              <input type="text" placeholder={tr("admin.hibernate_ph")} value={homeHib} onChange={(e) => setHomeHib(e.target.value)} />
            </label>
          </div>
          <p className="admin-hint">{tr("admin.hibernate_hint")}</p>
          <p className="admin-hint">{tr("admin.hibernate_warn")}</p>
        </div>
      )}

      {/* home が 1 つの AZ に固定されるのはこのランタイムだけなので、AZ ごと失う話も
          ここにしか無い。他では home はそもそも 1 AZ に縛られていない。 */}
      {hasPool && (
        <div className="admin-fgroup">
          <h4>{tr("admin.backup_title")}<span className="af-note">{tr("admin.empty_deploy_default")}</span></h4>
          <div className="admin-fgrid">
            <label className="admin-fld">
              <span className="af-cap">{tr("admin.backup_every")}</span>
              <input type="text" placeholder={tr("admin.backup_ph")} value={homeBackup} onChange={(e) => setHomeBackup(e.target.value)} />
            </label>
          </div>
          <p className="admin-hint">{tr("admin.backup_hint")}</p>
          <p className="admin-hint">{tr("admin.backup_warn")}</p>
        </div>
      )}

      <div className="admin-fgroup">
        <h4>{tr("admin.term_log_title")}</h4>
        <label className="admin-fld">
          <span className="af-cap">{tr("admin.retention")}</span>
          <select value={termRetention} onChange={(e) => setTermRetention(Number(e.target.value))}>
            <option value={0}>{tr("admin.retention_off")}</option>
            <option value={1}>{tr("admin.days_1")}</option>
            <option value={7}>{tr("admin.days_7")}</option>
            <option value={30}>{tr("admin.days_30")}</option>
          </select>
        </label>
        <p className="admin-hint">{tr("admin.term_log_hint")}</p>
      </div>

      <div className="admin-fgroup">
        <h4>{tr("admin.agent_cli_update")}</h4>
        <label className="admin-check">
          <input type="checkbox" checked={allowUpd} onChange={(e) => setAllowUpd(e.target.checked)} />
          <span>{tr("admin.allow_self_update")}</span>
        </label>
        <p className="admin-hint">{tr("admin.allow_self_update_hint")}</p>
      </div>

      <div className="admin-actions">
        <button onClick={saveLimits} className="primary">{tr("common.save")}</button>
        {saved && <span className="saved-note"><Icon name="check" /> {tr("admin.saved")}</span>}
      </div>
    </section>
  );
}

// TenantScopeBody — 選ばれたセクションの中身。メンバーは「一覧 → 詳細」の 2 段で、
// 段の状態（member）は呼び出し側が持つ: 端末の戻るをどう積むかは面ごとに違うため
// （テナント設定は詳細 → レール、管理はさらにテナント一覧まで戻る）。
export function TenantScopeBody({
  slug,
  tenant,
  section,
  isSuper,
  hasPool = false,
  member,
  onOpenMember,
  onCloseMember,
  onChanged,
}: {
  slug: string;
  tenant: Tenant | null;
  section: string;
  /** サーバが返した super_admin。上限の編集・規則の編集・サインイン方式の承認の出し分け。 */
  isSuper: boolean;
  hasPool?: boolean;
  member: Member | null;
  onOpenMember: (m: Member) => void;
  onCloseMember: () => void;
  onChanged: () => void;
}) {
  const tr = useT();
  if (section === "limits") {
    return isSuper ? (
      <TenantLimits slug={slug} tenant={tenant} hasPool={hasPool} onChanged={onChanged} />
    ) : (
      <TenantSummary tenant={tenant} />
    );
  }
  // ★ 規則は「テナント管理者には読み取り専用」（PUT が withSuperAdmin 固定）だが、
  //   super_admin がこの面に居るときまで読み取り専用にすると、「変更できるのは
  //   デプロイ管理者だけです」と書いてある画面を当のデプロイ管理者が眺めることになる。
  if (section === "rules") {
    return isSuper ? (
      <TenantLoginRules slug={slug} tenant={tenant} onChanged={onChanged} />
    ) : (
      <TenantLoginRulesView slug={slug} tenant={tenant} />
    );
  }
  if (section === "network") return <TenantNetworkView key={slug} slug={slug} />;
  if (section === "members") {
    if (member) {
      return (
        <>
          <div className="admin-nav tenant-drill">
            <button type="button" className="admin-back" onClick={onCloseMember}>
              <Icon name="arrow-left" /> {tr("common.back")}
            </button>
            <nav className="admin-crumbs">
              <button type="button" className="crumb" onClick={onCloseMember}>
                {tr("tenant.tab_members")}
              </button>
              <Icon name="chevron-right" className="crumb-sep" />
              <span className="crumb here">{member.user_key}</span>
            </nav>
          </div>
          <MemberView
            slug={slug}
            member={member}
            isSuper={isSuper}
            onChanged={onChanged}
            // 外した人の詳細を開いたままにしない — 一覧へ戻して読み直す。
            onRemoved={() => {
              onCloseMember();
              onChanged();
            }}
          />
        </>
      );
    }
    return <MembersPanel slug={slug} isSuper={isSuper} onOpenMember={onOpenMember} />;
  }
  // ★ 運用の面には、この画面が見ているテナント 1 つだけを渡す。isSuper={false} は
  // 「あなたはデプロイ管理者ではない」ではなく「この面はテナントを跨がない」の意味で、
  // テナント選択欄を出さないための指定（誰の分が返るかはサーバが決める）。slug を key に
  // しているのは、テナントを切り替えたらポーリングごと張り直すため。
  const one = tenant ? [tenant] : [];
  if (section === "sessions") return <AllSessionsView key={slug} tenants={one} isSuper={false} />;
  if (section === "usage") return <UsageView key={slug} tenants={one} isSuper={false} />;
  if (section === "cost") return <CloudCostAdminView key={slug} tenants={one} isSuper={false} />;
  if (section === "audit") return <AuditView key={slug} tenants={one} isSuper={false} />;
  if (section === "mcp") return <McpAdminView key={slug} tenants={one} />;
  return <TenantSignInMethods slug={slug} isSuper={isSuper} />;
}
