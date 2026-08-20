import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { api, apiJSON, rawJSON, errText } from "../../core/api/client.ts";
import { mobileMatches } from "../../lib/device.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { setTenantDict } from "../chat/ttsDict.ts";
import type { Member, Tenant } from "./adminShared.ts";
import { AllSessionsView, AuditView, UsageView } from "./tenantOps.tsx";
import { CloudCostAdminView, useCostProfile } from "../cost/CloudCostView.tsx";
import { McpAdminView } from "./mcpAdmin.tsx";
import { PoolView } from "./ec2Pool.tsx";
import { SignInMethodRegister } from "./tenantLogin.tsx";
// テナント 1 つ分の面（レールの並びも本文も）はテナント設定モーダルと共有する。
// 同じテナントを別の入口から見るだけなので、IA を入口ごとに分けない。
import { TenantScopeBody, tenantScopeGroups } from "./tenantScope.tsx";

// AdminTab（super_admin の面）— 個人設定・テナント設定と同じ左レール＋本文の二枚看板。
//
// レールは 2 段になっている:
//   ルート  … テナント一覧 / 登録簿・デプロイ全体（通信・読み上げ・スロット）・
//              横断で見る（セッション・稼働時間・費用・監査・MCP 配布）
//   テナント… 一覧からテナントを開くとレールごと切り替わり、そのテナントの
//              上限 / ログイン / 運用の節が並ぶ（テナント設定モーダルと同じ並び）。
//              メンバーは本文の中でもう 1 段ドリルする。
//
// ★ 旧実装は横一列のセグメントタブ 9 個＋その中だけパンくずドリルダウン、という
// 二重のナビだった。設定モーダルが「タブ 6 超で破綻する」として捨てた形がここに
// 残っていたもので、スマホでは横スクロールとスワイプの回避策まで要っていた。
// レール化でその 2 つは不要になり、戻る（端末/ブラウザ）も ui/Modal と同じ
// useBackClose の層に一本化した（独自 history エントリは撤去）。

interface RailGroup {
  key: string;
  label: string;
  items: [string, string][];
}

function rootGroups(opts: { pool: boolean; cost: boolean }): RailGroup[] {
  return [
    {
      key: "tenants",
      label: "admin.group_tenants",
      items: [
        ["tenants", "admin.tenants_list"],
        // テナントが定義したサインイン方法の登録簿（docs/61 §61.11.6）。承認できるのは
        // デプロイ管理者だけなので、デプロイ管理者にしか無い面。
        ["register", "admin.tab_register"],
      ],
    },
    {
      key: "deployment",
      label: "admin.group_deployment",
      items: [
        ["egress", "admin.mode_egress"],
        ["tts", "admin.mode_tts"],
        // スロットプールは 1 つのランタイムにしか無い。Fargate のデプロイに空の
        // 「スロット」が出ると「自分のスロットが消えた」と読める。
        ...(opts.pool ? ([["pool", "admin.mode_pool"]] as [string, string][]) : []),
      ],
    },
    {
      key: "across",
      label: "admin.group_across",
      items: [
        ["sessions", "admin.mode_sessions"],
        ["usage", "admin.mode_usage"],
        // docker / native には AWS の請求が無い。0 が並ぶ費用の面は「無料」と読める
        // ので、項目ごと作らない（docs/67 §67.8・ADR 0048 決定 9）。
        ...(opts.cost ? ([["cost", "admin.mode_cost"]] as [string, string][]) : []),
        ["audit", "admin.mode_audit"],
        ["mcp", "admin.mode_mcp"],
      ],
    },
  ];
}

export function AdminTab() {
  const tr = useT();
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  const [forbidden, setForbidden] = useState(false);
  const [isSuper, setIsSuper] = useState(false); // super_admin: unlocks deployment-wide controls
  // Whether this deployment HAS a slot pool. One cheap probe at mount; the endpoint
  // answers {"runtime":"other"} everywhere else.
  const [hasPool, setHasPool] = useState(false);
  // Whether this deployment HAS an AWS bill. Runtime-declared, not configured.
  const costProfile = useCostProfile();

  // scope = 開いているテナントの slug（null＝ルート）。section はレールの現在地で、
  // ルートとテナントで別々に覚える（テナントを閉じても一覧に戻ってくるだけ）。
  const [scope, setScope] = useState<string | null>(null);
  const [rootSection, setRootSection] = useState("tenants");
  const [scopeSection, setScopeSection] = useState("limits");
  const [member, setMember] = useState<Member | null>(null);
  // スマホは個人設定と同じドリルダウン（レール → 本文、戻るでレールへ）。
  const [entered, setEntered] = useState(false);

  // 戻るの層は積んだ順に剥がれる（useBackClose の共有スタック）。宣言順＝
  // レール ↓ テナント ↓ メンバーなので、端末の戻るは メンバー → テナント →
  // レール → モーダルを閉じる、の順で 1 段ずつ戻る。
  useBackClose(() => setEntered(false), mobileMatches() && entered);
  const leaveScope = useCallback(() => {
    setScope(null);
    setMember(null);
    // スマホではテナント一覧（本文）へ戻す。レールに戻してしまうと、どのテナントから
    // 出てきたのか分からなくなる。
    if (mobileMatches()) setEntered(true);
  }, []);
  useBackClose(leaveScope, !!scope);
  useBackClose(() => setMember(null), !!member);

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

  const cost = !!costProfile?.available;
  const groups = scope ? tenantScopeGroups({ cost }) : rootGroups({ pool: hasPool, cost });
  const section = scope ? scopeSection : rootSection;
  const scopeTenant = scope ? tenants.find((t) => t.slug === scope) || null : null;
  const currentLabel = tr(
    (groups.flatMap((g) => g.items).find(([k]) => k === section)?.[1] ??
      "admin.title") as Parameters<typeof tr>[0],
  );

  const openTenant = (slug: string) => {
    setScope(slug);
    setScopeSection("limits");
    setMember(null);
    // スマホ: テナントを開いたらそのテナントのレールを見せる（次に選ぶのは節）。
    if (mobileMatches()) setEntered(false);
  };

  const pick = (key: string) => {
    if (scope) setScopeSection(key);
    else setRootSection(key);
    setMember(null);
    setEntered(true);
  };

  const body = () => {
    if (scope) {
      return (
        <TenantScopeBody
          slug={scope}
          tenant={scopeTenant}
          section={scopeSection}
          isSuper={isSuper}
          hasPool={hasPool}
          member={member}
          onOpenMember={setMember}
          onCloseMember={() => setMember(null)}
          onChanged={loadTenants}
        />
      );
    }
    if (rootSection === "register") return <SignInMethodRegister />;
    if (rootSection === "egress") return <EgressView />;
    if (rootSection === "tts") return <TtsAdminView />;
    if (rootSection === "pool" && hasPool) return <PoolView />;
    if (rootSection === "sessions") return <AllSessionsView tenants={tenants} isSuper={isSuper} />;
    if (rootSection === "usage") return <UsageView tenants={tenants} isSuper={isSuper} />;
    if (rootSection === "cost" && cost) return <CloudCostAdminView tenants={tenants} isSuper={isSuper} />;
    if (rootSection === "audit") return <AuditView tenants={tenants} isSuper={isSuper} />;
    if (rootSection === "mcp") return <McpAdminView tenants={tenants} />;
    return <TenantsList tenants={tenants} isSuper={isSuper} onReload={loadTenants} onOpen={openTenant} />;
  };

  return (
    <div className={"settings-layout" + (entered ? " entered" : "")}>
      <nav className="settings-rail" aria-label={tr("admin.title")}>
        {/* テナントを開いている間はレールごとそのテナントの中身に入れ替わる。
            見出しは今どこに居るか（テナント名）、その上が出口。 */}
        {scope && (
          <div className="settings-rail-group admin-scope-head">
            <button type="button" className="admin-rail-back" onClick={leaveScope}>
              <Icon name="arrow-left" /> {tr("admin.all_tenants_back")}
            </button>
            <div className="admin-scope-name">
              <Icon name="organization" /> {scopeTenant?.name || scope}
            </div>
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
                onClick={() => pick(key)}
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
            ‹ {scope ? scopeTenant?.name || scope : tr("admin.title")}
          </button>
          <span className="settings-current" aria-current="page">
            {currentLabel}
          </span>
        </div>
        <div className="admin">{body()}</div>
      </div>
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

// --- テナント一覧（ルートの入口）-------------------------------------------
// カードを開くとレールごとそのテナントの面に入る（ドリルダウンの 1 段目）。

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
