// useStudioList — the studio picker's list. Every pane keeps its own copy, re-read on mount and
// whenever any pane in this window creates, renames or deletes a studio (`studiosChanged`):
// without that, a studio made in pane A never appears in pane B's picker until B remounts.
import { useCallback, useEffect, useRef, useState } from "react";
import { listStudios, type StudioSummary } from "./api.ts";
import { onStudiosChanged } from "./studioBus.ts";
import { cacheStudioList, useStudioCache } from "./studioCache.ts";

export function useStudioList(): [StudioSummary[], () => Promise<void>] {
  // The window's last list is the first frame, so a remounted pane's picker is not empty.
  const [studios, setStudios] = useState<StudioSummary[]>(() => useStudioCache.getState().list);
  // Only the newest request may answer: a slow first read landing after the one a create
  // triggered would put back a list without the new studio.
  const gen = useRef(0);
  const read = useCallback(async () => {
    const mine = ++gen.current;
    try {
      const r = await listStudios();
      if (mine !== gen.current) return;
      if (r && !r.error && Array.isArray(r.studios)) {
        setStudios(r.studios);
        cacheStudioList(r.studios);
      }
    } catch {
      /* the picker keeps what it had */
    }
  }, []);
  useEffect(() => onStudiosChanged(() => void read()), [read]);
  return [studios, read];
}
