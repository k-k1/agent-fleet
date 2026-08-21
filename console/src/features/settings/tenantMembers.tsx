// テナントのメンバー面（名簿・追加・メンバー詳細）。
//
// AdminTab.tsx から純粋移動した。docs/61 §61.10.6 で offboarding の一式
// （メンバーを外す → ワークスペースを止める → home を掃除）は tenant_admin のもの
// と決まった（決定 26）のに、実装は管理モーダルの中にしか無かった。
//
// ★ 出し分けの isSuper は「デプロイ管理者にしか意味が無い操作」（ロールの付与・剥奪）
// を出すかどうかだけ。付与の PUT /api/admin/membership-role は withSuperAdmin 固定で、
// ここでボタンを隠すのは案内でしかない。
import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, apiJSON, rawJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ConfirmDialog } from "../../ui/ConfirmDialog.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { kindLabel, kindClass, kindIcon } from "../../lib/sessionkind.ts";
// メンバー詳細のクラウド費用（docs/67 §67.15）。請求の無いデプロイでは部品自身が
// 何も描かないので、ここで出し分けは持たない。
import { MemberCostPanel } from "../cost/CloudCostView.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { stateInfo } from "../../lib/sessionview.ts";
import { fmtG, fmtPct, fmtGbHint, ladderFor, slotFor, WS_SIZE_PRESETS, WS_SIZING_FALLBACK } from "./adminShared.ts";
import type { Member, WsSizing, WsSlot } from "./adminShared.ts";

// MembersPanel — 名簿と「メンバー追加」。TenantView の中に直接書かれていたものを、
// テナント設定モーダルからも同じ実装を差せるように 1 つの部品にした（描画も
// 読み込みも元のまま）。
export function MembersPanel({
  slug,
  isSuper,
  onOpenMember,
}: {
  slug: string;
  isSuper: boolean;
  onOpenMember: (m: Member) => void;
}) {
  const tr = useT();
  const [members, setMembers] = useState<Member[] | null>(null);

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

  return (
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

export function MemberView({
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
  const [confirmDestroy, setConfirmDestroy] = useState(false);
  // 退職処理のついでに破棄するかどうか。既定 false のまま出す——現行の契約
  // （home を残し、戻ってきたら再招待するだけ）を、チェックしない限り変えない。
  const [purge, setPurge] = useState(false);
  const [busy, setBusy] = useState(false);
  const [limitOpen, setLimitOpen] = useState(false);
  const [limit, setLimit] = useState<number | string>(member.max_sessions ?? 0);
  // The three workspace size axes. Memory is stored in bytes and edited in MB, CPU in
  // Fargate units (1024 = 1 vCPU) and disk in GiB — 0 means "unset → deployment default"
  // on every axis. They are independent on purpose (ADR 0044 決定 1): the presets below
  // just fill all three at once with a combination Fargate accepts.
  const [memMb, setMemMb] = useState<number | string>(member.mem_limit ? Math.round(member.mem_limit / 1048576) : 0);
  const [cpuUnits, setCpuUnits] = useState<number | string>(member.cpu_limit ?? 0);
  const [diskGb, setDiskGb] = useState<number | string>(member.disk_gb ?? 0);
  // Which KIND of machine, where the deployment offers a choice (docs/70). "" = the
  // tenant default, which is a real value here and not "unset means smallest": there
  // is no numeric fallback for a class.
  const [slotClass, setSlotClass] = useState<string>(member.slot_class ?? "");
  // What those three numbers actually DO on this deployment's runtime (ADR 0045 決定 21).
  // Fetched rather than assumed: the same editor is shown for docker, native, Fargate and
  // the EC2 slot pool, and it used to describe all four as Fargate.
  const [sizing, setSizing] = useState<WsSizing>(WS_SIZING_FALLBACK);
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

  // Deployment-wide and immutable while the Console runs, so it is read once per open
  // member rather than on the 4s poll.
  useEffect(() => {
    let live = true;
    api("api/admin/workspace-sizing")
      .then((d: WsSizing) => {
        if (live && d && !(d as any).error && d.runtime) setSizing(d);
      })
      .catch(() => {
        /* keep the docker/Fargate description; the editor still works */
      });
    return () => {
      live = false;
    };
  }, []);

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

  // --- how to describe the three size axes on THIS runtime (ADR 0045 決定 21) ------
  // A ladder exists only on the EC2 slot pool; everywhere else the memory number is a
  // cap and keeps the wording it has always had.
  const onSlots = sizing.mem_meaning === "slot" && !!sizing.slots?.length;
  const slotSpec = (s: WsSlot) => (s.vcpu ? `${s.vcpu} vCPU / ${fmtGbHint(s.mem_mib)}` : fmtGbHint(s.mem_mib));
  // The machine-class picker exists only where the operator declared more than one
  // (docs/70 §70.10). The memory chips below are then the SELECTED class's ladder, so
  // switching class re-draws them and "you land on" recomputes — the same number can
  // land on a different box in a different class, and that is the whole point.
  const classes = onSlots ? (sizing.slot_classes ?? []) : [];
  const ladder = onSlots ? ladderFor(sizing, slotClass) : undefined;
  const landed = onSlots ? slotFor(ladder, +memMb || 0) : null;
  // Warn only when there is a home to migrate. A member who has never started has
  // nothing architecture-dependent on disk yet, so the warning would be noise.
  const classChanged = classes.length > 0 && slotClass !== (member.slot_class ?? "");
  const archOf = (id: string) => classes.find((c) => c.id === id)?.arch ?? "";
  const archChanged =
    classChanged && archOf(slotClass || (sizing.default_slot_class ?? "")) !== archOf(member.slot_class || (sizing.default_slot_class ?? ""));
  const memHint = !landed
    ? +memMb > 0
      ? tr("admin.eq_hint", { hint: fmtGbHint(+memMb) })
      : tr("admin.zero_deploy_default")
    : +memMb > 0
      ? tr("admin.ws_slot_lands", { type: landed.instance_type, spec: slotSpec(landed) })
      : // ⚠️ 0 is NOT "deployment default" here: slotTypeFor(0) lands on the smallest rung.
        tr("admin.ws_slot_zero", { type: landed.instance_type });
  const diskDefault = sizing.disk_default_gb ?? 0;
  const diskHint =
    sizing.disk_meaning === "home"
      ? tr("admin.ws_disk_home_hint", { n: String(diskDefault) })
      : sizing.disk_meaning === "quota"
        ? tr("admin.ws_disk_quota_hint")
        : +diskGb > 0
          ? tr("admin.ws_disk_warn")
          : diskDefault && diskDefault !== 20
            ? tr("admin.ws_disk_work_hint", { n: String(diskDefault) })
            : tr("admin.ws_disk_hint");

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
      cpu_limit: Math.round(+cpuUnits || 0),
      // Sent explicitly rather than omitted: the endpoint writes the whole quota row,
      // so leaving it out would silently reset a disk quota set elsewhere (MCP/API).
      disk_gb: Math.round(+diskGb || 0),
      slot_class: slotClass,
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
      const res = await apiJSON("api/admin/memberships", "DELETE", { tenant_slug: slug, user_key: key, purge });
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
  // Workspace の破棄（ADR 0045 決定 13）。退職処理とは別の 2 段目で、対象は
  // 「すでに外したメンバー」だけ——在席中の人の home を管理画面の 1 クリック隣で
  // 消せないようにするため（サーバ側も 409 で拒む）。
  //
  // leftovers は「消せなかったもの」。Fargate では EFS のディレクトリが残り、課金も
  // 残る。成功したことにして黙らせると、運用者は「消えた」と思い込む。
  const destroyWorkspace = async () => {
    setBusy(true);
    try {
      const res = await apiJSON("api/admin/workspaces", "DELETE", { tenant_slug: slug, user_key: key });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setConfirmDestroy(false);
      if (res?.leftovers?.length) toast(tr("admin.destroy_leftovers", { list: res.leftovers.join(", ") }));
      onChanged();
      poll();
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

      {/* リソースの「今」の直後に、費用の「期間」を別のカードで置く。同じカードに
          しないのは ADR 0048 決定 2（時間と $ を並べない）——上のタイルは 4 秒ごとの
          実測、こちらは約 24 時間遅れの請求で、読み方が違う。 */}
      <MemberCostPanel slug={slug} userKey={key} />

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
          <button onClick={() => {
            setLimit(member.max_sessions ?? 0);
            setMemMb(member.mem_limit ? Math.round(member.mem_limit / 1048576) : 0);
            setCpuUnits(member.cpu_limit ?? 0);
            setDiskGb(member.disk_gb ?? 0);
            setSlotClass(member.slot_class ?? "");
            setLimitOpen(true);
          }}>
            <Icon name="settings" /> {tr("admin.set_limits")}
          </button>
          {/* clean-home is a tenant_admin action now (docs/61 §61.10.6 / 決定 26):
              the department knows who left, so the whole offboarding sequence
              belongs to it rather than half of it being a ticket to IT. */}
          <button className="danger-btn" onClick={() => setConfirmClean(true)}>
            <Icon name="trash" /> {tr("admin.clean_home")}
          </button>
          {member.status !== "removed" ? (
            <button className="danger-btn" disabled={busy} onClick={() => setConfirmRemove(true)}>
              <Icon name="close" /> {tr("admin.remove_member")}
            </button>
          ) : (
            <button className="danger-btn" disabled={busy} onClick={() => setConfirmDestroy(true)}>
              <Icon name="trash" /> {tr("admin.destroy_ws")}
            </button>
          )}
        </div>
        {limitOpen && (
          <div className="limit-edit">
            <div className="le-head">{tr("admin.limits_edit_title")}</div>
            {/* Which KIND of machine, above the numbers — it changes what the numbers
                below mean (docs/70 §70.10). The operator's own label is what is shown;
                the instance type appears only in the "you land on" line. */}
            {classes.length > 0 && (
              <div className="le-presets">
                <span className="af-cap">{tr("admin.ws_machine")}</span>
                <button
                  className={slotClass === "" ? "chip on" : "chip"}
                  onClick={() => setSlotClass("")}
                >
                  {tr("admin.ws_machine_tenant_default")}
                </button>
                {classes.map((c) => (
                  <button
                    key={c.id}
                    className={slotClass === c.id ? "chip on" : "chip"}
                    onClick={() => setSlotClass(c.id)}
                  >
                    {c.label}
                  </button>
                ))}
              </div>
            )}
            <div className="admin-fgrid">
              <label className="admin-fld">
                <span className="af-cap">{tr("admin.max_sessions_label")}</span>
                <input type="number" min="0" value={limit} onChange={(e) => setLimit(e.target.value)} autoFocus />
                <span className="af-unit">{tr("admin.zero_unlimited")}</span>
              </label>
              <label className="admin-fld">
                <span className="af-cap">{onSlots ? tr("admin.ws_mem_req") : tr("admin.ws_memory")}</span>
                <span className="af-inputwrap">
                  <input type="number" min="0" step="256" value={memMb} onChange={(e) => setMemMb(e.target.value)} />
                  <span className="af-suffix">MB</span>
                </span>
                <span className="af-unit">{memHint}</span>
              </label>
              {/* A CPU number that never reaches the backend is worse than no field, so
                  on such a runtime it is not rendered at all — but saveLimit keeps
                  sending the stored value, so hiding it here cannot zero a value set
                  for a runtime where it does work. */}
              {sizing.cpu_effective && (
                <label className="admin-fld">
                  <span className="af-cap">{tr("admin.ws_cpu")}</span>
                  <input type="number" min="0" step="256" value={cpuUnits} onChange={(e) => setCpuUnits(e.target.value)} />
                  <span className="af-unit">
                    {+cpuUnits > 0 ? tr("admin.ws_cpu_vcpu", { n: String(+cpuUnits / 1024) }) : tr("admin.zero_deploy_default_cpu")}
                  </span>
                </label>
              )}
              <label className="admin-fld">
                <span className="af-cap">{sizing.disk_meaning === "home" ? tr("admin.ws_disk_home") : tr("admin.ws_disk")}</span>
                <span className="af-inputwrap">
                  <input type="number" min="0" step="10" value={diskGb} onChange={(e) => setDiskGb(e.target.value)} />
                  <span className="af-suffix">GB</span>
                </span>
                <span className="af-unit">{diskHint}</span>
              </label>
            </div>
            {/* Presets are a way to PRESENT valid combinations, not a stored size
                (ADR 0044 決定 1). On a slot pool the valid set is the ladder itself, so
                the chips become the rungs and touch only the memory axis — offering an
                in-between value there would just round up silently. */}
            <div className="le-presets">
              <span className="af-cap">{tr("admin.ws_size_preset")}</span>
              {onSlots
                ? ladder!.map((s) => (
                    <button
                      key={s.instance_type}
                      className={+memMb === s.mem_mib ? "chip on" : "chip"}
                      onClick={() => setMemMb(s.mem_mib)}
                    >
                      {fmtGbHint(s.mem_mib)}
                    </button>
                  ))
                : WS_SIZE_PRESETS.map((p) => {
                    const on = +memMb === p.mem && +cpuUnits === p.cpu && +diskGb === p.disk;
                    return (
                      <button
                        key={p.id}
                        className={on ? "chip on" : "chip"}
                        onClick={() => { setMemMb(p.mem); setCpuUnits(p.cpu); setDiskGb(p.disk); }}
                      >
                        {p.label}
                      </button>
                    );
                  })}
              <span className="af-unit">
                {(onSlots
                  ? ladder!.some((s) => +memMb === s.mem_mib)
                  : WS_SIZE_PRESETS.some((p) => +memMb === p.mem && +cpuUnits === p.cpu && +diskGb === p.disk))
                  ? ""
                  : tr("admin.ws_size_custom")}
              </span>
            </div>
            {!sizing.cpu_effective && <p className="admin-hint">{tr("admin.ws_cpu_na")}</p>}
            {onSlots && <p className="admin-hint">{tr("admin.ws_slot_note")}</p>}
            {/* ⚠️ The one destructive-ish consequence in this editor. `~` is a volume
                that follows the member, and its ~/.local CLIs, nvm node, Chromium and
                node_modules are architecture-dependent — a class change across
                architectures makes the next start reinstall them (docs/70 §70.5).
                Shown only when the architecture actually changes: within one
                architecture the home is portable and there is nothing to warn about. */}
            {archChanged && <p className="admin-hint warn">{tr("admin.ws_machine_arch_warn")}</p>}
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
          <label className="purge-opt">
            <input type="checkbox" checked={purge} onChange={(e) => setPurge(e.target.checked)} />
            <span>{tr("admin.remove_purge")}</span>
          </label>
          {purge && <p className="warn-text">{tr("admin.remove_purge_warn")}</p>}
        </ConfirmDialog>
      )}
      {confirmDestroy && (
        <ConfirmDialog
          title={tr("admin.destroy_title", { key })}
          confirmLabel={tr("admin.destroy_confirm")}
          busy={busy}
          onCancel={() => setConfirmDestroy(false)}
          onConfirm={destroyWorkspace}
        >
          <p>{tr("admin.destroy_body")}</p>
          <p className="warn-text">{tr("admin.destroy_locks")}</p>
          <p className="muted">{tr("admin.destroy_efs")}</p>
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
