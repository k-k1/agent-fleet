// Which views of a tabbed page hold edits not saved yet. Each editor reports its own state with
// useReportDirty; the nearest DirtyScope (one per view) folds them into one flag for the tab's
// mark. An editor outside any scope reports to nothing, so the editors work the same anywhere.
import { createContext, useCallback, useContext, useEffect, useId, useState, type ReactNode } from "react";

type Report = (id: string, dirty: boolean) => void;

const DirtyContext = createContext<Report | null>(null);

/** useReportDirty tells the enclosing DirtyScope whether this editor has unsaved edits. An
 *  editor that unmounts (a reload after a save, a switch to another tenant) stops counting. */
export function useReportDirty(dirty: boolean) {
  const report = useContext(DirtyContext);
  const id = useId();
  useEffect(() => {
    report?.(id, dirty);
  }, [report, id, dirty]);
  useEffect(() => () => report?.(id, false), [report, id]);
}

/** DirtyScope calls onChange with whether any editor inside it is dirty, each time that changes. */
export function DirtyScope({ onChange, children }: { onChange: (dirty: boolean) => void; children: ReactNode }) {
  const [dirty, setDirty] = useState<ReadonlySet<string>>(() => new Set());
  const report = useCallback<Report>((id, d) => {
    setDirty((prev) => {
      if (prev.has(id) === d) return prev;
      const next = new Set(prev);
      if (d) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);
  const any = dirty.size > 0;
  useEffect(() => {
    onChange(any);
  }, [any, onChange]);
  return <DirtyContext.Provider value={report}>{children}</DirtyContext.Provider>;
}
