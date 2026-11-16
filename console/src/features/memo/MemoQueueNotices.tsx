// What an auto-run category says about itself beyond its settings (ADR 0137 §6): that it is paused
// (and why, with the button that resumes it), and that its workspace cannot run it by itself yet.
// Drawn on the category and at the top of its run history, from the same live settings, so the two
// never disagree. The Control Plane's own sentence for the pause is English; the member reads the
// catalogue line, never it.
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { kindLabel } from "../../lib/sessionkind.ts";
import type { MemoQueue } from "../../types/memo.ts";
import { waitingWords } from "./queues.ts";

interface Props {
  queue: MemoQueue;
  busy: boolean;
  onResume: () => void;
}

export function MemoQueueNotices({ queue, busy, onResume }: Props) {
  const tr = useT();
  // A workspace that does not report is only worth a word while auto-run is on: with it off
  // nothing waits to run by itself.
  const noReport = queue.enabled && queue.noReportAt !== "";
  // Why nothing starts, for the reasons the two notices below do not already give.
  // Notes that point at a work item wait until the workspace is updated; the others run. Said
  // whenever the Control Plane says it (a set time or the waiting reason), enabled or not,
  // because pressing "Run now" tries again and fails the same way.
  const noRefs = queue.noRefsAt !== "" || queue.waiting?.reason === "no_references";
  const why = waitingWords(queue, (iso) => fmtDateTime(iso), (kind) => kindLabel(kind));
  if (!queue.paused && !noReport && !noRefs && !why) return null;
  return (
    <div className="memo-q-notices">
      {queue.paused && (
        <div className="memo-q-paused" role="status">
          <span>{tr("memo.paused_notice")}</span>
          <Button small variant="primary" disabled={busy} onClick={onResume}>
            {tr("memo.paused_resume")}
          </Button>
        </div>
      )}
      {why && (
        <div className="memo-q-why" role="status">
          {tr(why.key, why.params)}
        </div>
      )}
      {noRefs && (
        <div className="memo-q-noreport" role="status">
          {tr("memo.no_refs_notice")}
        </div>
      )}
      {noReport && (
        <div className="memo-q-noreport" role="status">
          {tr("memo.no_report_notice")}
        </div>
      )}
    </div>
  );
}
