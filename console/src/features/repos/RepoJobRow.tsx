// RepoJobRow — リポジトリ取り込み 1 件の行（docs/78）。リポジトリ一覧の先頭に、作業コピーの
// 行と同じ幅で並ぶ。
//
// なぜ行として出すのか: 取り込み中のフォルダは Agent が一覧から外している（半端な作業コピーで
// 起動されたり `svn status` を掛けられたりしないため）。何も出さないと「クローンを押したのに
// 何も起きない」になるので、進行そのものをここが受け持つ。**結末も**同じ場所に出す —— 失敗と
// 中断は既読にするまで残る。以前はトーストを見逃せば終わりで、残るのは半端なフォルダだけだった。
import { Icon } from "../../ui/Icon.tsx";
import { IconButton } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useRepoJobsStore, isRepoJobRunning, type RepoJob } from "./jobs.ts";

export function RepoJobRow({ job }: { job: RepoJob }) {
  const tr = useT();
  const remove = useRepoJobsStore((s) => s.remove);
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

  // 残った作業コピーの扱いは VCS で違う: svn は 更新 が続きから取るが、git の clone は
  // 途中から再開できない（残骸は消えている）。言い分を分けないと誤った指示になる。
  const kept = !running && job.state !== "done" && job.kept;

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
          onClick={() => void remove(job.id)}
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
