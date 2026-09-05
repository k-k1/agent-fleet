// The operator surface for the EC2 slot pool (AF_RUNTIME=ecs-ec2). docs/log/64 §64.18.6,
// ADR 0045 decision 13.
//
// It answers the three questions only this runtime raises:
//   1. How many machines are being paid for right now (slot count, and how many are running).
//   2. Which ones are asleep (stopped, billed only for the root EBS).
//   3. Where each home is (on a slot, evacuated, or turned into a snapshot).
//
// Everything shown is derived from AWS on each read, not state the CP holds (ADR 0012), so
// restarting the CP never changes the picture.
//
// Other runtimes have no notion of a pool. An empty table would read as "every slot is gone" on
// a Fargate deployment, so there the whole tab is omitted (in AdminTab).
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { api, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";

export type PoolBudget = {
  max_slots: number;
  reserved_slots: number;
  capacity: number;
  allocated: number;
  unbounded_tenants?: string[];
  over: boolean;
};

/**
 * PoolBudgetHint is the single place that emits the warning above. Both the pool screen (as a
 * standing state) and the moment after a tenant limit is saved (about the number just typed)
 * use it, so there is exactly one wording.
 */
export function PoolBudgetHint({ budget }: { budget: PoolBudget }) {
  const tr = useT();
  const unbounded = budget.unbounded_tenants || [];
  return (
    <>
      {budget.over && (
        <p className="admin-hint warn-text">
          {tr("pool.budget_over", {
            allocated: String(budget.allocated),
            capacity: String(budget.capacity),
            max: String(budget.max_slots),
            reserved: String(budget.reserved_slots),
          })}
        </p>
      )}
      {unbounded.length > 0 && (
        <p className="admin-hint warn-text">
          {tr("pool.budget_unbounded", { tenants: unbounded.join(", ") })}
        </p>
      )}
      {/* State the difference in denominators next to the numbers. Without it an operator
          reads "if the total fits, nothing gets evicted" — evictions still happen. */}
      <p className="admin-hint">{tr("pool.budget_denominator")}</p>
    </>
  );
}

export type PoolStatus = {
  runtime: string;
  pool?: string;
  max_slots?: number;
  slot_sleep_sec?: number;
  slot_terminate_sec?: number;
  hibernate_after_sec?: number;
  /**
   * The sum of the tenant limits checked against the pool limit. Returned only when there is a
   * problem: a total that fits is not news, and both inputs are already on this screen.
   *
   * The two count different denominators. `allocated` is the number of Workspaces running *at
   * once*; `max_slots` is the number of boxes allowed to *exist*, and a stopped Workspace holds
   * a box while counting against no tenant's quota. Never merge them into one figure.
   */
  budget?: PoolBudget;
  slots?: Slot[];
  homes?: Home[];
  golden_id?: string;
  golden_image?: string;
  golden_stale?: boolean;
  baking?: boolean;
  bake_rejected?: string;
  bake_reason?: string;
  running_image?: string;
  /** The golden per declared architecture (docs/log/70 §70.6). The six fields above mirror the
   *  first element (the default class's arch); a single-class deployment has one element. */
  goldens?: Golden[];
  slot_classes?: { id: string; label: string; arch: string }[];
  /** Whether auto-baking is on (AF_ECS_EC2_GOLDEN_AUTOBAKE). The one value that cannot be
   *  derived from AWS; without it "not baked yet" and "never will be" look identical. */
  auto_bake?: boolean;
};
type Golden = {
  arch: string;
  snapshot_id?: string;
  image?: string;
  stale?: boolean;
  baking?: boolean;
  rejected?: string;
  reason?: string;
  /** How far the bake has got (docs/log/64 §64.30): the six BAKE_STEPS stages, plus the
   *  reasons for not being baked (idle / blocked / rejected / gave_up / off). */
  phase?: string;
  phase_since?: string;
  candidate?: string;
  progress?: number;
  attempts?: number;
  slots_in_use?: number;
  seed?: BakeWorkspace;
  probe?: BakeWorkspace;
};
/** The reserved workspace a bake stands up. It holds a slot, so unless it is shown the slot
 *  table lists an occupancy belonging to nobody. */
type BakeWorkspace = { workspace: string; volume_id?: string; instance_id?: string };
type Slot = {
  instance_id: string;
  instance_type: string;
  az: string;
  state: string;
  registered: boolean;
  workspace: string;
  idle_minutes: number;
  // A quarantined slot (decision 20): out of the pool, still being billed.
  quarantined?: boolean;
  quarantine_reason?: string;
};
type Home = {
  volume_id: string;
  workspace: string;
  size_gib: number;
  az: string;
  attached_to: string;
  idle_minutes: number;
  hibernating: boolean;
  backups?: number;
  backup_age_minutes?: number;
  snapshot_id: string;
  snapshot_state: string;
};

// Minutes rendered as 45 minutes / 3.2 hours / 12 days. Dormancy spans minutes to days, so a
// fixed unit would print unreadable figures like 43200.
type TR = ReturnType<typeof useT>;

function fmtIdle(min: number, tr: TR): string {
  if (min < 60) return tr("pool.idle_min", { n: String(min) });
  if (min < 60 * 48) return tr("pool.idle_hour", { n: (min / 60).toFixed(1) });
  return tr("pool.idle_day", { n: String(Math.round(min / 1440)) });
}

function fmtDuration(sec: number, tr: TR): string {
  if (sec <= 0) return tr("pool.off");
  return fmtIdle(Math.round(sec / 60), tr);
}

export function PoolView() {
  const tr = useT();
  const [st, setSt] = useState<PoolStatus | null>(null);
  const [err, setErr] = useState("");
  const timer = useRef<ReturnType<typeof setInterval> | undefined>(undefined);

  const poll = useCallback(async () => {
    try {
      const d = await api("api/admin/ec2-pool");
      if (d?.error) {
        setErr(errText(d.error));
        return;
      }
      setErr("");
      setSt(d);
    } catch {
      /* transient; keep the last picture rather than blanking the screen */
    }
  }, []);

  useEffect(() => {
    poll();
    timer.current = setInterval(() => {
      if (!document.hidden) poll();
    }, 10000);
    return () => clearInterval(timer.current);
  }, [poll]);

  if (err) return <p className="muted pad">{err}</p>;
  if (st === null) return <p className="muted pad">{tr("common.loading")}</p>;
  if (st.runtime !== "ecs-ec2") return <p className="muted pad">{tr("pool.not_ec2")}</p>;

  const slots = st.slots || [];
  const homes = st.homes || [];
  // Quarantined boxes are excluded from the pool counts: counting them towards the cap or the
  // free slots shows an operator "one free slot that nobody can enter". They stay in the table,
  // because they are still being billed.
  const pool = slots.filter((s) => !s.quarantined);
  const quarantined = slots.filter((s) => s.quarantined);
  const running = pool.filter((s) => s.state === "running").length;
  const asleep = pool.filter((s) => s.state === "stopped").length;
  const free = pool.filter((s) => !s.workspace).length;
  const atCap = st.max_slots != null && pool.length >= st.max_slots;
  // The reserved workspaces a bake stood up. In the slot and home tables they look like people,
  // so mark them as belonging to the golden and to nobody — and in a pool near its cap, the fact
  // that these two are occupied is itself something that needs explaining.
  const bakeWS = new Set(
    (st.goldens || []).flatMap((g) => [g.seed?.workspace, g.probe?.workspace].filter(Boolean) as string[]),
  );

  return (
    <div className="admin-stage pool-view">
      <section className="admin-panel">
        <h4>{tr("pool.slots_title")}</h4>
        <div className="res-tiles">
          <PoolTile label={tr("pool.provisioned")} value={`${pool.length}`} sub={tr("pool.of_max", { n: String(st.max_slots ?? 0) })} warn={atCap} />
          <PoolTile label={tr("pool.running")} value={`${running}`} sub={tr("pool.running_sub")} />
          <PoolTile label={tr("pool.asleep")} value={`${asleep}`} sub={tr("pool.asleep_sub")} />
          <PoolTile label={tr("pool.free")} value={`${free}`} sub={tr("pool.free_sub")} />
        </div>
        {/* At the cap, the next person's slot is taken from someone else. What an operator
            needs first is that evictions will happen, not that the pool stops growing. */}
        {atCap && <p className="admin-hint warn-text">{tr("pool.at_cap")}</p>}
        {/* Quarantine has to read as "this box was pulled out because it is unusable, and it
            is still being billed", not as the pool shrinking on its own. State the count, and
            that terminating it is the operator's job. */}
        {quarantined.length > 0 && (
          <p className="admin-hint warn-text">{tr("pool.quarantined_hint", { n: String(quarantined.length) })}</p>
        )}
        {/* Dropping "never" into the "evacuates after …" slot yields "after never", which is
            unreadable. 0 gets its own sentence — and since the default is off, that is the
            common case. */}
        <p className="admin-hint">
          {(st.hibernate_after_sec ?? 0) > 0
            ? tr("pool.timers", {
                sleep: fmtDuration(st.slot_sleep_sec ?? 0, tr),
                hibernate: fmtDuration(st.hibernate_after_sec ?? 0, tr),
              })
            : tr("pool.timers_no_hibernate", { sleep: fmtDuration(st.slot_sleep_sec ?? 0, tr) })}{" "}
          {/* "Never terminates" is a standing state rather than an event, so it is worth saying
              precisely when the timer is off: stopping ends the compute charge only, and the
              root volume keeps billing until the box is gone — which appears nowhere else on
              the screen (the same reason AutoBake is shown). */}
          {(st.slot_terminate_sec ?? 0) > 0
            ? tr("pool.timers_terminate", { terminate: fmtDuration(st.slot_terminate_sec ?? 0, tr) })
            : tr("pool.timers_no_terminate", { max: String(st.max_slots ?? 0) })}
        </p>
        {/* Whether the concurrency handed out to tenants fits into this many boxes. The server
            only includes it when there is a problem. */}
        {st.budget && <PoolBudgetHint budget={st.budget} />}
        {slots.length === 0 ? (
          <p className="muted">{tr("pool.no_slots")}</p>
        ) : (
          <table className="admin-table pool-table">
            <thead>
              <tr>
                <th>{tr("pool.col_instance")}</th>
                <th>{tr("pool.col_type")}</th>
                <th>{tr("pool.col_state")}</th>
                <th>{tr("pool.col_occupant")}</th>
                <th>{tr("pool.col_dormant")}</th>
                <th>{tr("pool.col_backup")}</th>
              </tr>
            </thead>
            <tbody>
              {slots.map((s) => (
                <tr key={s.instance_id}>
                  <td className="mono">{s.instance_id}</td>
                  <td className="mono">{s.instance_type}<span className="muted"> {s.az}</span></td>
                  <td>
                    <span className={"state-dot " + (s.quarantined ? "off" : s.state === "running" ? "on" : "off")} />
                    {s.quarantined ? (
                      <span className="warn-text" title={s.quarantine_reason || ""}>{tr("pool.state_quarantined")}</span>
                    ) : s.state === "stopped" ? (
                      tr("pool.state_asleep")
                    ) : (
                      s.state
                    )}
                    {!s.quarantined && s.state === "running" && !s.registered && (
                      <span className="muted"> {tr("pool.not_registered")}</span>
                    )}
                  </td>
                  <td className="mono">
                    {s.workspace || <span className="muted">{tr("pool.free_slot")}</span>}
                    {bakeWS.has(s.workspace) && <span className="pool-badge bake">{tr("pool.bake_owner")}</span>}
                  </td>
                  <td>{s.workspace ? fmtIdle(s.idle_minutes, tr) : "–"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="admin-panel">
        <h4>{tr("pool.homes_title")}</h4>
        {homes.length === 0 ? (
          <p className="muted">{tr("pool.no_homes")}</p>
        ) : (
          <table className="admin-table pool-table">
            <thead>
              <tr>
                <th>{tr("pool.col_workspace")}</th>
                <th>{tr("pool.col_volume")}</th>
                <th>{tr("pool.col_where")}</th>
                <th>{tr("pool.col_dormant")}</th>
              </tr>
            </thead>
            <tbody>
              {homes.map((h) => (
                <tr key={h.volume_id || h.workspace}>
                  <td className="mono">
                    {h.workspace}
                    {bakeWS.has(h.workspace) && <span className="pool-badge bake">{tr("pool.bake_owner")}</span>}
                  </td>
                  <td className="mono">
                    {h.volume_id ? `${h.volume_id} (${h.size_gib} GiB)` : <span className="muted">{tr("pool.no_volume")}</span>}
                  </td>
                  <td>
                    {h.snapshot_id && !h.volume_id ? (
                      <span className="pool-badge hib"><Icon name="archive" /> {tr("pool.hibernated")}</span>
                    ) : h.hibernating ? (
                      <span className="pool-badge hib"><Icon name="archive" /> {tr("pool.hibernating", { state: h.snapshot_state || "…" })}</span>
                    ) : h.attached_to ? (
                      <span className="mono">{h.attached_to}</span>
                    ) : (
                      <span className="muted">{tr("pool.detached")}</span>
                    )}
                  </td>
                  <td>{h.volume_id && h.idle_minutes > 0 ? fmtIdle(h.idle_minutes, tr) : "–"}</td>
                  {/* "No backup" and "taken a moment ago" are opposite answers and must not
                      collapse into the same blank cell. An evacuated home is itself the
                      snapshot, so it is out of scope here. */}
                  <td>
                    {!h.volume_id ? (
                      "–"
                    ) : (h.backup_age_minutes ?? -1) >= 0 ? (
                      <span title={tr("pool.backup_count", { n: h.backups ?? 0 })}>
                        {fmtIdle(h.backup_age_minutes ?? 0, tr)}
                      </span>
                    ) : (
                      <span className="warn-text">{tr("pool.backup_none")}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="admin-panel">
        <h4>{tr("pool.golden_title")}</h4>
        {/* A golden is a home full of binaries, so it differs per architecture
            (docs/log/70 §70.6). On a deployment declaring several, showing only one reads as
            "the golden exists" while new users on the un-baked class start from an empty home
            every time — a failure nobody but they can see. */}
        {st.goldens?.length ? (
          <>
            {st.goldens.map((g) => (
              <GoldenBake key={g.arch} g={g} st={st} showArch={st.goldens!.length > 1} />
            ))}
            {/* "So what is handed out meanwhile" is said once. Repeating it per architecture
                buries the value worth reading (which stage the bake is at) in boilerplate. */}
            {st.goldens.some((g) => BAKE_STEPS.indexOf(g.phase || "") >= 0 && g.phase !== "published") && (
              <p className="admin-hint">{tr("pool.bake_meanwhile")}</p>
            )}
          </>
        ) : st.bake_rejected ? (
          // A rejection is a state, not an event: when a baked golden fails to boot the only
          // symptom is a restart loop, and the one CP log line scrolls away (§64.28.3). Keep it
          // on this surface until it is fixed.
          <p className="warn-text">
            {tr("pool.golden_rejected", { snapshot: st.bake_rejected, reason: st.bake_reason || "?" })}
          </p>
        ) : st.baking ? (
          <p className="muted">{tr("pool.golden_baking", { image: st.running_image || "" })}</p>
        ) : !st.golden_id ? (
          <p className="muted">{tr("pool.golden_none", { image: st.running_image || "" })}</p>
        ) : st.golden_stale ? (
          // Missed, this leaves new users silently starting on an old CLI, so say what is
          // happening rather than just "they do not match".
          <p className="warn-text">
            {tr("pool.golden_stale", { snapshot: st.golden_id, baked: st.golden_image || "?", running: st.running_image || "?" })}
          </p>
        ) : (
          <p>
            <span className="mono">{st.golden_id}</span>{" "}
            <span className="muted">{tr("pool.golden_ok", { image: st.golden_image || "" })}</span>
          </p>
        )}
      </section>

    </div>
  );
}

// The six bake stages, in the CP's ec2BakePhase* order and under the same names. This is the
// only place that shows the bake is progressing: it takes around 11 minutes, and through the
// first half (seed boot, boot-install, releasing the slot) no snapshot exists yet — so a
// surface without these stages tells an operator investigating slow first starts that there is
// no golden, the opposite of what is happening.
const BAKE_STEPS = ["seed", "boot", "capture", "snapshot", "probe", "published"];
const STEP_LABEL: Record<string, MsgKey> = {
  seed: "pool.bake_step_seed",
  boot: "pool.bake_step_boot",
  capture: "pool.bake_step_capture",
  snapshot: "pool.bake_step_snapshot",
  probe: "pool.bake_step_probe",
  published: "pool.bake_step_published",
};

// GoldenBake is one architecture's golden: what is published, and — when nothing is —
// either how far the bake has got or why there is no bake. The four "no bake" answers
// are deliberately different sentences: only one of them (idle) fixes itself, and the
// other three previously existed solely as a CP log line that scrolls away.
function GoldenBake({ g, st, showArch }: { g: Golden; st: PoolStatus; showArch: boolean }) {
  const tr = useT();
  const running = st.running_image || "";
  const phase = g.phase || "";
  const at = BAKE_STEPS.indexOf(phase);

  return (
    <div className="golden-arch">
      {showArch && <span className="golden-arch-name mono">{g.arch}</span>}
      {/* A stale golden still being in place is a fact separate from how the re-bake is going.
          Even mid-bake, say first that what is handed out right now is the old one — missed,
          new users silently start on an old CLI. */}
      {g.stale && (
        <p className="warn-text">
          {tr("pool.golden_stale", { snapshot: g.snapshot_id || "?", baked: g.image || "?", running: running || "?" })}
        </p>
      )}
      {phase === "published" ? (
        <p>
          <span className="mono">{g.snapshot_id}</span>{" "}
          <span className="muted">{tr("pool.golden_ok", { image: g.image || "" })}</span>
        </p>
      ) : at >= 0 ? (
        <>
          <p className="golden-head">
            <span className="state-dot on" />
            {tr("pool.bake_running", { image: running })}
            {g.phase_since && <span className="muted"> {fmtElapsed(g.phase_since, tr)}</span>}
          </p>
          <BakeSteps at={at} />
          <BakeDetail g={g} />
        </>
      ) : phase === "off" ? (
        <p className="muted">{tr("pool.bake_off")}</p>
      ) : phase === "blocked" ? (
        // This is what stops a bake on a real deployment. The brake works as intended; the
        // problem was that it working showed up only as a single log line.
        <p className="warn-text">
          {tr("pool.bake_blocked", { used: String(g.slots_in_use ?? 0), max: String(st.max_slots ?? 0) })}
        </p>
      ) : phase === "gave_up" ? (
        <p className="warn-text">
          {tr("pool.bake_gave_up", { snapshot: g.rejected || "?", reason: g.reason || "?" })}
        </p>
      ) : g.rejected ? (
        // A rejection is a state, not an event: when a baked golden fails to boot the only
        // symptom is a restart loop, and the one CP log line scrolls away (§64.28.3).
        <p className="warn-text">
          {tr("pool.golden_rejected", { snapshot: g.rejected, reason: g.reason || "?" })}{" "}
          {tr("pool.bake_retry_left")}
        </p>
      ) : (
        <p className="muted">{tr("pool.golden_none", { image: running })}</p>
      )}
    </div>
  );
}

// BakeSteps is the progress line. Steps already passed are filled, the current one is
// marked, the rest are muted — the question it answers is "is this moving", which a
// single status word cannot answer at all when one step takes 3 minutes and the next
// takes 90 seconds.
function BakeSteps({ at }: { at: number }) {
  const tr = useT();
  return (
    <ol className="bake-steps">
      {BAKE_STEPS.map((s, i) => (
        <li key={s} className={"bake-step" + (i < at ? " done" : i === at ? " now" : "")}>
          <span className="bake-dot" />
          <span className="bake-label">{tr(STEP_LABEL[s])}</span>
        </li>
      ))}
    </ol>
  );
}

// BakeDetail is the one line that says what is actually being waited on: the copy
// percentage, or which workspace is holding a slot. The reserved workspaces are on the
// screen because they occupy slots — without them the slot table shows a box taken by
// af-ws-af-golden-… that nothing else on the page accounts for.
function BakeDetail({ g }: { g: Golden }) {
  const tr = useT();
  const at = BAKE_STEPS.indexOf(g.phase || "");
  const parts: ReactNode[] = [];
  if (g.phase === "snapshot" && g.candidate) {
    parts.push(
      <span key="cand" className="mono">
        {g.candidate}
        {g.progress ? ` ${g.progress}%` : ""}
      </span>,
    );
  }
  if (g.phase === "probe") {
    parts.push(<span key="verify">{tr("pool.bake_detail_probe", { snapshot: g.candidate || "?" })}</span>);
  }
  // The seed is shown only while it holds a slot. From the snapshot stage on the box is back,
  // and the remaining home appears in the volume table, marked as belonging to the bake.
  if (g.seed && at >= 0 && at <= BAKE_STEPS.indexOf("capture")) {
    parts.push(<BakeWS key="seed" label={tr("pool.bake_detail_seed")} ws={g.seed} />);
  }
  if (g.probe) parts.push(<BakeWS key="probe" label={tr("pool.bake_detail_probe_ws")} ws={g.probe} />);
  if (!parts.length) return null;
  return <p className="bake-detail muted">{parts}</p>;
}

function BakeWS({ label, ws }: { label: string; ws: BakeWorkspace }) {
  return (
    <span>
      {label} <span className="mono">{ws.workspace}</span>
      {ws.instance_id && <span className="mono"> ({ws.instance_id})</span>}
    </span>
  );
}

// Bake elapsed time spans orders of magnitude (12 seconds, 4m12s, 1h03m). Fixed to minutes,
// everything just after the start reads "0 minutes", which cannot distinguish moving from stuck.
function fmtElapsed(since: string, tr: TR): string {
  const started = Date.parse(since);
  if (!Number.isFinite(started)) return "";
  const sec = Math.max(0, Math.round((Date.now() - started) / 1000));
  if (sec < 60) return tr("pool.elapsed_sec", { s: String(sec) });
  if (sec < 3600) return tr("pool.elapsed_min", { m: String(Math.floor(sec / 60)), s: String(sec % 60) });
  return tr("pool.elapsed_hour", { h: String(Math.floor(sec / 3600)), m: String(Math.floor((sec % 3600) / 60)) });
}

function PoolTile({ label, value, sub, warn }: { label: string; value: string; sub: string; warn?: boolean }) {
  return (
    <div className={"res-tile" + (warn ? " warn" : "")}>
      <div className="rt-label">{label}</div>
      <div className="rt-value">{value}</div>
      <div className="rt-sub muted">{sub}</div>
    </div>
  );
}
