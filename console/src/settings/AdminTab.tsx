import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, apiJSON, rawJSON, errText, rel } from "../api.js";
import { useApp } from "../state.jsx";
import { mobileMatches } from "../lib/device.js";
import Icon from "../components/Icon.jsx";
import ConfirmDialog from "../components/ConfirmDialog.jsx";
import { useToast } from "../components/ToastProvider.jsx";
import { kindLabel, kindClass, kindIcon } from "../lib/sessionkind.js";
import { fmtGiB } from "../lib/bytes.js";
import { stateInfo } from "../lib/sessionview.js";

// Admin API shapes (only the fields the UI reads; server responses may carry more).
interface Tenant {
  slug: string;
  name: string;
  users?: number;
  running?: number;
  max_workspaces?: number;
  max_sessions?: number;
  session_idle_timeout?: string;
  ws_idle_timeout?: string;
  allow_agent_self_update?: boolean;
}
interface Member {
  user_key: string;
  email?: string;
  role: string;
  super_admin?: boolean;
  state?: string;
  max_sessions?: number | null;
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
const ADMIN_MODES = ["manage", "sessions", "usage", "audit", "egress"]; // swipe order for the mode tabs
const fmtG = (b: number) => fmtGiB(b) + "G";
const fmtPct = (n: number | null | undefined) => (n == null ? "–" : Math.round(n) + "%");

export default function AdminTab() {
  const { adminDepthRef } = useApp();
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

  if (forbidden) return <p className="muted pad">権限がありません（super_admin のみ）。</p>;
  if (tenants === null) return <p className="muted pad">読み込み中…</p>;

  const tenant = view.slug ? tenants.find((t) => t.slug === view.slug) : null;
  const tenantName = tenant ? tenant.name : view.slug;

  const goBack = () => history.back(); // step up one drill level via history

  return (
    <div className="admin">
      <div className="seg admin-modes">
        <button type="button" className={"seg-btn" + (mode === "manage" ? " active" : "")} onClick={() => setMode("manage")}>
          <Icon name="organization" /> テナント管理
        </button>
        <button type="button" className={"seg-btn" + (mode === "sessions" ? " active" : "")} onClick={() => setMode("sessions")}>
          <Icon name="list-tree" /> セッション
        </button>
        <button type="button" className={"seg-btn" + (mode === "usage" ? " active" : "")} onClick={() => setMode("usage")}>
          <Icon name="graph" /> 使用量
        </button>
        <button type="button" className={"seg-btn" + (mode === "audit" ? " active" : "")} onClick={() => setMode("audit")}>
          <Icon name="history" /> 監査
        </button>
        <button type="button" className={"seg-btn" + (mode === "egress" ? " active" : "")} onClick={() => setMode("egress")}>
          <Icon name="globe" /> 通信
        </button>
      </div>

      {mode === "sessions" && <AllSessionsView tenants={tenants} isSuper={isSuper} />}
      {mode === "usage" && <UsageView tenants={tenants} isSuper={isSuper} />}
      {mode === "audit" && <AuditView tenants={tenants} isSuper={isSuper} />}
      {mode === "egress" && <EgressView />}

      {mode === "manage" && (
      <>
      <div className="admin-nav">
        {view.stage !== "tenants" && (
          <button className="admin-back" onClick={goBack}>
            <Icon name="arrow-left" /> 戻る
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
            <Icon name="organization" /> テナント
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
        <TenantsList
          tenants={tenants}
          isSuper={isSuper}
          onReload={loadTenants}
          onOpen={(slug) => drill({ stage: "tenant", slug })}
        />
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
          onBack={() => history.back()}
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
    timer.current = setInterval(poll, 5000);
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
              テナント
              <select value={tenant} onChange={(e) => setTenant(e.target.value)}>
                <option value="">全テナント</option>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>{t.name}</option>
                ))}
              </select>
            </label>
          )}
          <label className="as-search">
            検索
            <input type="text" value={q} onChange={(e) => setQ(e.target.value)} placeholder="ユーザー / ラベル / リポジトリ" />
          </label>
          <span className="as-count muted">{all.length} 稼働中</span>
        </div>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        {rows === null ? (
          <p className="muted">読み込み中…</p>
        ) : shown.length === 0 ? (
          <p className="muted">{all.length === 0 ? "稼働中のセッションはありません。" : "一致するセッションがありません。"}</p>
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
              const tName = (slugv: string) => tenants.find((t) => t.slug === slugv)?.name || slugv || "(不明)";
              return [...by.entries()]
                .sort((a, b) => tName(a[0]).localeCompare(tName(b[0])))
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
                          <span className="asx-user mono" title={s.email || ""}>{s.user_key || "(不明)"}</span>
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
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
};

function AuditView({ tenants, isSuper }: { tenants: Tenant[]; isSuper: boolean }) {
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
      setErr("読み込めません");
      setRows([]);
    }
  }, [tenant]);

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
              テナント
              <select value={tenant} onChange={(e) => setTenant(e.target.value)}>
                <option value="">全テナント</option>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>{t.name}</option>
                ))}
              </select>
            </label>
          )}
          <label className="as-search">
            検索
            <input type="text" value={q} onChange={(e) => setQ(e.target.value)} placeholder="操作 / 対象 / ユーザー" />
          </label>
          <button type="button" className="ghost" title="更新" onClick={load}>
            <Icon name="refresh" />
          </button>
          <span className="as-count muted">{(rows || []).length} 件</span>
        </div>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        {rows === null ? (
          <p className="muted">読み込み中…</p>
        ) : shown.length === 0 ? (
          <p className="muted">{(rows || []).length === 0 ? "監査ログはまだありません。" : "一致するログがありません。"}</p>
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

// --- Egress observations (docs/20 M2) ---------------------------------------
// Deployment-wide destination stats from the log-only forward proxy: each host with
// its would-allow / would-block hit counts. Reads GET /api/admin/egress (super_admin
// only). "blocked" is a would-block — M2 is log-only and enforces nothing yet.

function EgressView() {
  const [rows, setRows] = useState<any[] | null>(null);
  const [err, setErr] = useState("");
  const [logOnly, setLogOnly] = useState(true);
  const [days, setDays] = useState(7);

  const load = useCallback(async () => {
    setRows(null);
    setErr("");
    try {
      const d = await api("api/admin/egress?days=" + days);
      if (d?.error) {
        setErr(errText(d.error));
        setRows([]);
        return;
      }
      setRows(d.egress || []);
      setLogOnly(!!d.log_only);
    } catch {
      setErr("読み込めません");
      setRows([]);
    }
  }, [days]);
  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="admin-stage egress-view">
      <section className="admin-panel">
        <div className="usage-toolbar">
          <label>
            期間
            <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
              <option value={1}>1日</option>
              <option value={7}>7日</option>
              <option value={30}>30日</option>
            </select>
          </label>
          <button type="button" className="ghost" title="更新" onClick={load}>
            <Icon name="refresh" />
          </button>
          {logOnly && <span className="as-count muted">log-only（遮断はしていません）</span>}
        </div>
        {err && <p className="form-err">{err}</p>}
      </section>

      <section className="admin-panel">
        {rows === null ? (
          <p className="muted">読み込み中…</p>
        ) : rows.length === 0 ? (
          <p className="muted">記録がありません（egress プロキシ未設定か、対象期間に通信なし）。</p>
        ) : (
          <div className="adm-egress">
            {rows.map((e: any) => (
              <div key={e.host} className="adm-egress-row">
                <span className="as-name mono" title={e.host}>{e.host}</span>
                <span className="egress-allow">{e.allowed} 許可</span>
                {e.blocked > 0 && <span className="egress-block">{e.blocked} 遮断候補</span>}
              </div>
            ))}
          </div>
        )}
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
      setErr("読み込みに失敗しました。");
      setData(null);
    } finally {
      setLoading(false);
    }
  }, [usageQuery]);

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
        <h4>使用量（Workspace 稼働時間）</h4>
        <p className="muted" style={{ margin: "0 0 12px" }}>
          インフラ占有＝Workspace が起動していた時間の集計です（Claude 利用料は各自のサブスクで、ここには含みません）。約 5 分ごとのサンプリングのため誤差があります。
        </p>
        <div className="usage-toolbar">
          <label>
            開始
            <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
          </label>
          <label>
            終了
            <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
          </label>
          {isSuper && (
            <label>
              テナント
              <select value={tenant} onChange={(e) => setTenant(e.target.value)}>
                <option value="">全テナント</option>
                {tenants.map((t) => (
                  <option key={t.slug} value={t.slug}>{t.name}</option>
                ))}
              </select>
            </label>
          )}
          <button className="primary" onClick={load} disabled={loading}>
            {loading ? "…" : "適用"}
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
            <div className="us-lab muted">合計稼働</div>
          </div>
          <div className="us-metric">
            <div className="us-val">{totals.length}</div>
            <div className="us-lab muted">メンバー</div>
          </div>
          {data && (
            <div className="us-range muted">
              {data.from} 〜 {data.to}
            </div>
          )}
        </div>

        {data === null ? (
          <p className="muted">読み込み中…</p>
        ) : totals.length === 0 ? (
          <p className="muted">この期間の稼働記録はありません。</p>
        ) : (
          <div className="usage-rows">
            {totals.map((t: any) => (
              <div key={(t.tenant || "") + "|" + t.user_key} className="usage-row">
                <span className="ur-key mono" title={t.email || ""}>{t.user_key || "(不明)"}</span>
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
  const [adding, setAdding] = useState(false);
  return (
    <div className="admin-stage">
      <div className="stage-head">
        <h4>テナント一覧</h4>
        {isSuper && (
          <button className="primary" onClick={() => setAdding((v) => !v)}>
            <Icon name="add" /> 新規テナント
          </button>
        )}
      </div>
      {isSuper && adding && <NewTenant onCreated={() => { setAdding(false); onReload(); }} onCancel={() => setAdding(false)} />}
      {tenants.length === 0 ? (
        <p className="muted">テナントがありません。「新規テナント」から作成してください。</p>
      ) : (
        <div className="tenant-cards">
          {tenants.map((t) => (
            <button key={t.slug} className="tenant-card" onClick={() => onOpen(t.slug)}>
              <div className="tc-top">
                <span className="tc-name">{t.name}</span>
                <span className="tc-slug mono">{t.slug}</span>
              </div>
              <div className="tc-stats">
                <span title="メンバー数"><Icon name="person" /> {t.users} 人</span>
                <span className={(t.running || 0) > 0 ? "tc-run on" : "tc-run"} title="起動中の Workspace">
                  <Icon name="vm-running" /> {t.running} 起動中
                </span>
              </div>
              <div className="tc-limits muted">
                上限 — Workspace: {t.max_workspaces || "∞"} / Session: {t.max_sessions || "∞"}
              </div>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function NewTenant({ onCreated, onCancel }: { onCreated: () => void; onCancel: () => void }) {
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
      toast("作成に失敗: " + (er.error?.message || r.status));
    }
  };
  return (
    <form className="new-tenant" onSubmit={submit}>
      <input value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="slug（英数字）" autoFocus />
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder="表示名（任意）" />
      <button type="submit" className="primary">作成</button>
      <button type="button" className="ghost" onClick={onCancel}>キャンセル</button>
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
  const [maxWs, setMaxWs] = useState<number | string>(tenant?.max_workspaces || 0);
  const [maxSs, setMaxSs] = useState<number | string>(tenant?.max_sessions || 0);
  const [sessIdle, setSessIdle] = useState(tenant?.session_idle_timeout || "");
  const [wsIdle, setWsIdle] = useState(tenant?.ws_idle_timeout || "");
  const [allowUpd, setAllowUpd] = useState(!!tenant?.allow_agent_self_update);
  const [saved, setSaved] = useState(false);
  const toast = useToast();
  const [members, setMembers] = useState<Member[] | null>(null);

  useEffect(() => {
    setMaxWs(tenant?.max_workspaces || 0);
    setMaxSs(tenant?.max_sessions || 0);
    setSessIdle(tenant?.session_idle_timeout || "");
    setWsIdle(tenant?.ws_idle_timeout || "");
    setAllowUpd(!!tenant?.allow_agent_self_update);
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
      session_idle_timeout: sessIdle.trim(),
      ws_idle_timeout: wsIdle.trim(),
      allow_agent_self_update: allowUpd,
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
          <h4>上限（0 = 無制限）</h4>
          <div className="form-row">
            <label>
              最大 Workspace
              <input type="number" min="0" value={maxWs} onChange={(e) => setMaxWs(e.target.value)} />
            </label>
            <label>
              最大 Session
              <input type="number" min="0" value={maxSs} onChange={(e) => setMaxSs(e.target.value)} />
            </label>
          </div>
          <h4>アイドル自動停止（空=無効 / デプロイ既定に従う）</h4>
          <div className="form-row">
            <label>
              Session halt まで
              <input type="text" placeholder="例 30m（空=無効）" value={sessIdle} onChange={(e) => setSessIdle(e.target.value)} />
            </label>
            <label>
              Workspace 停止まで
              <input type="text" placeholder="例 60m（空=無効）" value={wsIdle} onChange={(e) => setWsIdle(e.target.value)} />
            </label>
          </div>
          <p className="muted" style={{ margin: "0 0 8px" }}>
            放置された claude セッションは Session halt まで で停止中（再開可）に畳まれ、接続も稼働も無い Workspace は Workspace 停止まで で docker 停止します。書式は <code>30m</code> / <code>2h</code> / <code>90s</code>。空欄はデプロイ既定（既定は無効）に従い、<code>0</code> で明示的に無効化します。
          </p>
          <h4>エージェント CLI の更新</h4>
          <label className="admin-check">
            <input type="checkbox" checked={allowUpd} onChange={(e) => setAllowUpd(e.target.checked)} />
            <span>メンバーが claude / opencode / codex を自分で最新へ更新するのを許可</span>
          </label>
          <p className="muted" style={{ margin: "0 0 8px" }}>
            OFF（既定）は全員がこのデプロイのイメージ版で固定。ON にすると各メンバーが自分の設定で「起動時に最新へ更新」を選べます（コンテナ内 in-place 更新・Stop → Start で反映／戻せます）。
          </p>
          <div className="form-row">
            <button onClick={saveLimits} className="primary">保存</button>
            {saved && <span className="saved-note"><Icon name="check" /> 保存しました</span>}
          </div>
        </section>
      )}

      <section className="admin-panel">
        <h4>メンバー</h4>
        {members === null ? (
          <p className="muted">…</p>
        ) : members.length === 0 ? (
          <p className="muted">メンバーがいません。下のフォームから追加してください。</p>
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
                <span className="mr-role">{m.role}</span>
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

function AddMember({ slug, isSuper, onAdded }: { slug: string; isSuper: boolean; onAdded: () => void }) {
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
      toast("追加に失敗: " + (er.error?.message || r.status));
    }
  };
  return (
    <form className="form add-member" onSubmit={submit}>
      <div className="sub-head">メンバー追加</div>
      <div className="form-row">
        <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="email" />
        <input value={key} onChange={(e) => setKey(e.target.value)} placeholder="または user_key" />
        {isSuper && (
          <select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="member">member</option>
            <option value="tenant_admin">tenant_admin</option>
          </select>
        )}
        <button type="submit" className="primary">追加</button>
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
}: {
  slug: string;
  member: Member;
  isSuper: boolean;
  onChanged: () => void;
  onBack?: () => void;
}) {
  const [stats, setStats] = useState<any>(null);
  const [sessions, setSessions] = useState<any[] | null>(null);
  const [confirmStop, setConfirmStop] = useState(false);
  const [confirmClean, setConfirmClean] = useState(false);
  const [confirmGrant, setConfirmGrant] = useState(false);
  const [busy, setBusy] = useState(false);
  const [limitOpen, setLimitOpen] = useState(false);
  const [limit, setLimit] = useState<number | string>(member.max_sessions ?? 0);
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
    timer.current = setInterval(poll, 4000);
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
    await apiJSON("api/admin/user-limits", "PUT", { user_key: key, tenant_slug: slug, max_sessions: +limit || 0 });
    setLimitOpen(false);
    onChanged();
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
          {stats == null ? "確認中…" : running ? "稼働中" : "停止中"}
        </span>
        <span className="mh-key mono">{key}</span>
        {member.super_admin && <Icon name="star-full" className="mr-star" title="super_admin（デプロイ全体）" />}
        <span className="mh-role">{role}{role === "tenant_admin" ? "（テナント管理者）" : ""}</span>
        {member.email && <span className="mh-email muted">{member.email}</span>}
      </header>

      <section className="admin-panel">
        <h4>Workspace リソース</h4>
        {stats === null ? (
          <p className="muted">読み込み中…</p>
        ) : !running ? (
          <p className="muted">Workspace は停止中です{stats.disk_used != null ? "（ディスク使用量のみ表示）" : ""}。</p>
        ) : null}
        <div className="res-tiles">
          <ResTile
            label="メモリ"
            value={stats?.mem_used != null ? fmtG(stats.mem_used) : "–"}
            sub={stats?.mem_max ? `/ ${fmtG(stats.mem_max)} · ${fmtPct(memRatio == null ? null : memRatio * 100)}` : ""}
            ratio={memRatio}
            warn={0.75}
            crit={0.9}
          />
          <ResTile
            label="CPU"
            value={stats?.cpu_pct != null ? fmtPct(stats.cpu_pct) : "–"}
            sub="1コア = 100%"
            ratio={stats?.cpu_pct != null ? stats.cpu_pct / 100 : null}
            warn={0.6}
            crit={0.9}
          />
          <ResTile
            label="ディスク"
            value={stats?.disk_used != null ? fmtG(stats.disk_used) : "–"}
            sub={stats?.disk_quota ? `/ ${fmtG(stats.disk_quota)} · ${fmtPct(diskRatio == null ? null : diskRatio * 100)}` : "(home)"}
            ratio={diskRatio}
            warn={0.75}
            crit={0.9}
          />
        </div>
      </section>

      <section className="admin-panel">
        <h4>セッション {sessions ? `(${sessions.length})` : ""}</h4>
        {sessions === null ? (
          <p className="muted">読み込み中…</p>
        ) : sessions.length === 0 ? (
          <p className="muted">セッションなし</p>
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
          <h4>権限</h4>
          {member.super_admin ? (
            <p className="muted">
              <Icon name="star-full" className="mr-star" /> このユーザーはデプロイ全体の super_admin です（env <code>SUPER_ADMIN_EMAILS</code> で管理）。
            </p>
          ) : (
            <div className="member-actions">
              {role === "tenant_admin" ? (
                <>
                  <span className="role-now"><Icon name="shield" /> テナント管理者（tenant_admin）</span>
                  <button disabled={busy} onClick={() => setRoleTo("member")}>
                    管理者権限を解除
                  </button>
                </>
              ) : (
                <button className="primary" disabled={busy} onClick={() => setConfirmGrant(true)}>
                  <Icon name="shield" /> このテナントの管理者にする
                </button>
              )}
            </div>
          )}
          <p className="muted role-hint">
            テナント管理者は <b>{slug}</b> 内のメンバー管理・リソース閲覧・Workspace 強制停止・セッション上限設定ができます（テナント作成・上限変更・home掃除・権限付与は不可）。
          </p>
        </section>
      )}

      <section className="admin-panel">
        <h4>操作</h4>
        <div className="member-actions">
          <button className="danger-btn" disabled={!running} onClick={() => setConfirmStop(true)}>
            <Icon name="debug-stop" /> Workspace を強制停止
          </button>
          <button onClick={() => { setLimit(member.max_sessions ?? 0); setLimitOpen(true); }}>
            <Icon name="settings" /> セッション上限を設定
          </button>
          {isSuper && (
            <button className="danger-btn" onClick={() => setConfirmClean(true)}>
              <Icon name="trash" /> home を掃除
            </button>
          )}
        </div>
        {limitOpen && (
          <div className="limit-edit">
            <label>
              最大セッション数（0 = 無制限）
              <input type="number" min="0" value={limit} onChange={(e) => setLimit(e.target.value)} autoFocus />
            </label>
            <button className="primary" onClick={saveLimit}>保存</button>
            <button className="ghost" onClick={() => setLimitOpen(false)}>キャンセル</button>
          </div>
        )}
      </section>

      {confirmStop && (
        <ConfirmDialog
          title={`${key} の Workspace を停止`}
          confirmLabel="停止する"
          busy={busy}
          onCancel={() => setConfirmStop(false)}
          onConfirm={stop}
        >
          <p>このメンバーの {slug} の Workspace コンテナを停止します。</p>
        </ConfirmDialog>
      )}
      {confirmClean && (
        <ConfirmDialog
          title={`${key} の home を掃除`}
          confirmLabel="掃除する"
          busy={busy}
          onCancel={() => setConfirmClean(false)}
          onConfirm={cleanHome}
        >
          <p>このユーザーの Workspace home を掃除します。コンテナは停止されます。</p>
          <p className="muted">保持: 接続情報 / git 認証 / Claude・Codex ログイン</p>
          <p className="muted">削除: repos（未コミット含む）/ キャッシュ / その他 home 配下</p>
        </ConfirmDialog>
      )}
      {confirmGrant && (
        <ConfirmDialog
          title={`${key} を ${slug} の管理者にする`}
          confirmLabel="管理者にする"
          danger={false}
          busy={busy}
          onCancel={() => setConfirmGrant(false)}
          onConfirm={() => setRoleTo("tenant_admin")}
        >
          <p>このメンバーに <b>{slug}</b> のテナント管理者権限を付与します。</p>
          <p className="muted">付与後はこのテナント内のメンバー管理・リソース閲覧・Workspace 強制停止・セッション上限設定ができるようになります（他テナントには影響しません）。</p>
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
