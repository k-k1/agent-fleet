// テナントのメンバー面（名簿・状態チップ・追加）。メンバー詳細は 1 人ぶんで 650 行
// あり、名簿とは読む理由が違うので tenantMemberDetail.tsx に分けてある。
//
// AdminTab.tsx から純粋移動した。docs/log/61 §61.10.6 で offboarding の一式
// （メンバーを外す → ワークスペースを止める → home を掃除）は tenant_admin のもの
// と決まった（決定 26）のに、実装は管理モーダルの中にしか無かった。
//
// ★ 出し分けの isSuper は「デプロイ管理者にしか意味が無い操作」（ロールの付与・剥奪）
// を出すかどうかだけ。付与の PUT /api/admin/membership-role は withSuperAdmin 固定で、
// ここでボタンを隠すのは案内でしかない。
import { useCallback, useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api, rawJSON } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
// メンバー詳細のクラウド費用（docs/log/67 §67.15）。請求の無いデプロイでは部品自身が
// 何も描かないので、ここで出し分けは持たない。
import { useT } from "../../../lib/i18n/index.ts";
import { remainingShort } from "../../../lib/sessionview.ts";
import { fmtGbHint, ladderFor, slotFor, slotMemLabel, WS_SIZING_FALLBACK } from "../parts/adminShared.ts";
import type { Member, MemberIdle, WsSizing } from "../parts/adminShared.ts";

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
  // The roster shows what each member is sized to, and that only reads correctly once
  // the runtime has said what the numbers MEAN (ADR 0045 決定 21). Same fetch the member
  // detail does — it is a small, cacheable, identity-agnostic document.
  const [sizing, setSizing] = useState<WsSizing>(WS_SIZING_FALLBACK);
  useEffect(() => {
    let live = true;
    api("api/admin/workspace-sizing")
      .then((d) => {
        if (live && d && !(d as any).error && d.runtime) setSizing(d);
      })
      .catch(() => {
        /* keep the fallback; the roster then just shows the raw numbers */
      });
    return () => {
      live = false;
    };
  }, []);

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
              <MemberSizeChips m={m} sizing={sizing} />
              <MemberIdleChip idle={m.idle} state={m.state} />
              <Icon name="chevron-right" className="mr-go" />
            </button>
          ))}
        </div>
      )}
      <AddMember slug={slug} isSuper={isSuper} onAdded={loadMembers} />
    </section>
  );
}

// MemberIdleChip — 自動停止の見通し（docs/log/75 P4）。
//
// なぜ名簿に出すのか: 自動停止が効かないとき、これまで運用者に見えるものが何も無かった。
// reaper はログを出すだけで、調べる唯一の手段が他人のコンテナへ docker exec して status
// ファイルを読むことだった。止まる予定と「止めているもの」は同じ場所に出す — 「あと 20 分」
// と「セッション s5 が実行中だから止まらない」は同じ問いへの答えで、別々に置くと
// 「予定が出ていない＝壊れている」と読まれる。
//
// ⚠️ ここで再計算はしない。自前で導出すると reaper が実際に見ているもの（在席・ピン・
// 背景作業・共有ウォーターマーク）とズレて、原因調査のための画面が別の答えを出す。
// 出すのは reaper が最後に観測した値そのもの。
function MemberIdleChip({ idle, state }: { idle?: MemberIdle; state?: string }) {
  const tr = useT();
  // 稼働していない Workspace に停止予定は無い（CP も running のときしか載せない）。
  if (state !== "running" || !idle) return null;
  if (!idle.enabled) {
    return <span className="mr-idle muted" title={tr("admin.idle_off_hint")}>{tr("admin.idle_off")}</span>;
  }
  const holders = idle.holders ?? [];
  if (holders.length > 0) {
    const h = holders[0];
    const label =
      h.kind === "pin"
        ? tr("admin.idle_hold_pin")
        : h.kind === "working"
          ? tr("admin.idle_hold_working")
          : h.kind === "background"
            ? tr("admin.idle_hold_background")
            : h.kind === "repojob"
              ? tr("admin.idle_hold_repojob")
              : tr("admin.idle_hold_watching");
    // 2 件目以降は件数だけ（名簿の行は 1 行に収める）。詳細はメンバー詳細で出す。
    const more = holders.length > 1 ? tr("admin.idle_hold_more", { n: String(holders.length - 1) }) : "";
    return (
      <span className="mr-idle hold" title={holdersTitle(holders, tr)}>
        {(h.session ? `${label} (${h.session})` : label) + more}
      </span>
    );
  }
  const left = remainingShort(idle.stopAt);
  if (!left) return null;
  return (
    <span className="mr-idle" title={tr("admin.idle_stop_at", { at: new Date(idle.stopAt!).toLocaleString() })}>
      {tr("admin.idle_stop_in", { left })}
    </span>
  );
}

function holdersTitle(holders: NonNullable<MemberIdle["holders"]>, tr: (k: never, p?: never) => string): string {
  return holders.map((h) => (h.session ? `${h.kind}: ${h.session}` : h.kind)).join(" / ");
}

// MemberIdleDetail — メンバー詳細の「自動停止の見通し」。
//
// 名簿のチップ（MemberIdleChip）が 1 行に畳んでいるものを開いて出すだけで、判定は
// どちらも reaper の観測そのもの。**観測時刻を必ず添える**: スイープ間隔（既定 60 秒）
// ぶん古いので、秒単位で断言させると「画面ではまだ 3 分あったのに止まった」になる。
export function MemberIdleDetail({ idle, state }: { idle?: MemberIdle; state?: string }) {
  const tr = useT();
  if (state !== "running" || !idle) return null;
  const holders = idle.holders ?? [];
  const left = remainingShort(idle.stopAt);
  return (
    <section className="admin-panel">
      <h4>{tr("admin.idle_heading")}</h4>
      {!idle.enabled ? (
        <p className="muted">{tr("admin.idle_off_hint")}</p>
      ) : holders.length === 0 ? (
        <p className="muted">
          {left
            ? tr("admin.idle_stop_in", { left }) + tr("admin.idle_stop_at_paren", { at: new Date(idle.stopAt!).toLocaleString() })
            : tr("admin.idle_stopping_soon")}
        </p>
      ) : (
        <>
          <p className="muted">{tr("admin.idle_held")}</p>
          <ul className="idle-holders">
            {holders.map((h, i) => (
              <li key={i}>
                {h.kind === "pin"
                  ? tr("admin.idle_hold_pin_row", { session: h.session ?? "", left: remainingShort(h.until) || "–" })
                  : h.kind === "working"
                    ? tr("admin.idle_hold_working_row", { session: h.session ?? "" })
                    : h.kind === "background"
                      ? tr("admin.idle_hold_background_row", { session: h.session ?? "" })
                      : h.kind === "repojob"
                        ? tr("admin.idle_hold_repojob_row")
                        : tr("admin.idle_hold_watching_row")}
              </li>
            ))}
          </ul>
        </>
      )}
      <p className="admin-hint">{tr("admin.idle_observed", { at: new Date(idle.observedAt).toLocaleTimeString() })}</p>
    </section>
  );
}

// MemberSizeChips — what this member is sized to, on the roster row.
//
// ★ It shows the BOX on a slot runtime and the NUMBERS everywhere else, because those
// are different statements. On ecs-ec2 the memory figure is a requirement that picks a
// machine and the person then gets the whole thing, so "m6i.large · 2 vCPU / 8 GiB" is
// the true answer and "8192 MB" is a half-truth. On docker/Fargate the number IS the
// cap, and there is no box to name.
//
// ⚠️ The CPU chip follows the same rule the member detail uses: when cpu_effective is
// false the axis never reaches the backend, so showing it would put a number on screen
// that does nothing. It is omitted rather than greyed out.
//
// ⚠️ Everything here is "unset → say nothing". A roster of rows all reading "0" teaches
// people to stop reading the column.
function MemberSizeChips({ m, sizing }: { m: Member; sizing: WsSizing }) {
  const tr = useT();
  const onSlots = sizing.mem_meaning === "slot" && !!sizing.slots?.length;
  const cls = (sizing.slot_classes ?? []).find((c) => c.id === (m.slot_class || sizing.default_slot_class));
  const box = onSlots ? slotFor(ladderFor(sizing, m.slot_class ?? ""), m.mem_limit ? Math.round(m.mem_limit / 1048576) : 0) : null;

  const out: ReactNode[] = [];
  if (box) {
    out.push(
      <span key="box" className="mr-size mono" title={cls ? cls.label : undefined}>
        {box.instance_type}
      </span>,
      <span key="spec" className="mr-size muted">
        {box.vcpu ? tr("admin.roster_spec", { n: String(box.vcpu), mem: slotMemLabel(tr, box) }) : slotMemLabel(tr, box)}
      </span>,
    );
  } else {
    if (m.mem_limit) out.push(<span key="mem" className="mr-size muted">{fmtGbHint(Math.round(m.mem_limit / 1048576))}</span>);
    if (sizing.cpu_effective && m.cpu_limit)
      out.push(<span key="cpu" className="mr-size muted">{tr("admin.ws_cpu_vcpu", { n: String(m.cpu_limit / 1024) })}</span>);
  }
  if (m.disk_gb) out.push(<span key="disk" className="mr-size muted">{tr("admin.roster_disk", { n: String(m.disk_gb) })}</span>);
  if (m.max_sessions) out.push(<span key="s" className="mr-lim muted">s≤{m.max_sessions}</span>);
  return <>{out}</>;
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
