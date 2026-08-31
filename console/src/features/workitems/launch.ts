// Ledger write on a successful launch from a work item (docs/log/80 §80.8).
//
// Split out of the section so StartHost — where a launch actually succeeds — does not
// have to import the rail. Failures are swallowed on purpose: the session exists either
// way, and losing the "already started" badge is not worth a toast on top of a launch
// that just worked. The next refresh re-reads the ledger from the CP.
import { workItemSessionCreate } from "./api.ts";
import { useWorkItemStore } from "./store.ts";

export async function recordWorkItemLaunch(
  item: { provider: string; key: string },
  sessionName: string,
  repo: string,
  branch: string,
): Promise<void> {
  try {
    await workItemSessionCreate({
      provider: item.provider || "github",
      itemKey: item.key,
      sessionName,
      repo,
      branch,
    });
  } catch {
    return;
  }
  void useWorkItemStore.getState().refresh();
}
