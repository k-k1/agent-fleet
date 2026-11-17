// Provided routes on Admin > Model access (ADR 0129 D1b, D3; ADR 0138 M2 P10): the deployment's
// Bedrock source, registered by the super_admin over REST (the stack grants only the IAM), and
// each tenant's grant with its monthly ceiling. Both are super_admin-only on the CP.
//
// Drawn only where the CP serves provided routes (its plain 404 draws nothing). The source's PUT
// takes SourceInput alone and reads an absent `active` as true, so the body always carries the
// loaded or edited `active`: re-saving an inactive source must never switch it back on.
import { useEffect, useState } from "react";
import type { Tenant } from "../parts/adminShared.ts";
import { useShowView, useViewActive } from "../parts/tabbedViews.tsx";
import { apiJSON } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { useReportDirty } from "../parts/dirtyScope.tsx";
import {
  modelsOf,
  parseProvidedTenant,
  parseSource,
  parseUsd,
  sameModels,
  usdInput,
  type GrantInput,
  type ProvidedTenantView,
  type SourceInput,
  type SourceView,
} from "../tenant/providedRoutes.ts";
import {
  ProvidedSpend,
  Usage,
  approx,
  Refusal,
  Warnings,
  useGuardedSave,
  useProvided,
  warningsOf,
} from "../tenant/tenantProvidedRoutes.tsx";

const SOURCE_PATH = "api/admin/provided-routes/deployment";

const sameSource = (a: SourceView | null, b: SourceView | null) =>
  a === null || b === null
    ? a === b
    : a.updated_at === b.updated_at && a.fingerprint === b.fingerprint && a.active === b.active;

function SourceEditor({
  source,
  peek,
  onSaved,
}: {
  source: SourceView | null;
  peek: () => Promise<{ source: SourceView | null } | null>;
  onSaved: () => void;
}) {
  const tr = useT();
  const [name, setName] = useState(source?.name ?? "");
  const [role, setRole] = useState(source?.role_arn ?? "");
  const [region, setRegion] = useState(source?.region ?? "");
  const [models, setModels] = useState((source?.models ?? []).join("\n"));
  // A new registration is meant to be used (the CP's own default); an existing one keeps its state.
  const [active, setActive] = useState(source?.active ?? true);
  const { busy, refusal, setRefusal, submit } = useGuardedSave(onSaved);
  const body: SourceInput = {
    name: name.trim(),
    role_arn: role.trim(),
    region: region.trim(),
    models: modelsOf(models),
    active,
  };
  // A first registration has nothing to differ from: it is ready once it has what the CP
  // requires (the role, the one Region and at least one model; only the name is defaulted), so
  // an empty or partial form never offers a Save the CP would refuse.
  const dirty = !source
    ? body.role_arn !== "" && body.region !== "" && body.models.length > 0
    : body.name !== source.name ||
      body.role_arn !== source.role_arn ||
      body.region !== source.region ||
      body.active !== source.active ||
      !sameModels(body.models, source.models);
  // Unsaved edits, for the tab's mark: on a first registration any field typed in counts, even
  // before the form is complete enough to save.
  useReportDirty(source ? dirty : name !== "" || role !== "" || region !== "" || models !== "" || !active);
  const field = (label: string, value: string, set: (v: string) => void) => (
    <label className="admin-fld">
      <span className="af-cap">{label}</span>
      <input
        type="text"
        value={value}
        disabled={busy}
        onChange={(e) => {
          set(e.target.value);
          setRefusal(null);
        }}
      />
    </label>
  );
  return (
    <div className="prov-source-editor">
      {field(tr("admin.prov_source_name"), name, setName)}
      {field(tr("admin.prov_source_role"), role, setRole)}
      {field(tr("admin.prov_source_region"), region, setRegion)}
      <label className="admin-fld">
        <span className="af-cap">{tr("admin.prov_source_models")}</span>
        <textarea
          rows={3}
          value={models}
          disabled={busy}
          onChange={(e) => {
            setModels(e.target.value);
            setRefusal(null);
          }}
        />
      </label>
      <label className="admin-check">
        <input
          type="checkbox"
          checked={active}
          disabled={busy}
          onChange={(e) => {
            setActive(e.target.checked);
            setRefusal(null);
          }}
        />
        {tr("admin.prov_source_active")}
      </label>
      <Refusal text={refusal} />
      <div className="admin-actions">
        <button
          type="button"
          className="btn primary"
          disabled={busy || !dirty}
          onClick={() =>
            submit(
              async () => {
                const now = await peek();
                return now && sameSource(now.source, source);
              },
              () => apiJSON(SOURCE_PATH, "PUT", body),
            )
          }
        >
          {tr("admin.mpol_save")}
        </button>
      </div>
    </div>
  );
}

/** AdminProvidedSourceView is the deployment source's section; nothing where the CP has no
 *  provided routes. */
export function AdminProvidedSourceView({ onServed }: { onServed?: (served: boolean) => void } = {}) {
  const tr = useT();
  const { load, reload, peek } = useProvided(SOURCE_PATH, parseSource);
  // Whether the CP serves provided routes, for a page that gives them a tab: only the plain 404
  // is "not served"; a failed read has to stay reachable.
  useEffect(() => {
    if (load) onServed?.(load.state !== "off");
  }, [load, onServed]);
  if (!load || load.state === "off") return null;
  if (load.state === "failed") {
    return (
      <section className="admin-panel model-access-provided-source">
        <h4>{tr("admin.prov_source_title")}</h4>
        <p className="admin-hint warn" role="alert">
          {load.reason ?? tr("admin.prov_load_failed")}
        </p>
      </section>
    );
  }
  const src = load.value.source;
  return (
    <section className="admin-panel model-access-provided-source">
      <h4>{tr("admin.prov_source_title")}</h4>
      <p className="admin-hint">{tr("admin.prov_source_note")}</p>
      {src ? (
        <dl className="mpol-summary" data-testid="prov-source">
          <dt>{tr("admin.prov_source_binding")}</dt>
          <dd>
            <code>{src.binding}</code>
          </dd>
          <dt>{tr("admin.prov_source_state")}</dt>
          <dd>{tr(src.active ? "admin.prov_source_on" : "admin.prov_source_off")}</dd>
          <dt>{tr("admin.prov_source_updated")}</dt>
          <dd>{fmtDateTime(src.updated_at)}</dd>
        </dl>
      ) : (
        <p className="muted">{tr("admin.prov_source_none")}</p>
      )}
      <SourceEditor
        key={src ? `${src.updated_at}:${src.fingerprint}:${src.active}` : "new"}
        source={src}
        peek={peek}
        onSaved={reload}
      />
    </section>
  );
}

function GrantEditor({
  path,
  view,
  peek,
  onSaved,
}: {
  path: string;
  view: ProvidedTenantView;
  peek: () => Promise<ProvidedTenantView | null>;
  onSaved: (warnings: string[]) => void;
}) {
  const tr = useT();
  const [granted, setGranted] = useState(view.granted);
  const [ceiling, setCeiling] = useState(usdInput(view.ceiling_micro));
  const { busy, refusal, setRefusal, submit } = useGuardedSave((res) => onSaved(warningsOf(res)));
  const micro = parseUsd(ceiling);
  const dirty = granted !== view.granted || micro !== view.ceiling_micro;
  useReportDirty(dirty);
  const save = () => {
    if (micro === undefined) {
      setRefusal(tr("admin.prov_bad_amount"));
      return;
    }
    const body: GrantInput = { granted, ceiling_micro: micro };
    submit(
      async () => {
        const now = await peek();
        return now && now.granted === view.granted && now.ceiling_micro === view.ceiling_micro;
      },
      () => apiJSON(`${path}/grant`, "PUT", body),
    );
  };
  return (
    <div className="admin-fgroup prov-grant">
      <label className="admin-check">
        <input
          type="checkbox"
          checked={granted}
          disabled={busy}
          onChange={(e) => {
            setGranted(e.target.checked);
            setRefusal(null);
          }}
        />
        {tr("admin.prov_grant")}
      </label>
      <label className="admin-fld">
        <span className="af-cap">{tr("admin.prov_ceiling")}</span>
        <input
          type="text"
          inputMode="decimal"
          value={ceiling}
          disabled={busy}
          onChange={(e) => {
            setCeiling(e.target.value);
            setRefusal(null);
          }}
        />
      </label>
      <Refusal text={refusal} />
      <div className="admin-actions">
        <button type="button" className="btn primary" disabled={busy || !dirty} onClick={save}>
          {tr("admin.mpol_save")}
        </button>
      </div>
    </div>
  );
}

/** AdminProvidedGrantView is one tenant's grant and ceiling with its spend this month. */
export function AdminProvidedGrantView({ slug }: { slug: string }) {
  const tr = useT();
  const path = `api/admin/tenants/${encodeURIComponent(slug)}/provided-routes/deployment`;
  const { load, reload, peek } = useProvided(path, parseProvidedTenant);
  const [warnings, setWarnings] = useState<string[]>([]);
  if (!load || load.state === "off") return null;
  return (
    <div className="model-access-provided-grant" data-tenant={slug}>
      <h5 className="admin-subhead">{tr("admin.prov_grant_title")}</h5>
      {load.state === "failed" ? (
        <p className="admin-hint warn" role="alert">
          {load.reason ?? tr("admin.prov_load_failed")}
        </p>
      ) : (
        <>
          <ProvidedSpend view={load.value} />
          <Warnings list={warnings} />
          <GrantEditor
            key={`${load.value.granted}:${load.value.ceiling_micro}`}
            path={path}
            view={load.value}
            peek={peek}
            onSaved={(w) => {
              setWarnings(w);
              reload();
            }}
          />
        </>
      )}
    </div>
  );
}

// The grant table reads one tenant's provided view per row (no CP list route; study §7 decision 9),
// so it waits until the Provided models view is first opened and then reads a page at a time.
const GRANT_PAGE = 20;

/** GrantRow: one tenant's grant and month, read-only; editing stays on the per-tenant view. */
function GrantRow({ tenant, onOpen }: { tenant: Tenant; onOpen: (slug: string) => void }) {
  const tr = useT();
  const path = `api/admin/tenants/${encodeURIComponent(tenant.slug)}/provided-routes/deployment`;
  const { load } = useProvided(path, parseProvidedTenant);
  const v = load?.state === "ok" ? load.value : null;
  return (
    <tr data-tenant={tenant.slug}>
      <td>{tenant.name || tenant.slug}</td>
      {!load ? (
        <td colSpan={3} className="muted">
          {tr("common.loading")}
        </td>
      ) : !v ? (
        <td colSpan={3} className="admin-hint warn">
          {(load.state === "failed" && load.reason) || tr("admin.prov_load_failed")}
        </td>
      ) : (
        <>
          <td data-label={tr("admin.prov_col_grant")}>
            <span className={v.granted ? "mpol-ok" : "muted"}>
              {tr(v.granted ? "admin.prov_grant_on" : "admin.prov_grant_off")}
            </span>
          </td>
          <td data-label={tr("admin.prov_ceiling")}>{v.ceiling_micro === null ? "—" : approx(v.ceiling_micro)}</td>
          <td data-label={tr("tenant.prov_col_use")}>
            <Usage
              used={v.spent_micro + v.reserved_micro}
              of={v.granted ? v.ceiling_micro : null}
              spent={v.spent_micro}
            />
          </td>
        </>
      )}
      <td>
        <button type="button" className="btn xs" onClick={() => onOpen(tenant.slug)}>
          {tr("admin.model_access_go_change")}
        </button>
      </td>
    </tr>
  );
}

/** AdminProvidedGrantTable: every tenant's grant, pool and month on the Provided models view.
 *  A row's button opens that tenant on the per-tenant view, where the grant is edited. */
export function AdminProvidedGrantTable({ tenants, onOpen }: { tenants: Tenant[]; onOpen: (slug: string) => void }) {
  const tr = useT();
  const active = useViewActive();
  const [opened, setOpened] = useState(false);
  const [shown, setShown] = useState(GRANT_PAGE);
  const showTenants = useShowView("tenants");
  useEffect(() => {
    if (active) setOpened(true);
  }, [active]);
  if (tenants.length === 0) return null;
  const open = (slug: string) => {
    onOpen(slug);
    showTenants?.();
  };
  return (
    <section className="admin-panel model-access-grants">
      <h4>{tr("admin.prov_grants_title")}</h4>
      <p className="admin-hint">{tr("admin.prov_grants_note")}</p>
      {opened && (
        <>
          <table className="admin-table prov-grants">
            <thead>
              <tr>
                <th>{tr("admin.model_access_tenant")}</th>
                <th>{tr("admin.prov_col_grant")}</th>
                <th>{tr("admin.prov_ceiling")}</th>
                <th>{tr("tenant.prov_col_use")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {tenants.slice(0, shown).map((t) => (
                <GrantRow key={t.slug} tenant={t} onOpen={open} />
              ))}
            </tbody>
          </table>
          {tenants.length > shown && (
            <button type="button" className="btn sm" onClick={() => setShown((n) => n + GRANT_PAGE)}>
              {tr("admin.prov_grants_more", { n: Math.min(GRANT_PAGE, tenants.length - shown) })}
            </button>
          )}
        </>
      )}
    </section>
  );
}
