// useStudioList — the studio picker's list. Every pane keeps its own copy, re-read on mount and
// whenever any pane in this window creates, renames or deletes a studio (`studiosChanged`):
// without that, a studio made in pane A never appears in pane B's picker until B remounts.
import { useCallback, useEffect, useState } from "react";
import { listStudios, type StudioSummary } from "./api.ts";
import { onStudiosChanged } from "./studioBus.ts";

export function useStudioList(): [StudioSummary[], () => Promise<void>] {
  const [studios, setStudios] = useState<StudioSummary[]>([]);
  const read = useCallback(async () => {
    try {
      const r = await listStudios();
      if (r && !r.error && Array.isArray(r.studios)) setStudios(r.studios);
    } catch {
      /* the picker keeps what it had */
    }
  }, []);
  useEffect(() => onStudiosChanged(() => void read()), [read]);
  return [studios, read];
}
