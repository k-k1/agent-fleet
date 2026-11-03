import { useEffect, useState } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr, tCount } from "../../../lib/i18n/index.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import type { BackgroundShell } from "../../../core/api/client.ts";

/** parseBackgroundShells keeps the well-formed entries of a messages payload's `backgroundShells`. */
export function parseBackgroundShells(v: unknown): BackgroundShell[] {
  if (!Array.isArray(v)) return [];
  return v.filter(
    (s): s is BackgroundShell =>
      !!s &&
      typeof s === "object" &&
      typeof (s as BackgroundShell).pid === "number" &&
      typeof (s as BackgroundShell).start === "number" &&
      typeof (s as BackgroundShell).command === "string",
  );
}

// A shell that has waited this long while using almost no CPU is probably waiting for
// something that will never come (measured in #3279: a bare `cat` blocked on stdin for 2 h).
// "Probably": a `sleep 3600` looks the same, so the panel only flags it and never acts on it.
const STUCK_AFTER_MS = 30 * 60 * 1000;
// The panel's own clock. A hung shell answers every poll with identical bytes, and the poll
// does not re-apply an unchanged payload, so without it the age and the stuck flag would stop
// where they were first drawn.
const CLOCK_MS = 30 * 1000;
const STUCK_CPU_SEC = 1;

/** ageMs is how long the shell has run, or null without a usable start time. */
export function ageMs(s: BackgroundShell, now: number): number | null {
  const at = Date.parse(s.startedAt ?? "");
  return Number.isFinite(at) ? Math.max(0, now - at) : null;
}

/** mayBeStuck: waiting (nothing on a CPU) for 30 min or more with under 1 s of CPU in total. */
export function mayBeStuck(s: BackgroundShell, now: number): boolean {
  const age = ageMs(s, now);
  return !s.running && age !== null && age >= STUCK_AFTER_MS && s.cpuSec < STUCK_CPU_SEC;
}

/** elapsedShort folds a duration into "45s" / "12m" / "1h20m". */
export function elapsedShort(ms: number): string {
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  const m = min % 60;
  return m ? `${h}h${m}m` : `${h}h`;
}

/** BackgroundShellsPanel lists the shells a claude session left running after its turn, just
 *  above the composer, and stops one on request (#3279). The typing row's stop button is an
 *  Esc into the pane, which ends a turn but never a background shell, so this is the only way
 *  to end one from the Console. */
export function BackgroundShellsPanel({
  shells,
  stopping,
  onStop,
  now: fixedNow,
}: {
  shells: BackgroundShell[];
  stopping: ReadonlySet<number>;
  onStop: (s: BackgroundShell) => void;
  /** Pins the clock (tests). */
  now?: number;
}) {
  const askConfirm = useConfirm();
  const [tick, setTick] = useState(() => Date.now());
  const has = shells.length > 0;
  useEffect(() => {
    if (!has || fixedNow !== undefined) return;
    setTick(Date.now());
    const id = setInterval(() => setTick(Date.now()), CLOCK_MS);
    return () => clearInterval(id);
  }, [has, fixedNow]);
  if (!has) return null;
  const now = fixedNow ?? tick;
  return (
    <div className="mirror-bg-shells" role="status">
      <div className="mbs-head">
        <Icon name="terminal" />
        <span className="mbs-msg">{tCount("mirror.bg_shells", shells.length)}</span>
      </div>
      <ul className="mbs-items">
        {shells.map((s) => {
          const age = ageMs(s, now);
          const stuck = mayBeStuck(s, now);
          const busy = stopping.has(s.pid);
          return (
            <li className={"mbs-item" + (stuck ? " stuck" : "")} key={`${s.pid}-${s.start}`}>
              <code className="mbs-cmd" title={s.command}>
                {s.command || `pid ${s.pid}`}
              </code>
              <span className="mbs-meta">
                {age !== null && <span className="mbs-age">{elapsedShort(age)}</span>}
                <span className="mbs-state">
                  {stuck ? tr("mirror.bg_shell_stuck") : s.running ? tr("mirror.bg_shell_running") : tr("mirror.bg_shell_waiting")}
                </span>
              </span>
              <button
                type="button"
                className="ghost mbs-stop"
                disabled={busy}
                title={tr("mirror.bg_shell_stop_title")}
                onClick={() =>
                  void (async () => {
                    // The command goes in a scrollable, wrapping block: it can be arbitrarily long.
                    const ok = await askConfirm({
                      title: tr("mirror.bg_shell_stop_confirm_title"),
                      body: (
                        <>
                          <pre className="ui-confirm-code">{s.command || `pid ${s.pid}`}</pre>
                          <p>{tr("mirror.bg_shell_stop_confirm_body")}</p>
                        </>
                      ),
                      confirmLabel: tr("mirror.bg_shell_stop"),
                      danger: true,
                    });
                    if (ok) onStop(s);
                  })()
                }
              >
                {busy ? tr("mirror.bg_shell_stopping") : tr("mirror.bg_shell_stop")}
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
