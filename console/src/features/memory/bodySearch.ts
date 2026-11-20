// The body half of a memory search, shared by the Memory view of the left pane and the memory
// viewer: the listed fields match at once, bodies only after a pause, through member-search (which
// counts no use of the memories it finds).
import { useEffect, useRef, useState } from "react";
import { api } from "../../core/api/client.ts";
import type { MemorySearchHit } from "../settings/memory/memoryTypes.ts";

/** Pause after the last keystroke before the body search is asked. */
export const SEARCH_DELAY_MS = 300;

/** Every scope at once: sends no scope param, which makes the Agent search the user scope and every project. */
export const ALL_SCOPES = "all";

export interface BodySearch {
  hits: MemorySearchHit[];
  /** "any": no memory held every word, so the hits hold only some of them. */
  fallback: string;
  searching: boolean;
  failed: boolean;
}

/**
 * The body hits for `q` (trimmed, lower-cased; "" asks nothing) in `scope` ("all", "user" or
 * "project:<id>"). A newer question or a scope change drops the answer of an older one.
 */
export function useMemoryBodySearch(q: string, scope: string): BodySearch {
  const [hits, setHits] = useState<MemorySearchHit[]>([]);
  const [fallback, setFallback] = useState("");
  const [searching, setSearching] = useState(false);
  const [failed, setFailed] = useState(false);
  const seqRef = useRef(0);
  useEffect(() => {
    const seq = ++seqRef.current;
    setHits([]);
    setFallback("");
    setFailed(false);
    if (!q) {
      setSearching(false);
      return;
    }
    setSearching(true);
    const [kind, projectId = ""] = scope.split(":");
    const timer = setTimeout(() => {
      const scopeQ =
        kind === ALL_SCOPES ? "" : kind === "user" ? "scope=user&" : `scope=project&project=${encodeURIComponent(projectId)}&`;
      api(`api/agents/memory/entries/member-search?${scopeQ}limit=50&q=${encodeURIComponent(q)}`)
        .then((res) => {
          if (seq !== seqRef.current) return;
          if (res?.error) setFailed(true);
          else {
            setHits(res?.hits ?? []);
            setFallback(res?.fallback ?? "");
          }
        })
        .catch(() => {
          if (seq === seqRef.current) setFailed(true);
        })
        .finally(() => {
          if (seq === seqRef.current) setSearching(false);
        });
    }, SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [q, scope]);
  return { hits, fallback, searching, failed };
}
