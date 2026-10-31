import type { MouseEvent as RMouseEvent } from "react";
import { Button } from "../../../ui/Button.tsx";
import { useSettingsUI } from "../store.ts";
import { useFilesStore } from "../../files/store.ts";
import { useLeftRail } from "../../../core/store/leftRail.ts";
import { openGallery } from "../../gallery/open.ts";
import { useT } from "../../../lib/i18n/index.ts";

/** Where a figure lives (usagePlace in cleanup_cache.go): path to read ("~/…"), browse = the
 *  same folder relative to the browse root, which the file tree and the gallery take ("" or
 *  absent when it is outside the root, or the Agent predates the field). */
export interface Place {
  path?: string;
  browse?: string;
}

/** Ctrl/⌘ and the middle button open beside, as everywhere else in the Console. */
const besideClick = (e: RMouseEvent) => e.ctrlKey || e.metaKey || e.button === 1;

// PlaceLine — a figure's folder: the path (a click opens that folder in the left pane's file
// tree and focuses it) and, for picture folders, the way into the gallery. Both leave the
// settings modal first — what they open is behind it.
export function PlaceLine({ place, gallery }: { place?: Place; gallery?: boolean }) {
  const tr = useT();
  const closeSettings = useSettingsUI((s) => s.closeSettings);
  if (!place?.path) return null;
  const browse = place.browse;
  const reveal = () => {
    if (!browse) return;
    closeSettings();
    useLeftRail.getState().ensureOpen();
    useFilesStore.getState().revealInFiles(browse, { focus: true });
  };
  const toGallery = (e: RMouseEvent) => {
    if (!browse) return;
    closeSettings();
    openGallery(browse, { newPane: besideClick(e) });
  };
  return (
    <span className="mv-place">
      {browse ? (
        <button
          type="button"
          className="mv-path"
          title={tr("machine.disk_reveal", { path: place.path })}
          onClick={reveal}
        >
          {place.path}
        </button>
      ) : (
        <span className="mv-path is-static" title={place.path}>
          {place.path}
        </span>
      )}
      {gallery && browse && (
        <Button small icon="file-media" onClick={toGallery} onAuxClick={toGallery}>
          {tr("machine.disk_open_gallery")}
        </Button>
      )}
    </span>
  );
}
