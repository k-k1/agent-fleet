// Client state for handoffs to another member (docs/log/77 / ADR 0057).
//
// Keeping the three roles apart is what makes this feature work (docs/log/77 §77.10):
//   notification = transient (may vanish once read) / badge = the backlog of undecided offers /
//   ledger = the sender's own history.
// Only the last two live here, and both read nothing but the CP's DB snapshot: showing them while
// the owner's Workspace is stopped is the requirement itself, so never route them through the
// owner the way the share list does.
import { create } from "zustand";
import { api } from "../../core/api/client.ts";

export type HandoffOfferStatus = "pending" | "accepted" | "declined" | "withdrawn" | "expired";

export interface HandoffOffer {
  id: string;
  /** Share catalogue id (the key that opens the recipient's shared view). */
  sessionId: string;
  /** Name of the source session (what the owner side displays and navigates to). */
  sessionName: string;
  recipientUserKey: string;
  ownerUserKey?: string;
  title: string;
  status: HandoffOfferStatus;
  branch?: string;
  repoRemote?: string;
  headSha?: string;
  createdAt?: string;
  expiresAt?: string;
  decidedAt?: string;
  acceptedSessionName?: string;
  /** Only the inbox carries the body: the recipient has to read it to decide whether to accept. */
  prompt?: string;
  sourceSessionKind?: string;
}

interface HandoffStore {
  /** Handoffs I sent (the ledger), including ones already decided. */
  owned: HandoffOffer[];
  /** Undecided handoffs sent to me (the inbox). */
  received: HandoffOffer[];
  refresh(): Promise<void>;
}

export const useHandoffStore = create<HandoffStore>((set) => ({
  owned: [],
  received: [],
  async refresh() {
    const [owned, received] = await Promise.all([
      api("api/session-handoff-offers").catch(() => null),
      api("api/session-handoff-offers/received").catch(() => null),
    ]);
    set({
      owned: owned && !owned.error && Array.isArray(owned.offers) ? owned.offers : [],
      received: received && !received.error && Array.isArray(received.offers) ? received.offers : [],
    });
  },
}));

/** The backlog is not cleared by "read" (docs/log/77 §77.10), so the raw count is the badge. */
export function pendingHandoffCount(offers: HandoffOffer[]): number {
  return offers.filter((o) => o.status === "pending").length;
}

/** Where a session stands right now. A session has at most one undecided offer (ADR 0057
 *  decision 10), so a pending one is the answer; otherwise return the most recent decision. */
export function offerForSession(offers: HandoffOffer[], sessionName: string): HandoffOffer | undefined {
  const mine = offers.filter((o) => o.sessionName === sessionName);
  return mine.find((o) => o.status === "pending") ?? mine[0];
}

// Polling is refcounted down to a single timer. The callers are the rail's sharing section and the
// shared view (opened straight from a notification; it subscribes itself so a detached tab with no
// rail still has the backlog), and one window.setInterval each would add two GETs per open pane.
let pollers = 0;
let pollTimer = 0;

export function startHandoffPolling(): () => void {
  const load = () => {
    if (!document.hidden) void useHandoffStore.getState().refresh();
  };
  load(); // give a new subscriber current data at once, rather than making it wait 15 seconds
  if (++pollers === 1) pollTimer = window.setInterval(load, 15000);
  let stopped = false;
  return () => {
    if (stopped) return;
    stopped = true;
    if (--pollers === 0) {
      window.clearInterval(pollTimer);
      pollTimer = 0;
    }
  };
}
