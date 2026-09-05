// tenantScope — the view of a single tenant (rail order plus body).
//
// The same tenant is reached from two entry points: a tenant admin sees their own tenant in
// the tenant settings modal, a deployment admin sees any tenant in the admin modal. If the
// information architecture differed per entry point, both the documentation and the operation
// would have to exist twice, so the rail order and the body switch live here once and are
// used from both (the only switch is the isSuper prop; server gates are untouched).
//
// Permission is the server's. Both the limits PUT and "approve and enable" for a sign-in
// method are fixed at withSuperAdmin / super_admin (ADR0043 decision 19 / decision 30); what
// is switched here is presentation only.
import { useEffect, useState } from "react";
import { apiJSON, errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { ConfirmDialog } from "../../../ui/ConfirmDialog.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { fmtGbHint } from "../parts/adminShared.ts";
import type { Member, Tenant } from "../parts/adminShared.ts";
import { TenantLoginRules, TenantLoginRulesView } from "./tenantLoginRules.tsx";
import { TenantSignInMethods } from "./tenantSignInMethods.tsx";
import { PoolBudgetHint, type PoolBudget } from "./ec2Pool.tsx";
import { TenantNetworkView } from "./tenantNetwork.tsx";
import { TenantGitOAuthView } from "./tenantGitOAuth.tsx";
import { TenantMachineView } from "./tenantMachine.tsx";
import { MembersPanel } from "./tenantMembers.tsx";
import { MemberView } from "./tenantMemberDetail.tsx";
import { AllSessionsView, AuditView, UsageView } from "./tenantOps.tsx";
import { CloudCostAdminView } from "../../cost/CloudCostView.tsx";
import { McpAdminView } from "../mcp/mcpAdmin.tsx";

export interface ScopeGroup {
  key: string;
  label: string;
  items: [string, string][];
}

// The rail order. An item is [section key, i18n key]; this order is the display order.
//
// The five under "manage" are the views where the CP returns a tenant admin their own tenant's
// rows (GET /api/admin/{sessions,usage,audit} and /api/admin/mcp-servers).
export function tenantScopeGroups(opts: { cost: boolean }): ScopeGroup[] {
  const manage: [string, string][] = [
    ["members", "tenant.tab_members"],
    ["sessions", "tenant.tab_sessions"],
    ["usage", "tenant.tab_usage"],
    // Cloud cost only exists on a deployment with AWS billing: the item is not created at all
    // rather than being created and then hidden from the rail (docs/log/67 §67.8).
    ...(opts.cost ? ([["cost", "tenant.tab_cost"]] as [string, string][]) : []),
    ["audit", "tenant.tab_audit"],
    ["mcp", "tenant.tab_mcp"],
  ];
  return [
    {
      key: "tenant",
      label: "tenant.group_tenant",
      // Limits are about the tenant itself, so they belong to neither login nor manage. The
      // deployment admin decides them, but a tenant admin can read them in the same place.
      items: [["limits", "tenant.tab_limits"]],
    },
    {
      key: "login",
      label: "tenant.group_login",
      items: [
        ["signin", "tenant.tab_signin"],
        ["rules", "tenant.tab_rules"],
        // Source-address restriction (docs/log/66). This is the one view here a tenant admin
        // can write; the two above are read-only / super_admin-approved and have another owner.
        ["network", "tenant.tab_network"],
      ],
    },
    // Integrations = registering credentials the tenant created on an external service. That is
    // neither login nor manage, so it is its own group (docs/log/71 §71.4). Today it holds only
    // the git provider OAuth app, but this is the group that grows.
    {
      key: "integrations",
      label: "tenant.group_integrations",
      items: [["git-oauth", "tenant.tab_git_oauth"]],
    },
    { key: "manage", label: "tenant.group_manage", items: manage },
  ];
}

export const TENANT_SCOPE_SECTIONS = tenantScopeGroups({ cost: true }).flatMap((g) =>
  g.items.map(([k]) => k),
);

// TenantSummary — the tenant's own numbers (members, running workspaces, tenant-wide limits),
// read-only. The deployment admin decides them (PUT .../limits is fixed at withSuperAdmin), so
// this is what a tenant admin is shown instead of the editor.
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

// TenantLimits — the tenant-wide limits and idle auto-stop (super_admin only).
export function TenantLimits({
  slug,
  tenant,
  // Home hibernate / backup only exist on the EC2 slot-pool runtime; elsewhere the fields
  // would do nothing, so they are not rendered at all.
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
  // A clock used only while waiting on a human decision (question, plan approval, permission).
  // Empty means: follow the session-halt timeout above.
  const [interIdle, setInterIdle] = useState(tenant?.interaction_idle_timeout || "");
  const [homeHib, setHomeHib] = useState(tenant?.home_hibernate_after || "");
  const [homeBackup, setHomeBackup] = useState(tenant?.home_backup_every || "");
  const [allowUpd, setAllowUpd] = useState(!!tenant?.allow_agent_self_update);
  const [termRetention, setTermRetention] = useState(tenant?.terminal_history_retention_days || 0);
  const [saved, setSaved] = useState(false);
  // Set only when the save response reports that the tenant limits no longer fit in the pool.
  const [budget, setBudget] = useState<PoolBudget | null>(null);

  useEffect(() => {
    setMaxWs(tenant?.max_workspaces || 0);
    setMaxSs(tenant?.max_sessions || 0);
    setMaxRepos(tenant?.max_git_repos || 0);
    setMaxLfsMb(Math.round((tenant?.max_lfs_bytes || 0) / 1048576));
    setMaxWsMemMb(Math.round((tenant?.max_workspace_mem || 0) / 1048576));
    setSessIdle(tenant?.session_idle_timeout || "");
    setWsIdle(tenant?.ws_idle_timeout || "");
    setInterIdle(tenant?.interaction_idle_timeout || "");
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
      interaction_idle_timeout: interIdle.trim(),
      home_hibernate_after: homeHib.trim(),
      home_backup_every: homeBackup.trim(),
      allow_agent_self_update: allowUpd,
      terminal_history_retention_days: termRetention,
    });
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    // The save went through: exceeding the pool is a warning, not a rejection (see the reason
    // on the server side in setTenantLimits). Keep the note about the numbers just entered
    // alongside the saved indicator; a toast would disappear and simply go unread.
    setBudget(res?.pool_budget || null);
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
            {/* State next to the field what the limit counts. On a slot-pool deployment,
                reading it as "how many instances this tenant occupies" is always wrong: a
                stopped workspace still holds its instance but is not counted here. */}
            <span className="af-note">{tr("admin.max_workspace_note")}</span>
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
            <span className="af-cap">{tr("admin.interaction_halt")}</span>
            <input type="text" placeholder={tr("admin.interaction_ph")} value={interIdle} onChange={(e) => setInterIdle(e.target.value)} />
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.ws_stop")}</span>
            <input type="text" placeholder={tr("admin.idle_ph_60m")} value={wsIdle} onChange={(e) => setWsIdle(e.target.value)} />
          </label>
        </div>
        <p className="admin-hint">
          {tr("admin.idle_hint_1")}<code>30m</code> / <code>2h</code> / <code>90s</code>{tr("admin.idle_hint_2")}<code>0</code>{tr("admin.idle_hint_3")}
        </p>
        <p className="admin-hint">{tr("admin.interaction_hint")}</p>
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

      {/* Only on this runtime is home pinned to a single AZ, so losing a whole AZ is a concern
          only here; elsewhere home is not bound to one AZ at all. */}
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
      {/* The save already happened. Placed below the save button so it reads as "saved, but it
          does not fit this deployment's slots" rather than "the save failed". */}
      {budget && <PoolBudgetHint budget={budget} />}
    </section>
  );
}

// TenantDeletePanel — delete a tenant (docs/log/61 §61.18, super_admin only).
//
// Placed at the end of the limits section, which is the "tenant itself" section rather than
// members, login or manage. Only reachable from the admin modal (the tenant settings modal
// passes no onDelete): a tenant's own settings screen has no business offering to delete it.
//
// The refusal reason is shown verbatim from the server. Each one is a variation of "what is
// not empty cannot be deleted", phrased as "do X first".
function TenantDeletePanel({ slug, onDeleted }: { slug: string; onDeleted: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const del = async () => {
    setBusy(true);
    try {
      const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}`, "DELETE", {});
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setConfirm(false);
      onDeleted();
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>{tr("admin.delete_tenant_title")}</h4>
        <p className="admin-hint">{tr("admin.delete_tenant_hint")}</p>
        <p className="admin-hint">{tr("admin.delete_tenant_repo_hint")}</p>
        <div className="admin-actions">
          <button className="danger-btn" disabled={busy} onClick={() => setConfirm(true)}>
            <Icon name="trash" /> {tr("admin.delete_tenant")}
          </button>
        </div>
      </div>
      {confirm && (
        <ConfirmDialog
          title={tr("admin.delete_tenant_confirm_title", { slug })}
          confirmLabel={tr("admin.delete_tenant_confirm")}
          busy={busy}
          onCancel={() => setConfirm(false)}
          onConfirm={del}
        >
          <p>{tr("admin.delete_tenant_body")}</p>
          <p className="muted">{tr("admin.delete_tenant_kept")}</p>
        </ConfirmDialog>
      )}
    </section>
  );
}

// TenantScopeBody — the body of the selected section. Members is two stages, list then detail,
// and the stage state (member) is owned by the caller because the back stack differs per entry
// point (tenant settings goes detail -> rail; admin goes on back to the tenant list).
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
  onDeleted,
}: {
  slug: string;
  tenant: Tenant | null;
  section: string;
  /** super_admin as returned by the server. Switches editing limits and rules, and approving
   *  a sign-in method. */
  isSuper: boolean;
  hasPool?: boolean;
  member: Member | null;
  onOpenMember: (m: Member) => void;
  onCloseMember: () => void;
  onChanged: () => void;
  /** Delete tenant is shown only when this is passed (admin modal only, docs/log/61 §61.18). */
  onDeleted?: () => void;
}) {
  const tr = useT();
  // The machine class belongs to the tenant admin, so unlike the limits themselves it is not
  // switched on isSuper (docs/log/70 §70.4.3; the PUT is gated by tenantAdminFor). On a
  // deployment with a single class the component renders nothing, so no condition is needed.
  if (section === "limits") {
    return (
      <>
        {isSuper ? (
          <TenantLimits slug={slug} tenant={tenant} hasPool={hasPool} onChanged={onChanged} />
        ) : (
          <TenantSummary tenant={tenant} />
        )}
        <TenantMachineView key={slug} slug={slug} />
        {/* Delete goes last: a destructive action never sits above the two everyday views. */}
        {isSuper && onDeleted && <TenantDeletePanel slug={slug} onDeleted={onDeleted} />}
      </>
    );
  }
  // The rules are read-only for a tenant admin (the PUT is fixed at withSuperAdmin), but making
  // them read-only for a super_admin too would leave the deployment admin staring at a screen
  // that says only the deployment admin can change this.
  if (section === "rules") {
    return isSuper ? (
      <TenantLoginRules slug={slug} tenant={tenant} onChanged={onChanged} />
    ) : (
      <TenantLoginRulesView slug={slug} tenant={tenant} />
    );
  }
  if (section === "network") return <TenantNetworkView key={slug} slug={slug} />;
  // Not switched on isSuper: the git provider OAuth app belongs to the tenant admin, and its
  // PUT is gated by tenantAdminFor (ADR 0052 decision 3).
  if (section === "git-oauth") return <TenantGitOAuthView key={slug} slug={slug} />;
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
            // Never leave a removed member's detail open: go back to the list and reload.
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
  // The manage views get only the single tenant this screen is looking at. isSuper={false} here
  // does not mean "you are not a deployment admin"; it means "this view does not cross tenants"
  // and suppresses the tenant selector (the server still decides whose rows come back). slug is
  // the key so switching tenants re-establishes the polling as well.
  const one = tenant ? [tenant] : [];
  if (section === "sessions") return <AllSessionsView key={slug} tenants={one} isSuper={false} />;
  if (section === "usage") return <UsageView key={slug} tenants={one} isSuper={false} />;
  if (section === "cost") return <CloudCostAdminView key={slug} tenants={one} isSuper={false} />;
  if (section === "audit") return <AuditView key={slug} tenants={one} isSuper={false} />;
  if (section === "mcp") return <McpAdminView key={slug} tenants={one} />;
  // tenant is passed because this view carries two toggles, "accept" and "show as a button"
  // (docs/log/61 §61.17.5). Only a super_admin can flip them, and the rules PUT stays fixed at
  // withSuperAdmin (decision 19). onChanged reloads the four columns.
  return <TenantSignInMethods slug={slug} isSuper={isSuper} tenant={tenant} onChanged={onChanged} />;
}
