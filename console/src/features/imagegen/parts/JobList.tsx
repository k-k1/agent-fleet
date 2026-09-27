// JobList — the queue as group rows (ADR 0081 decisions 8 and 12).
//
// A group of forty is ONE row. The bar's segments and the estimate are `jobs.ts`'s
// arithmetic, not this file's: the running segment fills by time against `typical_ms` and
// stops at 95 %, so the bar never claims a completion that has not been seen.
//
// Which verb is which unit is the part that is easy to get wrong and impossible to undo:
// pause / resume / skip / abort are GROUP operations (the group is what the person
// submitted), and only the per-job ✕ cancels one picture.
//
// The queue is the workspace's, shared by every studio (ADR 0100 decision 6). Given the pane's
// studio, rows pressed elsewhere fold under one closed line: the member still sees why their
// own batch waits, without taking another studio's batch for theirs.
//
// Finished rows fold too. The Agent keeps the last 500 finished jobs on the list, so without the
// fold every trial ever pressed stayed in the queue as a "done 1/1" row, pushing what is still
// waiting out of sight; the pictures themselves are in the results below.
import { useT } from "../../../lib/i18n/index.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { IconButton } from "../../../ui/Button.tsx";
import type { GroupOp, Job } from "../wire.ts";
import { jobElapsedMs } from "../wire.ts";
import { anyLive, barSegments, etaBucket, etaMs, rowFinished, splitRows, type JobRow } from "../jobs.ts";

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
  /** The pane's studio. Absent: every row is shown as the pane's own. */
  studioId?: string;
}

export function JobList({ rows, queuePaused, queued, queueMax, now, onGroupOp, onQueueOp, onCancelJob, studioId }: Props) {
  const tr = useT();
  const { own, other } = studioId ? splitRows(rows, studioId) : { own: rows, other: [] };
  const otherLive = other.filter((r) => anyLive(r.jobs)).length;
  const waiting = own.filter((r) => !rowFinished(r));
  const finished = own.filter(rowFinished);
  const finishedFailed = finished.filter((r) => r.failed > 0).length;
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
          title={tr("imggen.queue_all_hint")}
          onClick={() => onQueueOp(queuePaused ? "resume" : "pause")}
        >
          <Icon name={queuePaused ? "debug-continue" : "debug-pause"} />
          {queuePaused ? tr("imggen.resume_all") : tr("imggen.pause_all")}
        </button>
      </header>
      {waiting.length === 0 ? (
        <p className="igen-hint">{tr("imggen.queue_empty")}</p>
      ) : (
        waiting.map((row) => (
          <QueueRow key={row.key} row={row} now={now} onGroupOp={onGroupOp} onCancelJob={onCancelJob} />
        ))
      )}
      {finished.length > 0 && (
        <details className="igen-queue-done">
          <summary>
            {finishedFailed
              ? tr("imggen.queue_done_failed", { n: finished.length, failed: finishedFailed })
              : tr("imggen.queue_done", { n: finished.length })}
          </summary>
          {finished.map((row) => (
            <QueueRow key={row.key} row={row} now={now} onGroupOp={onGroupOp} onCancelJob={onCancelJob} />
          ))}
        </details>
      )}
      {other.length > 0 && (
        <details className="igen-queue-others">
          <summary>
            {otherLive
              ? tr("imggen.queue_others_live", { n: other.length, live: otherLive })
              : tr("imggen.queue_others", { n: other.length })}
          </summary>
          {other.map((row) => (
            <QueueRow key={row.key} row={row} now={now} onGroupOp={onGroupOp} onCancelJob={onCancelJob} other />
          ))}
        </details>
      )}
    </section>
  );
}

function QueueRow({
  row,
  now,
  onGroupOp,
  onCancelJob,
  other = false,
}: {
  row: JobRow;
  now: number;
  onGroupOp: (id: string, op: GroupOp) => void;
  onCancelJob: (id: string) => void;
  /** Pressed in another studio, or outside any (a session's generate_image). */
  other?: boolean;
}) {
  const tr = useT();
  const seg = barSegments(row, now);
  const eta = etaBucket(etaMs(row));
  const running = row.running;
  const gid = row.group?.id || null;
  const paused = row.group?.state === "paused";
  const finished = rowFinished(row);
  const pausedMin = row.group?.paused_at ? Math.floor((now - Date.parse(row.group.paused_at)) / 60_000) : 0;

  return (
    <div className={"igen-qrow" + (paused ? " paused" : "") + (other ? " other" : "")}>
      <div className="igen-qrow-head">
        {other && (
          <span className="igen-badge other">
            {row.jobs[0]?.studio ? tr("imggen.queue_other_studio") : tr("imggen.queue_no_studio")}
          </span>
        )}
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
