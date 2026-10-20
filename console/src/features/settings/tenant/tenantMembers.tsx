// The tenant members view (roster, state chips, add). The per-member detail is read for a
// different reason and lives in tenantMemberDetail.tsx.
//
// The offboarding set (remove the member, stop the workspace, clean home) belongs to the
// tenant_admin (docs/log/61 §61.10.6, decision 26), so it must be reachable from here and not
// only from the admin modal.
//
// isSuper only decides whether to show the operations that mean something to a deployment
// admin alone (granting and revoking the role). PUT /api/admin/membership-role is fixed at
// withSuperAdmin, so hiding the button here is presentation only.
import { useCallback, useEffect, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api, rawJSON } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
// Cloud cost in the member detail (docs/log/67 §67.15). On a deployment without billing the
// component renders nothing itself, so no condition is kept here.
import { useT } from "../../../lib/i18n/index.ts";
import type { MsgKey } from "../../../lib/i18n/index.ts";
import { elapsedShort, remainingShort } from "../../../lib/sessionview.ts";
import { fmtGbHint, ladderFor, memberClassID, slotFor, slotMemLabel, WS_SIZING_FALLBACK } from "../parts/adminShared.ts";
import type { Member, MemberAutoStop, MemberIdle, WsSizing } from "../parts/adminShared.ts";
import { startDeadlineBody } from "../../notifications/wording.ts";
import {
  attentionReasons,
  matchesFilter,
  matchesQuery,
  memberTitle,
  ROSTER_FILTERS,
  rosterCounts,
  rosterState,
  sortMembers,
} from "./memberRoster.ts";
import type { RosterFilter, RosterSort, RosterState } from "./memberRoster.ts";

// MembersPanel — the roster and "add member", as one component so the tenant settings modal
// and the admin modal use the same implementation.
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
  // the runtime has said what the numbers mean (ADR 0045 decision 21). Same fetch the member
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
        <MemberGrid members={members} sizing={sizing} onOpenMember={onOpenMember} />
      )}
      <AddMember slug={slug} isSuper={isSuper} onAdded={loadMembers} />
    </section>
  );
}

const FILTER_LABEL: Record<RosterFilter, MsgKey> = {
  all: "admin.roster_filter_all",
  running: "admin.roster_filter_running",
  attention: "admin.roster_filter_attention",
  stopped: "admin.roster_filter_stopped",
  removed: "admin.roster_filter_removed",
};

// MemberGrid — counts, filter, sort and search over the roster, then one card per member.
// Everything works on the single GET …/members response: tenants have dozens of members, and a
// card that fetched its own stats would fan out one Agent call each on every render.
function MemberGrid({
  members,
  sizing,
  onOpenMember,
}: {
  members: Member[];
  sizing: WsSizing;
  onOpenMember: (m: Member) => void;
}) {
  const tr = useT();
  const [filter, setFilter] = useState<RosterFilter>("all");
  const [sort, setSort] = useState<RosterSort>("attention");
  const [query, setQuery] = useState("");
  const now = Date.now();
  const counts = rosterCounts(members, now);
  const shown = sortMembers(
    members.filter((m) => matchesFilter(m, filter, now) && matchesQuery(m, query)),
    sort,
    now,
  );
  return (
    <>
      <p className="member-summary">
        <span>{tr("admin.roster_summary", { running: String(counts.running), total: String(counts.total) })}</span>
        {counts.attention > 0 && (
          <span className="ms-attention">
            <Icon name="warning" />
            {tr("admin.roster_summary_attention", { n: String(counts.attention) })}
          </span>
        )}
      </p>
      <div className="member-tools">
        <div className="ui-seg member-filter" role="group" aria-label={tr("admin.roster_filter_label")}>
          {ROSTER_FILTERS.map((f) => (
            <button
              key={f}
              type="button"
              className={"seg-btn" + (f === filter ? " active" : "")}
              aria-pressed={f === filter}
              onClick={() => setFilter(f)}
            >
              <span>
                {tr(FILTER_LABEL[f])} <span className="seg-count">{counts.byFilter[f]}</span>
              </span>
            </button>
          ))}
        </div>
        <select aria-label={tr("admin.roster_sort_label")} value={sort} onChange={(e) => setSort(e.target.value as RosterSort)}>
          <option value="attention">{tr("admin.roster_sort_attention")}</option>
          <option value="name">{tr("admin.roster_sort_name")}</option>
        </select>
        <input
          type="search"
          className="member-search"
          aria-label={tr("admin.roster_search_ph")}
          placeholder={tr("admin.roster_search_ph")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>
      {shown.length === 0 ? (
        <p className="muted">{tr("admin.roster_no_match")}</p>
      ) : (
        <div className="member-grid">
          {shown.map((m) => (
            <MemberCard key={m.user_key} m={m} sizing={sizing} now={now} onOpen={() => onOpenMember(m)} />
          ))}
        </div>
      )}
    </>
  );
}

function MemberCard({ m, sizing, now, onOpen }: { m: Member; sizing: WsSizing; now: number; onOpen: () => void }) {
  const tr = useT();
  const reasons = attentionReasons(m, now);
  const title = memberTitle(m);
  return (
    <button type="button" className={"member-card" + (reasons.length ? " attn" : "")} onClick={onOpen}>
      <span className="mc-head">
        <span className="mc-name">
          <span className="mc-title" title={title}>
            {title}
          </span>
          {m.email && (
            <span className="mc-key mono muted" title={m.user_key}>
              {m.user_key}
            </span>
          )}
        </span>
        <MemberStateBadge state={rosterState(m)} />
      </span>
      <span className="mc-meta">
        <span className="mr-role">{m.status === "removed" ? tr("admin.member_removed") : m.role}</span>
        {m.super_admin && <Icon name="star-full" className="mr-star" title="super_admin" />}
        {reasons.length > 0 && (
          <span
            className="mc-attn"
            title={reasons.map((r) => tr(r === "auto_stop" ? "admin.roster_attn_auto_stop" : "admin.roster_attn_hold_lapsed")).join(" / ")}
          >
            <Icon name="warning" />
            {tr("admin.roster_attention")}
          </span>
        )}
        <Icon name="chevron-right" className="mr-go" />
      </span>
      <span className="mc-foot">
        <MemberIdleChip idle={m.idle} state={m.state} />
        <MemberAutoStopChip autoStop={m.auto_stop} />
        <MemberSizeChips m={m} sizing={sizing} />
      </span>
    </button>
  );
}

const STATE_VIEW: Record<RosterState, { icon: string; label: MsgKey }> = {
  running: { icon: "pass-filled", label: "admin.roster_state_running" },
  starting: { icon: "sync", label: "admin.roster_state_starting" },
  stopped: { icon: "circle-large-outline", label: "admin.roster_state_stopped" },
  never: { icon: "circle-slash", label: "admin.roster_state_never" },
  auto_stopped: { icon: "warning", label: "admin.roster_state_auto_stopped" },
  removed: { icon: "trash", label: "admin.roster_state_removed" },
};

// MemberStateBadge — the workspace state as a word with an icon. The 8 px dot it replaces was
// colour alone, and in the light theme running (--ok) and stopped (--muted2) measured 1.13:1
// against each other, so both read as "dark dot". The word carries the meaning; the icon shape
// and the colour only reinforce it, which is what keeps it readable in greyscale.
function MemberStateBadge({ state }: { state: RosterState }) {
  const tr = useT();
  const v = STATE_VIEW[state];
  return (
    <span className={"member-state ms-" + state}>
      <Icon name={v.icon} spin={state === "starting"} />
      {tr(v.label)}
    </span>
  );
}

// MemberIdleChip — the auto-stop outlook (docs/log/75 P4).
//
// It is on the roster because when auto-stop does not fire, an operator otherwise sees nothing:
// the reaper only logs, and the only way to investigate was to docker exec into someone else's
// container and read the status file. The scheduled stop and whatever is holding it open belong
// in the same place — "20 minutes left" and "session s5 is running so it will not stop" answer
// the same question, and split apart, a missing schedule reads as breakage.
//
// Never recompute here. A local derivation would drift from what the reaper actually looks at
// (presence, pins, background work, the share watermark), and the screen used to diagnose the
// problem would give a different answer. Show the reaper's last observation verbatim.
function MemberIdleChip({ idle, state }: { idle?: MemberIdle; state?: string }) {
  const tr = useT();
  // A workspace that is not running has no scheduled stop (the CP only sends one for running).
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
              : h.kind === "imagejob"
                ? tr("admin.idle_hold_imagejob")
                : tr("admin.idle_hold_watching");
    // Beyond the first, only a count: a roster card keeps it to one line. Details are in the
    // member detail view.
    const more = holders.length > 1 ? tr("admin.idle_hold_more", { n: String(holders.length - 1) }) : "";
    return (
      <span className="mr-idle hold" title={holdersTitle(holders, tr)}>
        {(h.session ? `${label} (${h.session}${workingFor(h, tr)})` : label) + more}
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

// ", working for 21h" — how long a "working" holder has been in that state, so a session stuck
// for a day reads differently from one that started a turn a minute ago (#1819). "" for any
// other holder, or when the Agent did not report when the state began.
function workingFor(h: { kind: string; since?: string }, tr: ReturnType<typeof useT>): string {
  if (h.kind !== "working") return "";
  const f = elapsedShort(h.since);
  return f ? tr("admin.idle_hold_for", { for: f }) : "";
}

function holdersTitle(holders: NonNullable<MemberIdle["holders"]>, tr: (k: never, p?: never) => string): string {
  return holders.map((h) => (h.session ? `${h.kind}: ${h.session}` : h.kind)).join(" / ");
}

// The detail row's tail for a "working" holder: how long it has been working and when the hold
// lapses if the session stays frozen. Either part is left out when the Agent did not report it.
function workingDetail(h: { since?: string; lapseAt?: string }, tr: ReturnType<typeof useT>): string {
  const f = elapsedShort(h.since);
  const left = remainingShort(h.lapseAt);
  return (f ? tr("admin.idle_hold_for_row", { for: f }) : "") + (left ? tr("admin.idle_hold_lapse_row", { left }) : "");
}

// The same for a "background" holder: how long the background work has been running and when the
// hold ends (background_hold_max), the latter left out when the bound is off.
function backgroundDetail(h: { since?: string; lapseAt?: string }, tr: ReturnType<typeof useT>): string {
  const f = elapsedShort(h.since);
  const left = remainingShort(h.lapseAt);
  return (f ? tr("admin.idle_hold_bg_for_row", { for: f }) : "") + (left ? tr("admin.idle_hold_bg_lapse_row", { left }) : "");
}

// MemberIdleDetail — the auto-stop outlook in the member detail.
//
// The same reaper observation as MemberIdleChip, unfolded from one line. Always show the
// observation time: it lags by up to one sweep interval (60s by default), and stating it to the
// second produces "the screen still said 3 minutes and it stopped".
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
                    ? tr("admin.idle_hold_working_row", { session: h.session ?? "" }) + workingDetail(h, tr)
                    : h.kind === "background"
                      ? tr("admin.idle_hold_background_row", { session: h.session ?? "" }) + backgroundDetail(h, tr)
                      : h.kind === "repojob"
                        ? tr("admin.idle_hold_repojob_row")
                        : h.kind === "imagejob"
                          ? tr("admin.idle_hold_imagejob_row")
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

// MemberAutoStopChip — the workspace is down because the Control Plane stopped it, not
// because anybody asked (#1384). Without it the roster reads plain "stopped", and the member's
// notification is the only record of why. Warning colour: nothing restarts it, and the
// member may be waiting on the admin to fix what blocked the launch.
function MemberAutoStopChip({ autoStop }: { autoStop?: MemberAutoStop }) {
  const tr = useT();
  if (!autoStop) return null;
  return (
    <span className="mr-idle hold" title={autoStopReason(autoStop)}>
      {autoStopTitle(autoStop, tr)}
    </span>
  );
}

// The kinds the CP writes: "start-deadline" (start_deadline.go) and "home-wipe-failed", a
// member's Recreate / Clean home that failed after its request was answered (ecs, #1537).
// An unknown kind is shown raw rather than hidden: a newer CP may write one.
function autoStopTitle(a: MemberAutoStop, tr: ReturnType<typeof useT>): string {
  if (a.kind === "start-deadline") return tr("noti.kind_start_deadline");
  if (a.kind === "home-wipe-failed") return tr("admin.auto_stop_home_wipe");
  return a.kind;
}

function autoStopReason(a: MemberAutoStop): string {
  return a.kind === "start-deadline" ? startDeadlineBody(a.limit_minutes, a.phase) : a.phase;
}

// MemberAutoStopDetail — the same record in the member detail, with the raw phase in full
// (on ecs-ec2 it is the ECS sentence naming the constraint the admin has to fix) and when.
export function MemberAutoStopDetail({ autoStop }: { autoStop?: MemberAutoStop }) {
  const tr = useT();
  if (!autoStop) return null;
  return (
    <section className="admin-panel">
      <h4>{autoStopTitle(autoStop, tr)}</h4>
      <p>{autoStopReason(autoStop)}</p>
      <p className="admin-hint">
        {/* Only the start deadline writes the member a notification; a failed home wipe reaches
            them as a toast in a Console that happens to be open, so it claims nothing. */}
        {tr(autoStop.kind === "home-wipe-failed" ? "admin.auto_stop_home_wipe_at" : "admin.auto_stop_at", {
          at: new Date(autoStop.stopped_at).toLocaleString(),
        })}
      </p>
    </section>
  );
}

// MemberSizeChips — what this member is sized to.
//
// Exported because the member detail's "Size and limits" card states the same thing above
// its edit button. Two renderings of one fact drift, and this one already carries the
// three rules below that make the fact readable at all.
//
// It shows the BOX on a slot runtime and the NUMBERS everywhere else, because those
// are different statements. On ecs-ec2 the memory figure is a requirement that picks a
// machine and the person then gets the whole thing, so "m6i.large · 2 vCPU / 8 GiB" is
// the true answer and "8192 MB" is a half-truth. On docker/Fargate the number IS the
// cap, and there is no box to name.
//
// The CPU chip follows the same rule the member detail uses: when cpu_effective is
// false the axis never reaches the backend, so showing it would put a number on screen
// that does nothing. It is omitted rather than greyed out.
//
// Everything here is "unset → say nothing". A roster of rows all reading "0" teaches
// people to stop reading the column.
export function MemberSizeChips({ m, sizing }: { m: Member; sizing: WsSizing }) {
  const tr = useT();
  const onSlots = sizing.mem_meaning === "slot" && !!sizing.slots?.length;
  const classID = memberClassID(m);
  const cls = (sizing.slot_classes ?? []).find((c) => c.id === (classID || sizing.default_slot_class));
  const box = onSlots ? slotFor(ladderFor(sizing, classID), m.mem_limit ? Math.round(m.mem_limit / 1048576) : 0) : null;

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
      <h5 className="admin-subhead">{tr("admin.add_member")}</h5>
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
