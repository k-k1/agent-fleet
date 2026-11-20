// The state the Memory view of the left pane and the Memory pane share (ADR 0141 decision 7):
// a write version and the consolidator's proposals. A decision in the pane bumps the version, so
// the view's lists read again, and a write from the view reaches the pane the same way.
//
// The proposals are read here rather than by the view because the activity bar's badge counts the
// pending ones while the view is not mounted (ADR 0141 decisions 2 and 3): the memory list waits
// for the view, this one small read does not.
import { useEffect } from "react";
import { create } from "zustand";
import { api, errDetail, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import type { ConsolProposal } from "../settings/memory/memoryTypes.ts";

const PROPOSALS = "api/agents/memory-consolidation/proposals";
/** The consolidator runs about once a day; a decision made here re-reads at once through `bump`. */
const POLL_MS = 5 * 60_000;

interface MemoryStore {
  /** Bumped by every member write to memory (save, forget, decision): readers depend on it. */
  version: number;
  bump(): void;
  /** null until the first answer. */
  proposals: ConsolProposal[] | null;
  proposalsErr: string;
  /** false: a transient failure (the Agent not up yet) the caller should retry; what is known stays. */
  loadProposals(): Promise<boolean>;
  /** Forget everything read: the workspace stopped or the tenant changed, and its answers no longer hold. */
  reset(): void;
}

let seq = 0;

export const useMemoryStore = create<MemoryStore>((set) => ({
  version: 0,
  bump: () => set((s) => ({ version: s.version + 1 })),
  proposals: null,
  proposalsErr: "",
  loadProposals: async () => {
    const mine = ++seq;
    const res = await api(PROPOSALS).catch(() => null);
    // A newer read or a reset has started since: this answer is stale, and settled for its caller.
    if (mine !== seq) return true;
    if (res === null || isTransientErr(res)) return false;
    if (res?.error) set({ proposals: [], proposalsErr: errDetail(res.error) });
    else set({ proposals: res?.proposals ?? [], proposalsErr: "" });
    return true;
  },
  reset: () => {
    seq++;
    set({ proposals: null, proposalsErr: "" });
  },
}));

export const pendingProposals = (list: ConsolProposal[] | null): number =>
  (list ?? []).filter((p) => p.status === "pending").length;

/**
 * Keeps the proposals read while the workspace runs: at once (retried with backoff through the
 * window right after a start, when the Agent still answers 5xx), after every write, and on a slow
 * poll. `tenant` is another workspace: its proposals replace the old ones.
 */
export function useProposalsFeed(running: boolean, tenant: string): void {
  const version = useMemoryStore((s) => s.version);
  useEffect(() => {
    useMemoryStore.getState().reset();
  }, [running, tenant]);
  useRetryLoad(
    async (signal) => {
      if (!running) return true;
      const settled = await useMemoryStore.getState().loadProposals();
      return signal.aborted || settled;
    },
    [running, tenant, version],
  );
  // The poll is its own effect: a write re-reads above without pushing the next poll back.
  useEffect(() => {
    if (!running) return;
    const id = window.setInterval(() => void useMemoryStore.getState().loadProposals(), POLL_MS);
    return () => window.clearInterval(id);
  }, [running, tenant]);
}
