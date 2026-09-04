// cloneRepo / svnCheckout — importing a repository (docs/log/78). The POST only *starts*
// the work; the import itself runs as a job on the Agent (202 + job). This layer waits for
// that job and returns its outcome to the caller.
//
// Why this shape: an import takes minutes to hours while the upstream proxy cuts the
// response off at 60 seconds. When the POST was synchronous, this layer inferred success
// from "an error came back, but the folder appeared" — a test `svn checkout` satisfies one
// second in, since it creates `.svn` immediately, so it always fell to success. A
// still-running working copy was reported as checked out, and the user who then pressed
// refresh met E155037 / E200033, or lost the working copy once the 30-minute cap passed.
// The job's state is now the only truth, so a dropped connection cannot affect the outcome.
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

// initRepo — POST /api/repos/init: start with no import source (create ~/repos/<name> and
// `git init`). Unlike clone/checkout it does NOT go through a job, because it touches no
// network and finishes in milliseconds: the failure above (patching up a response the
// 60-second cutoff truncated) cannot happen here, so the outcome rides in the response.
export async function initRepo(
  name: string,
  toast: (msg: string) => void,
): Promise<{ ok: boolean; name: string }> {
  let res: { error?: unknown; repo?: { name?: string } };
  try {
    res = await apiJSON("api/repos/init", "POST", { name });
  } catch (e) {
    toast(t("rp.init_failed", { err: String(e) }));
    return { ok: false, name: "" };
  }
  if (res?.error) {
    toast(t("rp.init_failed", { err: errText(res.error as never) }));
    return { ok: false, name: "" };
  }
  const created = res?.repo?.name || name;
  await useReposStore.getState().refresh();
  revealNewRepo(created);
  return { ok: true, name: created };
}

// svnCheckout — POST /api/repos/svn, the SVN twin of cloneRepo (docs/log/41).
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
