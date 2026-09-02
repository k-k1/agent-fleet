import type { FormEvent } from "react";
import { useState } from "react";
import { rawJSON } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import type { Tenant } from "../parts/adminShared.ts";

export function TenantsList({
  tenants,
  isSuper,
  onReload,
  onOpen,
}: {
  tenants: Tenant[];
  isSuper: boolean;
  onReload: () => void;
  onOpen: (slug: string) => void;
}) {
  const tr = useT();
  const [adding, setAdding] = useState(false);
  return (
    <div className="admin-stage">
      <div className="stage-head">
        <h4>{tr("admin.tenants_list")}</h4>
        {isSuper && (
          <button className="primary" onClick={() => setAdding((v) => !v)}>
            <Icon name="add" /> {tr("admin.new_tenant")}
          </button>
        )}
      </div>
      {isSuper && adding && <NewTenant onCreated={() => { setAdding(false); onReload(); }} onCancel={() => setAdding(false)} />}
      {tenants.length === 0 ? (
        <p className="muted">{tr("admin.no_tenants")}</p>
      ) : (
        <div className="tenant-cards">
          {tenants.map((t) => (
            <button key={t.slug} className="tenant-card" onClick={() => onOpen(t.slug)}>
              <div className="tc-top">
                <span className="tc-name">{t.name}</span>
                <span className="tc-slug mono">{t.slug}</span>
              </div>
              <div className="tc-stats">
                <span title={tr("admin.member_count_title")}><Icon name="person" /> {tr("admin.person_count", { n: t.users ?? 0 })}</span>
                <span className={(t.running || 0) > 0 ? "tc-run on" : "tc-run"} title={tr("admin.running_ws_title")}>
                  <Icon name="vm-running" /> {tr("admin.running_ws", { n: t.running ?? 0 })}
                </span>
              </div>
              <div className="tc-limits muted">
                {tr("admin.tenant_limits", { ws: t.max_workspaces || "∞", ss: t.max_sessions || "∞" })}
              </div>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function NewTenant({ onCreated, onCancel }: { onCreated: () => void; onCancel: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!slug.trim()) return;
    const r = await rawJSON("api/admin/tenants", "POST", { slug: slug.trim(), name: name.trim() });
    if (r.ok) {
      onCreated();
    } else {
      const er = await r.json().catch(() => ({}));
      toast(tr("admin.create_failed", { msg: er.error?.message || r.status }));
    }
  };
  return (
    <form className="new-tenant" onSubmit={submit}>
      <input value={slug} onChange={(e) => setSlug(e.target.value)} placeholder={tr("admin.slug_ph")} autoFocus />
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder={tr("admin.display_name_ph")} />
      <button type="submit" className="primary">{tr("admin.create")}</button>
      <button type="button" className="ghost" onClick={onCancel}>{tr("common.cancel")}</button>
    </form>
  );
}
