import { useCallback, useEffect, useState } from "react";
import { api } from "../../../core/api/client.ts";
import { mobileMatches } from "../../../lib/device.ts";
import { useBackClose } from "../../../lib/backClose.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import type { Member, Tenant } from "../parts/adminShared.ts";
import { AllSessionsView, AuditView, UsageView } from "../tenant/tenantOps.tsx";
import { CloudCostAdminView, useCostProfile } from "../../cost/CloudCostView.tsx";
import { McpAdminView } from "../mcp/mcpAdmin.tsx";
import { PoolView } from "../tenant/ec2Pool.tsx";
import { SignInMethodRegister } from "../tenant/tenantSignInMethods.tsx";
// The per-tenant surface (both the rail order and the body) is shared with the tenant settings
// modal: the same tenant seen from a different entry point must not get a different IA.
import { TenantScopeBody, tenantScopeGroups } from "../tenant/tenantScope.tsx";
import { EgressView } from "./adminEgress.tsx";
import { TtsAdminView } from "./adminTts.tsx";
import { TenantsList } from "./adminTenants.tsx";

// AdminTab (the super_admin surface) — the same left rail + body two-pane shell as personal and
// tenant settings.
//
// The rail has two levels:
//   root   … tenant list / sign-in registry, deployment-wide (egress, TTS, slots), and
//            cross-cutting views (sessions, uptime, cost, audit, MCP distribution)
//   tenant … opening a tenant from the list swaps the whole rail for that tenant's quota /
//            login / operations sections, in the same order as the tenant settings modal.
//            Members drill one more level inside the body.
//
// A rail rather than a row of segmented tabs: a single row does not survive past ~6 tabs, and
// the old shape needed horizontal scrolling plus a swipe workaround on phones. Device/browser
// back goes through the same useBackClose layers as ui/Modal — no private history entries.

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
        // Registry of the sign-in methods tenants defined (docs/log/61 §61.11.6). Only a
        // deployment administrator can approve them, so the surface exists only for them.
        ["register", "admin.tab_register"],
      ],
    },
    {
      key: "deployment",
      label: "admin.group_deployment",
      items: [
        ["egress", "admin.mode_egress"],
        ["tts", "admin.mode_tts"],
        // The slot pool exists on one runtime only. An empty "slots" item on a Fargate
        // deployment reads as "my slots disappeared".
        ...(opts.pool ? ([["pool", "admin.mode_pool"]] as [string, string][]) : []),
      ],
    },
    {
      key: "across",
      label: "admin.group_across",
      items: [
        ["sessions", "admin.mode_sessions"],
        ["usage", "admin.mode_usage"],
        // docker / native have no AWS bill, and a cost surface full of zeros reads as "free",
        // so the item is not created at all (docs/log/67 §67.8, ADR 0048 decision 9).
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

  // scope = the slug of the open tenant (null = root). section is the rail position, remembered
  // separately for root and tenant so closing a tenant just returns to the list.
  const [scope, setScope] = useState<string | null>(null);
  const [rootSection, setRootSection] = useState("tenants");
  const [scopeSection, setScopeSection] = useState("limits");
  const [member, setMember] = useState<Member | null>(null);
  // Phones use the same drill-down as personal settings: rail → body, back returns to the rail.
  const [entered, setEntered] = useState(false);

  // Back layers pop in the order they were pushed (useBackClose's shared stack). Declaration
  // order is rail ↓ tenant ↓ member, so device back steps back one level at a time: member →
  // tenant → rail → close the modal.
  useBackClose(() => setEntered(false), mobileMatches() && entered);
  const leaveScope = useCallback(() => {
    setScope(null);
    setMember(null);
    // On a phone return to the tenant list in the body; returning to the rail loses which
    // tenant was just left.
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
    // Phone: opening a tenant shows that tenant's rail, since a section is picked next.
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
          // Once the tenant is gone there is nothing to stay inside — return to the list
          // and reload.
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
        {/* While a tenant is open the whole rail is replaced by that tenant's sections. The
            heading says where you are (the tenant name); above it is the exit. */}
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
