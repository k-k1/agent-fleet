// Where a handoff-offer notification leads (docs/log/77 §77.10, run-sheet step H-16 on #1960).
//
// A pending offer opens the shared view, whose banner accepts it. An offer that is no longer
// pending (unshared, taken over, declined, withdrawn, expired) has nowhere to go: the click says
// so instead of landing on the shared view's "no access" (#2041), and the caller marks it read.
//
// In the dom project because store.ts pulls in core/api/client.ts, which touches localStorage at
// import time.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { openNotificationTarget, type FleetNotification } from "./store.ts";
import { openSharedSession } from "../sharing/open.ts";
import { useHandoffStore, type HandoffOffer } from "../sharing/handoffStore.ts";
import { api } from "../../core/api/client.ts";
import { toast } from "../../ui/toast.ts";
import { t } from "../../lib/i18n/index.ts";

vi.mock("../sharing/open.ts", () => ({ openSharedSession: vi.fn() }));
vi.mock("../../ui/toast.ts", () => ({ toast: vi.fn() }));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  apiJSON: vi.fn(),
  api: vi.fn(async () => ({})),
}));

const offerNotification = (): FleetNotification => ({
  seq: 1, id: "handoff-offer-o1-m2", kind: "handoff-offer",
  target: { type: "shared-session", id: "cat-1" },
  displayName: "Carry on",
  payload: { offerId: "o1", catalogId: "cat-1", sessionName: "session-1" },
  createdAt: "2026-10-13T00:00:00Z", seen: false,
});

const offer = (id: string): HandoffOffer => ({
  id, sessionId: "cat-1", sessionName: "session-1", recipientUserKey: "b", title: "Carry on", status: "pending",
});

const RECEIVED = "api/session-handoff-offers/received";
const receivedCalls = () => vi.mocked(api).mock.calls.filter((c) => c[0] === RECEIVED).length;

// A hand-released response, so the order in which concurrent reads finish is the test's choice.
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

describe("a handoff-offer notification", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api).mockImplementation(async () => ({}));
    useHandoffStore.setState({ owned: [], received: [], receivedAt: 0 });
  });

  it("opens the shared view of a pending offer a current inbox already holds, without asking the CP", async () => {
    useHandoffStore.setState({ received: [offer("o1")], receivedAt: Date.now() });
    const r = await openNotificationTarget(offerNotification(), false);
    expect(r.opened).toBe(true);
    expect(openSharedSession).toHaveBeenCalledWith("cat-1", false);
    expect(api).not.toHaveBeenCalled();
  });

  it("does not trust an inbox older than the poll interval: an unshare since then still reads as closed", async () => {
    // The poll stops while the tab is hidden, which is when an OS notification gets clicked.
    useHandoffStore.setState({ received: [offer("o1")], receivedAt: Date.now() - 60_000 });
    vi.mocked(api).mockResolvedValue({ offers: [] });
    const r = await openNotificationTarget(offerNotification(), false);
    expect(receivedCalls()).toBe(1);
    expect(r).toEqual({ opened: false, handoffClosed: true });
    expect(openSharedSession).not.toHaveBeenCalled();
    expect(useHandoffStore.getState().received).toEqual([]);
  });

  it("re-reads the inbox once when the store predates the offer, and opens it when it is pending", async () => {
    vi.mocked(api).mockResolvedValue({ offers: [offer("o1")] });
    const r = await openNotificationTarget(offerNotification(), true);
    expect(api).toHaveBeenCalledTimes(1);
    expect(api).toHaveBeenCalledWith(RECEIVED);
    expect(r.opened).toBe(true);
    expect(openSharedSession).toHaveBeenCalledWith("cat-1", true);
    expect(useHandoffStore.getState().received.map((o) => o.id)).toEqual(["o1"]);
  });

  it("says the handoff is closed instead of opening a no-access view once the inbox no longer lists it (H-16)", async () => {
    // The inbox drops every offer that is not pending or whose share was revoked.
    vi.mocked(api).mockResolvedValue({ offers: [offer("o2")] });
    const r = await openNotificationTarget(offerNotification(), false);
    expect(r).toEqual({ opened: false, handoffClosed: true });
    expect(openSharedSession).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(t("noti.handoff_closed"), { kind: "warn" });
  });

  it("points to the inbox for a recovery row instead of opening the shared view (#2058)", async () => {
    // Unshared or archived while the recipient's launch still holds it: the only thing left to
    // do is the give-back, which lives in the inbox.
    vi.mocked(api).mockResolvedValue({ offers: [{ ...offer("o1"), recoveryOnly: true, takeState: "unknown" }] });
    const r = await openNotificationTarget(offerNotification(), false);
    expect(r).toEqual({ opened: false, handoffClosed: true });
    expect(openSharedSession).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(t("noti.handoff_recovery", { inbox: t("inbox.handoffs") }), { kind: "warn" });
    // #2085 moved received handoffs to the top-bar inbox; the shared sessions section is no longer the place.
    const text = String(vi.mocked(toast).mock.calls.at(-1)?.[0]);
    expect(text).toContain(t("inbox.handoffs"));
    expect(text).not.toMatch(/shared sessions section|共有セッション欄/);
  });

  it("falls back to the shared view when the inbox cannot be read, rather than calling it closed", async () => {
    for (const fail of [() => Promise.resolve({ error: { code: "internal" } }), () => Promise.reject(new Error("offline"))]) {
      vi.clearAllMocks();
      useHandoffStore.setState({ received: [offer("o9")], receivedAt: 0 });
      vi.mocked(api).mockImplementation(fail);
      const r = await openNotificationTarget(offerNotification(), false);
      expect(r.opened).toBe(true);
      expect(openSharedSession).toHaveBeenCalledWith("cat-1", false);
      expect(toast).not.toHaveBeenCalled();
      // A failed read is no proof the backlog is gone: the badge keeps what it had.
      expect(useHandoffStore.getState().received.map((o) => o.id)).toEqual(["o9"]);
    }
  });

  it("shares inbox reads between clicks in quick succession, with at most one shared re-read", async () => {
    const d = deferred<unknown>();
    let n = 0;
    vi.mocked(api).mockImplementation(() => (++n === 1 ? d.promise : Promise.resolve({ offers: [offer("o2")] })) as never);
    const o2 = { ...offerNotification(), id: "handoff-offer-o2-m2", payload: { offerId: "o2", catalogId: "cat-2" } };
    const clicks = [openNotificationTarget(offerNotification(), false), openNotificationTarget(offerNotification(), false), openNotificationTarget(o2, false)];
    d.resolve({ offers: [offer("o2")] });
    const rs = await Promise.all(clicks);
    // The first read, plus one re-read for the second click, which joined a read it did not start.
    expect(receivedCalls()).toBe(2);
    expect(rs.map((r) => r.opened)).toEqual([false, false, true]);
  });

  it("a click that joins another click's read answered before its offer existed reads again (F4)", async () => {
    // Click A starts a read; the CP answers [] before offer B is created and pushed; B's click joins.
    const first = deferred<unknown>();
    let n = 0;
    vi.mocked(api).mockImplementation(() => (++n === 1 ? first.promise : Promise.resolve({ offers: [offer("o1")] })) as never);
    const oldOne = { ...offerNotification(), id: "handoff-offer-o0-m2", payload: { offerId: "o0", catalogId: "cat-0" } };
    const clickA = openNotificationTarget(oldOne, false);
    const clickB1 = openNotificationTarget(offerNotification(), false);
    const clickB2 = openNotificationTarget(offerNotification(), false);
    first.resolve({ offers: [] });
    const [a, b1, b2] = await Promise.all([clickA, clickB1, clickB2]);
    expect(a).toEqual({ opened: false, handoffClosed: true });
    expect(b1.opened).toBe(true);
    expect(b2.opened).toBe(true);
    // Both joiners share the one re-read.
    expect(receivedCalls()).toBe(2);
  });

  it("reads again after a failed read instead of keeping the failure", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ offers: [] });
    expect((await openNotificationTarget(offerNotification(), false)).opened).toBe(true);
    expect(await openNotificationTarget(offerNotification(), false)).toEqual({ opened: false, handoffClosed: true });
    expect(receivedCalls()).toBe(2);
  });

  it("a poll that starts while a click reads joins it, so a late response cannot overwrite the newer list", async () => {
    const click = deferred<unknown>();
    vi.mocked(api).mockImplementation((path) => (path === RECEIVED ? click.promise : Promise.resolve({ offers: [] })) as never);
    const clicked = openNotificationTarget(offerNotification(), false);
    const polled = useHandoffStore.getState().refresh();
    click.resolve({ offers: [offer("o1"), offer("new")] });
    await Promise.all([clicked, polled]);
    expect(receivedCalls()).toBe(1);
    expect(useHandoffStore.getState().received.map((o) => o.id)).toEqual(["o1", "new"]);
    expect(openSharedSession).toHaveBeenCalledWith("cat-1", false);
  });

  it("a click that joins a poll which predates its offer reads once more rather than calling it closed", async () => {
    const poll = deferred<unknown>();
    let n = 0;
    vi.mocked(api).mockImplementation((path) =>
      (path !== RECEIVED ? Promise.resolve({ offers: [] }) : ++n === 1 ? poll.promise : Promise.resolve({ offers: [offer("o1")] })) as never);
    const polled = useHandoffStore.getState().refresh();
    const clicked = openNotificationTarget(offerNotification(), false);
    poll.resolve({ offers: [] });
    const [r] = await Promise.all([clicked, polled]);
    expect(receivedCalls()).toBe(2);
    expect(r.opened).toBe(true);
    expect(useHandoffStore.getState().received.map((o) => o.id)).toEqual(["o1"]);
  });
});
