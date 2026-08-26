// cloneRepo / svnCheckout — リポジトリの取り込み（docs/78）。POST は**開始**だけを行い、
// 実処理は Agent 側のジョブになる（202 + job）。ここはそのジョブが終わるまで待ち、結末を
// 呼び出し元に返す。
//
// なぜこの形か（実際に起きた事故）: 取り込みは分〜時間かかるのに、上流のプロキシは 60 秒で
// 応答を切る。同期 POST だった頃、この層は「エラーが返ったがフォルダが増えていれば成功」と
// 読み替えていた。`svn checkout` は開始 1 秒で `.svn` を作るので、**この判定は必ず成功に倒れる**
// —— 走行中の作業コピーを「チェックアウトしました」と報告し、そこへ 更新 を押した利用者は
// E155037 / E200033 に迎えられ、30 分の上限を越えると作業コピーごと消えた。
// 今はジョブの state が唯一の真実なので、切れた接続は結末に影響しない。
import { apiJSON, errText } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import { useReposStore } from "./store.ts";
import { useRepoJobsStore, type RepoJob } from "./jobs.ts";
import { useFilesStore } from "../files/store.ts";

export interface CloneRequest {
  remote_url: string;
  branch: string;
  name: string;
  new_branch?: string;
}

export interface SvnCheckoutRequest {
  url: string;
  subpath?: string;
  name?: string;
  username?: string;
  password?: string;
  save?: boolean;
  trustCert?: boolean;
}

// revealNewRepo surfaces a freshly cloned/checked-out working copy in the Files
// tree. A reveal alone only expands + selects a path; the tree renders its root
// level from a separately-fetched `entries` snapshot that refreshes on the files
// tick, so without also bumping the tick the brand-new top-level folder would not
// re-fetch and would stay invisible until a manual refresh. Bump first (refetch
// the root), then reveal (expand + scroll to it).
function revealNewRepo(name: string): void {
  useFilesStore.getState().bump();
  useFilesStore.getState().revealInFiles("repos/" + name);
}

// startImport posts the request and returns the accepted job, or an error message.
async function startImport(path: string, body: unknown): Promise<{ job?: RepoJob; err?: string }> {
  let res: { error?: unknown; job?: RepoJob };
  try {
    res = await apiJSON(path, "POST", body);
  } catch (e) {
    return { err: String(e) };
  }
  if (res?.error) return { err: errText(res.error as never) };
  if (!res?.job?.id) return { err: t("rp.import_no_job") };
  return { job: res.job };
}

// awaitImport waits for the job and turns its outcome into the caller's shape.
// A failure that KEPT a resumable working copy says so — for svn that folder is
// not garbage, it is a checkout that `svn update` can carry on from.
async function awaitImport(
  job: RepoJob,
  toast: (msg: string) => void,
  failedKey: "rp.clone_failed" | "rp.svn_checkout_failed",
): Promise<{ ok: boolean; name: string }> {
  // Draw the row immediately instead of waiting for the next poll tick.
  void useRepoJobsStore.getState().refresh();
  const done = await useRepoJobsStore.getState().wait(job.id);
  await useReposStore.getState().refresh();
  const landed = useReposStore.getState().repos.some((r) => r.name === job.name);
  if (!done) {
    // The record is gone (dismissed elsewhere). Believe the repo list, not a guess.
    if (landed) revealNewRepo(job.name);
    return { ok: landed, name: landed ? job.name : "" };
  }
  if (done.state === "done") {
    revealNewRepo(done.name);
    return { ok: true, name: done.name };
  }
  if (done.state !== "canceled") {
    toast(t(failedKey, { err: done.error || done.state }));
  }
  return { ok: false, name: "" };
}

export async function cloneRepo(
  req: CloneRequest,
  toast: (msg: string) => void,
): Promise<{ ok: boolean; name: string }> {
  const { job, err } = await startImport("api/repos", {
    remote_url: req.remote_url,
    branch: req.branch,
    name: req.name,
    new_branch: req.new_branch || "",
  });
  if (!job) {
    toast(t("rp.clone_failed", { err: err || "" }));
    return { ok: false, name: "" };
  }
  return awaitImport(job, toast, "rp.clone_failed");
}

// svnCheckout — POST /api/repos/svn, the SVN twin of cloneRepo (docs/41).
export async function svnCheckout(
  req: SvnCheckoutRequest,
  toast: (msg: string) => void,
): Promise<{ ok: boolean; name: string }> {
  const { job, err } = await startImport("api/repos/svn", {
    url: req.url,
    subpath: req.subpath || "",
    name: req.name || "",
    username: req.username || "",
    password: req.password || "",
    save: !!req.save,
    trustCert: !!req.trustCert,
  });
  if (!job) {
    toast(t("rp.svn_checkout_failed", { err: err || "" }));
    return { ok: false, name: "" };
  }
  return awaitImport(job, toast, "rp.svn_checkout_failed");
}
