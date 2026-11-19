// The deployment's provided-route source on Admin > Model access (ADR 0129 D1b, D3; ADR 0138 M2
// P10): the Bedrock source the super_admin registers over REST (the stack grants only the IAM).
// Super_admin-only on the CP. Each tenant's grant is edited on that tenant's own Model access page
// (tenantProvidedRoutes.tsx, the super_admin's card), not here.
//
// Drawn only where the CP serves provided routes (its plain 404 draws nothing). The source's PUT
// takes SourceInput alone and reads an absent `active` as true, so the body always carries the
// loaded or edited `active`: re-saving an inactive source must never switch it back on.
import { useState } from "react";
import { apiJSON } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { useReportDirty } from "../parts/dirtyScope.tsx";
import { modelsOf, parseSource, sameModels, type SourceInput, type SourceView } from "../tenant/providedRoutes.ts";
import { Refusal, useGuardedSave, useProvided } from "../tenant/tenantProvidedRoutes.tsx";

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

export type SourceRead = ReturnType<typeof useSourceRead>;

/** useSourceRead reads the deployment source once, for every part of a page that shows it. */
export const useSourceRead = () => useProvided(SOURCE_PATH, parseSource, true);

/** AdminProvidedSourceView is the deployment source's section, reading it itself; nothing where
 *  the CP has no provided routes. */
export function AdminProvidedSourceView() {
  return <AdminProvidedSourceCard read={useSourceRead()} />;
}

/** AdminProvidedSourceCard draws a source read its page holds (useSourceRead), so the page's other
 *  parts showing the source share the one request. */
export function AdminProvidedSourceCard({ read }: { read: SourceRead }) {
  const tr = useT();
  const { load, reload, peek } = read;
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
