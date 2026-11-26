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

/** Where the recipient's take of a pending offer stands (the CP's store.HandoffTakeState):
 *  starting = they are launching; confirming = the launch was sent and may still be running;
 *  unknown = it was sent longer ago than the take window and its result was never heard, which
 *  only the recipient can settle (launch again, or give the offer back). Absent: no take holds it. */
export type HandoffTakeState = "starting" | "confirming" | "unknown";

export interface HandoffOffer {
  id: string;
  /** Share catalogue id (the key that opens the recipient's shared view). */
  sessionId: string;
  /** Name of the source session (what the owner side displays and navigates to). */
  sessionName: string;
  recipientUserKey: string;
  /** The sender's routes only: the recipient's address as the picker showed it. Absent from an
   *  older CP, or for a member with no address or one who left; use recipientLabel. */
  recipientEmail?: string;
  ownerUserKey?: string;
  /** The inbox only: the sender's address, absent as recipientEmail is; use senderLabel. */
  ownerEmail?: string;
  title: string;
  status: HandoffOfferStatus;
  branch?: string;
  repoRemote?: string;
  headSha?: string;
  createdAt?: string;
  expiresAt?: string;
  decidedAt?: string;
  acceptedSessionName?: string;
  takeState?: HandoffTakeState;
  /** The inbox only: an opaque name for the recipient's confirmed launch, sent back with a
   *  give-back so the confirmation cannot release a later launch. */
  takeRev?: string;
  /** The inbox only: listed just so the recipient can give back a launch that still holds an
   *  offer no longer shared with them, or whose session was archived. No body or coordinates. */
  recoveryOnly?: boolean;
  /** The inbox only: the offer comes from a session that ran in the sender's space, whose files
   *  stay in their home. There is no repository to pick: the launch starts in a new space. */
  sourceSpace?: boolean;
  /** Only the inbox carries the body: the recipient has to read it to decide whether to accept. */
  prompt?: string;
  sourceSessionKind?: string;
}

interface HandoffStore {
  /** Handoffs I sent (the ledger), including ones already decided. */
  owned: HandoffOffer[];
  /** Undecided handoffs sent to me (the inbox). */
  received: HandoffOffer[];
  /** When `received` was last read from the CP (ms epoch), 0 when never or the last read failed. */
  receivedAt: number;
  refresh(): Promise<void>;
}

export const useHandoffStore = create<HandoffStore>((set) => ({
  owned: [],
  received: [],
  receivedAt: 0,
  async refresh() {
    const gen = generation;
    const [owned] = await Promise.all([api("api/session-handoff-offers").catch(() => null), loadReceived()]);
    if (gen !== generation) return;
    set({ owned: owned && !owned.error && Array.isArray(owned.offers) ? owned.offers : [] });
  },
}));

// Bumped by resetHandoffs: an answer asked under an earlier tenant or account is dropped, or a
// read still in flight at a switch would put the previous tenant's offers back on screen.
let generation = 0;

/** Forgets every offer at a tenant or account switch and reads again under the new one. The
 *  offers carry titles, prompts and addresses of the previous tenant, and a fresh receivedAt
 *  would otherwise keep refreshReceivedIfStale from asking again. */
export function resetHandoffs(): void {
  generation++;
  receivedFlight = null;
  useHandoffStore.setState({ owned: [], received: [], receivedAt: 0 });
  void useHandoffStore.getState().refresh();
}

// The inbox is read by the poll and by a notification click, and both go through loadReceived so
// that they cannot undo each other: a read joins the one in flight instead of racing it, so a
// response never lands after a newer one. With two requests in the air a slow one could put back
// a list from before an offer arrived, and the badge would lose it.
let receivedFlight: Promise<HandoffOffer[] | null> | null = null;

/** Reads the inbox, or joins the read already in flight. Resolves to null when it failed; a
 *  failed read keeps the last list, since the badge is the inventory and a dropped connection
 *  is no proof that the offers are gone. */
function loadReceived(): Promise<HandoffOffer[] | null> {
  if (receivedFlight) return receivedFlight;
  const gen = generation;
  const flight: Promise<HandoffOffer[] | null> = api("api/session-handoff-offers/received")
    .catch(() => null)
    .then((d) => {
      if (gen !== generation) return null;
      const offers: HandoffOffer[] | null = d && !d.error && Array.isArray(d.offers) ? d.offers : null;
      useHandoffStore.setState(offers ? { received: offers, receivedAt: Date.now() } : { receivedAt: 0 });
      return offers;
    })
    .finally(() => {
      if (receivedFlight === flight) receivedFlight = null;
    });
  receivedFlight = flight;
  return flight;
}

/** How long a read inbox is trusted to say "pending" without asking again: one poll interval.
 *  The poll stops while the tab is hidden, which is exactly when an OS notification is clicked,
 *  so an older list is read again rather than trusted. */
const RECEIVED_FRESH_MS = 15000;

/** The recipient as the sender's picker named them: the e-mail, the user key when there is none. */
export function recipientLabel(o: HandoffOffer): string {
  return o.recipientEmail || o.recipientUserKey;
}

/** The sender as the recipient's shared-sessions list names them (ownerLabel in ./store.ts). */
export function senderLabel(o: HandoffOffer): string {
  return o.ownerEmail || o.ownerUserKey || "";
}

/** Reads the inbox unless a read within the poll interval already answered, joining one in
 *  flight. For a surface that opens on demand (the top-bar inbox): the poll keeps the list
 *  current, and opening must not add a second request beside it. */
export function refreshReceivedIfStale(): void {
  const at = useHandoffStore.getState().receivedAt;
  if (at && Date.now() - at < RECEIVED_FRESH_MS) return;
  void loadReceived();
}

/** The backlog is not cleared by "read" (docs/log/77 §77.10), so the raw count is the badge. */
export function pendingHandoffCount(offers: HandoffOffer[]): number {
  return offers.filter((o) => o.status === "pending").length;
}

/** Whether a received offer can still be decided: "pending" when the inbox lists it, "closed"
 *  when a current inbox does not, "unknown" when the inbox could not be read. The inbox route
 *  returns only pending offers whose share is alive, so "closed" folds accepted, declined,
 *  withdrawn, expired and unshared together; telling them apart would need a route the
 *  recipient does not have. A list read within the poll interval that names the offer is
 *  trusted; otherwise the inbox is read. A read that was already in flight, whoever started it,
 *  may have been answered before the offer was created (the notification is pushed), so a click
 *  that joined one and does not find its offer reads once more; clicks doing so share that read
 *  too. A read this click started itself came after its notification, and its answer stands. */
export async function receivedOfferState(offerId: string): Promise<"pending" | "recovery" | "closed" | "unknown"> {
  const listed = (offers: HandoffOffer[]) => offers.some((o) => o.id === offerId && o.status === "pending");
  // A recovery row is pending only for the give-back: there is nothing in the shared view to open.
  const recovery = (offers: HandoffOffer[]) => offers.some((o) => o.id === offerId && o.recoveryOnly);
  const s = useHandoffStore.getState();
  if (s.receivedAt && Date.now() - s.receivedAt < RECEIVED_FRESH_MS && listed(s.received))
    return recovery(s.received) ? "recovery" : "pending";
  const joined = receivedFlight !== null;
  let offers = await loadReceived();
  if (offers && joined && !listed(offers)) offers = await loadReceived();
  if (!offers) return "unknown";
  if (!listed(offers)) return "closed";
  return recovery(offers) ? "recovery" : "pending";
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
