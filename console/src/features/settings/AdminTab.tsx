import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, apiJSON, rawJSON, errText, rel } from "../../core/api/client.ts";
import { mobileMatches } from "../../lib/device.ts";
import { adminDepthRef } from "./store.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ConfirmDialog } from "../../ui/ConfirmDialog.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { kindLabel, kindClass, kindIcon } from "../../lib/sessionkind.ts";
import { fmtDateTime, DATETIME_FULL, compareText } from "../../lib/intl.ts";
import { useLocale, useT } from "../../lib/i18n/index.ts";
import { fmtGiB } from "../../lib/bytes.ts";
import { stateInfo } from "../../lib/sessionview.ts";
import { setTenantDict } from "../chat/ttsDict.ts";
// Tenant MCP distribution reuses the member tab's wire contract (docs/48 P4), so the
// name rule, the masked round-trip and the "remote only" shape are pinned by
// mcpWire.test.ts rather than by this component.
import {
  MCP_KINDS,
  NAME_RE,
  bodyOfTenant,
  emptyTenantForm,
  tenantFormOf,
  tenantFormValid,
} from "./mcpWire.ts";
import type { TenantForm, TenantServer } from "./mcpWire.ts";
// Same field furniture as the member tab's form (McpTab), so the two MCP forms stay
// one design instead of two.
import { Field, KVEditor, CheckRow, Check } from "./mcpForm.tsx";
// Egress allowlist tie-in (docs/48 §9). It matters more here than on the member tab: a
// distributed server that the proxy blocks is broken for EVERY member of the tenant.
import { EgressNote, useEgressCheck } from "./EgressNote.tsx";
import { hostsOf } from "./egressCheck.ts";
import type { EgressCheck } from "./egressCheck.ts";

// Admin API shapes (only the fields the UI reads; server responses may carry more).
interface Tenant {
  slug: string;
  name: string;
  users?: number;
  running?: number;
  max_workspaces?: number;
  max_sessions?: number;
  max_git_repos?: number;
  max_lfs_bytes?: number;
  max_workspace_mem?: number; // per-workspace RAM cap in bytes (0 = no tenant cap)
  session_idle_timeout?: string;
  ws_idle_timeout?: string;
  allow_agent_self_update?: boolean;
  terminal_history_retention_days?: number;
  // Per-tenant login rules, stored as CSV (docs/61 §61.9.7).
  allowed_providers?: string;
  auto_join_domains?: string;
  allowed_domains?: string;
}
// A tenant-defined sign-in method (docs/61 §61.11). client_secret is write-only —
// it is never in a response, and has_secret is how the form knows one is stored.
interface TenantIdP {
  id: string;
  name: string;
  label_ja?: string;
  label_en?: string;
  issuer: string;
  client_id: string;
  client_secret?: string;
  trust: string;
  allowed_tids?: string;
  allowed_domains?: string;
  provider_id?: string;
  tenant_slug?: string;
  status?: string;
  has_secret?: boolean;
  approved_by?: string;
  approved_at?: string;
  usable?: boolean;
}
interface Member {
  user_key: string;
  email?: string;
  role: string;
  super_admin?: boolean;
  state?: string;
  max_sessions?: number | null;
  mem_limit?: number | null; // per-workspace RAM cap in bytes (0/undefined = unset)
  /** "active" | "removed". A removed member is off the roster and can no longer
   *  sign in, but stays on THIS list so the rest of the offboarding sequence
   *  (stop workspace → clean home) is still reachable (docs/61 §61.10.6). */
  status?: string;
}
// Drill-down location: stage plus (optionally) the tenant slug / member being viewed.
interface View {
  stage: string;
  slug?: string;
  member?: Member;
}

// AdminTab (super_admin only): a staged drill-down —
//   テナント一覧 → テナント詳細 → メンバー詳細
// Each stage stands on its own (no cramped two-column form); the breadcrumb walks
// back. The member stage surfaces live Workspace resources (mem / CPU / disk) and
// the member's session list, served by the per-member admin endpoints.

// GiB with adaptive precision (shared fmtGiB) plus AdminTab's "G" suffix.
const ADMIN_MODES = ["manage", "sessions", "usage", "audit", "egress", "mcp", "tts"]; // swipe order for the mode tabs
const fmtG = (b: number) => fmtGiB(b) + "G";
const fmtPct = (n: number | null | undefined) => (n == null ? "–" : Math.round(n) + "%");
// MB → a "N GiB" hint for the memory input (whole number when clean, else 1 decimal).
const fmtGbHint = (mb: number) => {
  const gb = mb / 1024;
  return (Number.isInteger(gb) ? String(gb) : gb.toFixed(1)) + " GiB";
};

export function AdminTab() {
  const tr = useT();
  // shared with the settings store so closeAdmin can pop all drill levels at once
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  const [forbidden, setForbidden] = useState(false);
  const [isSuper, setIsSuper] = useState(false); // super_admin: unlocks deployment-wide controls
  const [mode, setMode] = useState("manage"); // manage (tenant drilldown) | usage (showback)
  // view: {stage:'tenants'} | {stage:'tenant', slug} | {stage:'member', slug, member}
  const [view, setView] = useState<View>({ stage: "tenants" });

  // Drill-down navigation is driven by browser history so back/forward (and the device
  // back gesture) step through the levels. Each drill-in pushes an entry carrying the
  // target view; a back pops it and this listener restores the parent view (state.tsx
  // keeps the modal open while the entry is still modal:'admin'). depthOf feeds the
  // shared adminDepthRef so the X/backdrop can pop all levels at once.
  const depthOf = (v: View) => (v.stage === "member" ? 2 : v.stage === "tenant" ? 1 : 0);
  useEffect(() => {
    adminDepthRef.current = depthOf(view);
  }, [adminDepthRef, view]);
  useEffect(() => {
    const onPop = (e: PopStateEvent) => {
      if (e.state && e.state.modal === "admin") setView(e.state.adminView || { stage: "tenants" });
    };
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);
  const drill = (next: View) => {
    setView(next);
    try {
      history.pushState({ ...(history.state || {}), modal: "admin", adminView: next }, "");
    } catch {}
  };
  // Mobile: a horizontal swipe anywhere in the (full-screen) admin modal switches the
  // mode tabs (テナント管理 / セッション / 使用量). Window-level listeners are more
  // reliable than element handlers over a scrolling body; the drawer-open swipe is
  // suppressed while a modal is up, so there's no conflict.
  useEffect(() => {
    if (!mobileMatches()) return;
    let sx = 0, sy = 0;
    const start = (e: TouchEvent) => {
      const t = e.touches[0];
      if (t) {
        sx = t.clientX;
        sy = t.clientY;
      }
    };
    const end = (e: TouchEvent) => {
      const t = e.changedTouches[0];
      if (!t) return;
      const dx = t.clientX - sx;
      const dy = t.clientY - sy;
      if (Math.abs(dx) < 50 || Math.abs(dx) <= Math.abs(dy)) return; // horizontal only
      setMode((m) => {
        const i = ADMIN_MODES.indexOf(m);
        const n = i + (dx < 0 ? 1 : -1);
        return n >= 0 && n < ADMIN_MODES.length ? ADMIN_MODES[n] : m;
      });
    };
    window.addEventListener("touchstart", start, { passive: true });
    window.addEventListener("touchend", end, { passive: true });
    return () => {
      window.removeEventListener("touchstart", start);
      window.removeEventListener("touchend", end);
    };
  }, []);

  const loadTenants = useCallback(async () => {
    try {
      const d = await api("api/admin/tenants");
      if (d && d.error) {
        setForbidden(true);
        return;
      }
      setTenants(d.tenants || []);
      setIsSuper(!!d.super_admin);
    } catch {
      setForbidden(true);
    }
  }, []);
  useEffect(() => {
    loadTenants();
  }, [loadTenants]);

  if (forbidden) return <p className="muted pad">{tr("admin.forbidden")}</p>;
  if (tenants === null) return <p className="muted pad">{tr("common.loading")}</p>;

  const tenant = view.slug ? tenants.find((t) => t.slug === view.slug) : null;
  const tenantName = tenant ? tenant.name : view.slug;

  const goBack = () => history.back(); // step up one drill level via history

  return (
    <div className="admin">
      <div className="seg admin-modes">
        <button type="button" className={"seg-btn" + (mode === "manage" ? " active" : "")} onClick={() => setMode("manage")}>
          <Icon name="organization" /> {tr("admin.mode_manage")}
        </button>
        <button type="button" className={"seg-btn" + (mode === "sessions" ? " active" : "")} onClick={() => setMode("sessions")}>
          <Icon name="list-tree" /> {tr("admin.mode_sessions")}
        </button>
        <button type="button" className={"seg-btn" + (mode === "usage" ? " active" : "")} onClick={() => setMode("usage")}>
          <Icon name="graph" /> {tr("admin.mode_usage")}
        </button>
        <button type="button" className={"seg-btn" + (mode === "audit" ? " active" : "")} onClick={() => setMode("audit")}>
          <Icon name="history" /> {tr("admin.mode_audit")}
        </button>
        <button type="button" className={"seg-btn" + (mode === "egress" ? " active" : "")} onClick={() => setMode("egress")}>
          <Icon name="globe" /> {tr("admin.mode_egress")}
        </button>
        <button type="button" className={"seg-btn" + (mode === "mcp" ? " active" : "")} onClick={() => setMode("mcp")}>
          <Icon name="plug" /> {tr("admin.mode_mcp")}
        </button>
        <button type="button" className={"seg-btn" + (mode === "tts" ? " active" : "")} onClick={() => setMode("tts")}>
          <Icon name="unmute" /> {tr("admin.mode_tts")}
        </button>
      </div>

      {mode === "sessions" && <AllSessionsView tenants={tenants} isSuper={isSuper} />}
      {mode === "usage" && <UsageView tenants={tenants} isSuper={isSuper} />}
      {mode === "audit" && <AuditView tenants={tenants} isSuper={isSuper} />}
      {mode === "egress" && <EgressView />}
      {mode === "mcp" && <McpAdminView tenants={tenants} />}
      {mode === "tts" && <TtsAdminView />}

      {mode === "manage" && (
      <>
      <div className="admin-nav">
        {view.stage !== "tenants" && (
          <button className="admin-back" onClick={goBack}>
            <Icon name="arrow-left" /> {tr("common.back")}
          </button>
        )}
        <nav className="admin-crumbs">
          <button
            className={"crumb" + (view.stage === "tenants" ? " here" : "")}
            onClick={() => {
              const d = depthOf(view);
              if (d > 0) history.go(-d);
            }}
          >
            <Icon name="organization" /> {tr("admin.crumb_tenants")}
          </button>
          {view.slug && (
            <>
              <Icon name="chevron-right" className="crumb-sep" />
              <button
                className={"crumb" + (view.stage === "tenant" ? " here" : "")}
                onClick={() => {
                  if (view.stage === "member") history.back();
                }}
              >
                {tenantName}
              </button>
            </>
          )}
          {view.stage === "member" && (
            <>
              <Icon name="chevron-right" className="crumb-sep" />
              <span className="crumb here">{view.member?.user_key}</span>
            </>
          )}
        </nav>
      </div>

      {view.stage === "tenants" && (
        <>
          <TenantsList
            tenants={tenants}
            isSuper={isSuper}
            onReload={loadTenants}
            onOpen={(slug) => drill({ stage: "tenant", slug })}
          />
          {/* The deployment-wide register of tenant-defined sign-in methods
              (docs/61 §61.11.6). Only a super_admin approves one, so only a
              super_admin sees the list. */}
          {isSuper && <SignInMethodRegister />}
        </>
      )}
      {view.stage === "tenant" && (
        <TenantView
          slug={view.slug!}
          tenant={tenant}
          isSuper={isSuper}
          onChanged={loadTenants}
          onOpenMember={(member) => drill({ stage: "member", slug: view.slug, member })}
        />
      )}
      {view.stage === "member" && (
        <MemberView
          slug={view.slug!}
          member={view.member!}
          isSuper={isSuper}
          onChanged={loadTenants}
          onRemoved={() => {
            // The member no longer exists at this stage — step back to the tenant
            // rather than leave a detail view of somebody who is off the roster.
            loadTenants();
            goBack();
          }}
        />
      )}
      </>
      )}
    </div>
  );
}

// --- All sessions overview (P3-9 admin) -------------------------------------
// A flat, cross-user list of every session so an operator can see at a glance
// what is running / resumable across the deployment. Reads GET /api/admin/sessions
// (super_admin: all tenants, optionally filtered; tenant_admin: their tenant).
// Polled like the per-member view; a client-side search narrows by user/label/repo.

function AllSessionsView({ tenants, isSuper }: { tenants: Tenant[]; isSuper: boolean }) {
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
                    <div className="asx-group-head">
                      {tName(tslug)} <span className="muted">({list.length})</span>
                    </div>
                    {list.map((s: any) => {
                      const st = stateInfo(s);
                      return (
                        <div key={s.tenant + "|" + s.user_key + "|" + s.name} className="adm-session">
                          <span className={"kind-tag kind-" + kindClass(s.kind)}>
                            <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                          </span>
                          <span className="asx-user mono" title={s.email || ""}>{s.user_key || tr("admin.unknown")}</span>
                          <span className="as-name mono" title={s.dir || ""}>{s.label ? s.label.replace(/^\[AF\]\s*/, "") : s.name}</span>
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

// --- Audit log (docs/20 M1) -------------------------------------------------
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

function AuditView({ tenants, isSuper }: { tenants: Tenant[]; isSuper: boolean }) {
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

// --- Egress: allowlist + mode + observations (docs/20 M2/M3) -----------------
// Deployment-wide egress control (super_admin). Manages the versioned allowlist
// (approve agent-proposed entries, add/retire), toggles log-only vs enforce, and
// shows destination stats from the forward proxy (would-allow / would-block).

function EgressView() {
  const tr = useT();
  const [data, setData] = useState<any | null>(null); // { egress, mode, enforce }
  const [list, setList] = useState<any[] | null>(null); // allowlist entries
  const [err, setErr] = useState("");
  const [days, setDays] = useState(7);
  const [entry, setEntry] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setErr("");
    try {
      const [d, al] = await Promise.all([
        api("api/admin/egress?days=" + days),
        api("api/admin/egress/allowlist"),
      ]);
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setData(d);
      setList(al?.allowlist || []);
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [days, tr]);
  useEffect(() => {
    load();
  }, [load]);

  const setMode = async (enforce: boolean) => {
    setBusy(true);
    try {
      await apiJSON("api/admin/egress/mode", "PUT", { enforce });
      await load();
    } finally {
      setBusy(false);
    }
  };
  const addEntry = async (e: FormEvent) => {
    e.preventDefault();
    const v = entry.trim();
    if (!v) return;
    setBusy(true);
    try {
      await apiJSON("api/admin/egress/allowlist", "POST", { entry: v, reason });
      setEntry("");
      setReason("");
      await load();
    } finally {
      setBusy(false);
    }
  };
  const setState = async (id: string, state: string) => {
    setBusy(true);
    try {
      await apiJSON("api/admin/egress/allowlist/" + encodeURIComponent(id) + "/state", "POST", { state });
      await load();
    } finally {
      setBusy(false);
    }
  };

  const enforce = !!data?.enforce;
  const proposed = (list || []).filter((e: any) => e.state === "proposed");
  const active = (list || []).filter((e: any) => e.state === "active");
  const stats = data?.egress || [];

  return (
    <div className="admin-stage egress-view">
      {/* mode toggle */}
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.mode_label")}</span>
          {/* "log-only" / "enforce" はサーバ側モードの識別子そのもの（説明文の
              admin.egress_*_note でも同じ語で参照する）なので意図的に訳さない。 */}
          <span className="seg sm">
            <button
              type="button"
              className={"seg-btn" + (!enforce ? " active" : "")}
              disabled={busy}
              onClick={() => setMode(false)}
            >
              log-only
            </button>
            <button
              type="button"
              className={"seg-btn" + (enforce ? " active" : "")}
              disabled={busy}
              onClick={() => setMode(true)}
            >
              enforce
            </button>
          </span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        {enforce ? (
          <p className="form-err">{tr("admin.egress_enforce_note")}</p>
        ) : (
          <p className="muted">{tr("admin.egress_logonly_note")}</p>
        )}
        {err && <p className="form-err">{err}</p>}
      </section>

      {/* agent-proposed entries awaiting approval (docs/20 M4) */}
      {proposed.length > 0 && (
        <section className="admin-panel">
          <h4 className="egress-h">{tr("admin.egress_proposed")}</h4>
          {proposed.map((e: any) => (
            <div key={e.id} className="adm-allow-row">
              <span className="as-name mono" title={e.entry}>{e.entry}</span>
              <span className="as-repo muted" title={e.reason}>{e.reason}</span>
              <span className="muted" title={e.added_by}>{e.added_by}</span>
              <span className="allow-acts">
                <button type="button" className="btn xs" disabled={busy} onClick={() => setState(e.id, "active")}>{tr("admin.approve")}</button>
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setState(e.id, "retired")}>{tr("admin.reject")}</button>
              </span>
            </div>
          ))}
        </section>
      )}

      {/* active allowlist + add */}
      <section className="admin-panel">
        <h4 className="egress-h">{tr("admin.egress_allowlist")}</h4>
        <form className="egress-add" onSubmit={addEntry}>
          <input
            type="text"
            value={entry}
            onChange={(e) => setEntry(e.target.value)}
            placeholder={tr("admin.egress_entry_ph")}
          />
          <input
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={tr("admin.egress_reason_ph")}
          />
          <button type="submit" className="btn" disabled={busy || !entry.trim()}>{tr("admin.add")}</button>
        </form>
        {active.length === 0 ? (
          <p className="muted">{tr("admin.egress_no_entries")}</p>
        ) : (
          active.map((e: any) => (
            <div key={e.id} className="adm-allow-row">
              <span className="as-name mono" title={e.entry}>{e.entry}</span>
              <span className="as-repo muted" title={e.reason}>{e.reason}</span>
              <span className="muted" title={e.added_by}>{e.added_by}</span>
              <span className="allow-acts">
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setState(e.id, "retired")}>{tr("admin.retire")}</button>
              </span>
            </div>
          ))
        )}
      </section>

      {/* observed destinations */}
      <section className="admin-panel">
        <div className="usage-toolbar">
          <h4 className="egress-h">{tr("admin.egress_observed")}</h4>
          <label>
            {tr("admin.period")}
            <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
              <option value={1}>{tr("admin.days_1")}</option>
              <option value={7}>{tr("admin.days_7")}</option>
              <option value={30}>{tr("admin.days_30")}</option>
            </select>
          </label>
        </div>
        {data === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : stats.length === 0 ? (
          <p className="muted">{tr("admin.egress_no_records")}</p>
        ) : (
          <div className="adm-egress">
            {stats.map((e: any) => (
              <div key={e.host} className="adm-egress-row">
                <span className="as-name mono" title={e.host}>{e.host}</span>
                <span className="egress-allow">{tr("admin.egress_allowed", { n: e.allowed })}</span>
                {e.blocked > 0 && <span className="egress-block">{e.blocked} {enforce ? tr("admin.egress_blocked") : tr("admin.egress_blocked_candidate")}</span>}
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

// --- Tenant-distributed MCP servers (docs/48 P4 + ADR0031) -------------------------
//
// A tenant_admin registers a REMOTE MCP server once and every member of that tenant gets
// it in their workspace — assistants and interactive sessions both. There is deliberately
// no stdio option: distributing a command means the admin runs arbitrary code in every
// member's container, so ADR0031 決定 2 keeps the columns out of the CP table entirely
// rather than relying on a form that omits the field.
//
// Header values are write-only from here: they come back masked ("***") and sending them
// back unchanged keeps the stored value. The 秘密 that CANNOT be protected is the one
// distributed with values — every member can read it in their own container — which is
// what the "各メンバーが値を入力" toggle (user_secret) exists to avoid.

function McpAdminView({ tenants }: { tenants: Tenant[] }) {
  const tr = useT();
  const [slug, setSlug] = useState(tenants[0]?.slug || "");
  const [rows, setRows] = useState<TenantServer[] | null>(null);
  const [err, setErr] = useState("");
  const [form, setForm] = useState<TenantForm | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDel, setConfirmDel] = useState<TenantServer | null>(null);
  const { check: egress, recheck: recheckEgress } = useEgressCheck(
    hostsOf([...(rows || []).map((s) => s.url), form?.url]),
  );

  const load = useCallback(async () => {
    if (!slug) return;
    setErr("");
    try {
      const d = await api("api/admin/mcp-servers?tenant=" + encodeURIComponent(slug));
      if (d?.error) {
        setErr(errText(d.error));
        setRows([]);
        return;
      }
      setRows(d.servers || []);
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [slug, tr]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async (f: TenantForm) => {
    setBusy(true);
    try {
      const path = f.id ? "api/admin/mcp-servers/" + encodeURIComponent(f.id) : "api/admin/mcp-servers";
      const d = await apiJSON(path, f.id ? "PUT" : "POST", bodyOfTenant(f, slug));
      if (d && d.error) {
        setErr(errText(d.error));
        return;
      }
      setForm(null);
      setErr("");
      await load();
    } finally {
      setBusy(false);
    }
  };

  const remove = async (s: TenantServer) => {
    setBusy(true);
    try {
      const d = await apiJSON(
        "api/admin/mcp-servers/" + encodeURIComponent(s.id) + "?tenant=" + encodeURIComponent(slug),
        "DELETE",
      );
      if (d && d.error) setErr(errText(d.error));
      await load();
    } finally {
      setBusy(false);
      setConfirmDel(null);
    }
  };

  return (
    <div className="admin-stage mcp-admin">
      <section className="admin-panel">
        <div className="usage-toolbar">
          <label>
            {tr("admin.tenant")}
            <select value={slug} onChange={(e) => setSlug(e.target.value)}>
              {tenants.map((t) => (
                <option key={t.slug} value={t.slug}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        <p className="muted">{tr("admin.mcp_intro")}</p>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        <h4 className="egress-h">{tr("admin.mcp_distributed")}</h4>
        {rows === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : rows.length === 0 ? (
          <p className="muted">{tr("admin.mcp_none")}</p>
        ) : (
          rows.map((s) =>
            form && form.id === s.id ? (
              <McpAdminForm
                key={s.id}
                form={form}
                setForm={setForm}
                busy={busy}
                onSave={save}
                egress={egress}
                onProposed={recheckEgress}
              />
            ) : (
              <Fragment key={s.id}>
                <div className={"adm-mcp-row" + (s.enabled ? "" : " off")}>
                  <span className="as-name mono" title={s.name}>
                    {s.name}
                  </span>
                  <span className="as-repo muted" title={s.url}>
                    {s.label || s.url}
                  </span>
                  {s.user_secret && (
                    <span className="mcp-origin mcp-origin-tenant">{tr("admin.mcp_user_secret_badge")}</span>
                  )}
                  {!s.enabled && <span className="muted">{tr("admin.mcp_disabled")}</span>}
                  <span className="allow-acts">
                    <button type="button" className="ghost xs" disabled={busy} onClick={() => setForm(tenantFormOf(s))}>
                      {tr("mcp.edit")}
                    </button>
                    <button type="button" className="ghost xs danger" disabled={busy} onClick={() => setConfirmDel(s)}>
                      {tr("common.delete")}
                    </button>
                  </span>
                </div>
                <EgressNote
                  url={s.url}
                  check={egress}
                  defaultReason={tr("mcp.egress_reason_for", { name: s.name })}
                  onProposed={recheckEgress}
                />
              </Fragment>
            ),
          )
        )}
        {form && form.id === "" ? (
          <McpAdminForm
            form={form}
            setForm={setForm}
            busy={busy}
            onSave={save}
            egress={egress}
            onProposed={recheckEgress}
          />
        ) : (
          !form && (
            <button type="button" className="ghost" disabled={!slug} onClick={() => setForm(emptyTenantForm())}>
              <Icon name="add" /> {tr("admin.mcp_add")}
            </button>
          )
        )}
      </section>

      {confirmDel && (
        <ConfirmDialog
          title={tr("admin.mcp_del_title")}
          confirmLabel={tr("common.delete_confirm")}
          danger
          busy={busy}
          onConfirm={() => void remove(confirmDel)}
          onCancel={() => setConfirmDel(null)}
        >
          {tr("admin.mcp_del_body", { name: confirmDel.name })}
        </ConfirmDialog>
      )}
    </div>
  );
}

// McpAdminForm — the tenant distribution form. Remote only by construction (see
// bodyOfTenant): there is no transport switch to get wrong.
function McpAdminForm({
  form,
  setForm,
  busy,
  onSave,
  egress,
  onProposed,
}: {
  form: TenantForm;
  setForm: (f: TenantForm | null) => void;
  busy: boolean;
  onSave: (f: TenantForm) => Promise<void>;
  egress: EgressCheck | null;
  onProposed: () => void;
}) {
  const tr = useT();
  const patch = (part: Partial<TenantForm>) => setForm({ ...form, ...part });
  const valid = tenantFormValid(form);
  const nameBad = form.name.trim() !== "" && !NAME_RE.test(form.name.trim());

  return (
    <div className="ssm-frm mcp-frm adm-mcp-form">
      <div className="ssm-fgroup">
        <p className="ssm-fg-title">{form.id ? tr("admin.mcp_edit_title") : tr("admin.mcp_add")}</p>
        <div className="ssm-fgrid">
          <Field label={tr("mcp.f_name")} req hint={nameBad ? tr("mcp.f_name_bad") : tr("mcp.f_name_hint")}>
            <input
              className="cinput mono"
              placeholder="wiki"
              value={form.name}
              onChange={(e) => patch({ name: e.target.value })}
              autoFocus
            />
          </Field>
          <Field label={tr("mcp.f_label")} hint={tr("mcp.f_label_hint")}>
            <input
              className="cinput"
              placeholder={tr("mcp.f_label_placeholder")}
              value={form.label}
              onChange={(e) => patch({ label: e.target.value })}
            />
          </Field>
          <Field label="URL" req wide hint={tr("admin.mcp_url_hint")}>
            <input
              className="cinput"
              placeholder="https://mcp.example.com/mcp"
              value={form.url}
              onChange={(e) => patch({ url: e.target.value })}
            />
          </Field>

          {/* The credential decision comes BEFORE the headers, because it decides what
              the header rows even ask for (name+value vs name only). */}
          <Field label={tr("admin.mcp_secret_policy")} wide hint={tr("admin.mcp_user_secret_hint")}>
            <CheckRow>
              <Check checked={form.userSecret} onChange={(v) => patch({ userSecret: v })}>
                {tr("admin.mcp_user_secret")}
              </Check>
            </CheckRow>
          </Field>
          <Field
            label={tr("mcp.f_headers")}
            wide
            hint={form.userSecret ? tr("admin.mcp_headers_names_hint") : tr("admin.mcp_headers_hint")}
          >
            <KVEditor
              rows={form.headers}
              onChange={(headers) => patch({ headers })}
              keyPlaceholder="Authorization"
              addLabel={tr("mcp.add_header")}
              noValue={form.userSecret}
            />
          </Field>

          {/* Deliberately NOT marked required: both off is a legal staging state
              (stored, distributed to nothing) — see secrets.MCPTargets. */}
          <Field label={tr("mcp.f_targets")} wide hint={tr("mcp.f_targets_hint")}>
            <CheckRow>
              <Check checked={form.assistant} onChange={(v) => patch({ assistant: v })}>
                {tr("mcp.target_assistant")}
              </Check>
              <Check checked={form.session} onChange={(v) => patch({ session: v })}>
                {tr("mcp.target_session")}
              </Check>
            </CheckRow>
          </Field>
          <Field label={tr("mcp.f_kinds")} wide hint={tr("mcp.f_kinds_hint")}>
            <CheckRow>
              {MCP_KINDS.map((k) => (
                <Check
                  key={k}
                  checked={form.kinds.includes(k)}
                  onChange={() =>
                    patch({ kinds: form.kinds.includes(k) ? form.kinds.filter((x) => x !== k) : [...form.kinds, k] })
                  }
                >
                  {kindLabel(k)}
                </Check>
              ))}
            </CheckRow>
          </Field>
          <Field label={tr("mcp.f_enabled")} wide hint={tr("admin.mcp_enabled_hint")}>
            <CheckRow>
              <Check checked={form.enabled} onChange={(v) => patch({ enabled: v })}>
                {tr("mcp.enabled_on")}
              </Check>
            </CheckRow>
          </Field>
        </div>
        <EgressNote
          url={form.url}
          check={egress}
          defaultReason={tr("mcp.egress_reason_for", { name: form.name.trim() || form.url.trim() })}
          onProposed={onProposed}
        />
        <p className="ps-note">{tr("admin.mcp_restart_note")}</p>
      </div>
      <div className="ssm-frm-foot">
        <button type="button" className="primary" disabled={busy || !valid} onClick={() => void onSave(form)}>
          {form.id ? tr("common.save") : tr("admin.mcp_save_add")}
        </button>
        <button type="button" className="ghost" onClick={() => setForm(null)}>
          {tr("common.cancel")}
        </button>
        <span className="req-note">
          <b>*</b> {tr("ssm.req_note")}
        </span>
      </div>
    </div>
  );
}

// --- TTS: VOICEVOX エンジンの管理者トグル（docs/24 Phase 2） -------------------
// super_admin のみ。AWS では ECS Service の desired count を 0↔1（オンデマンド起動・
// 停止中コスト 0）。起動〜ready まで 1〜2 分かかるので、その間は 5s ポーリングで
// 「準備中」を追従表示する（auto ルーティングは Polly JP が代読）。ECS 管理外（dev の
// 常駐 docker 等）ではトグルはルーティングの有効/無効のみ。

function TtsAdminView() {
  const tr = useT();
  const [data, setData] = useState<any | null>(null); // { managed, enabled, engine, polly, dict }
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  // テナント共通の読み仮名辞書（全ユーザーの読み上げに適用。ユーザー辞書が同表記を上書き）。
  // dict=編集中の値（null=未ロード）、savedDict=サーバ側の値（dirty 判定用）。
  const [dict, setDict] = useState<string | null>(null);
  const [savedDict, setSavedDict] = useState("");
  const [dictBusy, setDictBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await api("api/admin/tts");
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      setData(d);
      const dv = typeof d.dict === "string" ? d.dict : "";
      setSavedDict(dv);
      setDict((cur) => (cur === null ? dv : cur)); // 編集中の入力はポーリングで潰さない
    } catch {
      setErr(tr("admin.load_error"));
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);
  // 有効なのに未 ready（ECS 起動中）の間は自動更新して readiness を追う。
  useEffect(() => {
    if (!data?.enabled || data?.engine?.ready) return;
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [data, load]);

  const setEnabled = async (enabled: boolean) => {
    setBusy(true);
    try {
      const d = await apiJSON("api/admin/tts", "PUT", { enabled });
      if (d?.error) setErr(errText(d.error));
      else setData(d);
    } finally {
      setBusy(false);
    }
  };

  const saveDict = async () => {
    if (dict === null) return;
    setDictBusy(true);
    try {
      const d = await apiJSON("api/admin/tts/dict", "PUT", { dict });
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      setData(d);
      setSavedDict(dict);
      setTenantDict(dict); // 自分のブラウザの読み上げにも即反映（他ユーザーは次回ロードから）
    } finally {
      setDictBusy(false);
    }
  };

  const enabled = !!data?.enabled;
  const engine = data?.engine || {};
  const engineLabel = !data
    ? "…"
    : engine.ready
      ? tr("admin.tts_running")
      : engine.state === "starting"
        ? tr("admin.tts_starting")
        : engine.state === "running"
          ? tr("admin.tts_running_waiting")
          : enabled && data.managed
            ? tr("admin.tts_stopped")
            : tr("admin.tts_stopped_or_off");

  return (
    <div className="admin-stage">
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.tts_engine_label")}</span>
          <span className="seg sm">
            <button
              type="button"
              className={"seg-btn" + (enabled ? " active" : "")}
              disabled={busy || data === null}
              onClick={() => setEnabled(true)}
            >
              {tr("admin.enable")}
            </button>
            <button
              type="button"
              className={"seg-btn" + (!enabled ? " active" : "")}
              disabled={busy || data === null}
              onClick={() => setEnabled(false)}
            >
              {tr("admin.disable")}
            </button>
          </span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        {data && (
          <>
            <p className={engine.ready ? "muted" : enabled ? "form-err" : "muted"}>
              {tr("admin.tts_engine_prefix")}{engineLabel}
              {data.managed ? tr("admin.tts_managed") : tr("admin.tts_external")}
              {tr("admin.tts_polly_sep")}{data.polly?.ready ? tr("admin.tts_polly_ready") : tr("admin.tts_polly_unset")}
            </p>
            {enabled && !engine.ready && data.managed && (
              <p className="muted">{tr("admin.tts_starting_note")}</p>
            )}
            {engine.error && <p className="form-err">{engine.error}</p>}
          </>
        )}
        {err && <p className="form-err">{err}</p>}
        <p className="muted">{tr("admin.tts_disable_note")}</p>
      </section>
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.tts_dict_title")}</span>
          <button
            type="button"
            className="btn primary"
            disabled={dictBusy || dict === null || dict === savedDict}
            onClick={saveDict}
          >
            {dictBusy ? tr("admin.saving") : tr("common.save")}
          </button>
        </div>
        <textarea
          className="ds-userdict"
          value={dict ?? ""}
          onChange={(e) => setDict(e.target.value)}
          rows={8}
          spellCheck={false}
          disabled={dict === null}
          placeholder={tr("admin.tts_dict_ph")}
        />
        <p className="muted">{tr("admin.tts_dict_note")}</p>
      </section>
    </div>
  );
}

// --- Usage (showback, P3-9 段2) ---------------------------------------------
// Deployment-wide occupancy the operator can attribute per tenant/member. Reads
// GET /api/admin/usage (JSON: per-member totals over the window). super_admin sees
// every tenant (optionally filtered); a tenant_admin is scoped to a tenant they
// administer. CSV export is a plain download link (cookie-authed; the endpoint
// scopes by the ?tenant= query param, so no X-AF-Tenant header is needed).

const fmtHrs = (secs: number) => (secs / 3600).toFixed(secs < 3600 ? 2 : 1) + " h";

function UsageView({ tenants, isSuper }: { tenants: Tenant[]; isSuper: boolean }) {
  // native <input type="date"> の表示形式を、ブラウザ言語ではなくアプリのロケールに追従させる
  // （lang を尊重する Chrome/Safari で有効。Firefox は OS ロケール依存で不変＝ブラウザ側の制約）。
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
  // applied explicitly via the 適用 button so typing a partial date doesn't refetch.
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
    </div>
  );
}

// --- Stage 1: tenant list ---------------------------------------------------

function TenantsList({
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

function NewTenant({ onCreated, onCancel }: { onCreated: () => void; onCancel: () => void }) {
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

// --- Stage 2: tenant detail (limits + members) ------------------------------

function TenantView({
  slug,
  tenant,
  isSuper,
  onChanged,
  onOpenMember,
}: {
  slug: string;
  tenant: Tenant | null | undefined;
  isSuper: boolean;
  onChanged: () => void;
  onOpenMember: (m: Member) => void;
}) {
  const tr = useT();
  const [maxWs, setMaxWs] = useState<number | string>(tenant?.max_workspaces || 0);
  const [maxSs, setMaxSs] = useState<number | string>(tenant?.max_sessions || 0);
  const [maxRepos, setMaxRepos] = useState<number | string>(tenant?.max_git_repos || 0);
  // LFS cap is stored in bytes but edited in MB for usability.
  const [maxLfsMb, setMaxLfsMb] = useState<number | string>(Math.round((tenant?.max_lfs_bytes || 0) / 1048576));
  // Per-workspace RAM cap: stored in bytes, edited in MB.
  const [maxWsMemMb, setMaxWsMemMb] = useState<number | string>(Math.round((tenant?.max_workspace_mem || 0) / 1048576));
  const [sessIdle, setSessIdle] = useState(tenant?.session_idle_timeout || "");
  const [wsIdle, setWsIdle] = useState(tenant?.ws_idle_timeout || "");
  const [allowUpd, setAllowUpd] = useState(!!tenant?.allow_agent_self_update);
  const [termRetention, setTermRetention] = useState(tenant?.terminal_history_retention_days || 0);
  const [saved, setSaved] = useState(false);
  const toast = useToast();
  const [members, setMembers] = useState<Member[] | null>(null);

  useEffect(() => {
    setMaxWs(tenant?.max_workspaces || 0);
    setMaxSs(tenant?.max_sessions || 0);
    setMaxRepos(tenant?.max_git_repos || 0);
    setMaxLfsMb(Math.round((tenant?.max_lfs_bytes || 0) / 1048576));
    setMaxWsMemMb(Math.round((tenant?.max_workspace_mem || 0) / 1048576));
    setSessIdle(tenant?.session_idle_timeout || "");
    setWsIdle(tenant?.ws_idle_timeout || "");
    setAllowUpd(!!tenant?.allow_agent_self_update);
    setTermRetention(tenant?.terminal_history_retention_days || 0);
  }, [slug, tenant]);

  const loadMembers = useCallback(async () => {
    try {
      const d = await api(`api/admin/tenants/${encodeURIComponent(slug)}/members`);
      setMembers(d.members || []);
    } catch {
      setMembers([]);
    }
  }, [slug]);
  useEffect(() => {
    setMembers(null);
    loadMembers();
  }, [loadMembers]);

  const saveLimits = async () => {
    const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/limits`, "PUT", {
      max_workspaces: +maxWs || 0,
      max_sessions: +maxSs || 0,
      max_git_repos: +maxRepos || 0,
      max_lfs_bytes: Math.round(+maxLfsMb || 0) * 1048576,
      max_workspace_mem: Math.round(+maxWsMemMb || 0) * 1048576,
      session_idle_timeout: sessIdle.trim(),
      ws_idle_timeout: wsIdle.trim(),
      allow_agent_self_update: allowUpd,
      terminal_history_retention_days: termRetention,
    });
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    setSaved(true);
    setTimeout(() => setSaved(false), 1500);
    onChanged();
  };

  return (
    <div className="admin-stage">
      {isSuper && (
        <section className="admin-panel">
          <div className="admin-fgroup">
            <h4>{tr("admin.limits")}<span className="af-note">{tr("admin.zero_unlimited")}</span></h4>
            <div className="admin-fgrid">
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.max_workspace")}</span>
                <input type="number" min="0" value={maxWs} onChange={(e) => setMaxWs(e.target.value)} />
              </label>
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.max_session")}</span>
                <input type="number" min="0" value={maxSs} onChange={(e) => setMaxSs(e.target.value)} />
              </label>
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.max_repos")}</span>
                <input type="number" min="0" value={maxRepos} onChange={(e) => setMaxRepos(e.target.value)} />
              </label>
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.max_lfs")}</span>
                <span className="af-inputwrap">
                  <input type="number" min="0" value={maxLfsMb} onChange={(e) => setMaxLfsMb(e.target.value)} />
                  <span className="af-suffix">MB</span>
                </span>
              </label>
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.max_ws_mem")}</span>
                <span className="af-inputwrap">
                  <input type="number" min="0" step="256" value={maxWsMemMb} onChange={(e) => setMaxWsMemMb(e.target.value)} />
                  <span className="af-suffix">MB</span>
                </span>
                <span className="af-unit">{+maxWsMemMb > 0 ? tr("admin.per_container", { hint: fmtGbHint(+maxWsMemMb) }) : tr("admin.zero_no_tenant_cap")}</span>
              </label>
            </div>
            <p className="admin-hint">
              {tr("admin.ws_mem_hint_1")}<code>WS_MEMORY</code>{tr("admin.ws_mem_hint_2")}<code>AF_MAX_WORKSPACE_MEM</code>{tr("admin.ws_mem_hint_3")}<b>{tr("admin.ws_mem_hint_bold")}</b>{tr("admin.ws_mem_hint_4")}
            </p>
          </div>

          <div className="admin-fgroup">
            <h4>{tr("admin.idle_autostop")}<span className="af-note">{tr("admin.empty_deploy_default")}</span></h4>
            <div className="admin-fgrid">
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.session_halt")}</span>
                <input type="text" placeholder={tr("admin.idle_ph_30m")} value={sessIdle} onChange={(e) => setSessIdle(e.target.value)} />
              </label>
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.ws_stop")}</span>
                <input type="text" placeholder={tr("admin.idle_ph_60m")} value={wsIdle} onChange={(e) => setWsIdle(e.target.value)} />
              </label>
            </div>
            <p className="admin-hint">
              {tr("admin.idle_hint_1")}<code>30m</code> / <code>2h</code> / <code>90s</code>{tr("admin.idle_hint_2")}<code>0</code>{tr("admin.idle_hint_3")}
            </p>
          </div>

          <div className="admin-fgroup">
            <h4>{tr("admin.term_log_title")}</h4>
            <label className="admin-fld">
              <span className="af-cap">{tr("admin.retention")}</span>
              <select value={termRetention} onChange={(e) => setTermRetention(Number(e.target.value))}>
                <option value={0}>{tr("admin.retention_off")}</option>
                <option value={1}>{tr("admin.days_1")}</option>
                <option value={7}>{tr("admin.days_7")}</option>
                <option value={30}>{tr("admin.days_30")}</option>
              </select>
            </label>
            <p className="admin-hint">{tr("admin.term_log_hint")}</p>
          </div>

          <div className="admin-fgroup">
            <h4>{tr("admin.agent_cli_update")}</h4>
            <label className="admin-check">
              <input type="checkbox" checked={allowUpd} onChange={(e) => setAllowUpd(e.target.checked)} />
              <span>{tr("admin.allow_self_update")}</span>
            </label>
            <p className="admin-hint">{tr("admin.allow_self_update_hint")}</p>
          </div>

          <div className="admin-actions">
            <button onClick={saveLimits} className="primary">{tr("common.save")}</button>
            {saved && <span className="saved-note"><Icon name="check" /> {tr("admin.saved")}</span>}
          </div>
        </section>
      )}

      {/* Per-tenant login rules (docs/61 §61.9). super_admin only: two of the
          three reach past this tenant — an auto-join domain widens the whole
          deployment's entry gate, and the provider list decides which IdP is
          trusted to say who somebody is. */}
      {isSuper && <TenantLoginRules slug={slug} tenant={tenant} onChanged={onChanged} />}

      {/* Tenant-defined sign-in methods (docs/61 §61.11). Unlike the rules above,
          this one IS the tenant_admin's — they write the row, including the client
          secret. What they cannot do is activate it: that is the operator's one
          step (決定 30), so the approve control appears only for a super_admin. */}
      <TenantSignInMethods slug={slug} isSuper={isSuper} />

      <section className="admin-panel">
        <h4>{tr("admin.members")}</h4>
        {members === null ? (
          <p className="muted">…</p>
        ) : members.length === 0 ? (
          <p className="muted">{tr("admin.no_members")}</p>
        ) : (
          <div className="member-rows">
            {members.map((m) => (
              <button key={m.user_key} className="member-row" onClick={() => onOpenMember(m)}>
                <span className={"state-dot " + (m.state === "running" ? "on" : "off")} title={m.state} />
                <span className="mr-key mono">
                  {m.user_key}
                  {m.super_admin && <Icon name="star-full" className="mr-star" title="super_admin" />}
                </span>
                <span className="mr-email muted">{m.email || ""}</span>
                <span className="mr-role">{m.status === "removed" ? tr("admin.member_removed") : m.role}</span>
                {m.max_sessions != null && <span className="mr-lim muted">s≤{m.max_sessions}</span>}
                <Icon name="chevron-right" className="mr-go" />
              </button>
            ))}
          </div>
        )}
        <AddMember slug={slug} isSuper={isSuper} onAdded={loadMembers} />
      </section>
    </div>
  );
}

// TenantLoginRules — the editor for the three CSV columns of docs/61 §61.9.7.
//
// The three are deliberately unlike each other and the hints say so, because the
// costly mistake is treating allowed_domains as "who may use this tenant": it is
// only a bound on who may be INVITED. Continuing access is membership, and making
// the domain a per-request constraint would lock out the contractor somebody
// invited on purpose (§61.9.5).
function TenantLoginRules({
  slug,
  tenant,
  onChanged,
}: {
  slug: string;
  tenant: Tenant | null | undefined;
  onChanged: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [providers, setProviders] = useState(tenant?.allowed_providers || "");
  const [autoJoin, setAutoJoin] = useState(tenant?.auto_join_domains || "");
  const [domains, setDomains] = useState(tenant?.allowed_domains || "");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setProviders(tenant?.allowed_providers || "");
    setAutoJoin(tenant?.auto_join_domains || "");
    setDomains(tenant?.allowed_domains || "");
  }, [slug, tenant]);

  const save = async () => {
    const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/login`, "PUT", {
      allowed_providers: providers.trim(),
      auto_join_domains: autoJoin.trim(),
      allowed_domains: domains.trim(),
    });
    if (res?.error) {
      toast(errText(res.error));
      return;
    }
    setSaved(true);
    setTimeout(() => setSaved(false), 1500);
    onChanged();
  };

  // The URL a tenant_admin hands to a new colleague (§61.10.4). Shown here because
  // there is no notification path — passing it on is a human step, on purpose
  // (決定 28: no SMTP in the Control Plane).
  const loginURL = new URL("login/" + encodeURIComponent(slug), document.baseURI).toString();

  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>{tr("admin.login_rules")}<span className="af-note">{tr("admin.login_rules_note")}</span></h4>
        <div className="admin-fgrid">
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.allowed_providers")}</span>
            <input type="text" placeholder="entra, google" value={providers} onChange={(e) => setProviders(e.target.value)} />
            <span className="af-unit">{tr("admin.allowed_providers_unit")}</span>
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.auto_join_domains")}</span>
            <input type="text" placeholder="@sales.acme.co.jp" value={autoJoin} onChange={(e) => setAutoJoin(e.target.value)} />
            <span className="af-unit">{tr("admin.auto_join_domains_unit")}</span>
          </label>
          <label className="admin-fld">
            <span className="af-cap">{tr("admin.invite_domains")}</span>
            <input type="text" placeholder="@acme.co.jp" value={domains} onChange={(e) => setDomains(e.target.value)} />
            <span className="af-unit">{tr("admin.invite_domains_unit")}</span>
          </label>
        </div>
        <p className="admin-hint">{tr("admin.login_rules_hint")}</p>
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
      </div>
      <div className="admin-actions">
        <button onClick={save} className="primary">{tr("common.save")}</button>
        {saved && <span className="saved-note"><Icon name="check" /> {tr("admin.saved")}</span>}
      </div>
    </section>
  );
}

// --- tenant-defined sign-in methods (docs/61 §61.11 / ADR0043 決定 29-33) ------

// idpStatusLabel maps the row status to what the reader needs to know: not the
// state name, but whether anybody can sign in with it right now.
type IdPStatusKey =
  | "admin.idp_state_active"
  | "admin.idp_state_broken"
  | "admin.idp_state_suspended"
  | "admin.idp_state_pending";

function idpStatusKey(row: TenantIdP): IdPStatusKey {
  if (row.status === "active") return row.usable ? "admin.idp_state_active" : "admin.idp_state_broken";
  if (row.status === "suspended") return "admin.idp_state_suspended";
  return "admin.idp_state_pending";
}

const emptyIdP = (): TenantIdP => ({
  id: "",
  name: "",
  issuer: "",
  client_id: "",
  client_secret: "",
  trust: "issuer",
  allowed_domains: "",
  allowed_tids: "",
});

// TenantSignInMethods — the tenant's own IdP definitions.
//
// ★ The two things this screen has to make obvious, because getting either wrong is
// how a subsidiary onboarding stalls:
//
//  1. a new method does NOT work until a deployment administrator approves it. The
//     status chip says so, and the sign-in URL is shown only once it does — handing
//     out a URL whose button does not exist yet just produces support tickets
//     (docs/61 §61.14 の 2 つ目).
//  2. the allowed domains are not optional. They are what the approval is FOR: they
//     bound which addresses this issuer may assert.
function TenantSignInMethods({ slug, isSuper }: { slug: string; isSuper: boolean }) {
  const tr = useT();
  const toast = useToast();
  const [rows, setRows] = useState<TenantIdP[] | null>(null);
  const [form, setForm] = useState<TenantIdP | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDel, setConfirmDel] = useState<TenantIdP | null>(null);
  const base = `api/admin/tenants/${encodeURIComponent(slug)}/idp`;

  const load = useCallback(async () => {
    const res = await api(base);
    setRows(res?.providers || []);
  }, [base]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    if (!form) return;
    setBusy(true);
    try {
      const body = {
        name: form.name.trim(),
        label_ja: (form.label_ja || "").trim(),
        label_en: (form.label_en || "").trim(),
        issuer: form.issuer.trim(),
        client_id: form.client_id.trim(),
        // An empty secret on an edit keeps the stored one (the server merges), which
        // is why the field is blank rather than pre-filled with a mask.
        client_secret: (form.client_secret || "").trim(),
        trust: form.trust,
        allowed_tids: (form.allowed_tids || "").trim(),
        allowed_domains: (form.allowed_domains || "").trim(),
      };
      const res = form.id
        ? await apiJSON(`${base}/${encodeURIComponent(form.id)}`, "PUT", body)
        : await apiJSON(base, "POST", body);
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setForm(null);
      load();
    } finally {
      setBusy(false);
    }
  };

  const setStatus = async (row: TenantIdP, status: string) => {
    setBusy(true);
    try {
      const res = await apiJSON(`${base}/${encodeURIComponent(row.id)}/status`, "POST", { status });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      load();
    } finally {
      setBusy(false);
    }
  };

  const remove = async (row: TenantIdP) => {
    setBusy(true);
    try {
      const res = await apiJSON(`${base}/${encodeURIComponent(row.id)}`, "DELETE");
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setConfirmDel(null);
      load();
    } finally {
      setBusy(false);
    }
  };

  const loginURL = new URL("login/" + encodeURIComponent(slug), document.baseURI).toString();
  const anyActive = (rows || []).some((r) => r.status === "active" && r.usable);

  return (
    <section className="admin-panel">
      <h4>
        {tr("admin.idp_title")}
        <span className="af-note">{tr("admin.idp_note")}</span>
      </h4>
      <p className="admin-hint">{tr("admin.idp_hint")}</p>
      {rows === null ? (
        <p className="muted">{tr("common.loading")}</p>
      ) : rows.length === 0 ? (
        <p className="muted">{tr("admin.idp_none")}</p>
      ) : (
        rows.map((row) =>
          form && form.id === row.id ? (
            <IdPForm key={row.id} form={form} setForm={setForm} busy={busy} onSave={save} onCancel={() => setForm(null)} />
          ) : (
            <div key={row.id} className={"adm-mcp-row" + (row.status === "active" && row.usable ? "" : " off")}>
              <span className="as-name mono" title={row.provider_id}>
                {row.name}
              </span>
              <span className="as-repo muted" title={row.issuer}>
                {row.issuer}
              </span>
              <span className={"idp-state idp-" + (row.status || "pending")}>{tr(idpStatusKey(row))}</span>
              <span className="allow-acts">
                <button type="button" className="ghost xs" disabled={busy} onClick={() => setForm({ ...row, client_secret: "" })}>
                  {tr("mcp.edit")}
                </button>
                {/* ★ Activation is the deployment administrator's step and nobody
                    else's — that single asymmetry is what keeps a tenant admin from
                    being able to make themselves one (決定 30). */}
                {isSuper && row.status !== "active" && (
                  <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "active")}>
                    {tr("admin.idp_approve")}
                  </button>
                )}
                {row.status === "active" ? (
                  <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "suspended")}>
                    {tr("admin.idp_suspend")}
                  </button>
                ) : (
                  row.status === "suspended" && (
                    <button type="button" className="ghost xs" disabled={busy} onClick={() => setStatus(row, "pending")}>
                      {tr("admin.idp_reapply")}
                    </button>
                  )
                )}
                <button type="button" className="ghost xs danger" disabled={busy} onClick={() => setConfirmDel(row)}>
                  {tr("common.delete")}
                </button>
              </span>
            </div>
          ),
        )
      )}
      {form && form.id === "" ? (
        <IdPForm form={form} setForm={setForm} busy={busy} onSave={save} onCancel={() => setForm(null)} />
      ) : (
        !form && (
          <button type="button" className="ghost" onClick={() => setForm(emptyIdP())}>
            <Icon name="add" /> {tr("admin.idp_add")}
          </button>
        )
      )}
      {/* The sign-in URL appears only once something on it works. Before that it is
          a page with no button, and a URL handed out early is worse than none. */}
      {anyActive && (
        <p className="admin-hint">
          {tr("admin.login_url")} <code>{loginURL}</code>
        </p>
      )}
      {confirmDel && (
        <ConfirmDialog
          title={tr("admin.idp_delete_title", { name: confirmDel.name })}
          confirmLabel={tr("common.delete")}
          danger
          busy={busy}
          onCancel={() => setConfirmDel(null)}
          onConfirm={() => remove(confirmDel)}
        >
          <p>{tr("admin.idp_delete_body")}</p>
        </ConfirmDialog>
      )}
    </section>
  );
}

function IdPForm({
  form,
  setForm,
  busy,
  onSave,
  onCancel,
}: {
  form: TenantIdP;
  setForm: (f: TenantIdP) => void;
  busy: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const tr = useT();
  const set = (patch: Partial<TenantIdP>) => setForm({ ...form, ...patch });
  const valid = form.name.trim() && form.issuer.trim() && form.client_id.trim() && (form.allowed_domains || "").trim() && (form.id || (form.client_secret || "").trim());
  return (
    <div className="ssm-frm adm-mcp-form">
      <div className="ssm-fgrid">
        <Field label={tr("admin.idp_name")} req hint={tr("admin.idp_name_hint")}>
          <input value={form.name} placeholder="entra" onChange={(e) => set({ name: e.target.value })} />
        </Field>
        <Field label={tr("admin.idp_trust")} req hint={tr("admin.idp_trust_hint")}>
          <select value={form.trust} onChange={(e) => set({ trust: e.target.value })}>
            <option value="issuer">{tr("admin.idp_trust_issuer")}</option>
            <option value="email_verified">{tr("admin.idp_trust_email")}</option>
          </select>
        </Field>
        <Field label={tr("admin.idp_issuer")} req wide hint={tr("admin.idp_issuer_hint")}>
          <input
            value={form.issuer}
            placeholder="https://login.microsoftonline.com/<tenant-guid>/v2.0"
            onChange={(e) => set({ issuer: e.target.value })}
          />
        </Field>
        <Field label={tr("admin.idp_client_id")} req>
          <input value={form.client_id} onChange={(e) => set({ client_id: e.target.value })} />
        </Field>
        <Field
          label={tr("admin.idp_client_secret")}
          req={!form.id}
          hint={form.id && form.has_secret ? tr("admin.idp_secret_kept") : tr("admin.idp_secret_hint")}
        >
          <input
            type="password"
            autoComplete="new-password"
            value={form.client_secret || ""}
            placeholder={form.id && form.has_secret ? "***" : ""}
            onChange={(e) => set({ client_secret: e.target.value })}
          />
        </Field>
        <Field label={tr("admin.idp_domains")} req wide hint={tr("admin.idp_domains_hint")}>
          <input
            value={form.allowed_domains || ""}
            placeholder="@sub.co.jp"
            onChange={(e) => set({ allowed_domains: e.target.value })}
          />
        </Field>
        <Field label={tr("admin.idp_tids")} wide hint={tr("admin.idp_tids_hint")}>
          <input value={form.allowed_tids || ""} onChange={(e) => set({ allowed_tids: e.target.value })} />
        </Field>
        <Field label={tr("admin.idp_label_ja")}>
          <input value={form.label_ja || ""} onChange={(e) => set({ label_ja: e.target.value })} />
        </Field>
        <Field label={tr("admin.idp_label_en")}>
          <input value={form.label_en || ""} onChange={(e) => set({ label_en: e.target.value })} />
        </Field>
      </div>
      <p className="admin-hint">{tr("admin.idp_repend_hint")}</p>
      <div className="admin-actions">
        <button className="primary" disabled={busy || !valid} onClick={onSave}>
          {tr("common.save")}
        </button>
        <button className="ghost" disabled={busy} onClick={onCancel}>
          {tr("common.cancel")}
        </button>
      </div>
    </div>
  );
}

// SignInMethodRegister — the deployment-wide view of every tenant-defined method
// (docs/61 §61.11.6), super_admin only.
//
// ★ It is deliberately a REGISTER rather than a queue that empties. Approval is a
// single point-in-time check, but the IdP behind it stays under somebody else's
// control and its settings can change afterwards (self-sign-up being switched on is
// the classic one) — so the approved rows stay listed, with who approved them and
// when, and that list is what a periodic review reads. Pending rows sort first
// because somebody is waiting on those.
function SignInMethodRegister() {
  const tr = useT();
  const [rows, setRows] = useState<TenantIdP[] | null>(null);
  useEffect(() => {
    api("api/admin/idp").then((res) => setRows(res?.providers || []));
  }, []);
  if (rows === null || rows.length === 0) return null;
  const pending = rows.filter((r) => r.status === "pending").length;
  return (
    <section className="admin-panel">
      <h4>
        {tr("admin.idp_register")}
        {pending > 0 && <span className="af-note">{tr("admin.idp_pending_count", { n: pending })}</span>}
      </h4>
      <p className="admin-hint">{tr("admin.idp_register_hint")}</p>
      {rows.map((row) => (
        <div key={row.id} className={"adm-mcp-row" + (row.status === "active" && row.usable ? "" : " off")}>
          <span className="as-name mono" title={row.provider_id}>
            {row.tenant_slug}
          </span>
          <span className="as-repo muted" title={row.issuer}>
            {row.issuer}
          </span>
          <span className="muted">{row.allowed_domains}</span>
          <span className={"idp-state idp-" + (row.status || "pending")}>{tr(idpStatusKey(row))}</span>
          {row.approved_at && (
            <span className="muted">{fmtAt(row.approved_at)}</span>
          )}
        </div>
      ))}
    </section>
  );
}

function AddMember({ slug, isSuper, onAdded }: { slug: string; isSuper: boolean; onAdded: () => void }) {
  const tr = useT();
  const toast = useToast();
  const [email, setEmail] = useState("");
  const [key, setKey] = useState("");
  const [role, setRole] = useState("member");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const r = await rawJSON("api/admin/memberships", "POST", {
      email: email.trim(),
      user_key: key.trim(),
      tenant_slug: slug,
      role,
    });
    if (r.ok) {
      setEmail("");
      setKey("");
      onAdded();
    } else {
      const er = await r.json().catch(() => ({}));
      toast(tr("admin.add_failed", { msg: er.error?.message || r.status }));
    }
  };
  return (
    <form className="form add-member" onSubmit={submit}>
      <div className="sub-head">{tr("admin.add_member")}</div>
      <div className="form-row">
        <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="email" />
        <input value={key} onChange={(e) => setKey(e.target.value)} placeholder={tr("admin.or_user_key")} />
        {isSuper && (
          <select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="member">member</option>
            <option value="tenant_admin">tenant_admin</option>
          </select>
        )}
        <button type="submit" className="primary">{tr("admin.add")}</button>
      </div>
    </form>
  );
}

// --- Stage 3: member detail (resources + sessions + actions) ----------------

function MemberView({
  slug,
  member,
  isSuper,
  onChanged,
  onRemoved,
}: {
  slug: string;
  member: Member;
  isSuper: boolean;
  onChanged: () => void;
  onRemoved: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [stats, setStats] = useState<any>(null);
  const [sessions, setSessions] = useState<any[] | null>(null);
  const [confirmStop, setConfirmStop] = useState(false);
  const [confirmClean, setConfirmClean] = useState(false);
  const [confirmGrant, setConfirmGrant] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [busy, setBusy] = useState(false);
  const [limitOpen, setLimitOpen] = useState(false);
  const [limit, setLimit] = useState<number | string>(member.max_sessions ?? 0);
  // Per-workspace RAM cap, stored in bytes, edited in MB (0 = unset → deployment default).
  const [memMb, setMemMb] = useState<number | string>(member.mem_limit ? Math.round(member.mem_limit / 1048576) : 0);
  const [role, setMemberRole] = useState(member.role); // tenant-scoped role, live-updated on grant/revoke
  const timer = useRef<ReturnType<typeof setInterval> | undefined>(undefined);
  // Only setState on an actual change so an unchanged 4s poll doesn't re-render
  // (and flicker the cursor); mirrors the sessions poller in state.jsx.
  const statsSer = useRef("");
  const sessSer = useRef("");

  const key = member.user_key;
  const base = `api/admin/tenants/${encodeURIComponent(slug)}/members/${encodeURIComponent(key)}`;

  const poll = useCallback(async () => {
    try {
      const [s, ss] = await Promise.all([api(`${base}/stats`), api(`${base}/sessions`)]);
      const st = s && !s.error ? s : { running: false };
      const stSer = JSON.stringify(st);
      if (stSer !== statsSer.current) {
        statsSer.current = stSer;
        setStats(st);
      }
      const list = ss && ss.sessions ? ss.sessions : [];
      const ssSer = JSON.stringify(list);
      if (ssSer !== sessSer.current) {
        sessSer.current = ssSer;
        setSessions(list);
      }
    } catch {
      /* keep last values; transient */
    }
  }, [base]);

  useEffect(() => {
    statsSer.current = "";
    sessSer.current = "";
    setStats(null);
    setSessions(null);
    poll();
    timer.current = setInterval(() => {
      if (!document.hidden) poll(); // hidden tab: skip the tick
    }, 4000);
    return () => clearInterval(timer.current);
  }, [poll]);

  const running = stats?.running;
  const memRatio = stats?.mem_max ? stats.mem_used / stats.mem_max : null;
  const diskRatio = stats?.disk_quota ? stats.disk_used / stats.disk_quota : null;

  const stop = async () => {
    setBusy(true);
    try {
      await apiJSON("api/admin/stop-workspace", "POST", { tenant_slug: slug, user_key: key });
      setConfirmStop(false);
      poll();
      onChanged();
    } finally {
      setBusy(false);
    }
  };
  const cleanHome = async () => {
    setBusy(true);
    try {
      await apiJSON("api/admin/clean-home", "POST", { tenant_slug: slug, user_key: key });
      setConfirmClean(false);
      poll();
      onChanged();
    } finally {
      setBusy(false);
    }
  };
  const saveLimit = async () => {
    await apiJSON("api/admin/user-limits", "PUT", {
      user_key: key,
      tenant_slug: slug,
      max_sessions: +limit || 0,
      mem_limit: Math.round(+memMb || 0) * 1048576,
    });
    setLimitOpen(false);
    poll(); // mem_max reflects the new cap after the next start; refresh sessions/stats
    onChanged();
  };
  // Offboarding (docs/61 §61.10.6). The membership is deactivated, not deleted:
  // the workspace, its home and its secrets stay put, so a transfer can be undone
  // by re-inviting. It is the step that actually revokes access — the signed
  // session cookie itself lives for up to AF_SESSION_TTL and cannot be revoked
  // individually — so it comes FIRST in the sequence, before stopping the
  // workspace and wiping the home.
  const removeMember = async () => {
    setBusy(true);
    try {
      const res = await apiJSON("api/admin/memberships", "DELETE", { tenant_slug: slug, user_key: key });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setConfirmRemove(false);
      onRemoved();
    } finally {
      setBusy(false);
    }
  };
  const setRoleTo = async (newRole: string) => {
    setBusy(true);
    try {
      await apiJSON("api/admin/membership-role", "PUT", { user_key: key, tenant_slug: slug, role: newRole });
      setMemberRole(newRole);
      setConfirmGrant(false);
      onChanged();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="admin-stage member-detail">
      <header className="member-head">
        <span className={"state-dot " + (stats == null ? "" : running ? "on" : "off")} />
        <span className={"state-word " + (stats == null ? "" : running ? "on" : "off")}>
          {stats == null ? tr("admin.checking") : running ? tr("admin.running_state") : tr("admin.stopped_state")}
        </span>
        <span className="mh-key mono">{key}</span>
        {member.super_admin && <Icon name="star-full" className="mr-star" title={tr("admin.super_admin_deploy_title")} />}
        <span className="mh-role">
          {member.status === "removed"
            ? tr("admin.member_removed")
            : role + (role === "tenant_admin" ? tr("admin.tenant_admin_paren") : "")}
        </span>
        {member.email && <span className="mh-email muted">{member.email}</span>}
      </header>

      <section className="admin-panel">
        <h4>{tr("admin.ws_resources")}</h4>
        {stats === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : !running ? (
          <p className="muted">{tr("admin.ws_stopped", { suffix: stats.disk_used != null ? tr("admin.ws_stopped_disk_suffix") : "" })}</p>
        ) : null}
        <div className="res-tiles">
          <ResTile
            label={tr("admin.res_memory")}
            value={stats?.mem_used != null ? fmtG(stats.mem_used) : "–"}
            sub={stats?.mem_max ? `/ ${fmtG(stats.mem_max)} · ${fmtPct(memRatio == null ? null : memRatio * 100)}` : ""}
            ratio={memRatio}
            warn={0.75}
            crit={0.9}
          />
          <ResTile
            label="CPU"
            value={stats?.cpu_pct != null ? fmtPct(stats.cpu_pct) : "–"}
            sub={tr("admin.cpu_sub")}
            ratio={stats?.cpu_pct != null ? stats.cpu_pct / 100 : null}
            warn={0.6}
            crit={0.9}
          />
          <ResTile
            label={tr("admin.res_disk")}
            value={stats?.disk_used != null ? fmtG(stats.disk_used) : "–"}
            sub={stats?.disk_quota ? `/ ${fmtG(stats.disk_quota)} · ${fmtPct(diskRatio == null ? null : diskRatio * 100)}` : tr("admin.disk_home_sub")}
            ratio={diskRatio}
            warn={0.75}
            crit={0.9}
          />
        </div>
      </section>

      <section className="admin-panel">
        <h4>{tr("admin.sessions_heading")} {sessions ? `(${sessions.length})` : ""}</h4>
        {sessions === null ? (
          <p className="muted">{tr("common.loading")}</p>
        ) : sessions.length === 0 ? (
          <p className="muted">{tr("admin.no_sessions")}</p>
        ) : (
          <div className="admin-sessions">
            {sessions.map((s: any) => {
              const st = stateInfo(s);
              return (
                <div key={s.name} className="adm-session">
                  <span className={"kind-tag kind-" + kindClass(s.kind)}>
                    <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
                  </span>
                  <span className="as-name mono" title={s.dir || ""}>{s.label ? s.label.replace(/^\[AF\]\s*/, "") : s.name}</span>
                  <span className="as-repo muted">{s.repo || ""}</span>
                  <span className={"session-state " + st.cls}>
                    <Icon name={st.icon} spin={st.spin} /> {st.text}
                  </span>
                  <span className="as-time muted">{s.started || ""}</span>
                </div>
              );
            })}
          </div>
        )}
      </section>

      {isSuper && (
        <section className="admin-panel">
          <h4>{tr("admin.permissions")}</h4>
          {member.super_admin ? (
            <p className="muted">
              <Icon name="star-full" className="mr-star" /> {tr("admin.super_admin_note_1")}<code>SUPER_ADMIN_EMAILS</code>{tr("admin.super_admin_note_2")}
            </p>
          ) : (
            <div className="member-actions">
              {role === "tenant_admin" ? (
                <>
                  <span className="role-now"><Icon name="shield" /> {tr("admin.tenant_admin_role")}</span>
                  <button disabled={busy} onClick={() => setRoleTo("member")}>
                    {tr("admin.revoke_admin")}
                  </button>
                </>
              ) : (
                <button className="primary" disabled={busy} onClick={() => setConfirmGrant(true)}>
                  <Icon name="shield" /> {tr("admin.make_admin")}
                </button>
              )}
            </div>
          )}
          <p className="muted role-hint">
            {tr("admin.tenant_admin_hint_1")}<b>{slug}</b>{tr("admin.tenant_admin_hint_2")}
          </p>
        </section>
      )}

      <section className="admin-panel">
        <h4>{tr("admin.operations")}</h4>
        <div className="member-actions">
          <button className="danger-btn" disabled={!running} onClick={() => setConfirmStop(true)}>
            <Icon name="debug-stop" /> {tr("admin.force_stop_ws")}
          </button>
          <button onClick={() => { setLimit(member.max_sessions ?? 0); setMemMb(member.mem_limit ? Math.round(member.mem_limit / 1048576) : 0); setLimitOpen(true); }}>
            <Icon name="settings" /> {tr("admin.set_limits")}
          </button>
          {/* clean-home is a tenant_admin action now (docs/61 §61.10.6 / 決定 26):
              the department knows who left, so the whole offboarding sequence
              belongs to it rather than half of it being a ticket to IT. */}
          <button className="danger-btn" onClick={() => setConfirmClean(true)}>
            <Icon name="trash" /> {tr("admin.clean_home")}
          </button>
          {member.status !== "removed" && (
            <button className="danger-btn" disabled={busy} onClick={() => setConfirmRemove(true)}>
              <Icon name="close" /> {tr("admin.remove_member")}
            </button>
          )}
        </div>
        {limitOpen && (
          <div className="limit-edit">
            <div className="le-head">{tr("admin.limits_edit_title")}</div>
            <div className="admin-fgrid">
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.max_sessions_label")}</span>
                <input type="number" min="0" value={limit} onChange={(e) => setLimit(e.target.value)} autoFocus />
                <span className="af-unit">{tr("admin.zero_unlimited")}</span>
              </label>
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.ws_memory")}</span>
                <span className="af-inputwrap">
                  <input type="number" min="0" step="256" value={memMb} onChange={(e) => setMemMb(e.target.value)} />
                  <span className="af-suffix">MB</span>
                </span>
                <span className="af-unit">{+memMb > 0 ? tr("admin.eq_hint", { hint: fmtGbHint(+memMb) }) : tr("admin.zero_deploy_default")}</span>
              </label>
            </div>
            <p className="admin-hint">
              {tr("admin.mem_clamp_1")}<b>{tr("admin.ws_mem_hint_bold")}</b>{tr("admin.mem_clamp_2")}
            </p>
            <div className="le-actions">
              <button className="primary" onClick={saveLimit}>{tr("common.save")}</button>
              <button className="ghost" onClick={() => setLimitOpen(false)}>{tr("common.cancel")}</button>
            </div>
          </div>
        )}
      </section>

      {confirmStop && (
        <ConfirmDialog
          title={tr("admin.stop_ws_title", { key })}
          confirmLabel={tr("admin.stop_confirm")}
          busy={busy}
          onCancel={() => setConfirmStop(false)}
          onConfirm={stop}
        >
          <p>{tr("admin.stop_body", { slug })}</p>
        </ConfirmDialog>
      )}
      {confirmClean && (
        <ConfirmDialog
          title={tr("admin.clean_title", { key })}
          confirmLabel={tr("admin.clean_confirm")}
          busy={busy}
          onCancel={() => setConfirmClean(false)}
          onConfirm={cleanHome}
        >
          <p>{tr("admin.clean_body")}</p>
          <p className="muted">{tr("admin.clean_keep")}</p>
          <p className="muted">{tr("admin.clean_delete")}</p>
        </ConfirmDialog>
      )}
      {confirmRemove && (
        <ConfirmDialog
          title={tr("admin.remove_title", { key, slug })}
          confirmLabel={tr("admin.remove_confirm")}
          busy={busy}
          onCancel={() => setConfirmRemove(false)}
          onConfirm={removeMember}
        >
          <p>{tr("admin.remove_body", { slug })}</p>
          <p className="muted">{tr("admin.remove_keeps")}</p>
          <p className="muted">{tr("admin.remove_undo")}</p>
        </ConfirmDialog>
      )}
      {confirmGrant && (
        <ConfirmDialog
          title={tr("admin.grant_title", { key, slug })}
          confirmLabel={tr("admin.grant_confirm")}
          danger={false}
          busy={busy}
          onCancel={() => setConfirmGrant(false)}
          onConfirm={() => setRoleTo("tenant_admin")}
        >
          <p>{tr("admin.grant_body_1")}<b>{slug}</b>{tr("admin.grant_body_2")}</p>
          <p className="muted">{tr("admin.grant_note")}</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

// One resource tile: label, big value, sub-line, and a fill bar tinted by level.
function ResTile({
  label,
  value,
  sub,
  ratio,
  warn,
  crit,
}: {
  label: string;
  value: string;
  sub: string;
  ratio: number | null;
  warn: number;
  crit: number;
}) {
  const level = ratio == null ? 0 : ratio >= crit ? 2 : ratio >= warn ? 1 : 0;
  const cls = "res-tile" + (level === 2 ? " crit" : level === 1 ? " warn" : "");
  return (
    <div className={cls}>
      <div className="rt-label">{label}</div>
      <div className="rt-value">{value}</div>
      <div className="rt-sub muted">{sub}</div>
      {ratio != null && (
        <div className="rt-bar">
          <div className="rt-fill" style={{ width: Math.min(100, Math.round(ratio * 100)) + "%" }} />
        </div>
      )}
    </div>
  );
}
