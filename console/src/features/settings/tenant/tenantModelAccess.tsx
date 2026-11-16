// Tenant settings > Model access (ADR 0138 D16, M2 P8): the tenant's own credential-policy rule,
// with what the deployment allows shown above it, and the tenant's approved self-hosted endpoints.
//
// The rule can only narrow (D7a): the layers above (the operator's ceiling, the deployment rule,
// this tenant's cap) are shown read-only, and the CP intersects them with what is saved here.
// Reached by tenant_admin and super_admin only. The CP gates both the GET and the PUT
// (TenantAdminFor); a refused or unreadable GET draws no editor, so nothing on this page can
// write a rule built from an answer it did not get.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { HiddenList } from "./routePolicyHidden.tsx";
import { TenantProvidedRoutesView } from "./tenantProvidedRoutes.tsx";
import { RouteName, RuleEditor, RuleSummary, layersLabel } from "./routePolicyRuleEditor.tsx";
import { parseTenantPolicy, routeIds, routesAllowedAbove, type TenantPolicyView } from "./routePolicyRule.ts";

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

/** The two sentences D12 and D13 require the policy page to state. */
export function PolicyLimits() {
  const tr = useT();
  return (
    <>
      <p className="admin-hint">{tr("admin.mpol_delay")}</p>
      <p className="admin-hint">{tr("admin.mpol_hand_login")}</p>
    </>
  );
}

export function TenantModelAccessView({ slug, isSuper = false }: { slug: string; isSuper?: boolean }) {
  const tr = useT();
  const { view, failed, load, peek, path } = useTenantRoutePolicy(slug);

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

  const above = routesAllowedAbove(view.routes);
  return (
    <>
      <section className="admin-panel model-access">
        <h4>{tr("tenant.model_access_title")}</h4>
        <p className="admin-hint">{tr("tenant.model_access_note")}</p>
        <PolicyLimits />
      </section>

      <section className="admin-panel model-access-above">
        <h4>{tr("tenant.model_access_above")}</h4>
        <div className="mpol-layers">
          <div className="mpol-layer" data-layer="ceiling">
            <h5 className="admin-subhead">{tr("admin.mpol_layer_ceiling")}</h5>
            <RuleSummary rule={view.ceiling.rule} ceiling />
          </div>
          <div className="mpol-layer" data-layer="deployment">
            <h5 className="admin-subhead">{tr("admin.mpol_layer_deployment")}</h5>
            <RuleSummary rule={view.deployment.rule} />
          </div>
          <div className="mpol-layer" data-layer="tenant_cap">
            <h5 className="admin-subhead">{tr("admin.mpol_layer_tenant_cap")}</h5>
            <RuleSummary rule={view.cap.rule} />
          </div>
        </div>
        {/* The cap is the super_admin's, written from the admin page; showing it editable
            here too would make two places to change one value. */}
        {isSuper && <p className="admin-hint">{tr("tenant.model_access_cap_where")}</p>}
        <h5 className="admin-subhead">{tr("tenant.model_access_above_routes")}</h5>
        {above.length === 0 ? (
          <p className="admin-hint warn">{tr("tenant.model_access_above_none")}</p>
        ) : (
          <ul className="mpol-routes" data-testid="routes-above">
            {above.map((r) => (
              <li key={r.id}>
                <RouteName id={r.id} />
              </li>
            ))}
          </ul>
        )}
      </section>

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

      <section className="admin-panel model-access-endpoints">
        <h4>{tr("tenant.model_access_endpoints")}</h4>
        {view.endpoints.length === 0 ? (
          <p className="muted">{tr("tenant.model_access_endpoints_empty")}</p>
        ) : (
          <ul className="mpol-routes">
            {view.endpoints.map((e) => (
              <li key={e.host} data-host={e.host}>
                <code>{e.host}</code>{" "}
                <span className={e.approved ? "mpol-ok" : "mpol-no"}>
                  {e.approved
                    ? tr("admin.mpol_approved")
                    : tr("admin.mpol_excluded_by", { layers: layersLabel(tr, e.excludedBy) })}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
      <HiddenList view={view} />
      {/* Provided routes (ADR 0129): absent where the CP does not serve them. */}
      <TenantProvidedRoutesView key={`prov-${slug}`} slug={slug} />
    </>
  );
}
