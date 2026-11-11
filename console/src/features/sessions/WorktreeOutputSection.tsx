// WorktreeOutputSection — the cleanup modal's stage for dropping regenerable build output
// (node_modules, .next, target, .gradle) from dormant worktrees (#3800, Agent side #3202).
//
// The only delete in the modal that is neither bundled to the trash nor selectable with the
// rest, so it is built to be hard to fire by accident:
//   - nothing is surveyed until the person asks (the Agent walks the disks);
//   - the real POST is reachable only from a row that holds a dry-run Report listing
//     candidates, and only after a confirm that says it cannot be undone;
//   - one delete at a time, locked synchronously (a ref, not state) so a double click or a
//     second row cannot start another before the first answers;
//   - a refusal (live session, dirty, in use, ...) is shown as its own reason, not as a
//     generic failure, and a delete whose answer was lost is reported as "outcome unknown".
import { useRef, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT, tMaybe } from "../../lib/i18n/index.ts";
import { humanSize } from "../../lib/filemeta.ts";
import {
  applyWorktreeOutput,
  loadSurvey,
  sortReports,
  wtoGrade,
  type WtoDir,
  type WtoReport,
  type WtoSurvey,
} from "./worktreeOutput.ts";

type RowState =
  | { phase: "idle" }
  | { phase: "previewing" }
  | { phase: "preview"; report: WtoReport } // a dry run that lists candidates: the only way to a delete
  | { phase: "deleting"; report: WtoReport }
  | { phase: "done"; report: WtoReport }
  | { phase: "blocked"; report: WtoReport } // refused, or nothing to drop
  | { phase: "failed"; message: string; unknown: boolean }; // unknown = a delete whose answer never came

type Load = "idle" | "loading" | "unsupported" | "failed" | "ready";

export function WorktreeOutputSection({ onChanged }: { onChanged?: () => void }) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const [load, setLoad] = useState<Load>("idle");
  const [survey, setSurvey] = useState<WtoSurvey | null>(null);
  const [rows, setRows] = useState<Record<string, RowState>>({});
  const [showRest, setShowRest] = useState(false);
  // Set synchronously: state would still read "free" for a second click in the same tick.
  const lock = useRef(false);
  const [locked, setLocked] = useState(false);
  const setLock = (v: boolean) => {
    lock.current = v;
    setLocked(v);
  };

  const put = (name: string, s: RowState) => setRows((p) => ({ ...p, [name]: s }));

  const runSurvey = async () => {
    if (lock.current) return;
    setLock(true);
    setLoad("loading");
    try {
      const res = await loadSurvey();
      if (res.kind === "ok") {
        setSurvey(res.survey);
        setRows({});
        setLoad("ready");
      } else {
        setLoad(res.kind === "unsupported" ? "unsupported" : "failed");
      }
    } finally {
      setLock(false);
    }
  };

  const preview = async (name: string) => {
    if (lock.current) return;
    setLock(true);
    put(name, { phase: "previewing" });
    try {
      const res = await applyWorktreeOutput(name, true);
      if (res.kind === "error") put(name, { phase: "failed", message: res.message, unknown: false });
      else if (res.report.status === "candidate") put(name, { phase: "preview", report: res.report });
      else put(name, { phase: "blocked", report: res.report });
    } finally {
      setLock(false);
    }
  };

  const drop = async (name: string, planned: WtoReport) => {
    if (lock.current) return;
    setLock(true);
    try {
      const ok = await askConfirm({
        title: tr("clean.wto_confirm_title", { name }),
        body: tr("clean.wto_confirm_body", {
          count: planned.dirs?.length ?? 0,
          size: humanSize(planned.bytes) + (planned.truncated ? "+" : ""),
        }),
        confirmLabel: tr("clean.wto_confirm_do"),
        danger: true,
      });
      if (!ok) return;
      put(name, { phase: "deleting", report: planned });
      const res = await applyWorktreeOutput(name, false);
      if (res.kind === "error") {
        // The request may have got through before the answer was lost: say so rather than
        // offering the same delete again from a plan that is no longer true.
        put(name, { phase: "failed", message: res.message, unknown: true });
        return;
      }
      if (res.report.status === "refused") {
        put(name, { phase: "blocked", report: res.report });
        return;
      }
      put(name, { phase: "done", report: res.report });
      toast(tr("clean.wto_done", { name, size: humanSize(res.report.bytes) }));
      onChanged?.();
    } finally {
      setLock(false);
    }
  };

  const reasonText = (r: WtoReport) => {
    if (r.status === "empty") return tr("clean.wto_nothing");
    const known = r.reason ? tMaybe("clean.wto_reason_" + r.reason) : undefined;
    return known ?? tr("clean.wto_reason_unknown");
  };

  const dirList = (dirs: WtoDir[] | undefined) => (
    <ul className="wto-dirs">
      {(dirs || []).map((d) => (
        <li key={d.rel}>
          <code>{d.rel}</code>
          <span className="clean-size">
            {humanSize(d.bytes)}
            {d.link ? tr("clean.wto_link") : ""}
          </span>
        </li>
      ))}
    </ul>
  );

  const skipped = (r: WtoReport) =>
    r.skipped && r.skipped.length > 0 ? (
      <div className="wto-note">
        {tr("clean.wto_skipped")} {r.skipped.join("; ")}
      </div>
    ) : null;

  const renderRow = (r: WtoReport) => {
    const st: RowState = rows[r.name] ?? { phase: "idle" };
    const grade = wtoGrade(r);
    return (
      <li key={r.name} className={"wto-row" + (grade === "keep" ? " is-keep" : "")} data-name={r.name}>
        <div className="wto-head">
          <span className={"clean-badge clean-badge-" + grade}>{tMaybe("clean.safety_" + grade)}</span>
          <span className="clean-target" title={r.path}>
            {r.name}
          </span>
          {r.status === "candidate" && (
            <span className="clean-size">
              {humanSize(r.bytes)}
              {r.truncated ? tr("clean.cache_partial") : ""}
            </span>
          )}
          <span className="wto-spacer" />
          {r.status === "candidate" && (st.phase === "idle" || st.phase === "previewing" || st.phase === "failed") && (
            <Button small disabled={locked} onClick={() => void preview(r.name)}>
              {tr("clean.wto_preview")}
            </Button>
          )}
        </div>
        {r.status === "candidate" && st.phase === "idle" && dirList(r.dirs)}
        {r.status !== "candidate" && (
          <p className="wto-note" title={r.detail}>
            {reasonText(r)}
            {r.reason === "live_session" && r.detail ? ` (${r.detail})` : ""}
          </p>
        )}
        {st.phase === "previewing" && <p className="wto-note">{tr("clean.wto_previewing")}</p>}
        {(st.phase === "preview" || st.phase === "deleting") && (
          <div className="wto-plan">
            <p className="wto-note">{tr("clean.wto_plan", { size: humanSize(st.report.bytes) })}</p>
            {dirList(st.report.dirs)}
            {skipped(st.report)}
            <p className="wto-warn">{tr("clean.wto_irreversible")}</p>
            <Button
              small
              variant="danger"
              disabled={locked}
              onClick={() => void drop(r.name, st.report)}
            >
              {st.phase === "deleting" ? tr("clean.wto_deleting") : tr("clean.wto_delete")}
            </Button>
          </div>
        )}
        {st.phase === "done" && (
          <div className="wto-plan">
            <p className="wto-note">{tr("clean.wto_done", { name: r.name, size: humanSize(st.report.bytes) })}</p>
            {dirList(st.report.removed)}
            {skipped(st.report)}
          </div>
        )}
        {st.phase === "blocked" && (
          <div className="wto-note wto-refused" role="status" title={st.report.detail}>
            {r.status === "candidate" ? tr("clean.wto_refused_now") + " " : ""}
            {reasonText(st.report)}
            {st.report.reason === "live_session" && st.report.detail ? ` (${st.report.detail})` : ""}
            {skipped(st.report)}
          </div>
        )}
        {st.phase === "failed" && (
          <p className="wto-note wto-refused" role="alert">
            {st.unknown ? tr("clean.wto_unknown") : tr("clean.wto_failed")}
            {st.message ? ` ${st.message}` : ""}
          </p>
        )}
      </li>
    );
  };

  const sorted = survey ? sortReports(survey.worktrees) : [];
  const candidates = sorted.filter((r) => r.status === "candidate");
  const rest = sorted.filter((r) => r.status !== "candidate");

  return (
    <section className="clean-stage" data-testid="wto-stage">
      <div className="clean-stage-head">
        <span className="clean-stage-title">{tr("clean.wto_title")}</span>
        <span className="clean-toolbar-spacer" />
        <Button small disabled={locked} title={tr("clean.wto_survey_title")} onClick={() => void runSurvey()}>
          {load === "loading" ? tr("clean.wto_surveying") : survey ? tr("clean.wto_resurvey") : tr("clean.wto_survey")}
        </Button>
      </div>
      <p className="clean-stage-empty">{tr("clean.wto_intro")}</p>
      {load === "unsupported" && <p className="clean-stage-empty">{tr("clean.wto_unsupported")}</p>}
      {load === "failed" && <p className="clean-stage-empty">{tr("clean.wto_survey_failed")}</p>}
      {load === "ready" && survey && (
        <>
          {candidates.length === 0 && <p className="clean-stage-empty">{tr("clean.wto_none", { days: survey.dormant_days })}</p>}
          {candidates.length > 0 && <ul className="clean-list wto-list">{candidates.map(renderRow)}</ul>}
          {rest.length > 0 && (
            <>
              <Button small variant="ghost" onClick={() => setShowRest(!showRest)}>
                {showRest ? tr("clean.wto_hide_rest") : tr("clean.wto_show_rest", { count: rest.length })}
              </Button>
              {showRest && <ul className="clean-list wto-list">{rest.map(renderRow)}</ul>}
            </>
          )}
        </>
      )}
    </section>
  );
}
