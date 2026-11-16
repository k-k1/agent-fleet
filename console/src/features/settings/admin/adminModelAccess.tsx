// Admin > Model access (ADR 0138 D9c, D16, M2 P8): the operator ceiling read-only with its
// source, the deployment-wide rule, and each tenant's cap. Every PUT here is super_admin-only on
// the CP, and the CP refuses one that would allow what the ceiling forbids
// (route_policy_exceeds_ceiling); this page shows that refusal and keeps the edits.
//
// Endpoint patterns are part of each rule (ADR 0138 §2): the ceiling names which hosts may be
// approved at all, the deployment rule and a cap narrow that, and a tenant's own rule approves
// exact hosts inside what is left (on the tenant page).
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { Tenant } from "../parts/adminShared.ts";
import { RuleEditor, RuleSummary } from "../tenant/routePolicyRuleEditor.tsx";
import { HiddenList } from "../tenant/routePolicyHidden.tsx";
import { parseDeploymentPolicy, routeIds, type DeploymentPolicyView } from "../tenant/routePolicyRule.ts";
import { PolicyLimits, useTenantRoutePolicy } from "../tenant/tenantModelAccess.tsx";

// The variable the ceiling is read from (routepolicy.EnvCeiling), named when the wire's source is
// "deployment", the only source the CP has; any other source is shown as the CP spells it.
const CEILING_ENV = "AF_MODEL_ROUTE_CEILING";
const PATH = "api/admin/route-policy";

function useDeploymentRoutePolicy() {
  const tr = useT();
  const [view, setView] = useState<DeploymentPolicyView | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const load = useCallback(async () => {
    try {
      const d = await api(PATH);
      const v = parseDeploymentPolicy(d);
      if (v) {
        setView(v);
        setFailed(null);
        return;
      }
      setView(null);
      setFailed(d?.error ? errText(d.error) : tr("admin.mpol_load_failed"));
    } catch {
      setView(null);
      setFailed(tr("admin.mpol_load_failed"));
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);
  return { view, failed, load };
}

async function currentDeploymentRevision(): Promise<number | null> {
  try {
    return parseDeploymentPolicy(await api(PATH))?.deployment.revision ?? null;
  } catch {
    return null;
  }
}

function TenantCap({ slug }: { slug: string }) {
  const tr = useT();
  const { view, failed, load, peek, path } = useTenantRoutePolicy(slug);
  if (!view) {
    return (
      <p className={failed ? "admin-hint warn" : "muted"} role={failed ? "alert" : undefined}>
        {failed ?? tr("common.loading")}
      </p>
    );
  }
  return (
    <div className="mpol-cap" data-tenant={slug}>
      <RuleEditor
        key={`${slug}:${view.cap.revision}`}
        base={view.cap.rule}
        revision={view.cap.revision}
        currentRevision={async () => (await peek())?.cap.revision ?? null}
        routeOptions={routeIds(view.routes.map((r) => r.id))}
        endpointsMode="patterns"
        save={(body) => apiJSON(`${path}/cap`, "PUT", body)}
        onSaved={load}
      />
      <h5 className="admin-subhead">{tr("admin.model_access_tenant_rule")}</h5>
      <RuleSummary rule={view.tenant.rule} />
      {/* Per selected tenant: the effective policy, and so what it hides, is per tenant. */}
      <HiddenList view={view} />
    </div>
  );
}

export function AdminModelAccessView({ tenants }: { tenants: Tenant[] }) {
  const tr = useT();
  const { view, failed, load } = useDeploymentRoutePolicy();
  const [slug, setSlug] = useState(tenants[0]?.slug ?? "");
  const shownSlug = tenants.some((t) => t.slug === slug) ? slug : (tenants[0]?.slug ?? "");

  if (!view) {
    return (
      <section className="admin-panel model-access">
        <h4>{tr("admin.model_access_title")}</h4>
        <p className={failed ? "admin-hint warn" : "muted"} role={failed ? "alert" : undefined}>
          {failed ?? tr("common.loading")}
        </p>
      </section>
    );
  }

  return (
    <>
      <section className="admin-panel model-access">
        <h4>{tr("admin.model_access_title")}</h4>
        <p className="admin-hint">{tr("admin.model_access_note")}</p>
        <PolicyLimits />
      </section>

      <section className="admin-panel model-access-ceiling">
        <h4>{tr("admin.mpol_layer_ceiling")}</h4>
        <p className="admin-hint">
          {view.ceiling.source === "deployment"
            ? tr("admin.model_access_ceiling_source", { env: CEILING_ENV })
            : tr("admin.model_access_ceiling_source_other", { source: view.ceiling.source })}
        </p>
        {view.ceiling.raw ? (
          <p>
            <code className="mpol-raw">{view.ceiling.raw}</code>
          </p>
        ) : (
          <p className="muted">{tr("admin.model_access_ceiling_none")}</p>
        )}
        <RuleSummary rule={view.ceiling.rule} ceiling />
      </section>

      <section className="admin-panel model-access-deployment">
        <h4>
          {tr("admin.mpol_layer_deployment")}
          <span className="af-note">{tr("admin.mpol_revision", { n: view.deployment.revision })}</span>
        </h4>
        <p className="admin-hint">{tr("admin.model_access_deployment_note")}</p>
        <RuleEditor
          key={view.deployment.revision}
          base={view.deployment.rule}
          revision={view.deployment.revision}
          currentRevision={currentDeploymentRevision}
          routeOptions={routeIds()}
          endpointsMode="patterns"
          save={(body) => apiJSON(PATH, "PUT", body)}
          onSaved={load}
        />
      </section>

      <section className="admin-panel model-access-caps">
        <h4>{tr("admin.mpol_layer_tenant_cap")}</h4>
        <p className="admin-hint">{tr("admin.model_access_caps_note")}</p>
        {tenants.length === 0 ? (
          <p className="muted">{tr("admin.model_access_no_tenants")}</p>
        ) : (
          <>
            <label className="admin-fld">
              <span className="af-cap">{tr("admin.model_access_tenant")}</span>
              <select value={shownSlug} onChange={(e) => setSlug(e.target.value)}>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>
                    {t.name || t.slug}
                  </option>
                ))}
              </select>
            </label>
            <TenantCap key={shownSlug} slug={shownSlug} />
          </>
        )}
      </section>
      {/* ADR 0138 M2 P10 (provided routes, ADR 0129 Q2) goes here. */}
    </>
  );
}
