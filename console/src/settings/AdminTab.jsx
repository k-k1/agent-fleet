import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON, rawJSON } from "../api.js";
import Icon from "../components/Icon.jsx";
import ConfirmDialog from "../components/ConfirmDialog.jsx";
import { kindLabel, kindClass, kindIcon } from "../lib/sessionkind.js";
import { stateInfo } from "../lib/sessionview.js";

// AdminTab (super_admin only): a staged drill-down —
//   テナント一覧 → テナント詳細 → メンバー詳細
// Each stage stands on its own (no cramped two-column form); the breadcrumb walks
// back. The member stage surfaces live Workspace resources (mem / CPU / disk) and
// the member's session list, served by the per-member admin endpoints.

const GiB = 1073741824;
// GiB with adaptive precision (matches WsBar): 2 decimals under 10, 1 above.
const fmtG = (b) => {
  const v = b / GiB;
  return (v < 10 ? v.toFixed(2) : v.toFixed(1)) + "G";
};
const fmtPct = (n) => (n == null ? "–" : Math.round(n) + "%");

export default function AdminTab() {
  const [tenants, setTenants] = useState(null);
  const [forbidden, setForbidden] = useState(false);
  const [isSuper, setIsSuper] = useState(false); // super_admin: unlocks deployment-wide controls
  // view: {stage:'tenants'} | {stage:'tenant', slug} | {stage:'member', slug, member}
  const [view, setView] = useState({ stage: "tenants" });

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

  const goBack = () => {
    if (view.stage === "member") setView({ stage: "tenant", slug: view.slug });
    else if (view.stage === "tenant") setView({ stage: "tenants" });
  };

  return (
    <div className="admin">
      <div className="admin-nav">
        {view.stage !== "tenants" && (
          <button className="admin-back" onClick={goBack}>
            <Icon name="arrow-left" /> 戻る
          </button>
        )}
        <nav className="admin-crumbs">
          <button className={"crumb" + (view.stage === "tenants" ? " here" : "")} onClick={() => setView({ stage: "tenants" })}>
            <Icon name="organization" /> テナント
          </button>
          {view.slug && (
            <>
              <Icon name="chevron-right" className="crumb-sep" />
              <button
                className={"crumb" + (view.stage === "tenant" ? " here" : "")}
                onClick={() => setView({ stage: "tenant", slug: view.slug })}
              >
                {tenantName}
              </button>
            </>
          )}
          {view.stage === "member" && (
            <>
              <Icon name="chevron-right" className="crumb-sep" />
              <span className="crumb here">{view.member.user_key}</span>
            </>
          )}
        </nav>
      </div>

      {view.stage === "tenants" && (
        <TenantsList
          tenants={tenants}
          isSuper={isSuper}
          onReload={loadTenants}
          onOpen={(slug) => setView({ stage: "tenant", slug })}
        />
      )}
      {view.stage === "tenant" && (
        <TenantView
          slug={view.slug}
          tenant={tenant}
          isSuper={isSuper}
          onChanged={loadTenants}
          onOpenMember={(member) => setView({ stage: "member", slug: view.slug, member })}
        />
      )}
      {view.stage === "member" && (
        <MemberView
          slug={view.slug}
          member={view.member}
          isSuper={isSuper}
          onChanged={loadTenants}
          onBack={() => setView({ stage: "tenant", slug: view.slug })}
        />
      )}
    </div>
  );
}

// --- Stage 1: tenant list ---------------------------------------------------

function TenantsList({ tenants, isSuper, onReload, onOpen }) {
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
                <span className={t.running > 0 ? "tc-run on" : "tc-run"} title="起動中の Workspace">
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

function NewTenant({ onCreated, onCancel }) {
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [err, setErr] = useState("");
  const submit = async (e) => {
    e.preventDefault();
    if (!slug.trim()) return;
    const r = await rawJSON("api/admin/tenants", "POST", { slug: slug.trim(), name: name.trim() });
    if (r.ok) {
      onCreated();
    } else {
      const er = await r.json().catch(() => ({}));
      setErr("作成に失敗: " + (er.error?.message || r.status));
    }
  };
  return (
    <form className="new-tenant" onSubmit={submit}>
      <input value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="slug（英数字）" autoFocus />
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder="表示名（任意）" />
      <button type="submit" className="primary">作成</button>
      <button type="button" className="ghost" onClick={onCancel}>キャンセル</button>
      {err && <span className="form-err">{err}</span>}
    </form>
  );
}

// --- Stage 2: tenant detail (limits + members) ------------------------------

function TenantView({ slug, tenant, isSuper, onChanged, onOpenMember }) {
  const [maxWs, setMaxWs] = useState(tenant?.max_workspaces || 0);
  const [maxSs, setMaxSs] = useState(tenant?.max_sessions || 0);
  const [saved, setSaved] = useState(false);
  const [members, setMembers] = useState(null);

  useEffect(() => {
    setMaxWs(tenant?.max_workspaces || 0);
    setMaxSs(tenant?.max_sessions || 0);
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
    await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/limits`, "PUT", {
      max_workspaces: +maxWs || 0,
      max_sessions: +maxSs || 0,
    });
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

function AddMember({ slug, isSuper, onAdded }) {
  const [email, setEmail] = useState("");
  const [key, setKey] = useState("");
  const [role, setRole] = useState("member");
  const [err, setErr] = useState("");
  const submit = async (e) => {
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
      setErr("");
      onAdded();
    } else {
      const er = await r.json().catch(() => ({}));
      setErr("追加に失敗: " + (er.error?.message || r.status));
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
      {err && <span className="form-err">{err}</span>}
    </form>
  );
}

// --- Stage 3: member detail (resources + sessions + actions) ----------------

function MemberView({ slug, member, isSuper, onChanged }) {
  const [stats, setStats] = useState(null);
  const [sessions, setSessions] = useState(null);
  const [confirmStop, setConfirmStop] = useState(false);
  const [confirmClean, setConfirmClean] = useState(false);
  const [confirmGrant, setConfirmGrant] = useState(false);
  const [busy, setBusy] = useState(false);
  const [limitOpen, setLimitOpen] = useState(false);
  const [limit, setLimit] = useState(member.max_sessions ?? 0);
  const [role, setMemberRole] = useState(member.role); // tenant-scoped role, live-updated on grant/revoke
  const timer = useRef(null);

  const key = member.user_key;
  const base = `api/admin/tenants/${encodeURIComponent(slug)}/members/${encodeURIComponent(key)}`;

  const poll = useCallback(async () => {
    try {
      const [s, ss] = await Promise.all([api(`${base}/stats`), api(`${base}/sessions`)]);
      setStats(s && !s.error ? s : { running: false });
      setSessions(ss && ss.sessions ? ss.sessions : []);
    } catch {
      /* keep last values; transient */
    }
  }, [base]);

  useEffect(() => {
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
  const setRoleTo = async (newRole) => {
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
        <span className={"state-dot " + (running ? "on" : "off")} />
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
            {sessions.map((s) => {
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
function ResTile({ label, value, sub, ratio, warn, crit }) {
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
