// lib/aiAssistResolution.ts — GET /api/ai-assist/resolution (docs/log/103 §103.6/§103.7's
// "いま使うのは" row): which backend each AI-assist feature currently resolves to.
//
// Cheap on purpose: the Agent answers from its own 1-minute availability cache and never
// starts a CLI (workspace/agent/ai_assist.go's header explains why), so polling this while the
// settings tab is open costs nothing it would not already be paying elsewhere. Model NAMES are
// not part of the answer (docs/log/103 decision 5 / §103.8-3) — a caller that wants one draws
// it from the model catalog it already fetches (lib/agentModels.ts's useModelOptions).
import { useEffect, useState } from "react";
import { api } from "../core/api/client.ts";

export interface AiAssistResolutionRow {
  feature: string;
  enabled: boolean;
  kind?: string;
  // "unavailable": the feature runs deny-all (memory.review) and its pinned agent cannot.
  source: "pin" | "default" | "unknown" | "unavailable";
}

const POLL_MS = 10_000;

/** Keyed by feature id. null while the first fetch is still in flight — a feature absent from
 *  a loaded map means the Agent could not answer yet (a cold availability cache), which reads
 *  as "unknown", not as a guess. */
export function useAiAssistResolution(enabled = true): Record<string, AiAssistResolutionRow> | null {
  const [rows, setRows] = useState<Record<string, AiAssistResolutionRow> | null>(null);
  useEffect(() => {
    if (!enabled) {
      setRows(null); // a disabled hook must not keep, or later reuse, an earlier answer
      return;
    }
    let alive = true;
    const load = () => {
      void api("api/ai-assist/resolution").then((d) => {
        if (!alive) return;
        const list: AiAssistResolutionRow[] = Array.isArray(d?.features) ? d.features : [];
        setRows(Object.fromEntries(list.map((r) => [r.feature, r])));
      });
    };
    load();
    const id = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [enabled]);
  return enabled ? rows : null;
}

/** True only on the Agent's definite "no deny-all backend" answer for the memory review (ADR 0117);
 *  while the answer is loading or unknown the review is not blocked, the Agent re-checks it. */
export function useMemoryReviewUnavailable(enabled = true): boolean {
  return useAiAssistResolution(enabled)?.["memory.review"]?.source === "unavailable";
}
