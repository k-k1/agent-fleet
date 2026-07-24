// cloneRepo — the POST /api/repos clone with the proxy-timeout re-check, shared
// by the rail's クローン flow (ProjectTree spinner row) and the はじめる hub's
// clone-and-continue stage (起動導線 Ph2). Refreshes the repo store and reveals
// the new working copy in Files; returns its folder name ("" when unknown).
import { apiJSON, errText } from "../../core/api/client.ts";
import { t } from "../../lib/i18n/index.ts";
import { useReposStore } from "./store.ts";
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

export async function cloneRepo(
  req: CloneRequest,
  toast: (msg: string) => void,
): Promise<{ ok: boolean; name: string }> {
  // Snapshot the existing folders so we can tell an actually-new working copy
  // apart from the ones already here, if we have to re-check after an error.
  const beforeNames = new Set(useReposStore.getState().repos.map((r) => r.name));
  const refreshRepos = () => useReposStore.getState().refresh();
  try {
    const res = await apiJSON("api/repos", "POST", {
      remote_url: req.remote_url,
      branch: req.branch,
      name: req.name,
      new_branch: req.new_branch || "",
    });
    if (res && res.error) {
      // A big clone can outlive an upstream proxy timeout: the server keeps
      // cloning (it doesn't watch the request context) and usually finishes,
      // but the browser gets an empty/gateway response that surfaces here as an
      // error. Re-check the repo list — if a new working copy appeared, the
      // clone actually succeeded and we should reveal it, not cry failure.
      await refreshRepos();
      const added = useReposStore.getState().repos.find((r) => !beforeNames.has(r.name));
      if (!added) {
        toast(t("rp.clone_failed", { err: errText(res.error) }));
        return { ok: false, name: "" };
      }
      revealNewRepo(added.name);
      return { ok: true, name: added.name };
    }
    await refreshRepos();
    if (res && res.name) revealNewRepo(res.name);
    else useFilesStore.getState().bump();
    return { ok: true, name: res?.name || "" };
  } catch (e) {
    toast(t("rp.clone_failed", { err: String(e) }));
    return { ok: false, name: "" };
  }
}

// svnCheckout — POST /api/repos/svn, the SVN twin of cloneRepo (docs/41). Same
// proxy-timeout re-check (a large checkout can outlive the upstream timeout while
// the server keeps working), same refresh + reveal on success.
export async function svnCheckout(
  req: SvnCheckoutRequest,
  toast: (msg: string) => void,
): Promise<{ ok: boolean; name: string }> {
  const beforeNames = new Set(useReposStore.getState().repos.map((r) => r.name));
  const refreshRepos = () => useReposStore.getState().refresh();
  try {
    const res = await apiJSON("api/repos/svn", "POST", {
      url: req.url,
      subpath: req.subpath || "",
      name: req.name || "",
      username: req.username || "",
      password: req.password || "",
      save: !!req.save,
      trustCert: !!req.trustCert,
    });
    if (res && res.error) {
      await refreshRepos();
      const added = useReposStore.getState().repos.find((r) => !beforeNames.has(r.name));
      if (!added) {
        toast(t("rp.svn_checkout_failed", { err: errText(res.error) }));
        return { ok: false, name: "" };
      }
      revealNewRepo(added.name);
      return { ok: true, name: added.name };
    }
    await refreshRepos();
    if (res && res.name) revealNewRepo(res.name);
    else useFilesStore.getState().bump();
    return { ok: true, name: res?.name || "" };
  } catch (e) {
    toast(t("rp.svn_checkout_failed", { err: String(e) }));
    return { ok: false, name: "" };
  }
}
