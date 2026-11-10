import { useState } from "react";
import { readFold, writeFold } from "./foldStore.ts";

// A collapse flag persisted under `key`. While nothing is stored yet the flag
// FOLLOWS `dflt` live (it's derived, not snapshotted) — so a node whose default
// depends on data that loads async (has sessions → open) settles correctly, and
// launching a session into a folded-by-default empty repo pops it open. The
// first explicit toggle pins the choice. Mirrors ui/Section's localStorage
// convention so a folded node stays folded across reloads.
//
// Shared by the owner-side project tree (features/project/RepoNode) and the
// recipient-side shared-sessions tree (features/sharing/SharedProjectNode) — both
// are "fold a node of the left rail" and must behave identically.
export function usePersistedOpen(key: string, dflt = true): { open: boolean; toggle: () => void; set: (v: boolean) => void } {
  const [stored, setStored] = useState<boolean | null>(() => readFold(key));
  const open = stored === null ? dflt : stored;
  const set = (v: boolean) => {
    setStored(v);
    writeFold(key, v);
  };
  return { open, toggle: () => set(!open), set };
}
