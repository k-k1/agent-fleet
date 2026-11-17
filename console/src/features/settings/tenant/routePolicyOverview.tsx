// The tenant Overview tab (#4380 P-4; ADR 0138 D10a, D16): what members can use, agent by agent,
// and who restricts what, one line per layer. The data is routePolicySummary's; this file only
// words it. Layers are named by who decided them here, the ADR's layer words stay in the editors.
//
// Reached by tenant_admin and super_admin only: members' own screens never name a layer or a
// hidden item. A member's own switches live in their Workspace, out of the CP's sight (the user's
// decision 9 on #4169), and the list says so.
import { tMaybe, useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { ownerName } from "./routePolicyHidden.tsx";
import { classLabel, destLabel, layerLabel } from "./routePolicyRuleEditor.tsx";
import {
  LAYERS,
  layerRules,
  memberUse,
  summarizeLayer,
  type LayerSummary,
  type ListSummary,
  type OwnerRow,
  type RouteRow,
} from "./routePolicySummary.ts";
import type { TenantPolicyView } from "./routePolicyRule.ts";

type Tr = ReturnType<typeof useT>;

const WHO = {
  ceiling: "tenant.mpol_who_ceiling",
  deployment: "tenant.mpol_who_deployment",
  tenant_cap: "tenant.mpol_who_tenant_cap",
  tenant: "tenant.mpol_who_tenant",
} as const;

const BY = {
  ceiling: "tenant.mpol_by_ceiling",
  deployment: "tenant.mpol_by_deployment",
  tenant_cap: "tenant.mpol_by_tenant_cap",
  tenant: "tenant.mpol_by_tenant",
} as const;

// A layer the Console does not know (a newer CP's, or "vendor" for an endpoint) keeps its own word.
const byLabel = (tr: Tr, l: string) => (Object.hasOwn(BY, l) ? tr(BY[l as keyof typeof BY]) : layerLabel(tr, l));
const byList = (tr: Tr, ls: string[]) => ls.map((l) => byLabel(tr, l)).join(tr("tenant.mpol_list_sep"));

/** RouteLabel: the route's name; its id, which the CP's refusals use, on hover. */
function RouteLabel({ id }: { id: string }) {
  const name = tMaybe("cred.route." + id);
  return name ? (
    <span className="mpol-route" title={id}>
      {name}
    </span>
  ) : (
    <code>{id}</code>
  );
}

function Mark({ ok }: { ok: boolean }) {
  const tr = useT();
  return (
    <span
      className={ok ? "mpol-ok" : "mpol-no"}
      role="img"
      aria-label={tr(ok ? "tenant.mpol_mark_ok" : "tenant.mpol_mark_no")}
    >
      {ok ? "✔" : "✖"}
    </span>
  );
}

const reason = (tr: Tr, r: RouteRow) =>
  r.stoppedBy.length > 0
    ? tr("tenant.mpol_stopped_by", { who: byList(tr, r.stoppedBy) })
    : r.needsServer
      ? tr("tenant.mpol_needs_server")
      : "";

function rowText(tr: Tr, o: OwnerRow): string {
  const ok = o.routes.filter((r) => r.allowed).length;
  if (ok === o.routes.length) return tr("tenant.mpol_use_all");
  if (ok > 0) return tr("tenant.mpol_use_some", { n: ok, m: o.routes.length });
  const why = o.stoppedBy.length > 0 ? byList(tr, o.stoppedBy) : tr("tenant.mpol_needs_server");
  return tr("tenant.mpol_use_none", { who: why });
}

function Group({ name, title, rows }: { name: string; title: string; rows: OwnerRow[] }) {
  const tr = useT();
  if (rows.length === 0) return null;
  return (
    <>
      <h5 className="admin-subhead">{title}</h5>
      <ul className="mpol-use" data-group={name}>
        {rows.map((o) => (
          <li key={o.id} data-owner={o.id} data-usable={o.usable} data-hidden={o.usable ? undefined : o.id}>
            <details>
              <summary>
                <Mark ok={o.usable} /> <strong>{ownerName(tr, o.id)}</strong>{" "}
                <span className="muted">{rowText(tr, o)}</span>
              </summary>
              <ul className="mpol-routes">
                {o.routes.map((r) => (
                  <li key={r.id} data-route={r.id}>
                    <Mark ok={r.allowed} /> <RouteLabel id={r.id} />
                    {!r.allowed && <span className="muted"> — {reason(tr, r)}</span>}
                  </li>
                ))}
              </ul>
            </details>
          </li>
        ))}
      </ul>
    </>
  );
}

function MemberUse({ view }: { view: TenantPolicyView }) {
  const tr = useT();
  const use = memberUse(view);
  return (
    <section className="admin-panel model-access-use">
      <h4>{tr("tenant.mpol_use_title")}</h4>
      {use.state === "unavailable" ? (
        <p className="admin-hint warn" role="alert">
          {tr("admin.mpol_hidden_unavailable")}
        </p>
      ) : (
        <>
          {use.state === "hiddenOnly" && <p className="admin-hint">{tr("tenant.mpol_use_hidden_only")}</p>}
          {use.state === "hiddenOnly" &&
            use.groups.kinds.length + use.groups.chat.length + use.groups.imagegen.length === 0 && (
              <p className="muted" data-testid="hidden-none">
                {tr("admin.mpol_hidden_none")}
              </p>
            )}
          <Group name="kinds" title={tr("admin.mpol_hidden_kinds")} rows={use.groups.kinds} />
          <Group name="imagegen" title={tr("admin.mpol_hidden_imagegen")} rows={use.groups.imagegen} />
          <Group name="chat" title={tr("admin.mpol_hidden_chat")} rows={use.groups.chat} />
        </>
      )}
      <p className="admin-hint">{tr("tenant.mpol_use_own_switches")}</p>
    </section>
  );
}

function listClause(tr: Tr, s: ListSummary, label: (v: string) => string, but: MsgKey, only: MsgKey): string {
  const names = (l: string[]) => (l.length ? l.map(label).join(tr("tenant.mpol_list_sep")) : tr("admin.mpol_none"));
  return s.allowed.length > 0 && s.missing.length <= s.allowed.length
    ? tr(but, { n: s.allowed.length, m: s.total, list: names(s.missing) })
    : tr(only, { n: s.allowed.length, m: s.total, list: names(s.allowed) });
}

/** layerClauses words one layer; the tenant's own hosts are drawn with their state instead. */
export function layerClauses(tr: Tr, s: LayerSummary, ownHosts: boolean): string[] {
  if (s.none) return [tr("admin.mpol_unrestricted")];
  const routes = (l: string[]) => l.map((id) => tMaybe("cred.route." + id) ?? id).join(tr("tenant.mpol_list_sep"));
  const out: string[] = [];
  if (s.classes)
    out.push(
      listClause(
        tr,
        s.classes,
        (c) => classLabel(tr, c),
        "tenant.mpol_sum_classes_but",
        "tenant.mpol_sum_classes_only",
      ),
    );
  if (s.destinations)
    out.push(
      listClause(tr, s.destinations, (d) => destLabel(tr, d), "tenant.mpol_sum_dest_but", "tenant.mpol_sum_dest_only"),
    );
  if (s.except.length) out.push(tr("tenant.mpol_sum_except", { list: routes(s.except) }));
  if (s.deny.length) out.push(tr("tenant.mpol_sum_deny", { list: routes(s.deny) }));
  if (s.endpoints && !ownHosts)
    out.push(
      tr("tenant.mpol_sum_patterns", {
        list: s.endpoints.length ? s.endpoints.join(tr("tenant.mpol_list_sep")) : tr("admin.mpol_none"),
      }),
    );
  if (s.noEndpoints) out.push(tr("tenant.mpol_sum_no_servers"));
  return out;
}

function WhoRestricts({ view, isSuper }: { view: TenantPolicyView; isSuper: boolean }) {
  const tr = useT();
  const rules = layerRules(view);
  return (
    <section className="admin-panel model-access-who">
      <h4>{tr("tenant.mpol_who_title")}</h4>
      <p className="admin-hint">{tr("tenant.mpol_who_note")}</p>
      <ol className="mpol-who">
        {LAYERS.map((l) => {
          const s = summarizeLayer(rules[l], l === "ceiling");
          const own = l === "tenant";
          const clauses = layerClauses(tr, s, own);
          return (
            <li key={l} data-layer={l}>
              <strong>{tr(WHO[l])}</strong> <span>{clauses.join(tr("tenant.mpol_clause_sep"))}</span>
              {own && s.endpoints && (
                <div className="mpol-hosts">
                  {tr("tenant.mpol_sum_hosts")}{" "}
                  {view.endpoints.length === 0
                    ? tr("admin.mpol_none")
                    : view.endpoints.map((e) => (
                        <span key={e.host} data-host={e.host} className="mpol-host">
                          <code>{e.host}</code> <Mark ok={e.approved} />
                          {!e.approved && (
                            <span className="muted">
                              {" "}
                              {tr("tenant.mpol_stopped_by", { who: byList(tr, e.excludedBy ?? []) })}
                            </span>
                          )}
                        </span>
                      ))}
                </div>
              )}
            </li>
          );
        })}
      </ol>
      {/* The cap is the super_admin's, written from the admin page; showing it editable
          here too would make two places to change one value. */}
      {isSuper && <p className="admin-hint">{tr("tenant.model_access_cap_where")}</p>}
    </section>
  );
}

export function TenantOverview({ view, isSuper }: { view: TenantPolicyView; isSuper: boolean }) {
  const tr = useT();
  return (
    <>
      <p className="admin-hint">{tr("tenant.model_access_note")}</p>
      <MemberUse view={view} />
      <WhoRestricts view={view} isSuper={isSuper} />
    </>
  );
}
