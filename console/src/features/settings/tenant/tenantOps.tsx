// Tenant-scoped operational views (sessions / audit / usage).
//
// For all three the CP returns "the whole deployment for a super_admin, their own tenant
// for a tenant_admin" (GET /api/admin/sessions, /audit, /usage), so these views belong to
// tenant administrators as well and must not live only in the admin modal.
//
// isSuper only decides whether the tenant selector (i.e. whether to look across tenants)
// is shown; the server always decides whose rows come back.
import { useCallback, useEffect, useRef, useState } from "react";
import { api, errText, rel } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { kindLabel, kindClass, kindIcon } from "../../../lib/sessionkind.ts";
import { fmtDateTime, DATETIME_FULL, compareText } from "../../../lib/intl.ts";
import { useLocale, useT } from "../../../lib/i18n/index.ts";
import { stateInfo, stripLabelTag } from "../../../lib/sessionview.ts";
import type { Tenant } from "../parts/adminShared.ts";
import { UptimeAdminView } from "../../usage/UptimeHeatmap.tsx";

// --- All sessions overview (P3-9 admin) -------------------------------------
// A flat, cross-user list of every session so an operator can see at a glance
// what is running / resumable across the deployment. Reads GET /api/admin/sessions
// (super_admin: all tenants, optionally filtered; tenant_admin: their tenant).
// Polled like the per-member view; a client-side search narrows by user/label/repo.

export function AllSessionsView({ tenants, isSuper }: { tenants: Tenant[]; isSuper: boolean }) {
  const tr = useT();
  const [tenant, setTenant] = useState(isSuper ? "" : tenants[0]?.slug || "");
  const [q, setQ] = useState("");
  const [rows, setRows] = useState<any[] | null>(null);
  const [err, setErr] = useState("");
  const timer = useRef<ReturnType<typeof setInterval> | undefined>(undefined);
  const ser = useRef("");

  const poll = useCallback(async () => {
    try {
      const d = await api("api/admin/sessions" + (tenant ? "?tenant=" + encodeURIComponent(tenant) : ""));
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      const list = d.sessions || [];
      const s = JSON.stringify(list);
      if (s !== ser.current) {
        ser.current = s;
        setRows(list);
      }
    } catch {
      /* transient; keep last */
    }
  }, [tenant]);

  useEffect(() => {
    ser.current = "";
    setRows(null);
    poll();
    timer.current = setInterval(() => {
      if (!document.hidden) poll(); // hidden tab: skip the tick
    }, 5000);
    return () => clearInterval(timer.current);
  }, [poll]);

  // Deployment-wide overview: show RUNNING sessions only (stopped/resumable ones are
  // noise here — the per-member detail still lists them). Member detail is unchanged.
  const all = (rows || []).filter((s: any) => s.alive);
  const needle = q.trim().toLowerCase();
  const shown = needle
    ? all.filter((s: any) =>
        [s.user_key, s.email, s.label, s.repo, s.name, s.tenant]
          .some((v) => (v || "").toLowerCase().includes(needle)),
      )
    : all;

  return (
    <div className="admin-stage all-sessions-view">
      <section className="admin-panel">
        <div className="usage-toolbar">
          {isSuper && (
            <label>
              {tr("admin.tenant")}
              <select value={tenant} onChange={(e) => setTenant(e.target.value)}>
                <option value="">{tr("admin.all_tenants")}</option>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>{t.name}</option>
                ))}
              </select>
            </label>
          )}
          <label className="as-search">
            {tr("admin.search")}
            <input type="text" value={q} onChange={(e) => setQ(e.target.value)} placeholder={tr("admin.search_ph_sessions")} />
          </label>
          <span className="as-count muted">{tr("admin.running_count", { n: all.length })}</span>
        </div>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        {rows === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : shown.length === 0 ? (
          <p className="muted">{all.length === 0 ? tr("admin.no_running_sessions") : tr("admin.no_matching_sessions")}</p>
        ) : (
          <div className="all-sessions">
            {(() => {
              // Group by tenant (a header per tenant), so the row drops its tenant column
              // and stays narrow enough for a phone. Groups sorted by tenant name.
              const by = new Map<string, any[]>();
              for (const s of shown) {
                const k = s.tenant || "";
                (by.get(k) || by.set(k, []).get(k)!).push(s);
              }
              const tName = (slugv: string) => tenants.find((t) => t.slug === slugv)?.name || slugv || tr("admin.unknown");
              return [...by.entries()]
                .sort((a, b) => compareText(tName(a[0]), tName(b[0])))
                .map(([tslug, list]) => (
                  <div key={tslug || "_"} className="asx-group">
                    {/* With a single tenant in view (the tenant settings modal, or an active
                        filter) the header would just repeat the same name above every row. */}
                    {by.size > 1 && (
                      <div className="asx-group-head">
                        {tName(tslug)} <span className="muted">({list.length})</span>
                      </div>
                    )}
                    {list.map((s: any) => {
                      const st = stateInfo(s);
                      return (
                        <div key={s.tenant + "|" + s.user_key + "|" + s.name} className="adm-session">
                          <span className={"kind-tag kind-" + kindClass(s.kind)}>
                            <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                          </span>
                          <span className="asx-user mono" title={s.email || ""}>{s.user_key || tr("admin.unknown")}</span>
                          <span className="as-name mono" title={s.dir || ""}>{s.label ? stripLabelTag(s.label) : s.name}</span>
                          <span className="as-repo muted">{s.repo || ""}</span>
                          <span className={"session-state " + st.cls}>
                            <Icon name={st.icon} spin={st.spin} /> {st.text}
                          </span>
                          <span className="as-time muted">{s.started || ""}</span>
                        </div>
                      );
                    })}
                  </div>
                ));
            })()}
          </div>
        )}
      </section>
    </div>
  );
}

// --- Audit log (docs/log/20 M1) -------------------------------------------------
// The change-operation ledger: file / git / session mutations recorded by the CP
// proxy (actor = the member behind the resolved request). Reads GET /api/admin/audit
// (super_admin: whole deployment, optionally filtered by ?tenant=; tenant_admin:
// their tenant). Historical, so it's fetched on demand + manual refresh, not polled.

const auditCat = (action: string) => action.split(".")[0]; // fs | git | repo | session | egress
const fmtAt = (iso: string) => {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : fmtDateTime(d, DATETIME_FULL);
};

export function AuditView({ tenants, isSuper }: { tenants: Tenant[]; isSuper: boolean }) {
  const tr = useT();
  const [tenant, setTenant] = useState(isSuper ? "" : tenants[0]?.slug || "");
  const [q, setQ] = useState("");
  const [rows, setRows] = useState<any[] | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    setRows(null);
    setErr("");
    try {
      const d = await api("api/admin/audit" + (tenant ? "?tenant=" + encodeURIComponent(tenant) : ""));
      if (d?.error) {
        setErr(errText(d.error));
        setRows([]);
        return;
      }
      setRows(d.audit || []);
    } catch {
      setErr(tr("admin.load_error"));
      setRows([]);
    }
  }, [tenant, tr]);

  useEffect(() => {
    load();
  }, [load]);

  const needle = q.trim().toLowerCase();
  const shown = needle
    ? (rows || []).filter((a: any) =>
        [a.action, a.target, a.actor_email, a.actor_id, a.tenant].some((v) =>
          (v || "").toLowerCase().includes(needle),
        ),
      )
    : rows || [];

  return (
    <div className="admin-stage audit-view">
      <section className="admin-panel">
        <div className="usage-toolbar">
          {isSuper && (
            <label>
              {tr("admin.tenant")}
              <select value={tenant} onChange={(e) => setTenant(e.target.value)}>
                <option value="">{tr("admin.all_tenants")}</option>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>{t.name}</option>
                ))}
              </select>
            </label>
          )}
          <label className="as-search">
            {tr("admin.search")}
            <input type="text" value={q} onChange={(e) => setQ(e.target.value)} placeholder={tr("admin.search_ph_audit")} />
          </label>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
          <span className="as-count muted">{tr("admin.count_items", { n: (rows || []).length })}</span>
        </div>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        {rows === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : shown.length === 0 ? (
          <p className="muted">{(rows || []).length === 0 ? tr("admin.no_audit") : tr("admin.no_matching_audit")}</p>
        ) : (
          <div className="adm-audit">
            {shown.map((a: any) => (
              <div key={a.id} className="adm-audit-row">
                <span className="as-time muted">{fmtAt(a.at)}</span>
                <span className={"audit-action cat-" + auditCat(a.action)}>{a.action}</span>
                <span className="asx-user mono" title={a.actor_id}>{a.actor_email || a.actor_kind}</span>
                <span className="as-name mono" title={a.target}>{a.target}</span>
                {isSuper && <span className="as-repo muted">{a.tenant}</span>}
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

// --- Usage (showback, P3-9 stage 2) -----------------------------------------
// Deployment-wide occupancy the operator can attribute per tenant/member. Reads
// GET /api/admin/usage (JSON: per-member totals over the window). super_admin sees
// every tenant (optionally filtered); a tenant_admin is scoped to a tenant they
// administer. CSV export is a plain download link (cookie-authed; the endpoint
// scopes by the ?tenant= query param, so no X-AF-Tenant header is needed).

const fmtHrs = (secs: number) => (secs / 3600).toFixed(secs < 3600 ? 2 : 1) + " h";

export function UsageView({ tenants, isSuper }: { tenants: Tenant[]; isSuper: boolean }) {
  // Make the native <input type="date"> display format follow the app locale rather than the
  // browser language (works on Chrome/Safari, which honour lang; Firefox always follows the OS
  // locale — a browser limitation, not something to work around here).
  const locale = useLocale();
  const tr = useT();
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  // Non-super callers must scope to a tenant they administer (the API rejects the
  // deployment-wide view); default to their first tenant.
  const [tenant, setTenant] = useState(isSuper ? "" : tenants[0]?.slug || "");
  const [data, setData] = useState<any>(null);
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(false);

  const usageQuery = useCallback(() => {
    const qs = new URLSearchParams();
    if (from) qs.set("from", from);
    if (to) qs.set("to", to);
    if (tenant) qs.set("tenant", tenant);
    return qs.toString();
  }, [from, to, tenant]);

  const load = useCallback(async () => {
    setLoading(true);
    setErr("");
    try {
      const q = usageQuery();
      const d = await api("api/admin/usage" + (q ? "?" + q : ""));
      if (d?.error) {
        setErr(errText(d.error));
        setData(null);
      } else {
        setData(d);
      }
    } catch {
      setErr(tr("admin.usage_load_error"));
      setData(null);
    } finally {
      setLoading(false);
    }
  }, [usageQuery, tr]);

  // Load once on mount and whenever the tenant filter changes; the date range is
  // applied explicitly via the "apply" button (「適用」) so a partial date doesn't refetch.
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tenant]);

  const csvHref = () => {
    const u = new URL(rel("api/admin/usage"));
    u.searchParams.set("format", "csv");
    if (from) u.searchParams.set("from", from);
    if (to) u.searchParams.set("to", to);
    if (tenant) u.searchParams.set("tenant", tenant);
    return u.toString();
  };

  const totals: any[] = (data?.totals || []).slice().sort((a: any, b: any) => b.running_secs - a.running_secs);
  const maxSecs = totals.reduce((m: number, t: any) => Math.max(m, t.running_secs), 0);
  const grandSecs = totals.reduce((s: number, t: any) => s + t.running_secs, 0);

  return (
    <div className="admin-stage usage-view">
      <section className="admin-panel">
        <h4>{tr("admin.usage_title")}</h4>
        <p className="muted" style={{ margin: "0 0 12px" }}>{tr("admin.usage_intro")}</p>
        <div className="usage-toolbar">
          <label>
            {tr("admin.from")}
            <input type="date" lang={locale} value={from} onChange={(e) => setFrom(e.target.value)} />
          </label>
          <label>
            {tr("admin.to")}
            <input type="date" lang={locale} value={to} onChange={(e) => setTo(e.target.value)} />
          </label>
          {isSuper && (
            <label>
              {tr("admin.tenant")}
              <select value={tenant} onChange={(e) => setTenant(e.target.value)}>
                <option value="">{tr("admin.all_tenants")}</option>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>{t.name}</option>
                ))}
              </select>
            </label>
          )}
          <button className="primary" onClick={load} disabled={loading}>
            {loading ? "…" : tr("admin.apply")}
          </button>
          <a className="ghost usage-csv" href={csvHref()} download>
            <Icon name="cloud-download" /> CSV
          </a>
        </div>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        <div className="usage-summary">
          <div className="us-metric">
            <div className="us-val">{fmtHrs(grandSecs)}</div>
            <div className="us-lab muted">{tr("admin.total_running")}</div>
          </div>
          <div className="us-metric">
            <div className="us-val">{totals.length}</div>
            <div className="us-lab muted">{tr("admin.members")}</div>
          </div>
          {data && (
            <div className="us-range muted">{tr("admin.range", { from: data.from, to: data.to })}</div>
          )}
        </div>

        {data === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : totals.length === 0 ? (
          <p className="muted">{tr("admin.usage_no_records")}</p>
        ) : (
          <div className="usage-rows">
            {totals.map((t: any) => (
              <div key={(t.tenant || "") + "|" + t.user_key} className="usage-row">
                <span className="ur-key mono" title={t.email || ""}>{t.user_key || tr("admin.unknown")}</span>
                {isSuper && !tenant && <span className="ur-tenant muted">{t.tenant}</span>}
                <span className="ur-bar">
                  <span className="ur-fill" style={{ width: (maxSecs ? Math.round((t.running_secs / maxSecs) * 100) : 0) + "%" }} />
                </span>
                <span className="ur-hrs mono">{fmtHrs(t.running_secs)}</span>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* The same occupancy broken down by hour (docs/log/83). The totals above only answer
          "how many hours in August", which cannot separate a workspace left running from time
          actually worked. The tenant selector applies here too (it narrows everything below). */}
      <UptimeAdminView tenant={tenant} />
    </div>
  );
}
