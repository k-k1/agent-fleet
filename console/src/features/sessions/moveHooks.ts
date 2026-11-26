// Reads the move dialog and the mirror's limit banner share: which kinds are connected, and how
// much quota the offered kinds have left. Both are display and ordering only; the Agent's move
// looks at neither.
import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { api } from "../../core/api/client.ts";
import { agentOf } from "../../agents/registry.ts";
import { kindLaunchable } from "../../agents/launchAccess.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useMemberCatalog } from "../settings/credentials/memberCatalog.ts";
import { getCachedConns, setCachedConns, subscribeConns } from "../repos/connsCache.ts";
import { MOVE_USAGE_ENDPOINTS, readMoveUsage, type MoveUsage } from "./move.ts";
import type { ConnectionsStatus, SessionKind } from "../../types/session.ts";

/**
 * The launch check the move dialog and the limit banner rely on: the connection state (warm from
 * the rail, fetched only cold) judged by the live, policy-filtered catalogue (kindLaunchable,
 * ADR 0138 D10a), so a harness the policy forbids or the member switched off is not offered.
 * `pending` is the catalogue still being read: nothing may be chosen yet. `connected` is null
 * while it is, and, once it has answered, judges by the catalogue alone while the connection
 * check is still cold (the Agent's own refusals cover the rest). An answer that failed or cannot
 * be vouched for offers no kind, as the launch dialog does. `connsKnown` is false while the
 * connection check is cold.
 */
export function useMoveGate(): {
  connected: ((k: SessionKind) => boolean) | null;
  pending: boolean;
  connsKnown: boolean;
  /** The kind's own login check passes (true while the connection check is cold): a kind that is
   *  logged in yet not `connected` is hidden by the policy or the member, not unconnected. */
  loggedIn: (k: SessionKind) => boolean;
} {
  const running = useWorkspaceStore((s) => s.state) === "running";
  const { kinds, pending } = useMemberCatalog(running);
  const conns = useSyncExternalStore(subscribeConns, getCachedConns, getCachedConns);
  useEffect(() => {
    if (conns) return;
    let alive = true;
    api("api/connections")
      .then((d) => alive && setCachedConns(d && !d.error ? (d as ConnectionsStatus) : null))
      .catch(() => {});
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once on mount; `conns` is the warm-cache guard
  }, []);
  const connected = useMemo(
    () =>
      pending ? null : (k: SessionKind) => (conns ? kindLaunchable(k, { conns }, kinds, { routeless: true }) : !!kinds?.has(k)),
    [conns, kinds, pending],
  );
  const loggedIn = useCallback((k: SessionKind) => !conns || agentOf(k).available({ conns }), [conns]);
  return { connected, pending: !!pending, connsKnown: !!conns, loggedIn };
}

/** The same check for callers that must know the connection state too (the limit banner): null until both answered. */
export function useConnectedKinds(): ((k: SessionKind) => boolean) | null {
  const { connected, connsKnown } = useMoveGate();
  return connsKnown ? connected : null;
}

// useMoveUsage reads, once per mount, the usage of every offered kind the Console already has a
// usage source for. Display only: the Agent's move does not look at quotas.
export function useMoveUsage(kinds: SessionKind[], tick = 0): Partial<Record<SessionKind, MoveUsage>> {
  const [out, setOut] = useState<Partial<Record<SessionKind, MoveUsage>>>({});
  const key = kinds.join(",");
  useEffect(() => {
    let alive = true;
    for (const k of kinds) {
      const ep = MOVE_USAGE_ENDPOINTS[k];
      if (!ep) continue;
      // A failed or empty re-read (an error body or a rejected fetch alike) drops the kind back to
      // "unknown" rather than keep a number that may have rolled over.
      const settle = (u: MoveUsage | null) => {
        if (!alive) return;
        setOut((o) => {
          const { [k]: _old, ...rest } = o;
          return u ? { ...rest, [k]: u } : rest;
        });
      };
      api(ep)
        .then((d) => settle(readMoveUsage(d)))
        .catch(() => settle(null));
    }
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed by the kind list's contents and the caller's refresh tick
  }, [key, tick]);
  return out;
}

