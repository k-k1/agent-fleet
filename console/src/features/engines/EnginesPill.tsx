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
  lastUsedSecs,
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
  in_use: "engine.state_in_use",
  ready: "engine.state_ready",
  running: "engine.state_running",
  starting: "engine.state_starting",
  stopping: "engine.state_stopping",
  stopped: "engine.state_stopped",
  available: "engine.state_available",
};

// Badge/dot color class per state word. Shared by the pill and the popover's per-row badge.
const STATE_TONE: Record<EngineStateWord, string> = {
  // Its own tone rather than `on`'s: "warm" and "warm and answering somebody" are the two states
  // a member most needs to tell apart at a glance, and they are the two that look alike.
  in_use: "busy",
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
  const single = rows.length === 1;
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
  // The head row's loaded model rides in the tooltip and the accessible name, not in the pill
  // itself: it is the longest string here and the topbar is where the brand already wraps to two
  // lines (topbar.css). Below 760px the pill is an icon and a dot, so this is the only place a
  // phone can read it from at all.
  const headModel = head.warm_model_label || head.warm_model;
  if (headModel) parts.push(tr("engine.warm_model") + headModel);
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
          {/* With one row — what nearly every deployment has — the row's own head would repeat
              this line: the engine KEY is an operator's word (it names a row in the admin panel,
              nothing a member can press), and "チャット" already says which engine this is. So the
              state badge moves up here and the row is detail lines only. With two or more rows the
              key is the ONLY thing that tells them apart (decision 11), so it stays on each. */}
          <div className="engine-popover-head">
            <span>{roleLabel}</span>
            {single && (
              <span className="engine-popover-head-state">
                <span className={"engine-row-state engine-row-state-" + tone}>{stateLabel}</span>
                <LifecycleBadge row={head} />
              </span>
            )}
          </div>
          {rows.map((row) => (
            <EngineRowLine key={row.key} row={row} now={now} dur={dur} showHead={!single} />
          ))}
        </div>
      )}
    </div>
  );
}

/** The "this deployment does not manage it" badge, with the hover text that says what that
 *  means. Drawn either on a row's own head or — when the role has a single row — up in the
 *  popover's header beside the state, so the two sites cannot word it differently. */
function LifecycleBadge({ row }: { row: EngineMemberRow }) {
  const tr = useT();
  if (!row.lifecycle) return null;
  return (
    <span
      className="engine-row-lifecycle"
      title={tr(row.lifecycle === "remote" ? "engine.lifecycle_remote_hint" : "engine.lifecycle_external_hint")}
    >
      {tr(row.lifecycle === "remote" ? "engine.lifecycle_remote" : "engine.lifecycle_external")}
    </span>
  );
}

function EngineRowLine({
  row,
  now,
  dur,
  showHead,
}: {
  row: EngineMemberRow;
  now: number;
  dur: (secs: number) => string;
  showHead: boolean;
}) {
  const tr = useT();
  const word = stateWord(row);
  const tone = STATE_TONE[word];
  const stateLabel = tr(STATE_KEY[word]);
  const leftSecs = stopSecs(row, now);
  const queued = row.queue && queueIsCertain(row.queue) ? row.queue.count : undefined;
  // How long ago somebody last used it — the fact `warm` cannot carry, because it stays true for
  // the whole idle window after the last turn. Suppressed while the row IS in use: "in use" and
  // "last used a minute ago" are the same sentence, and the first one is the true one.
  const sinceSecs = word === "in_use" ? null : lastUsedSecs(row, now);

  return (
    <div className="engine-row">
      {showHead && (
        <div className="engine-row-head">
          <span className="engine-row-key mono">{row.key}</span>
          <span className={"engine-row-state engine-row-state-" + tone}>{stateLabel}</span>
          <LifecycleBadge row={row} />
        </div>
      )}
      {/* Which model is in VRAM right now. The label when the catalogue has one, the id when it
          does not (ADR 0090 決定 2: the id is the key, and every reader falls back to it) — mono
          only in the fallback, because an id is a key to compare character by character and a
          name is a name. */}
      {row.warm_model && (
        <div className="engine-row-line">
          {tr("engine.warm_model")}
          <span className={row.warm_model_label ? undefined : "mono"}>
            {row.warm_model_label || row.warm_model}
          </span>
        </div>
      )}
      {sinceSecs !== null && (
        <div className="engine-row-line muted">{tr("engine.last_used", { d: dur(sinceSecs) })}</div>
      )}
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
