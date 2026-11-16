import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../../core/api/client.ts";
import { useTenantStore } from "../../../core/store/tenant.ts";
import type { LiveKindAccess } from "../../../agents/launchAccess.ts";

/** One kind of the live GET /agents/kinds answer, as far as these pages read it. */
export type LiveKind = LiveKindAccess;

/** What the member's settings pages may show (ADR 0138 D10a): the kinds the policy allows and the
 *  member has not switched off, and whether the member's own switches hide something. */
export interface MemberCatalog {
  /** Allowed, switched-on kinds by name; null while there is no fresh answer. */
  kinds: Map<string, LiveKind> | null;
  /** The member's own switches hide at least one kind the policy allows. */
  hiddenByOwn: boolean;
  /** No harness has a route the policy allows (access.noRouteAvailable). */
  noRoute: boolean;
  /** The policy allows some harness and the member's own switches turned every one off. */
  allOff: boolean;
  /** No answer has been taken yet for this principal: "checking", not "nothing". */
  pending?: boolean;
  /** Per-connection routes the policy would allow on a host it approves, though not on the
   *  connection stored now (access.addableRoutes): their entry is offered. Names no host. */
  addable: Set<string>;
}

const EMPTY: MemberCatalog = { kinds: null, hiddenByOwn: false, noRoute: false, allOff: false, addable: new Set() };
const PENDING: MemberCatalog = { ...EMPTY, pending: true };

/** parseMemberCatalog reads a GET /agents/kinds answer; null when it is not one, or when it does
 *  not prove itself filtered by the policy in force: an older Agent sends the same `kinds` with no
 *  `access.policyConfirmed`, and a current one sends it false while the policy is unconfirmed. */
export function parseMemberCatalog(raw: unknown): MemberCatalog | null {
  const r = raw as { kinds?: unknown; access?: { allOffByOwnSettings?: unknown; someOffByOwnSettings?: unknown } } | null;
  if (!r || typeof r !== "object" || !Array.isArray(r.kinds)) return null;
  if (!r.access || typeof r.access !== "object") return null;
  // A catalogue the Agent cannot vouch for: no policy recorded yet, or one past its freshness
  // limit (D12). Its kinds may be unfiltered or filtered by a stale policy.
  if ((r.access as { policyConfirmed?: unknown }).policyConfirmed !== true) return null;
  const kinds = new Map<string, LiveKind>();
  for (const k of r.kinds as LiveKind[]) {
    if (k && typeof k.kind === "string") kinds.set(k.kind, k);
  }
  const a = r.access as { noRouteAvailable?: unknown; allOffByOwnSettings?: unknown; someOffByOwnSettings?: unknown; addableRoutes?: unknown };
  const addable = new Set(Array.isArray(a.addableRoutes) ? a.addableRoutes.filter((x): x is string => typeof x === "string") : []);
  return {
    kinds,
    hiddenByOwn: a.someOffByOwnSettings === true || a.allOffByOwnSettings === true,
    noRoute: a.noRouteAvailable === true,
    allOff: a.allOffByOwnSettings === true,
    addable,
  };
}

// Requests in flight, per principal: the screens that read the catalogue are often mounted
// together (the repo rail's three containers, a dialog over them), and one answer serves them
// all. Only a request still in flight is shared, never a settled answer, so every mount still asks
// the Agent afresh; a refresh the page asks for (after a reset, a stored credential) never rides
// one already out, which may have been answered before the change.
const inflight = new Map<string, Promise<unknown>>();

function fetchKinds(identity: string, fresh: boolean): Promise<unknown> {
  let p = fresh ? undefined : inflight.get(identity);
  if (!p) {
    p = api("api/agents/kinds");
    inflight.set(identity, p);
    const drop = () => {
      if (inflight.get(identity) === p) inflight.delete(identity);
    };
    p.then(drop, drop);
  }
  return p;
}

// CATALOG_REFRESH_MS bounds how long a policy change takes to reach an open page: the Agent pulls
// the policy at least every 30 s (ADR 0138 D12), and this adds at most one more such period.
export const CATALOG_REFRESH_MS = 30_000;

/**
 * useMemberCatalog asks the Agent afresh on every mount, on every tenant or member switch, every
 * CATALOG_REFRESH_MS, and on refresh(): the catalogue these pages filter by must be the policy and
 * the member layer as they are now. Only the newest request may set it. While the workspace is
 * stopped, the first answer is out, the latest request failed, or no answer has been taken for a
 * whole refresh period, kinds is null and the caller shows no kind at all.
 */
export function useMemberCatalog(running: boolean): MemberCatalog & { refresh: () => Promise<void> } {
  const [cat, setCat] = useState<MemberCatalog>(PENDING);
  const seq = useRef(0);
  const deadline = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const identity = useTenantStore((s) => `${s.tenant}|${s.identityRev}`);
  const who = useRef(identity);
  who.current = identity;
  const load = useCallback(async (fresh = false) => {
    const id = ++seq.current;
    // Fails closed when no answer is taken for a whole period from the oldest open request.
    if (deadline.current === undefined) {
      deadline.current = setTimeout(() => {
        deadline.current = undefined;
        if (id <= seq.current) setCat(EMPTY);
      }, CATALOG_REFRESH_MS);
    }
    let next: MemberCatalog | null = null;
    try {
      next = parseMemberCatalog(await fetchKinds(who.current, fresh));
    } catch {
      next = null;
    }
    if (id !== seq.current) return;
    clearTimeout(deadline.current);
    deadline.current = undefined;
    setCat(next ?? EMPTY);
  }, []);
  // A refresh the page asks for (after the reset, after a credential is stored) keeps what is
  // drawn until the answer replaces it: clearing would unmount the cards, and a card in the middle
  // of a sign-in would lose it.
  const refresh = useCallback(() => load(true), [load]);
  useEffect(() => {
    // A new principal starts from nothing: the previous one's catalogue is never shown for it.
    setCat(running ? PENDING : EMPTY);
    if (!running) {
      seq.current++;
      return;
    }
    void load();
    const timer = setInterval(() => void load(), CATALOG_REFRESH_MS);
    return () => {
      seq.current++;
      clearInterval(timer);
      clearTimeout(deadline.current);
      deadline.current = undefined;
    };
  }, [running, load, identity]);
  return { ...cat, refresh };
}

/** The routes a live kind may use at all: its allowed selectable and fixed routes. */
export function allowedRoutesOf(k: LiveKind | undefined): Set<string> {
  return new Set([...(k?.routes ?? []), ...(k?.fixedRoutes ?? [])]);
}
