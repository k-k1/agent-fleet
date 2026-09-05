// Remembers whether the collapsible sections of the "start work" dialog (LaunchModal) are
// open. Per device, not per repository: the difference between someone who types a branch
// name every time and someone who launches with the defaults belongs to the person and does
// not follow the repository. Not weighty enough for settings (which sync to the server), so
// it stays in localStorage — if it cannot be read, the collapsed default is fine.
export type LaunchSectionKey = "place" | "adv";

const KEY = (k: LaunchSectionKey) => "af.launch-open." + k;

export function readLaunchOpen(k: LaunchSectionKey): boolean {
  try {
    return localStorage.getItem(KEY(k)) === "1";
  } catch {
    return false; // private mode — fall back to the collapsed default
  }
}

export function writeLaunchOpen(k: LaunchSectionKey, open: boolean): void {
  try {
    if (open) localStorage.setItem(KEY(k), "1");
    else localStorage.removeItem(KEY(k));
  } catch {
    /* private mode / quota — it just opens collapsed again next time */
  }
}
