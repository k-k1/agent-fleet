// JobList — the queue as group rows (ADR 0081 decisions 8 and 12).
//
// A group of forty is ONE row. The bar's segments and the estimate are `jobs.ts`'s
// arithmetic, not this file's: the running segment fills by time against `typical_ms` and
// stops at 95 %, so the bar never claims a completion that has not been seen.
//
// Which verb is which unit is the part that is easy to get wrong and impossible to undo:
// pause / resume / skip / abort are GROUP operations (the group is what the person
// submitted), and only the per-job ✕ cancels one picture.
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { IconButton } from "../../../ui/Button.tsx";
import type { GroupOp, Job } from "../wire.ts";
import { jobElapsedMs } from "../wire.ts";
import { barSegments, etaBucket, etaMs, type JobRow } from "../jobs.ts";

const STATE_KEY = {
  queued: "imggen.state_queued",
  waking: "imggen.state_waking",
  uploading: "imggen.state_uploading",
  running: "imggen.state_running",
  fetching: "imggen.state_fetching",
  done: "imggen.state_done",
  failed: "imggen.state_failed",
  cancelled: "imggen.state_cancelled",
} as const;

interface Props {
  rows: JobRow[];
  queuePaused: boolean;
  /** The Agent's cap and how much of it is used. 0 = an Agent that does not report one. */
  queued: number;
  queueMax: number;
  now: number;
  onGroupOp: (id: string, op: GroupOp) => void;
  onQueueOp: (op: "pause" | "resume") => void;
  onCancelJob: (id: string) => void;
}

export function JobList({ rows, queuePaused, queued, queueMax, now, onGroupOp, onQueueOp, onCancelJob }: Props) {
  const tr = useT();
  return (
    <section className="igen-queue">
      <header className="igen-queue-head">
        <h3>{tr("imggen.queue")}</h3>
        {queueMax > 0 && <span className="igen-hint">{tr("imggen.queue_cap", { n: queued, max: queueMax })}</span>}
        {queuePaused && <span className="igen-badge paused">{tr("imggen.queue_paused")}</span>}
        <span className="igen-spacer" />
        <button
          type="button"
          className="ui-btn ui-btn-ghost"
          onClick={() => onQueueOp(queuePaused ? "resume" : "pause")}
        >
          <Icon name={queuePaused ? "debug-continue" : "debug-pause"} />
          {queuePaused ? tr("imggen.resume_all") : tr("imggen.pause_all")}
        </button>
      </header>
      {rows.length === 0 ? (
        <p className="igen-hint">{tr("imggen.queue_empty")}</p>
      ) : (
        rows.map((row) => (
          <QueueRow key={row.key} row={row} now={now} onGroupOp={onGroupOp} onCancelJob={onCancelJob} />
        ))
      )}
    </section>
  );
}

function QueueRow({
  row,
  now,
  onGroupOp,
  onCancelJob,
}: {
  row: JobRow;
  now: number;
  onGroupOp: (id: string, op: GroupOp) => void;
  onCancelJob: (id: string) => void;
}) {
  const tr = useT();
  const seg = barSegments(row, now);
  const eta = etaBucket(etaMs(row));
  const running = row.running;
  const gid = row.group?.id || null;
  const paused = row.group?.state === "paused";
  const finished = row.done + row.failed + row.cancelled >= row.total && !running;
  const pausedMin = row.group?.paused_at ? Math.floor((now - Date.parse(row.group.paused_at)) / 60_000) : 0;

  return (
    <div className={"igen-qrow" + (paused ? " paused" : "")}>
      <div className="igen-qrow-head">
        {row.trial && <span className="igen-badge trial">{tr("imggen.trial_row")}</span>}
        <span className="igen-qrow-label">{row.label || row.jobs[0]?.model || row.key}</span>
        <span className="igen-qrow-counts">{tr("imggen.group_counts", { done: row.done, total: row.total })}</span>
        {row.failed > 0 && <span className="igen-badge failed">{tr("imggen.group_failed", { n: row.failed })}</span>}
      </div>
      <div className="igen-bar" role="progressbar" aria-valuemin={0} aria-valuemax={row.total} aria-valuenow={row.done}>
        <span className="igen-bar-done" style={{ width: `${seg.done * 100}%` }} />
        <span className="igen-bar-failed" style={{ width: `${seg.failed * 100}%` }} />
        <span className="igen-bar-running" style={{ width: `${seg.running * 100}%` }} />
      </div>
      <div className="igen-qrow-foot">
        {!finished && (
          <span className="igen-eta">
            {eta.unit === "min"
              ? tr("imggen.eta_min", { n: eta.value })
              : eta.unit === "sec"
                ? tr("imggen.eta_sec", { n: eta.value })
                : etaMs(row) == null
                  ? tr("imggen.eta_unknown")
                  : tr("imggen.eta_soon")}
          </span>
        )}
        {running && (
          <span className="igen-phase">
            {/* A running job carries no elapsed_ms on the wire — jobElapsedMs subtracts
                started_at instead (lane A, deviation 1). */}
            {tr("imggen.running_now", {
              phase: tr(STATE_KEY[running.state]),
              sec: Math.round((jobElapsedMs(running, now) ?? 0) / 1000),
            })}
          </span>
        )}
        {paused && pausedMin > 0 && <span className="igen-hint">{tr("imggen.paused_since", { min: pausedMin })}</span>}
        <span className="igen-spacer" />
        {gid && !finished && (
          <>
            <button type="button" className="ui-btn ui-btn-ghost" onClick={() => onGroupOp(gid, paused ? "resume" : "pause")}>
              {paused ? tr("imggen.resume") : tr("imggen.pause")}
            </button>
            <button type="button" className="ui-btn ui-btn-ghost" onClick={() => onGroupOp(gid, "skip")}>
              {tr("imggen.skip")}
            </button>
            <button type="button" className="ui-btn ui-btn-ghost igen-abort" onClick={() => onGroupOp(gid, "cancel")}>
              {tr("imggen.abort")}
            </button>
          </>
        )}
      </div>
      <ul className="igen-jobs">
        {row.jobs.slice(0, 8).map((j) => (
          <JobLine key={j.id} job={j} onCancel={() => onCancelJob(j.id)} />
        ))}
      </ul>
    </div>
  );
}

function JobLine({ job, onCancel }: { job: Job; onCancel: () => void }) {
  const tr = useT();
  const live = job.state !== "done" && job.state !== "failed" && job.state !== "cancelled";
  return (
    <li className={"igen-job igen-job-" + job.state}>
      <span className="igen-job-state">{tr(STATE_KEY[job.state])}</span>
      {job.position != null && job.state === "queued" && <span className="igen-job-pos">#{job.position}</span>}
      {job.seed != null && <span className="igen-job-seed">seed {job.seed}</span>}
      {job.error && <span className="igen-err">{job.error}</span>}
      {(job.warnings || []).map((w) => (
        <span className="igen-warn" key={w}>
          {w}
        </span>
      ))}
      <span className="igen-spacer" />
      {live && <IconButton icon="close" label={tr("imggen.cancel_job")} onClick={onCancel} />}
    </li>
  );
}
