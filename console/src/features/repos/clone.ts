// cloneRepo — the POST /api/repos clone with the proxy-timeout re-check, shared
// by the rail's クローン flow (ProjectTree spinner row) and the はじめる hub's
// clone-and-continue stage (起動導線 Ph2). Refreshes the repo store and reveals
// the new working copy in Files; returns its folder name ("" when unknown).
import { apiJSON, errText } from "../../core/api/client.ts";
import { useReposStore } from "./store.ts";
import { useFilesStore } from "../files/store.ts";

export interface CloneRequest {
  remote_url: string;
  branch: string;
  name: string;
  new_branch?: string;
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
        toast("クローンに失敗: " + errText(res.error));
        return { ok: false, name: "" };
      }
      useFilesStore.getState().revealInFiles("repos/" + added.name);
      return { ok: true, name: added.name };
    }
    await refreshRepos();
    if (res && res.name) useFilesStore.getState().revealInFiles("repos/" + res.name);
    else useFilesStore.getState().bump();
    return { ok: true, name: res?.name || "" };
  } catch (e) {
    toast("クローンに失敗: " + e);
    return { ok: false, name: "" };
  }
}
