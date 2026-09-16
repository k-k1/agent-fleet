// Engine indicator (ADR 0084 decision 10/11) — one pill per role (llm/`chat`, image/`images`;
// at most two), each with a per-row popover. Lives beside the TTS pill in TopBar's
// `topbar-right`.
//
// `EnginesPillView` takes `rows` as a prop rather than reading the store itself, so it can be
// mounted and asserted on directly in a DOM test without a store or a mocked `api()` — the
// same split `imagegen/parts/JobList.tsx` uses. `EnginesPill` (the default export TopBar
// renders) is the thin store-connected wrapper.
import { useEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";
import { useEnginesStore, startEnginesPolling } from "./store.ts";
import {
  ROLE_ORDER,
  groupByRole,
  pickHeadRow,
  queueIsCertain,
  showsColdHint,
  splitHM,
  stateWord,
  stopSecs,
  totalQueue,
  type EngineMemberRow,
  type EngineRole,
  type EngineStateWord,
} from "./wire.ts";
import "./engines.css";

const STATE_KEY: Record<EngineStateWord, MsgKey> = {
  ready: "engine.state_ready",
  running: "engine.state_running",
  starting: "engine.state_starting",
  stopping: "engine.state_stopping",
  stopped: "engine.state_stopped",
  available: "engine.state_available",
};

// Badge/dot color class per state word. Shared by the pill and the popover's per-row badge.
const STATE_TONE: Record<EngineStateWord, string> = {
  ready: "on",
  running: "on",
  starting: "lead",
  stopping: "warn",
  stopped: "off",
  available: "avail",
};

const ROLE_LABEL_KEY: Record<EngineRole, MsgKey> = {
  chat: "engine.role_chat",
  images: "engine.role_images",
};

const ROLE_ICON: Record<EngineRole, string> = {
  chat: "comment-discussion",
  images: "wand",
};

/** A clock that ticks only while there is a countdown to redraw — same reasoning and the
 *  same 15s (figures round to whole minutes, so a 1s tick would just repaint identical text)
 *  as the admin panel's `useSecondHand` (`adminEngines.tsx`), duplicated rather than shared
 *  for the bundle-isolation reason `wire.ts`'s `secsUntil` states. Torn down — not just
 *  idle — the moment `active` goes false, so a deployment with nothing counting runs no
 *  timer at all (decision 2's "数える物が無くなれば interval ごと畳む"). */
const ENGINE_TICK_MS = 15_000;
function useEngineTick(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), ENGINE_TICK_MS);
    return () => clearInterval(id);
  }, [active]);
  return now;
}

function useEngineDur(): (secs: number) => string {
  const tr = useT();
  return (secs: number) => {
    const { hours, mins } = splitHM(secs);
    return hours > 0 ? tr("engine.dur_hm", { h: hours, m: mins }) : tr("engine.dur_m", { m: mins });
  };
}

/** Compact local stamp for the popover's stop time — no seconds, they carry no meaning for a
 *  minutes-away countdown. Same shape as the admin panel's `localStamp`, duplicated for the
 *  same bundle-isolation reason. */
function localStamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** Store-connected pill group — what TopBar renders. Starts the REST-fallback poll once per
 *  mount (TopBar mounts exactly once for the app's lifetime, same as the account menu). */
export function EnginesPill() {
  const rows = useEnginesStore((s) => s.rows);
  useEffect(() => startEnginesPolling(), []);
  return <EnginesPillView rows={rows || []} />;
}

/** Pure presentational half — no store, no fetch. */
export function EnginesPillView({ rows }: { rows: EngineMemberRow[] }) {
  const groups = groupByRole(rows);
  return (
    <>
      {ROLE_ORDER.filter((role) => groups.has(role)).map((role) => (
        <EngineRolePill key={role} role={role} rows={groups.get(role)!} />
      ))}
    </>
  );
}

function EngineRolePill({ role, rows }: { role: EngineRole; rows: EngineMemberRow[] }) {
  const tr = useT();
  const dur = useEngineDur();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDismiss(ref, open, () => setOpen(false));

  const head = pickHeadRow(rows);
  const word = stateWord(head);
  const tone = STATE_TONE[word];
  const roleLabel = tr(ROLE_LABEL_KEY[role]);
  const stateLabel = tr(STATE_KEY[word]);

  // One shared clock for the whole popover: the header's countdown (the head row's stop_eta
  // only — decision 11, "stop_eta は畳まない") and every row's own line below all read off the
  // same `now`, torn down together the moment nothing in this role is counting down.
  const anyCountdown = rows.some((r) => !r.lifecycle && !!r.stop_eta);
  const now = useEngineTick(anyCountdown);
  const headLeftSecs = stopSecs(head, now);
  const queued = totalQueue(rows);

  const parts = [roleLabel, stateLabel];
  if (headLeftSecs !== null && headLeftSecs > 0) parts.push(tr("engine.stops_in", { d: dur(headLeftSecs) }));
  if (queued !== undefined) parts.push(tr("engine.queue_shared", { n: queued }));
  const summary = parts.join(tr("ui.sep"));

  return (
    <div className="engine-pill-wrap" ref={ref}>
      <button
        type="button"
        className={"engine-pill engine-pill-" + tone}
        title={summary}
        aria-label={summary}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={ROLE_ICON[role]} />
        <span className={"engine-pill-dot engine-pill-dot-" + tone} aria-hidden="true" />
        <span className="engine-pill-state">
          {stateLabel}
          {rows.length > 1 && <span className="engine-pill-count">×{rows.length}</span>}
        </span>
        {headLeftSecs !== null && headLeftSecs > 0 && (
          <span className="engine-pill-countdown">{tr("engine.stops_in", { d: dur(headLeftSecs) })}</span>
        )}
        {queued !== undefined && <span className="engine-pill-queue">{tr("engine.queue_shared", { n: queued })}</span>}
      </button>
      {open && (
        <div className="engine-popover" role="dialog" aria-label={roleLabel}>
          <div className="engine-popover-head">{roleLabel}</div>
          {rows.map((row) => (
            <EngineRowLine key={row.key} row={row} now={now} dur={dur} />
          ))}
        </div>
      )}
    </div>
  );
}

function EngineRowLine({ row, now, dur }: { row: EngineMemberRow; now: number; dur: (secs: number) => string }) {
  const tr = useT();
  const word = stateWord(row);
  const tone = STATE_TONE[word];
  const stateLabel = tr(STATE_KEY[word]);
  const leftSecs = stopSecs(row, now);
  const queued = row.queue && queueIsCertain(row.queue) ? row.queue.count : undefined;

  return (
    <div className="engine-row">
      <div className="engine-row-head">
        <span className="engine-row-key mono">{row.key}</span>
        <span className={"engine-row-state engine-row-state-" + tone}>{stateLabel}</span>
        {row.lifecycle && (
          <span
            className="engine-row-lifecycle"
            title={tr(row.lifecycle === "remote" ? "engine.lifecycle_remote_hint" : "engine.lifecycle_external_hint")}
          >
            {tr(row.lifecycle === "remote" ? "engine.lifecycle_remote" : "engine.lifecycle_external")}
          </span>
        )}
      </div>
      {/* stop_eta absent => no countdown line at all (decision 4) — never a fallback. */}
      {leftSecs !== null && leftSecs > 0 && (
        <div className="engine-row-line">
          {tr("engine.stops_at", { t: localStamp(row.stop_eta!) })}
          {tr("ui.sep")}
          {tr("engine.stops_in", { d: dur(leftSecs) })}
        </div>
      )}
      {/* Guarded on !lifecycle too, same as the countdown (stopSecs): decision 4's "ピルは
          『利用可』とだけ言う" is categorical for an external/remote row, so this does not
          trust the wire to have already omitted idle_secs for one. */}
      {!row.lifecycle && row.idle_secs !== undefined && (
        <div className="engine-row-line muted">{tr("engine.idle_policy", { d: dur(row.idle_secs) })}</div>
      )}
      {showsColdHint(row) && <div className="engine-row-line muted">{tr("engine.cold_hint")}</div>}
      {queued !== undefined && <div className="engine-row-line">{tr("engine.queue_shared", { n: queued })}</div>}
    </div>
  );
}
