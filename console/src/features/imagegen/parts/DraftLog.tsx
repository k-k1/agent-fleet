// DraftLog — the studio's edit history (ADR 0100 decision 9): who changed which field, each
// press as a version, and "back to this point" on every line that carries a whole draft.
//
// A press_result is never a row of its own: it is the outcome of a press, drawn on the press's
// row. Only the FIRST one per version counts (versionsOf).
import { useMemo } from "react";
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import type { DraftLogEntry } from "../wire.ts";
import { describeChange, rewindable, versionsOf } from "../studioSync.ts";

const hhmm = (at: string): string => {
  const d = new Date(at);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
};

export function DraftLog({
  log,
  recordPending,
  hasOlder,
  onOlder,
  onRewind,
}: {
  log: DraftLogEntry[];
  recordPending: ReadonlySet<string>;
  hasOlder: boolean;
  onOlder: () => void;
  onRewind: (seq: number) => void;
}) {
  const tr = useT();
  const versions = useMemo(() => new Map(versionsOf(log).map((v) => [v.version, v])), [log]);
  const rows = log.filter((e) => e.kind !== "press_result");
  return (
    <section className="igen-log" aria-label={tr("imggen.log_title")}>
      {rows.length === 0 ? (
        <p className="igen-hint">{tr("imggen.log_none")}</p>
      ) : (
        <ol className="igen-log-list">
          {rows.map((e) => {
            const v = e.kind === "press" && e.version ? versions.get(e.version) : undefined;
            const pending = !!(e.version && recordPending.has(e.version) && v?.state === "pending");
            return (
              <li key={e.seq} className={"igen-log-row igen-log-" + e.kind} data-seq={e.seq}>
                <div className="igen-log-head">
                  <span className="igen-log-seq">#{e.seq}</span>
                  <span className="igen-log-time">{hhmm(e.at)}</span>
                  <span className={"igen-log-who " + (e.author || "")}>
                    {e.kind === "rewind"
                      ? tr("imggen.log_rewind", { n: e.rewind_to ?? "?" })
                      : e.kind === "press"
                        ? tr(`imggen.log_press_${e.mode || "trial"}` as "imggen.log_press_trial")
                        : tr(e.author === "agent" ? "imggen.log_by_agent" : "imggen.log_by_human")}
                  </span>
                  {v && (
                    <span className={"igen-log-state " + (pending ? "record_pending" : v.state)} title={v.error || undefined}>
                      {tr(pending ? "imggen.version_record_pending" : (`imggen.version_${v.state}` as "imggen.version_ok"))}
                    </span>
                  )}
                  {rewindable(e) && (
                    <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm igen-log-rewind" onClick={() => onRewind(e.seq)}>
                      <Icon name="discard" /> {tr("imggen.log_rewind_here")}
                    </button>
                  )}
                </div>
                {e.kind !== "press" && (e.changes || []).length > 0 && (
                  <ul className="igen-log-changes">
                    {(e.changes || []).flatMap(describeChange).map((line, i) => (
                      <li key={i}>{line}</li>
                    ))}
                  </ul>
                )}
              </li>
            );
          })}
        </ol>
      )}
      {hasOlder && (
        <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" onClick={onOlder}>
          {tr("imggen.log_older")}
        </button>
      )}
    </section>
  );
}
