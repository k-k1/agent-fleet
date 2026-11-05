import { useCallback } from "react";
import { create } from "zustand";
import { api } from "../../../core/api/client.ts";
import { useTenantStore } from "../../../core/store/tenant.ts";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import { useSettingsUI } from "../store.ts";
import type { ConnectionsStatus } from "../../../types/session.ts";

// useConnections: the shared api/connections loader for the settings tabs. Keeps a
// local copy (null = loading) and, on reload, notifies global listeners (onboarding
// card, repo launch filter) via bumpConn so they refetch after a connect/disconnect.
// Used by AgentsTab / GitTab / OpsTab instead of each re-implementing the same fetch.
//
// Failure handling (the "connected but the card still says not connected" bug): a
// transient CP 502 — api() resolves it as {error:{code:http_5xx}} — must NOT become the
// displayed truth of a one-shot fetch (the ws-boot-view lesson). Retry a couple of
// times, and never downgrade an already-loaded snapshot to {}: stale-but-real beats
// wrongly-empty, which flipped every card to "not connected" (「未接続」) right after a
// successful connect.
//
// The last good snapshot lives in a module-level store, not in component state: reopening a
// tab draws from it at once and the refetch replaces it in place, instead of every mount
// starting from null and waiting out the whole GET /connections again. It is only ever
// replaced by a real answer (or set to {} when nothing was ever loaded), never cleared by a
// failure; resetConnectionsCache() is for when the workspace it described is gone.
const useConnectionsCache = create<{ conns: ConnectionsStatus | null }>(() => ({ conns: null }));

// Request generation, at the same scope as the snapshot: the latest reload from ANY consumer
// wins, and a reset invalidates every request still out, so a slow GET from an earlier mount
// (or from before a workspace stop) can neither overwrite a newer state nor repopulate the
// cleared cache.
let connSeq = 0;

export function resetConnectionsCache() {
  connSeq++;
  useConnectionsCache.setState({ conns: null });
}

// Any path that takes the Agent away (stop, restart, recreate) clears the snapshot, whether or
// not a tab that uses it is mounted. The guard is for tests that stub the store.
useWorkspaceStore.subscribe?.((s, prev) => {
  if (s.state !== "running" && prev.state === "running") resetConnectionsCache();
});

// Switching tenant or member does not reload the page: the old snapshot (account emails and
// all) must not show under the new one, and its requests still out must not land.
useTenantStore.subscribe?.((s, prev) => {
  if (s.tenant !== prev.tenant || s.identityRev !== prev.identityRev) resetConnectionsCache();
});

export function useConnections() {
  const bumpConn = useSettingsUI((s) => s.bumpConn);
  const conns = useConnectionsCache((s) => s.conns);
  const setConns = useCallback(
    (v: ConnectionsStatus | ((prev: ConnectionsStatus | null) => ConnectionsStatus)) =>
      useConnectionsCache.setState((s) => ({ conns: typeof v === "function" ? v(s.conns) : v })),
    [],
  );
  const reload = useCallback(() => {
    const id = ++connSeq; // a newer reload wins over an older retry chain
    const attempt = (retriesLeft: number) => {
      api("api/connections")
        .then((d) => {
          if (id !== connSeq) return;
          if (d && !d.error) {
            setConns(d);
            return;
          }
          if (retriesLeft > 0) setTimeout(() => attempt(retriesLeft - 1), 1200);
          else setConns((prev) => prev ?? {});
        })
        .catch(() => {
          if (id !== connSeq) return;
          if (retriesLeft > 0) setTimeout(() => attempt(retriesLeft - 1), 1200);
          else setConns((prev) => prev ?? {});
        });
    };
    attempt(2);
    bumpConn();
  }, [bumpConn, setConns]);
  return { conns, reload };
}
