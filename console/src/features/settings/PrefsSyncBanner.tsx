import { useT } from "../../lib/i18n/index.ts";
import { retryPrefsSync, usePrefsSyncState } from "../../lib/settings.ts";

// Shown while the settings on screen have not reached the Agent's ui-prefs. Until they do, the
// Agent acts on the previous settings — the launch guard refuses a model un-hidden here, and
// runs one hidden here — so the member needs to see it where they change settings.
export function PrefsSyncBanner() {
  const tr = useT();
  if (usePrefsSyncState() !== "failed") return null;
  return (
    <p className="ps-note ps-note-warn" role="status">
      {tr("set.prefs_unsynced")}{" "}
      <button type="button" className="ghost xs" onClick={retryPrefsSync}>
        {tr("set.prefs_retry")}
      </button>
    </p>
  );
}
