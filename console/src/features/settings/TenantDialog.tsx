// TenantDialog — tenant settings, the tenant administrator's surface.
//
// There are three modals: personal settings = yourself, admin = the whole deployment (deployment
// administrator), and this one = the tenants you administer.
//
// Separate surfaces do not grant permission — the server does. "Approve and enable" for a
// sign-in method is checked against super_admin by the CP's setStatus (ADR0043 decision 30), and
// the login-rule PUT is fixed to withSuperAdmin (decision 19); what this screen shows or hides
// is only guidance.
//
// The layout is the same two-pane shell as personal settings (left rail + body) and reuses the
// settings-* CSS as is. Do not redefine those classes: two definitions of the same global class
// are silently merged in @import order. tenant-modal only adds small colour adjustments.
//
// The rail order and body live in tenantScope, shared with the admin modal, so the same tenant
// seen from a different entry point does not get a different information architecture.
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { useSettingsUI } from "./store.ts";
import { mobileMatches } from "../../lib/device.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { Modal } from "../../ui/Modal.tsx";
import { useCostProfile } from "../cost/CloudCostView.tsx";
import { TENANT_SCOPE_SECTIONS, TenantScopeBody, tenantScopeGroups } from "./tenant/tenantScope.tsx";
import type { Member, Tenant } from "./parts/adminShared.ts";

export function TenantDialog() {
  const tr = useT();
  // The runtime declares whether a cloud-cost surface exists. docker / native have no AWS bill,
  // so the item is not shown at all (ADR 0048 decision 9).
  const costProfile = useCostProfile();
  const groups = tenantScopeGroups({ cost: !!costProfile?.available });
  const closeTenantSettings = useSettingsUI((s) => s.closeTenantSettings);
  const tenantSection = useSettingsUI((s) => s.tenantSection);
  const [section, setSection] = useState(
    TENANT_SCOPE_SECTIONS.includes(tenantSection) ? tenantSection : "signin",
  );
  // Mobile uses the same drill-down as personal settings: rail → body, back returns to the rail.
  const [entered, setEntered] = useState(false);
  useBackClose(() => setEntered(false), mobileMatches() && entered);

  // Skip the rail when openTenantSettings(section) is called while already open (a deep link
  // from another screen). The mount pass is ignored so a phone still starts at the list.
  const mounted = useRef(false);
  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true;
      return;
    }
    if (TENANT_SCOPE_SECTIONS.includes(tenantSection)) {
      setSection(tenantSection);
      setMember(null);
      setEntered(true);
    }
  }, [tenantSection]);

  // isSuper is the super_admin field of the same GET /api/admin/tenants response the admin modal
  // uses — a separate surface must not mean a separate source (a tenant-only admin gets false
  // and never sees the approve button). tenants is filtered server side: every tenant for a
  // super_admin, otherwise only those where the caller is tenant_admin.
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  const [isSuper, setIsSuper] = useState(false);
  const [forbidden, setForbidden] = useState(false);
  const [slug, setSlug] = useState("");

  // Members are two levels, list → detail. Growing the rail by one item per person breaks down
  // in a 40-person department, and the rail would shift the moment someone is added or removed,
  // so the second level is stacked here in the body instead.
  const [member, setMember] = useState<Member | null>(null);
  // Device back closes the detail first rather than the whole modal: this layer is pushed after
  // the rail's, so it is popped first.
  useBackClose(() => setMember(null), !!member);

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/tenants");
      if (d && d.error) {
        setForbidden(true);
        return;
      }
      const list: Tenant[] = d.tenants || [];
      setTenants(list);
      setIsSuper(!!d.super_admin);
      setSlug((cur) => (cur && list.some((t) => t.slug === cur) ? cur : list[0]?.slug || ""));
    } catch {
      setForbidden(true);
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const tenant = tenants?.find((t) => t.slug === slug) || null;
  const currentLabel = tr(
    (groups.flatMap((g) => g.items).find(([k]) => k === section)?.[1] ??
      "tenant.title") as Parameters<typeof tr>[0],
  );

  const body = () => {
    if (forbidden) return <p className="muted pad">{tr("tenant.forbidden")}</p>;
    if (tenants === null) return <p className="muted pad">{tr("common.loading")}</p>;
    if (!tenant) return <p className="muted pad">{tr("tenant.none")}</p>;
    return (
      <TenantScopeBody
        slug={tenant.slug}
        tenant={tenant}
        section={section}
        isSuper={isSuper}
        member={member}
        onOpenMember={setMember}
        onCloseMember={() => setMember(null)}
        onChanged={load}
      />
    );
  };

  return (
    <Modal title={tr("tenant.title")} onClose={closeTenantSettings} className="settings-modal tenant-modal">
      <div className="ui-modal-body">
        <div className={"settings-layout" + (entered ? " entered" : "")}>
          <nav className="settings-rail" aria-label={tr("tenant.title")}>
            {/* A switcher for people who administer several tenants. With only one there is
                nothing to choose, so it is not rendered — as in personal settings, no controls
                that do nothing. */}
            {(tenants?.length || 0) > 1 && (
              <div className="settings-rail-group">
                <div className="settings-rail-head">{tr("tenant.picker")}</div>
                <select
                  className="tenant-picker"
                  value={slug}
                  onChange={(e) => {
                    setSlug(e.target.value);
                    setMember(null); // don't leave the previous tenant's person on another roster
                  }}
                  aria-label={tr("tenant.picker")}
                >
                  {tenants?.map((t) => (
                    <option key={t.slug} value={t.slug}>
                      {t.name || t.slug}
                    </option>
                  ))}
                </select>
              </div>
            )}
            {groups.map((g) => (
              <div key={g.key} className="settings-rail-group">
                <div className="settings-rail-head">{tr(g.label as Parameters<typeof tr>[0])}</div>
                {g.items.map(([key, label]) => (
                  <button
                    key={key}
                    type="button"
                    className={"settings-rail-item" + (section === key ? " active" : "")}
                    aria-current={section === key ? "page" : undefined}
                    onClick={() => {
                      setSection(key);
                      setMember(null);
                      setEntered(true);
                    }}
                  >
                    {tr(label as Parameters<typeof tr>[0])}
                  </button>
                ))}
              </div>
            ))}
          </nav>
          <div className="settings-content">
            <div className="settings-crumb">
              <button type="button" className="settings-back" onClick={() => setEntered(false)}>
                ‹ {tr("tenant.back")}
              </button>
              <span className="settings-current" aria-current="page">
                {currentLabel}
              </span>
            </div>
            {body()}
          </div>
        </div>
      </div>
    </Modal>
  );
}
