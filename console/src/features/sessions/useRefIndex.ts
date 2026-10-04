import { useMemo } from "react";
import { useWorkItemStore } from "../workitems/store.ts";
import { refIndex, type RefIndex } from "./refSearch.ts";

/** The work-item ledger as a session → ticket keys index, rebuilt only when the ledger changes. */
export function useRefIndex(): RefIndex {
  const ledger = useWorkItemStore((s) => s.payload?.sessions);
  return useMemo(() => refIndex(ledger), [ledger]);
}
