// Admin > Model access (ADR 0138 D9c, D16, M2 P8): the operator ceiling read-only with its
// source, the deployment-wide rule, and each tenant's cap. Every PUT here is super_admin-only on
// the CP, and the CP refuses one that would allow what the ceiling forbids
// (route_policy_exceeds_ceiling); this page shows that refusal and keeps the edits.
//
// Endpoint patterns are part of each rule (ADR 0138 §2): the ceiling names which hosts may be
// approved at all, the deployment rule and a cap narrow that, and a tenant's own rule approves
// exact hosts inside what is left (on the tenant page).
import { useCallback, useEffect, useState } from "react";
import { api, errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { Tenant } from "../parts/adminShared.ts";
import { RuleEditor, RuleSummary, putRule } from "../tenant/routePolicyRuleEditor.tsx";
import { AdminProvidedSourceCard, useSourceRead, type SourceRead } from "./adminProvidedRoutes.tsx";
import { layerClauses } from "../tenant/routePolicyOverview.tsx";
import { summarizeLayer } from "../tenant/routePolicySummary.ts";
import { Usage, useProvided } from "../tenant/tenantProvidedRoutes.tsx";
import { parseProvidedTenant } from "../tenant/providedRoutes.ts";
import {
  isOlderPolicyAnswer,
  parseDeploymentPolicy,
  type DeploymentPolicyView,
  type RouteRule,
  type Vocabulary,
} from "../tenant/routePolicyRule.ts";
import { PolicyLimits, useTenantRoutePolicy } from "../tenant/tenantModelAccess.tsx";
import { TabbedViews, usePageDirty, useShowView, useViewActive } from "../parts/tabbedViews.tsx";
import { askConfirm } from "../../../ui/confirmBridge.ts";

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
      setFailed(
        d?.error
          ? errText(d.error)
          : tr(isOlderPolicyAnswer(d) ? "admin.mpol_vocab_missing" : "admin.mpol_load_failed"),
      );
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

// The tenant table reads each tenant's policy view (and its provided view) per row: the CP has no
// list route (study §7 decision 9). It waits until the view is first opened and reads a page at
// a time, like the provided view's table.
const TENANT_PAGE = 20;

/** TenantRow: one tenant's restrictions at a glance; opening it goes to that tenant's own Model
 *  access page, where the cap and the grant are edited (ADR 0138 D16, note of 2026-11-17). */
function TenantRow({
  tenant,
  provided,
  onOpen,
}: {
  tenant: Tenant;
  provided: boolean;
  onOpen?: (slug: string) => void;
}) {
  const tr = useT();
  const { view, failed } = useTenantRoutePolicy(tenant.slug);
  const words = (rule: RouteRule, vocab: Vocabulary) => {
    const s = summarizeLayer(rule, vocab);
    return s.none ? tr("admin.mpol_unrestricted") : layerClauses(tr, s, false).join(tr("tenant.mpol_clause_sep"));
  };
  return (
    <tr data-tenant={tenant.slug}>
      <td className="prov-member-name">{tenant.name || tenant.slug}</td>
      {!view ? (
        <td colSpan={2} className={failed ? "admin-hint warn" : "muted"}>
          {failed ?? tr("common.loading")}
        </td>
      ) : (
        <>
          <td data-label={tr("admin.model_access_col_cap")} data-col="cap">
            {words(view.cap.rule, view.vocabulary)}
          </td>
          <td data-label={tr("admin.model_access_col_own")} data-col="own">
            {words(view.tenant.rule, view.vocabulary)}
          </td>
        </>
      )}
      {provided && (
        <td data-label={tr("admin.model_access_view_provided")} data-col="provided">
          <GrantCell slug={tenant.slug} />
        </td>
      )}
      <td className="prov-member-save">
        {onOpen && (
          <button type="button" className="btn xs" onClick={() => onOpen(tenant.slug)}>
            {tr("admin.model_access_go_open")}
          </button>
        )}
      </td>
    </tr>
  );
}

/** GrantCell: a tenant's provided-route grant and month, read-only. */
function GrantCell({ slug }: { slug: string }) {
  const tr = useT();
  const { load } = useProvided(
    `api/admin/tenants/${encodeURIComponent(slug)}/provided-routes/deployment`,
    parseProvidedTenant,
  );
  if (!load) return <span className="muted">{tr("common.loading")}</span>;
  if (load.state !== "ok")
    return (
      <span className="admin-hint warn">
        {(load.state === "failed" && load.reason) || tr("admin.prov_load_failed")}
      </span>
    );
  const v = load.value;
  if (!v.granted) return <span className="muted">{tr("admin.prov_grant_off")}</span>;
  return <Usage used={v.spent_micro + v.reserved_micro} of={v.ceiling_micro} spent={v.spent_micro} />;
}

/** AdminTenantsTable: every tenant's restrictions and grant on the per-tenant view, opened from
 *  each row on the tenant's own page. */
function AdminTenantsTable({
  tenants,
  provided,
  onOpen,
}: {
  tenants: Tenant[];
  provided: boolean;
  onOpen?: (slug: string) => void;
}) {
  const tr = useT();
  const active = useViewActive();
  const [opened, setOpened] = useState(false);
  const [shown, setShown] = useState(TENANT_PAGE);
  const pageDirty = usePageDirty();
  // Opening a tenant leaves this page, which drops an unsaved restriction for all tenants: ask
  // first, as other places that would discard edits do.
  const open = onOpen
    ? async (slug: string) => {
        if (
          pageDirty() &&
          !(await askConfirm({
            title: tr("admin.model_access_open_discard_title"),
            body: tr("admin.model_access_open_discard_body"),
            danger: true,
          }))
        )
          return;
        onOpen(slug);
      }
    : undefined;
  useEffect(() => {
    if (active) setOpened(true);
  }, [active]);
  return (
    <section className="admin-panel model-access-caps">
      <h4>{tr("admin.model_access_view_tenants")}</h4>
      <p className="admin-hint">{tr("admin.model_access_tenants_note")}</p>
      {tenants.length === 0 ? (
        <p className="muted">{tr("admin.model_access_no_tenants")}</p>
      ) : (
        opened && (
          <>
            <table className="admin-table prov-members mpol-tenants">
              <thead>
                <tr>
                  <th>{tr("admin.model_access_tenant")}</th>
                  <th>{tr("admin.model_access_col_cap")}</th>
                  <th>{tr("admin.model_access_col_own")}</th>
                  {provided && <th>{tr("admin.model_access_view_provided")}</th>}
                  <th />
                </tr>
              </thead>
              <tbody>
                {tenants.slice(0, shown).map((t) => (
                  <TenantRow key={t.slug} tenant={t} provided={provided} onOpen={open} />
                ))}
              </tbody>
            </table>
            {tenants.length > shown && (
              <button type="button" className="btn sm" onClick={() => setShown((n) => n + TENANT_PAGE)}>
                {tr("admin.prov_grants_more", { n: Math.min(TENANT_PAGE, tenants.length - shown) })}
              </button>
            )}
          </>
        )
      )}
    </section>
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
function AdminOverview({ view, source }: { view: DeploymentPolicyView; source: SourceRead }) {
  const tr = useT();
  const words = (rule: RouteRule, ceiling: boolean) =>
    layerClauses(tr, summarizeLayer(rule, view.vocabulary, ceiling), false).join(tr("tenant.mpol_clause_sep"));
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
      <ProvidedSourceLine read={source} />
    </>
  );
}

/** ProvidedSourceLine: the provided-route source's state for the overview. Nothing where the CP
 *  does not serve provided routes; a failed read says so and claims no state. */
function ProvidedSourceLine({ read }: { read: SourceRead }) {
  const tr = useT();
  const { load } = read;
  if (load?.state === "off") return null;
  if (!load) {
    // Held in place while the source is read, so the overview does not shift under the reader.
    return (
      <section className="admin-panel model-access-provided-line" data-testid="overview-source">
        <h4>{tr("admin.model_access_view_provided")}</h4>
        <p className="muted">{tr("common.loading")}</p>
      </section>
    );
  }
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

export function AdminModelAccessView({
  tenants,
  onOpenTenant,
}: {
  tenants: Tenant[];
  onOpenTenant?: (slug: string) => void;
}) {
  const tr = useT();
  const { view, failed, load } = useDeploymentRoutePolicy();
  // One read of the deployment source for the overview's line and the provided view's card.
  const source = useSourceRead();
  // Whether the CP serves provided routes: only its plain 404 says no; a failed read keeps the tab.
  const provided = !!source.load && source.load.state !== "off";

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

  const overview = <AdminOverview view={view} source={source} />;
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
        vocab={view.vocabulary}
        endpointsMode="patterns"
        ceiling={view.ceiling.rule}
        save={(body, rev) => putRule(PATH, body, rev)}
        onSaved={load}
      />
    </section>
  );
  const perTenant = <AdminTenantsTable tenants={tenants} provided={provided} onOpen={onOpenTenant} />;
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
            node: <AdminProvidedSourceCard read={source} />,
          },
        ]}
      />
    </>
  );
}
