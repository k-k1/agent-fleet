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
  loadProposals(): Promise<void>;
  /** Forget everything read: the workspace stopped, and its answers no longer hold. */
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
    // A newer read or a reset has started since: this answer is stale.
    if (mine !== seq) return;
    // A transient failure (the Agent not up yet) keeps what is known rather than emptying it.
    if (res === null || isTransientErr(res)) return;
    if (res?.error) set({ proposals: [], proposalsErr: errDetail(res.error) });
    else set({ proposals: res?.proposals ?? [], proposalsErr: "" });
  },
  reset: () => {
    seq++;
    set({ proposals: null, proposalsErr: "" });
  },
}));

export const pendingProposals = (list: ConsolProposal[] | null): number =>
  (list ?? []).filter((p) => p.status === "pending").length;

/** Keeps the proposals read while the workspace runs: at once, after every write, and on a slow poll. */
export function useProposalsFeed(running: boolean): void {
  const version = useMemoryStore((s) => s.version);
  useEffect(() => {
    const st = useMemoryStore.getState();
    if (!running) {
      st.reset();
      return;
    }
    void st.loadProposals();
    const id = window.setInterval(() => void useMemoryStore.getState().loadProposals(), POLL_MS);
    return () => window.clearInterval(id);
  }, [running, version]);
}
