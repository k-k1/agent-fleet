// What an auto-run category says about itself beyond its settings (ADR 0137 §6): that it is paused
// (and why, with the button that resumes it), and that its workspace cannot run it by itself yet or cannot take the notes a tidy made.
// Drawn on the category and at the top of its run history, from the same live settings, so the two
// never disagree. The Control Plane's own sentence for the pause is English; the member reads the
// catalogue line, never it.
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import { kindLabel } from "../../lib/sessionkind.ts";
import type { MemoQueue } from "../../types/memo.ts";
import { Icon } from "../../ui/Icon.tsx";
import { waitingIsWarning, waitingWords } from "./queues.ts";

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
  // The notes a tidy made wait while the Agent cannot take them. Said whenever the Control Plane
  // says it, whether or not the switch is still on: they wait either way, and undoing the tidy is
  // what frees them.
  const noTidy = queue.noTidyAt !== "" || queue.waiting?.reason === "no_tidy";
  const why = waitingWords(queue, (iso) => fmtDateTime(iso), (kind) => kindLabel(kind));
  const warnWhy = waitingIsWarning(queue);
  if (!queue.paused && !noReport && !noRefs && !noTidy && !why) return null;
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
        <div className={warnWhy ? "memo-q-why memo-q-why-warn" : "memo-q-why"} role="status">
          {warnWhy && <Icon name="warning" />}
          <span>{tr(why.key, why.params)}</span>
        </div>
      )}
      {why && queue.waiting?.reason === "daily_cap" && (
        <div className="memo-q-why-hint">{tr("memo.waiting_cap_hint")}</div>
      )}
      {noRefs && (
        <div className="memo-q-noreport" role="status">
          {tr("memo.no_refs_notice")}
        </div>
      )}
      {noTidy && (
        <div className="memo-q-noreport" role="status">
          {tr("memo.no_tidy_notice")}
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
