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

export function TenantEngineAccessView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [view, setView] = useState<EngineAccessView | null>(null);
  const [busy, setBusy] = useState(false);
  const base = `api/admin/tenants/${encodeURIComponent(slug)}/engine-access`;

  const load = useCallback(async () => {
    try {
      const d = await api(base);
      if (d && !d.error) setView(d);
    } catch {
      /* transient; the panel keeps its last values */
    }
  }, [base]);
  useEffect(() => {
    load();
  }, [load]);

  if (!view) return null;

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
