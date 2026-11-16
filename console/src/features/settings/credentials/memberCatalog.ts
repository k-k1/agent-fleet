import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../../core/api/client.ts";

/** One kind of the live GET /agents/kinds answer, as far as these pages read it. */
export interface LiveKind {
  kind: string;
  /** Selectable routes the policy allows. */
  routes?: string[];
  /** Routes the kind runs on with no selection, narrowed to the allowed ones. */
  fixedRoutes?: string[];
}

/** What the member's settings pages may show (ADR 0138 D10a): the kinds the policy allows and the
 *  member has not switched off, and whether the member's own switches hide something. */
export interface MemberCatalog {
  /** Allowed, switched-on kinds by name; null while there is no fresh answer. */
  kinds: Map<string, LiveKind> | null;
  /** The member's own switches hide at least one kind the policy allows. */
  hiddenByOwn: boolean;
}

const EMPTY: MemberCatalog = { kinds: null, hiddenByOwn: false };

/** parseMemberCatalog reads a GET /agents/kinds answer; null when it is not one. */
export function parseMemberCatalog(raw: unknown): MemberCatalog | null {
  const r = raw as { kinds?: unknown; access?: { allOffByOwnSettings?: unknown; someOffByOwnSettings?: unknown } } | null;
  if (!r || typeof r !== "object" || !Array.isArray(r.kinds)) return null;
  const kinds = new Map<string, LiveKind>();
  for (const k of r.kinds as LiveKind[]) {
    if (k && typeof k.kind === "string") kinds.set(k.kind, k);
  }
  const a = r.access;
  return { kinds, hiddenByOwn: !!a && (a.someOffByOwnSettings === true || a.allOffByOwnSettings === true) };
}

/**
 * useMemberCatalog asks the Agent afresh on every mount and refresh: the catalogue this page
 * filters by must be the policy and the member layer as they are now, never a copy from before a
 * tightening. While the workspace is stopped, the answer is out, or it failed, kinds is null and
 * the caller shows no kind at all.
 */
export function useMemberCatalog(running: boolean): MemberCatalog & { refresh: () => Promise<void> } {
  const [cat, setCat] = useState<MemberCatalog>(EMPTY);
  const seq = useRef(0);
  const refresh = useCallback(async () => {
    const id = ++seq.current;
    setCat(EMPTY);
    let next: MemberCatalog | null = null;
    try {
      next = parseMemberCatalog(await api("api/agents/kinds"));
    } catch {
      next = null;
    }
    if (id === seq.current) setCat(next ?? EMPTY);
  }, []);
  useEffect(() => {
    if (!running) {
      seq.current++;
      setCat(EMPTY);
      return;
    }
    void refresh();
    return () => {
      seq.current++;
    };
  }, [running, refresh]);
  return { ...cat, refresh };
}

/** The routes a live kind may use at all: its allowed selectable and fixed routes. */
export function allowedRoutesOf(k: LiveKind | undefined): Set<string> {
  return new Set([...(k?.routes ?? []), ...(k?.fixedRoutes ?? [])]);
}
