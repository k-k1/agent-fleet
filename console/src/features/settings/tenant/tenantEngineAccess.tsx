// Per-member access to the deployment's self-hosted engines (#1215).
//
// Two layers, and only the lower one is edited here. The deployment admin decides whether this
// tenant may use a role at all (the limits screen, ADR 0084 decision 7). Under that, the tenant
// admin either leaves a role open to every member or restricts it to the members ticked below.
// A tick can never give back what the deployment admin took away, so a role the tenant lacks is
// shown with its controls disabled and a note naming who can change it.
//
// The ticks stay editable while a role is open to everyone: the server keeps the list, so an
// admin can prepare it before switching the restriction on and nobody loses access in between.
//
// The same ticks appear once more on the member detail (MemberEngineAccessPanel), reading and
// writing the same endpoint, so the two surfaces cannot disagree. The mode stays here only: it is
// a tenant-wide choice and has no place on one person's page.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

type EngineRole = "llm" | "image";

interface RoleView {
  role: EngineRole;
  tenant_allowed: boolean;
  members_only: boolean;
}

interface MemberGrant {
  membership_id: string;
  user_key: string;
  email: string;
  role: string;
  grants: EngineRole[];
}

interface EngineAccessView {
  roles: RoleView[];
  members: MemberGrant[];
}

const ROLE_LABEL = {
  llm: "tenant.engine_access_llm",
  image: "tenant.engine_access_image",
} as const satisfies Record<EngineRole, string>;

function useEngineAccess(slug: string) {
  const toast = useToast();
  const [view, setView] = useState<EngineAccessView | null>(null);
  const [busy, setBusy] = useState(false);
  const base = `api/admin/tenants/${encodeURIComponent(slug)}/engine-access`;

  const load = useCallback(async () => {
    try {
      const d = await api(base);
      // Shape-checked rather than trusted: a CP older than #1215 answers this path with
      // something else, and the member detail page must not break over a panel it can omit.
      if (d && !d.error && Array.isArray(d.roles) && Array.isArray(d.members)) setView(d);
    } catch {
      /* transient; the panel keeps its last values */
    }
  }, [base]);
  useEffect(() => {
    load();
  }, [load]);

  const put = async (path: string, body: object) => {
    setBusy(true);
    try {
      const res = await apiJSON(base + path, "PUT", body);
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      await load();
    } finally {
      setBusy(false);
    }
  };
  return { view, busy, put };
}

export function TenantEngineAccessView({ slug }: { slug: string }) {
  const tr = useT();
  const { view, busy, put } = useEngineAccess(slug);
  if (!view) return null;

  const roleOf = (r: EngineRole) => view.roles.find((x) => x.role === r);
  const roles = view.roles.map((r) => r.role);

  return (
    <section className="admin-panel engine-access">
      <h4>{tr("tenant.engine_access_title")}</h4>
      <p className="admin-hint">{tr("tenant.engine_access_note")}</p>
      {view.roles.map((r) => (
        <div key={r.role} className="admin-fgroup" data-role={r.role}>
          <h4>{tr(ROLE_LABEL[r.role])}</h4>
          {!r.tenant_allowed && <p className="admin-hint warn">{tr("tenant.engine_access_tenant_denied")}</p>}
          <div className="le-presets">
            <button
              className={!r.members_only ? "chip on" : "chip"}
              disabled={busy || !r.tenant_allowed}
              onClick={() => r.members_only && put("", { role: r.role, members_only: false })}
            >
              {tr("tenant.engine_access_everyone")}
            </button>
            <button
              className={r.members_only ? "chip on" : "chip"}
              disabled={busy || !r.tenant_allowed}
              onClick={() => !r.members_only && put("", { role: r.role, members_only: true })}
            >
              {tr("tenant.engine_access_members_only")}
            </button>
          </div>
        </div>
      ))}
      <table className="admin-table engine-access-table">
        <thead>
          <tr>
            <th>{tr("tenant.tab_members")}</th>
            {roles.map((r) => (
              <th key={r} className={roleOf(r)?.members_only ? "" : "muted"}>
                {tr(ROLE_LABEL[r])}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {view.members.map((m) => (
            <tr key={m.membership_id}>
              <td>
                <span className="mono">{m.user_key}</span>
                {m.role === "tenant_admin" && <span className="af-note">{tr("tenant.engine_access_admin")}</span>}
              </td>
              {roles.map((r) => (
                <td key={r}>
                  <input
                    type="checkbox"
                    aria-label={`${m.user_key} ${tr(ROLE_LABEL[r])}`}
                    checked={m.grants.includes(r)}
                    disabled={busy || !roleOf(r)?.tenant_allowed}
                    onChange={(e) =>
                      put("/members", { membership_id: m.membership_id, role: r, granted: e.target.checked })
                    }
                  />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <p className="admin-hint">{tr("tenant.engine_access_ticks_note")}</p>
    </section>
  );
}

// MemberEngineAccessPanel is one member's row of the table above, on their detail page. Absent for
// a removed member (the server lists active members only) and until the first read answers.
export function MemberEngineAccessPanel({ slug, userKey }: { slug: string; userKey: string }) {
  const tr = useT();
  const { view, busy, put } = useEngineAccess(slug);
  const m = view?.members.find((x) => x.user_key === userKey);
  if (!view || !m) return null;

  // What the member can actually do right now, so the page answers "can they use it?" without
  // the reader having to combine the mode, the tick and the tenant switch in their head.
  const status = (r: RoleView) => {
    if (!r.tenant_allowed) return tr("tenant.engine_access_state_tenant_off");
    if (!r.members_only) return tr("tenant.engine_access_state_everyone");
    return m.grants.includes(r.role) ? tr("tenant.engine_access_state_granted") : tr("tenant.engine_access_state_not_granted");
  };

  return (
    <section className="admin-panel member-engine-access">
      <h4>{tr("tenant.engine_access_title")}</h4>
      {view.roles.map((r) => (
        <div key={r.role} className="admin-fgroup" data-role={r.role}>
          <label className="admin-check">
            <input
              type="checkbox"
              checked={m.grants.includes(r.role)}
              disabled={busy || !r.tenant_allowed}
              onChange={(e) => put("/members", { membership_id: m.membership_id, role: r.role, granted: e.target.checked })}
            />
            <span>{tr(ROLE_LABEL[r.role])}</span>
          </label>
          <p className="admin-hint">{status(r)}</p>
        </div>
      ))}
      <p className="admin-hint">{tr("tenant.engine_access_member_note")}</p>
    </section>
  );
}
