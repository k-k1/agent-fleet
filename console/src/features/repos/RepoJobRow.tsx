// RepoJobRow — one repository-import row (docs/log/78). Sits at the head of the repo list,
// the same width as a working-copy row.
//
// Why it is shown as a row: the Agent keeps an importing folder out of the list, so nobody
// can launch into a half-made working copy or run `svn status` on it. Showing nothing then
// reads as "clone did nothing", so the progress itself belongs here. The OUTCOME goes in the
// same place, and a failure or an interruption stays until it is acknowledged — a toast that
// is missed leaves nothing behind but the half-made folder.
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useRepoJobsStore, isRepoJobRunning, type RepoJob } from "./jobs.ts";

export function RepoJobRow({ job }: { job: RepoJob }) {
  const tr = useT();
  const remove = useRepoJobsStore((s) => s.remove);
  const askConfirm = useConfirm();
  const running = isRepoJobRunning(job);
  const svn = job.kind === "svn";

  const title = running
    ? tr(svn ? "pj.checking_out" : "pj.cloning", { name: job.name })
    : job.state === "done"
      ? tr(svn ? "pj.checked_out" : "pj.cloned", { name: job.name })
      : job.state === "canceled"
        ? tr("pj.job_canceled", { name: job.name })
        : job.state === "interrupted"
          ? tr("pj.job_interrupted", { name: job.name })
          : tr("pj.job_failed", { name: job.name });

  // What a leftover working copy is worth differs per VCS: svn resumes from where it stopped
  // on a refresh, while a git clone cannot be resumed (its debris is already gone). Telling
  // both the same thing would give the wrong instruction.
  const kept = !running && job.state !== "done" && job.kept;

  // Cancelling asks for confirmation. An import is measured in tens of minutes to hours
  // (an 11.4GB working copy), and one mis-click starts all of it over. What is lost differs
  // per VCS, so the prompt says which: svn keeps what it has and resumes on a refresh, a git
  // clone cannot be resumed.
  const onClick = async () => {
    if (running && !(await askConfirm({
      title: tr("pj.job_cancel_confirm_title", { name: job.name }),
      body: tr(svn ? "pj.job_cancel_confirm_svn" : "pj.job_cancel_confirm_git"),
      confirmLabel: tr("pj.job_cancel"),
      danger: true,
    }))) {
      return;
    }
    await remove(job.id);
  };

  return (
    <li className={"repo-job" + (running ? " running" : " settled")}>
      <div className="repo-job-head">
        {running ? (
          <Icon name="loading" spin />
        ) : job.state === "done" ? (
          <Icon name="check" />
        ) : (
          <Icon name="warning" />
        )}
        <span className="repo-job-title">{title}</span>
        <IconButton
          icon={running ? "stop-circle" : "close"}
          label={running ? tr("pj.job_cancel") : tr("pj.job_dismiss")}
          onClick={() => void onClick()}
        />
      </div>
      {running && (job.items || job.progress) && (
        <div className="repo-job-progress" title={job.progress}>
          {job.items ? tr("pj.job_items", { n: String(job.items) }) : ""}
          {job.items && job.progress ? " · " : ""}
          <span className="repo-job-line">{job.progress}</span>
        </div>
      )}
      {!running && job.error && <div className="repo-job-error">{job.error}</div>}
      {kept && <div className="repo-job-hint">{tr(svn ? "pj.job_kept_svn" : "pj.job_kept")}</div>}
    </li>
  );
}
