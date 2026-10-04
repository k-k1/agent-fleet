import { useEffect, useMemo } from "react";
import { ensureWorkItems, POLL_MS, useWorkItemStore } from "../workitems/store.ts";
import { refIndex, type RefIndex } from "./refSearch.ts";

/** The work-item ledger as a session → ticket keys index, rebuilt only when the ledger changes. */
export function useRefIndex(): RefIndex {
  const ledger = useWorkItemStore((s) => s.payload?.sessions);
  return useMemo(() => refIndex(ledger), [ledger]);
}

/** Load the ledger while `active` (a reference-shaped query is in the box) and it is not loaded
 * yet, retrying past ensureWorkItems' cooldown: one failed read must not leave every later
 * reference search blind while the work-items section, which would poll, is hidden. */
export function useLedgerWhile(active: boolean): void {
  const loaded = useWorkItemStore((s) => s.loaded);
  useEffect(() => {
    if (!active || loaded) return;
    void ensureWorkItems();
    const id = setInterval(() => void ensureWorkItems(), POLL_MS + 1000);
    return () => clearInterval(id);
  }, [active, loaded]);
}
