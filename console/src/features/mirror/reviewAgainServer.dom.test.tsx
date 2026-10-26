// "Review again" keeps the planner→reviewer link at the Agent (Session.planReviewers), so a
// device that never launched the reviewer still offers it. localStorage stays the fallback.
import { describe, it, expect, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";

const setLink = vi.hoisted(() => vi.fn());
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  sessionPlanReviewerSet: setLink,
}));

import {
  forgetReviewer, hasRememberedReviewer, liveReviewer, rememberReviewer, rememberedReviewer, reloadReviewersForTest, useReviewer,
} from "./reviewAgain.ts";
import { useSessionsStore } from "../sessions/store.ts";
import type { Session } from "../../types/session.ts";

const sess = (o: Partial<Session> & { name: string }) => ({ kind: "codex", alive: true, resumable: true, ...o }) as Session;
const planner = (links: { path: string; reviewer: string }[]) => sess({ name: "planner", kind: "claude", planReviewers: links });

beforeEach(() => {
  localStorage.clear();
  setLink.mockReset().mockResolvedValue(true);
  useSessionsStore.setState({ sessions: [], refresh: async () => {} });
  reloadReviewersForTest();
});

describe("reviewer link held by the Agent", () => {
  it("is offered on a browser whose localStorage never saw the launch", () => {
    const list = [planner([{ path: "/p/a.md", reviewer: "rev" }]), sess({ name: "rev" })];
    expect(rememberedReviewer("planner", "/p/a.md", list)).toBe("rev");
    expect(hasRememberedReviewer("planner", list)).toBe(true);
    expect(liveReviewer("planner", "/p/a.md", list)?.name).toBe("rev");
    // another plan of the same planner has no link
    expect(liveReviewer("planner", "/p/b.md", list)).toBeNull();
  });

  it("is never offered once the reviewer is gone from the list", () => {
    const list = [planner([{ path: "/p/a.md", reviewer: "rev" }])];
    expect(liveReviewer("planner", "/p/a.md", list)).toBeNull();
  });

  it("falls back to localStorage when the Agent returns no record (older Agent)", () => {
    setLink.mockResolvedValue(false);
    rememberReviewer("planner", "/p/a.md", "rev");
    reloadReviewersForTest();
    expect(rememberedReviewer("planner", "/p/a.md", [sess({ name: "planner" })])).toBe("rev"); // no planReviewers field
  });

  it("writes the link to the Agent, and an unlink too", () => {
    rememberReviewer("planner", "/p/a.md", "rev");
    expect(setLink).toHaveBeenCalledWith("planner", "/p/a.md", "rev");
    forgetReviewer("planner", "/p/a.md");
    expect(setLink).toHaveBeenLastCalledWith("planner", "/p/a.md", "");
  });

  it("keeps this tab's new launch when a refresh returns the Agent's older list", async () => {
    const old = [planner([{ path: "/p/a.md", reviewer: "rev-old" }])];
    useSessionsStore.setState({ sessions: old, refresh: async () => {} }); // refresh resolves, list stays stale
    rememberReviewer("planner", "/p/a.md", "rev-new");
    await vi.waitFor(() => expect(setLink).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 20));
    expect(rememberedReviewer("planner", "/p/a.md", old)).toBe("rev-new");
  });

  it("settles on the Agent's record once a later list shows the written value", async () => {
    useSessionsStore.setState({ sessions: [planner([{ path: "/p/a.md", reviewer: "rev-old" }])] });
    rememberReviewer("planner", "/p/a.md", "rev-new");
    await new Promise((r) => setTimeout(r, 20));
    // another device then relinks: the pending marker was cleared by the matching list
    useSessionsStore.setState({ sessions: [planner([{ path: "/p/a.md", reviewer: "rev-new" }])] });
    useSessionsStore.setState({ sessions: [planner([{ path: "/p/a.md", reviewer: "rev-third" }])] });
    expect(rememberedReviewer("planner", "/p/a.md")).toBe("rev-third");
  });

  it("drops a cached link the Agent once confirmed and no longer holds (unlinked elsewhere)", () => {
    localStorage.setItem("af.plan-reviewers", JSON.stringify({ "planner\u0000/p/a.md": "rev-old" }));
    reloadReviewersForTest();
    const live = [sess({ name: "rev-old" })];
    // before the Agent ever listed it, the cache is a legacy link and is offered
    expect(liveReviewer("planner", "/p/a.md", [planner([]), ...live])?.name).toBe("rev-old");
    useSessionsStore.setState({ sessions: [planner([{ path: "/p/a.md", reviewer: "rev-old" }]), ...live] }); // confirmed
    const gone = [planner([]), ...live]; // unlinked on another device
    useSessionsStore.setState({ sessions: gone });
    expect(liveReviewer("planner", "/p/a.md", gone)).toBeNull();
    reloadReviewersForTest(); // and it stays gone after a reload
    expect(liveReviewer("planner", "/p/a.md", gone)).toBeNull();
  });

  it("an unlink hides an Agent record this tab has not seen go away yet", () => {
    const list = [planner([{ path: "/p/a.md", reviewer: "rev" }])];
    forgetReviewer("planner", "/p/a.md");
    expect(rememberedReviewer("planner", "/p/a.md", list)).toBe("");
  });

  it("keeps this tab's choice when the write failed", async () => {
    setLink.mockResolvedValue(false);
    const old = [planner([{ path: "/p/a.md", reviewer: "rev-old" }])];
    rememberReviewer("planner", "/p/a.md", "rev-new");
    await Promise.resolve();
    await Promise.resolve();
    expect(rememberedReviewer("planner", "/p/a.md", old)).toBe("rev-new");
  });

  it("does not fall back to an older reviewer when the Agent is unreachable (mirror row without the field)", () => {
    localStorage.setItem("af.plan-reviewers", JSON.stringify({ "planner\u0000/p/a.md": "rev-old" }));
    reloadReviewersForTest();
    const live = [sess({ name: "rev-old" }), sess({ name: "rev-new" })];
    useSessionsStore.setState({ sessions: [planner([{ path: "/p/a.md", reviewer: "rev-old" }]), ...live] });
    const list2 = [planner([{ path: "/p/a.md", reviewer: "rev-new" }]), ...live];
    useSessionsStore.setState({ sessions: list2 });
    const mirror = [sess({ name: "planner", kind: "claude" }), ...live]; // no planReviewers
    useSessionsStore.setState({ sessions: mirror });
    expect(liveReviewer("planner", "/p/a.md", mirror)?.name).toBe("rev-new");
  });

  it("syncs the confirmation set between tabs, so a link confirmed in another tab counts as confirmed here", () => {
    const live = [sess({ name: "rev-old" })];
    useSessionsStore.setState({ sessions: [planner([]), ...live] });
    // another tab launched rev-old and had the Agent confirm it
    localStorage.setItem("af.plan-reviewers", JSON.stringify({ "planner\u0000/p/a.md": "rev-old" }));
    window.dispatchEvent(new StorageEvent("storage", { key: "af.plan-reviewers" }));
    localStorage.setItem("af.plan-reviewers-synced", JSON.stringify(["planner\u0000/p/a.md"]));
    window.dispatchEvent(new StorageEvent("storage", { key: "af.plan-reviewers-synced" }));
    // a list without the link now means it was unlinked elsewhere
    const gone = [planner([]), ...live];
    useSessionsStore.setState({ sessions: [...gone] });
    expect(liveReviewer("planner", "/p/a.md", gone)).toBeNull();
  });

  it("shows a new launch or an unlink at once, while the POST is still unsettled", async () => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    const live = [sess({ name: "rev-old" }), sess({ name: "rev-new" })];
    useSessionsStore.setState({ sessions: [planner([{ path: "/p/a.md", reviewer: "rev-old" }]), ...live] });
    setLink.mockImplementation(() => new Promise(() => {})); // never settles
    const host = document.createElement("div");
    const root = createRoot(host);
    const Probe = () => <span>{useReviewer("planner", "/p/a.md")?.name ?? "none"}</span>;
    await act(async () => root.render(<Probe />));
    expect(host.textContent).toBe("rev-old");
    await act(async () => rememberReviewer("planner", "/p/a.md", "rev-new"));
    expect(host.textContent).toBe("rev-new");
    await act(async () => forgetReviewer("planner", "/p/a.md"));
    expect(host.textContent).toBe("none");
    await act(async () => root.unmount());
  });
});
