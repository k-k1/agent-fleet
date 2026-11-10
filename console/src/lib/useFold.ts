import { useState, useSyncExternalStore } from "react";
import { getFoldScopeRev, readFold, subscribeFoldScope, writeFold } from "./foldStore.ts";

// The stored fold flag for `key` (null = never chosen; undefined key = not persisted).
// Re-reads when the (user, tenant) scope changes under a mounted component (tenant switch,
// late whoami), so a toggle never writes a value shown for the previous scope.
export function useFold(key: string | undefined): [boolean | null, (v: boolean) => void] {
  const rev = useSyncExternalStore(subscribeFoldScope, getFoldScopeRev);
  const [st, setSt] = useState<{ rev: number; key?: string; v: boolean | null }>(() => ({ rev, key, v: key ? readFold(key) : null }));
  let cur = st;
  if (st.rev !== rev || st.key !== key) {
    cur = { rev, key, v: key ? readFold(key) : null };
    setSt(cur);
  }
  const set = (v: boolean) => {
    setSt((s) => ({ ...s, v }));
    if (key) writeFold(key, v);
  };
  return [cur.v, set];
}
