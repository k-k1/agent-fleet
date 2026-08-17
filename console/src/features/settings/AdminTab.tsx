import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { api, apiJSON, rawJSON, errText } from "../../core/api/client.ts";
import { mobileMatches } from "../../lib/device.ts";
import { adminDepthRef } from "./store.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { setTenantDict } from "../chat/ttsDict.ts";
import { fmtGbHint } from "./adminShared.ts";
import type { Member, Tenant } from "./adminShared.ts";
// テナント管理者にも意味のある面は、テナント設定モーダルと実装を共有する。ここは
// 「デプロイ管理者から見た」置き場として差すだけ。
import { MembersPanel, MemberView } from "./tenantMembers.tsx";
import { AllSessionsView, AuditView, UsageView } from "./tenantOps.tsx";
import { McpAdminView } from "./mcpAdmin.tsx";
import { PoolView } from "./ec2Pool.tsx";
import { TenantLoginRules, TenantSignInMethods, SignInMethodRegister } from "./tenantLogin.tsx";
import { TenantNetworkView } from "./tenantNetwork.tsx";

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

const ADMIN_MODES = ["manage", "sessions", "usage", "audit", "egress", "mcp", "tts"]; // swipe order for the mode tabs
// "pool" is appended at runtime (see hasPool): the EC2 slot pool exists on one runtime
// profile, and an empty Slots tab on a Fargate deployment reads as "my slots vanished".

export function AdminTab() {
  const tr = useT();
  // shared with the settings store so closeAdmin can pop all drill levels at once
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  const [forbidden, setForbidden] = useState(false);
  const [isSuper, setIsSuper] = useState(false); // super_admin: unlocks deployment-wide controls
  const [mode, setMode] = useState("manage"); // manage (tenant drilldown) | usage (showback)
  // Whether this deployment HAS a slot pool. One cheap probe at mount; the endpoint
  // answers {"runtime":"other"} everywhere else.
  const [hasPool, setHasPool] = useState(false);
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
  useEffect(() => {
    // super_admin only, so a tenant_admin simply never sees the tab (the endpoint 403s
    // and this stays false).
    api("api/admin/ec2-pool")
      .then((d) => setHasPool(d?.runtime === "ecs-ec2"))
      .catch(() => setHasPool(false));
  }, []);

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
        {hasPool && (
          <button type="button" className={"seg-btn" + (mode === "pool" ? " active" : "")} onClick={() => setMode("pool")}>
            <Icon name="server" /> {tr("admin.mode_pool")}
          </button>
        )}
      </div>

      {mode === "sessions" && <AllSessionsView tenants={tenants} isSuper={isSuper} />}
      {mode === "usage" && <UsageView tenants={tenants} isSuper={isSuper} />}
      {mode === "audit" && <AuditView tenants={tenants} isSuper={isSuper} />}
      {mode === "egress" && <EgressView />}
      {mode === "mcp" && <McpAdminView tenants={tenants} />}
      {mode === "tts" && <TtsAdminView />}
      {mode === "pool" && hasPool && <PoolView />}

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
          hasPool={hasPool}
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
  hasPool,
  onChanged,
  onOpenMember,
}: {
  slug: string;
  tenant: Tenant | null | undefined;
  isSuper: boolean;
  // Home hibernation only exists on the EC2 slot pool runtime. Everywhere else the
  // setting would be a field that quietly does nothing, so it is not shown at all.
  hasPool: boolean;
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
  const [homeHib, setHomeHib] = useState(tenant?.home_hibernate_after || "");
  const [homeBackup, setHomeBackup] = useState(tenant?.home_backup_every || "");
  const [allowUpd, setAllowUpd] = useState(!!tenant?.allow_agent_self_update);
  const [termRetention, setTermRetention] = useState(tenant?.terminal_history_retention_days || 0);
  const [saved, setSaved] = useState(false);
  const toast = useToast();

  useEffect(() => {
    setMaxWs(tenant?.max_workspaces || 0);
    setMaxSs(tenant?.max_sessions || 0);
    setMaxRepos(tenant?.max_git_repos || 0);
    setMaxLfsMb(Math.round((tenant?.max_lfs_bytes || 0) / 1048576));
    setMaxWsMemMb(Math.round((tenant?.max_workspace_mem || 0) / 1048576));
    setSessIdle(tenant?.session_idle_timeout || "");
    setWsIdle(tenant?.ws_idle_timeout || "");
    setHomeHib(tenant?.home_hibernate_after || "");
    setHomeBackup(tenant?.home_backup_every || "");
    setAllowUpd(!!tenant?.allow_agent_self_update);
    setTermRetention(tenant?.terminal_history_retention_days || 0);
  }, [slug, tenant]);

  const saveLimits = async () => {
    const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/limits`, "PUT", {
      max_workspaces: +maxWs || 0,
      max_sessions: +maxSs || 0,
      max_git_repos: +maxRepos || 0,
      max_lfs_bytes: Math.round(+maxLfsMb || 0) * 1048576,
      max_workspace_mem: Math.round(+maxWsMemMb || 0) * 1048576,
      session_idle_timeout: sessIdle.trim(),
      ws_idle_timeout: wsIdle.trim(),
      home_hibernate_after: homeHib.trim(),
      home_backup_every: homeBackup.trim(),
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

          {/* Only the EC2 slot pool has somewhere cheaper to put a home; on the other
              runtimes this field would be a control that does nothing. */}
          {hasPool && (
            <div className="admin-fgroup">
              <h4>{tr("admin.hibernate_title")}<span className="af-note">{tr("admin.empty_deploy_default")}</span></h4>
              <div className="admin-fgrid">
                <label className="admin-fld">
                  <span className="af-cap">{tr("admin.hibernate_after")}</span>
                  <input type="text" placeholder={tr("admin.hibernate_ph")} value={homeHib} onChange={(e) => setHomeHib(e.target.value)} />
                </label>
              </div>
              <p className="admin-hint">{tr("admin.hibernate_hint")}</p>
              <p className="admin-hint">{tr("admin.hibernate_warn")}</p>
            </div>
          )}

          {/* home が 1 つの AZ に固定されるのはこのランタイムだけなので、AZ ごと失う話も
              ここにしか無い。他では home はそもそも 1 AZ に縛られていない。 */}
          {hasPool && (
            <div className="admin-fgroup">
              <h4>{tr("admin.backup_title")}<span className="af-note">{tr("admin.empty_deploy_default")}</span></h4>
              <div className="admin-fgrid">
                <label className="admin-fld">
                  <span className="af-cap">{tr("admin.backup_every")}</span>
                  <input type="text" placeholder={tr("admin.backup_ph")} value={homeBackup} onChange={(e) => setHomeBackup(e.target.value)} />
                </label>
              </div>
              <p className="admin-hint">{tr("admin.backup_hint")}</p>
              <p className="admin-hint">{tr("admin.backup_warn")}</p>
            </div>
          )}

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

      {/* Tenant-defined sign-in methods (docs/61 §61.11). The rows are the
          tenant_admin's — they write them, including the client secret — so their
          place is the tenant settings modal, and that is where a tenant_admin now
          finds them. It stays here for the operator because approval is theirs and
          nobody else's (決定 30) and because this screen shows one tenant's rows in
          full — the register (tenant list, below) now carries 承認・停止 itself. */}
      {isSuper && <TenantSignInMethods slug={slug} isSuper={isSuper} />}

      {/* テナントの接続元制限（docs/66・ADR 0047）。持ち主は tenant_admin だが、
          ★ 入口をテナント設定モーダルだけにすると、**tenant_admin の在籍が無い
          super_admin からは一生見えない** —— アカウントメニューの「テナント設定」は
          tenant_admin の在籍だけで出るため。実際にそれで「設定にも管理にも無い」に
          なった。ログイン規則・サインイン方法と同じで面は両方に置く（権限はサーバ）。 */}
      <TenantNetworkView key={slug} slug={slug} />

      <MembersPanel slug={slug} isSuper={isSuper} onOpenMember={onOpenMember} />
    </div>
  );
}
