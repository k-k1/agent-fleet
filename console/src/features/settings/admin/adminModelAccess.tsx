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
import { AdminProvidedGrantTable, AdminProvidedGrantView, AdminProvidedSourceView } from "./adminProvidedRoutes.tsx";
import { layerClauses } from "../tenant/routePolicyOverview.tsx";
import { summarizeLayer } from "../tenant/routePolicySummary.ts";
import { useProvided } from "../tenant/tenantProvidedRoutes.tsx";
import { parseSource } from "../tenant/providedRoutes.ts";
import {
  parseDeploymentPolicy,
  routeIds,
  type DeploymentPolicyView,
  type RouteRule,
} from "../tenant/routePolicyRule.ts";
import { PolicyLimits, useTenantRoutePolicy } from "../tenant/tenantModelAccess.tsx";
import { TabbedViews, useShowView } from "../parts/tabbedViews.tsx";

// The variable the ceiling is read from (routepolicy.EnvCeiling), named when the wire's source is
// "deployment", the only source the CP has; any other source is shown as the CP spells it.
const CEILING_ENV = "AF_MODEL_ROUTE_CEILING";
const PATH = "api/admin/route-policy";
const SOURCE_PATH = "api/admin/provided-routes/deployment";

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
        ceiling={view.ceiling.rule}
        above={[{ layer: "deployment", rule: view.deployment.rule }]}
        save={(body) => apiJSON(`${path}/cap`, "PUT", body)}
        onSaved={load}
      />
      <h5 className="admin-subhead">{tr("admin.model_access_tenant_rule")}</h5>
      <RuleSummary rule={view.tenant.rule} />
      {/* Per selected tenant: the effective policy, and so what it hides, is per tenant. */}
      <HiddenList view={view} />
      <AdminProvidedGrantView slug={slug} />
    </div>
  );
}

/** OverviewLink: a button that opens another view of the page; nothing outside the tabbed page. */
function OverviewLink({ view, label }: { view: string; label: string }) {
  const show = useShowView(view);
  if (!show) return null;
  return (
    <button type="button" className="btn sm" data-goto={view} onClick={show}>
      {label}
    </button>
  );
}

/** AdminOverview: who decides what across the deployment, top first, in plain lines (#4380 P-7):
 *  the operator setting, the restriction for all tenants, the per-tenant restrictions, and the
 *  provided-route source. Each points at the view where it is changed. */
function AdminOverview({ view }: { view: DeploymentPolicyView }) {
  const tr = useT();
  const words = (rule: RouteRule, ceiling: boolean) =>
    layerClauses(tr, summarizeLayer(rule, ceiling), false).join(tr("tenant.mpol_clause_sep"));
  return (
    <>
      <p className="admin-hint">{tr("admin.model_access_note")}</p>
      <section className="admin-panel model-access-ceiling model-access-who">
        <h4>{tr("admin.model_access_who_title")}</h4>
        <ol className="mpol-who">
          <li data-layer="ceiling">
            <strong>{tr("admin.mpol_layer_ceiling")}</strong>{" "}
            <span>{view.ceiling.raw ? words(view.ceiling.rule, true) : tr("admin.model_access_ceiling_none")}</span>
            <p className="admin-hint">
              {view.ceiling.source === "deployment"
                ? tr("admin.model_access_ceiling_source", { env: CEILING_ENV })
                : tr("admin.model_access_ceiling_source_other", { source: view.ceiling.source })}
            </p>
            {view.ceiling.raw && (
              <details className="mpol-raw-fold">
                <summary>{tr("admin.model_access_ceiling_raw")}</summary>
                <code className="mpol-raw">{view.ceiling.raw}</code>
                <RuleSummary rule={view.ceiling.rule} ceiling />
              </details>
            )}
          </li>
          <li data-layer="deployment">
            <strong>{tr("admin.mpol_layer_deployment")}</strong> <span>{words(view.deployment.rule, false)}</span>{" "}
            <OverviewLink view="deployment" label={tr("admin.model_access_go_change")} />
          </li>
          <li data-layer="tenant_cap">
            <strong>{tr("admin.model_access_who_caps")}</strong> <span>{tr("admin.model_access_who_caps_note")}</span>{" "}
            <OverviewLink view="tenants" label={tr("admin.model_access_go_tenants")} />
          </li>
        </ol>
      </section>
      <ProvidedSourceLine />
    </>
  );
}

/** ProvidedSourceLine: the provided-route source's state for the overview. Nothing where the CP
 *  does not serve provided routes; a failed read says so and claims no state. */
function ProvidedSourceLine() {
  const tr = useT();
  const { load } = useProvided(SOURCE_PATH, parseSource);
  if (!load || load.state === "off") return null;
  const src = load.state === "ok" ? load.value.source : undefined;
  return (
    <section className="admin-panel model-access-provided-line" data-testid="overview-source">
      <h4>{tr("admin.model_access_view_provided")}</h4>
      <p>
        {load.state === "failed" ? (
          <span className="admin-hint warn" role="alert">
            {load.reason ?? tr("admin.prov_load_failed")}
          </span>
        ) : src === null ? (
          <span className="muted">{tr("admin.prov_source_none")}</span>
        ) : (
          <span>
            {tr("admin.model_access_source_line", { name: src!.name, region: src!.region })}{" "}
            <span className={src!.active ? "mpol-ok" : "mpol-no"}>
              {tr(src!.active ? "admin.prov_source_on" : "admin.prov_source_off")}
            </span>
          </span>
        )}{" "}
        <OverviewLink view="provided" label={tr("admin.model_access_go_open")} />
      </p>
    </section>
  );
}

export function AdminModelAccessView({ tenants }: { tenants: Tenant[] }) {
  const tr = useT();
  const { view, failed, load } = useDeploymentRoutePolicy();
  const [slug, setSlug] = useState(tenants[0]?.slug ?? "");
  const shownSlug = tenants.some((t) => t.slug === slug) ? slug : (tenants[0]?.slug ?? "");
  const [provided, setProvided] = useState(false);

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

  const overview = <AdminOverview view={view} />;
  const deployment = (
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
        ceiling={view.ceiling.rule}
        save={(body) => apiJSON(PATH, "PUT", body)}
        onSaved={load}
      />
    </section>
  );
  // From a provided grant's row: open that tenant on the per-tenant view.
  const openTenant = (t: string) => setSlug(t);
  const perTenant = (
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
  );
  return (
    <>
      {/* Above the views, so D12's and D13's sentences stay on screen whichever view is open. */}
      <section className="admin-panel model-access">
        <h4>{tr("admin.model_access_title")}</h4>
        <PolicyLimits />
      </section>
      <TabbedViews
        label={tr("admin.model_access_title")}
        prefix="admin-mpol"
        views={[
          { key: "overview", label: tr("admin.model_access_view_overview"), node: overview },
          { key: "deployment", label: tr("admin.model_access_view_deployment"), node: deployment },
          { key: "tenants", label: tr("admin.model_access_view_tenants"), node: perTenant },
          {
            key: "provided",
            label: tr("admin.model_access_view_provided"),
            // The deployment's provided-route source (ADR 0129): no tab where the CP does not serve it.
            shown: provided,
            node: (
              <>
                <AdminProvidedSourceView onServed={setProvided} />
                <AdminProvidedGrantTable tenants={tenants} onOpen={openTenant} />
              </>
            ),
          },
        ]}
      />
    </>
  );
}
