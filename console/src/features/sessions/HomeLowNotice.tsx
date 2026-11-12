// The body of the low-disk toast (wsStatsFeed, key "home-disk-low"). A component rather than a
// string so the reclaimable figure and the button can follow the store without the toast being
// raised again: the feed raises it once per crossing and never touches it after.
import { useEffect, useState } from "react";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { humanSize } from "../../lib/filemeta.ts";
import { reclaimExpiresAt, useReclaimEstimate } from "../../core/store/homeReclaim.ts";
import { useSessionUI } from "./ui.ts";

export function HomeLowNotice({ free, total }: { free: string; total: string }) {
  const tr = useT();
  const est = useReclaimEstimate();
  // The figure is only valid for RECENT_WALK_MS; the store changes nothing as time passes, so
  // a toast left open for hours would keep quoting it without this nudge.
  const [, tick] = useState(0);
  // Re-armed per walk, not per figure: a newer walk of the same size has a later expiry.
  const at = reclaimExpiresAt();
  useEffect(() => {
    if (est == null || at == null) return;
    const id = setTimeout(() => tick((n) => n + 1), Math.max(0, at - Date.now()) + 50);
    return () => clearTimeout(id);
  }, [est, at]);
  const openReclaim = useSessionUI((s) => s.openReclaim);
  return (
    <span>
      {tr("disk.home_low", { free, total })}
      {est != null && (
        <>
          {" "}
          {tr("disk.home_low_reclaim", { size: humanSize(est) })}{" "}
          <Button small onClick={openReclaim}>
            {tr("disk.home_low_clean")}
          </Button>
        </>
      )}
    </span>
  );
}
