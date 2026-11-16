// Provided routes on Admin > Model access (ADR 0129 D1b, D3; ADR 0138 M2 P10): the deployment's
// Bedrock source, registered by the super_admin over REST (the stack grants only the IAM), and
// each tenant's grant with its monthly ceiling. Both are super_admin-only on the CP.
//
// Drawn only where the CP serves provided routes (its plain 404 draws nothing). The source's PUT
// takes SourceInput alone and reads an absent `active` as true, so the body always carries the
// loaded or edited `active`: re-saving an inactive source must never switch it back on.
import { useState } from "react";
import { apiJSON } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
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
  Refusal,
  Warnings,
  useGuardedSave,
  useProvided,
  warningsOf,
} from "../tenant/tenantProvidedRoutes.tsx";

const SOURCE_PATH = "api/admin/provided-routes/deployment";

const sameSource = (a: SourceView | null, b: SourceView | null) =>
  a === null || b === null ? a === b : a.updated_at === b.updated_at && a.fingerprint === b.fingerprint && a.active === b.active;

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
  const body: SourceInput = { name: name.trim(), role_arn: role.trim(), region: region.trim(), models: modelsOf(models), active };
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
export function AdminProvidedSourceView() {
  const tr = useT();
  const { load, reload, peek } = useProvided(SOURCE_PATH, parseSource);
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
      <SourceEditor key={src ? `${src.updated_at}:${src.fingerprint}:${src.active}` : "new"} source={src} peek={peek} onSaved={reload} />
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
