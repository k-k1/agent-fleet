// Tenant settings > Model access (ADR 0138 D16, M2 P8): the tenant's own credential-policy rule,
// with an overview of what members can use and who restricts what (routePolicyOverview.tsx).
//
// The rule can only narrow (D7a): the layers above (the operator's ceiling, the deployment rule,
// this tenant's cap) are shown read-only, and the CP intersects them with what is saved here.
// Reached by tenant_admin and super_admin only. The CP gates both the GET and the PUT
// (TenantAdminFor); a refused or unreadable GET draws no editor, so nothing on this page can
// write a rule built from an answer it did not get.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { TenantOverview } from "./routePolicyOverview.tsx";
import { TenantProvidedRoutesView } from "./tenantProvidedRoutes.tsx";
import { TabbedViews } from "../parts/tabbedViews.tsx";
import { RuleEditor } from "./routePolicyRuleEditor.tsx";
import { parseTenantPolicy, routeIds, type TenantPolicyView } from "./routePolicyRule.ts";

export function useTenantRoutePolicy(slug: string) {
  const tr = useT();
  const [view, setView] = useState<TenantPolicyView | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const path = `api/admin/tenants/${encodeURIComponent(slug)}/route-policy`;
  const load = useCallback(async () => {
    try {
      const d = await api(path);
      const v = parseTenantPolicy(d);
      if (v) {
        setView(v);
        setFailed(null);
        return;
      }
      // A failed reload drops the view too: an editor left on an older answer would save over
      // whatever changed since.
      setView(null);
      setFailed(d?.error ? errText(d.error) : tr("admin.mpol_load_failed"));
    } catch {
      setView(null);
      setFailed(tr("admin.mpol_load_failed"));
    }
  }, [path, tr]);
  useEffect(() => {
    load();
  }, [load]);
  // The stored view as it is now, without touching the drawn one: what an editor checks its
  // revision against right before it saves.
  const peek = useCallback(async () => {
    try {
      return parseTenantPolicy(await api(path));
    } catch {
      return null;
    }
  }, [path]);
  return { view, failed, load, peek, path };
}

/** The sentences D12 (the delay, and the window while the CP is unreachable) and D13 require
 *  the policy page to state. */
export function PolicyLimits() {
  const tr = useT();
  return (
    <>
      <p className="admin-hint">{tr("admin.mpol_delay")}</p>
      <p className="admin-hint">{tr("admin.mpol_outage")}</p>
      <p className="admin-hint">{tr("admin.mpol_hand_login")}</p>
    </>
  );
}

export function TenantModelAccessView({ slug, isSuper = false }: { slug: string; isSuper?: boolean }) {
  const tr = useT();
  const { view, failed, load, peek, path } = useTenantRoutePolicy(slug);
  // Whether the CP serves provided routes, as this tenant's view last heard: an answer for
  // another tenant does not count, so a switch never shows the tab before its own read answers.
  const [served, setServed] = useState<{ slug: string; on: boolean }>({ slug: "", on: false });
  const provided = served.slug === slug && served.on;
  const onServed = useCallback((on: boolean) => setServed({ slug, on }), [slug]);

  if (!view) {
    return (
      <section className="admin-panel model-access">
        <h4>{tr("tenant.model_access_title")}</h4>
        <p className={failed ? "admin-hint warn" : "muted"} role={failed ? "alert" : undefined}>
          {failed ?? tr("common.loading")}
        </p>
      </section>
    );
  }

  const overview = <TenantOverview view={view} isSuper={isSuper} />;
  return (
    <>
      {/* Above the views, so D12's and D13's sentences stay on screen whichever view is open. */}
      <section className="admin-panel model-access">
        <h4>{tr("tenant.model_access_title")}</h4>
        <PolicyLimits />
      </section>
      <TabbedViews
        label={tr("tenant.model_access_title")}
        prefix="tenant-mpol"
        views={[
          { key: "overview", label: tr("tenant.model_access_view_overview"), node: overview },
          {
            key: "restrictions",
            label: tr("tenant.model_access_view_restrictions"),
            node: (
              <section className="admin-panel model-access-rule">
                <h4>
                  {tr("tenant.model_access_rule")}
                  <span className="af-note">{tr("admin.mpol_revision", { n: view.tenant.revision })}</span>
                </h4>
                <RuleEditor
                  key={`${slug}:${view.tenant.revision}`}
                  base={view.tenant.rule}
                  revision={view.tenant.revision}
                  currentRevision={async () => (await peek())?.tenant.revision ?? null}
                  routeOptions={routeIds(view.routes.map((r) => r.id))}
                  endpointsMode="hosts"
                  save={(body) => apiJSON(path, "PUT", body)}
                  onSaved={load}
                />
              </section>
            ),
          },
          {
            key: "provided",
            label: tr("tenant.model_access_view_provided"),
            // Provided routes (ADR 0129): no tab where the CP does not serve them.
            shown: provided,
            node: <TenantProvidedRoutesView key={`prov-${slug}`} slug={slug} onServed={onServed} />,
          },
        ]}
      />
    </>
  );
}
