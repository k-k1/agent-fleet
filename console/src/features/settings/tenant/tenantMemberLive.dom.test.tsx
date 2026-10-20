// Live memory / CPU on running roster cards (#2389): fetched only for cards that are on screen,
// only while the panel is mounted and the document is visible, with the disk du opted out.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { MembersPanel } from "./tenantMembers.tsx";
import { LIVE_DWELL_MS } from "./memberLiveStats.ts";
import { setLocale } from "../../../lib/i18n/index.ts";

class FakeIO {
  static all: FakeIO[] = [];
  els = new Set<Element>();
  live = true;
  constructor(private cb: IntersectionObserverCallback) {
    FakeIO.all.push(this);
  }
  observe(el: Element) {
    this.els.add(el);
  }
  disconnect() {
    this.live = false;
    this.els.clear();
  }
  fire(el: Element, isIntersecting: boolean) {
    this.cb([{ target: el, isIntersecting } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
  }
}
const liveObservers = () => FakeIO.all.filter((o) => o.live);
// Make the card titled `t` visible or not.
async function show(t: string, v: boolean) {
  const card = [...document.querySelectorAll(".member-card")].find((c) => c.querySelector(".mc-title")?.textContent === t)!;
  const io = liveObservers().find((o) => o.els.has(card))!;
  await act(async () => io.fire(card, v));
}

const ROSTER = [
  { user_key: "u-alice", email: "alice@x.com", role: "member", state: "running", status: "active" },
  { user_key: "u-bob", email: "bob@x.com", role: "member", state: "running", status: "active" },
  { user_key: "u-carol", email: "carol@x.com", role: "member", state: "stopped", status: "active" },
];
const STATS = { running: true, mem_used: 2 * 1024 ** 3, mem_max: 8 * 1024 ** 3, cpu_pct: 12.4 };
const statsCalls = () => api.mock.calls.map((c) => c[0] as string).filter((p) => p.includes("/stats"));

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  api.mockImplementation((p: string) =>
    p === "api/admin/workspace-sizing"
      ? Promise.resolve({})
      : p.includes("/stats")
        ? Promise.resolve(STATS)
        : Promise.resolve({ members: ROSTER }),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MembersPanel slug="acme" isSuper={false} onOpenMember={() => {}} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const setHidden = (h: boolean) => {
  Object.defineProperty(document, "hidden", { configurable: true, get: () => h });
  document.dispatchEvent(new Event("visibilitychange"));
};

beforeEach(() => {
  setLocale("en");
  vi.useFakeTimers({ shouldAdvanceTime: false });
  vi.stubGlobal("IntersectionObserver", FakeIO);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  FakeIO.all = [];
  api.mockReset();
  setHidden(false);
  vi.unstubAllGlobals();
  vi.useRealTimers();
  setLocale("ja");
});

describe("live memory / CPU on roster cards", () => {
  it("makes no stats call until a running card becomes visible, then exactly one", async () => {
    await mount();
    expect(statsCalls()).toEqual([]);
    // Only running cards are observed.
    expect(liveObservers()).toHaveLength(2);
    // Only the two running cards carry the placeholder.
    expect(document.querySelectorAll(".mc-live.muted").length).toBe(2);
    await show("alice@x.com", true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_DWELL_MS);
    });
    expect(statsCalls()).toEqual(["api/admin/tenants/acme/members/u-alice/stats?disk=0"]);
    const alice = document.querySelector(".member-card")!;
    expect(alice.querySelector(".mc-live")?.textContent).toContain("Memory 2.0 / 8.0 GiB (25%)");
    expect(alice.querySelector(".mc-live")?.textContent).toContain("CPU 12%");
    // Every bar has its number beside it.
    expect(alice.querySelectorAll(".mc-live [role=progressbar]").length).toBe(2);
    // Stopped card: nothing, no placeholder.
    const carol = [...document.querySelectorAll(".member-card")].find((c) => c.textContent?.includes("carol"))!;
    expect(carol.querySelector(".mc-live")).toBeNull();
  });

  it("does not fetch a card that only flashes through the viewport", async () => {
    await mount();
    await show("alice@x.com", true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_DWELL_MS - 100);
    });
    await show("alice@x.com", false);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(statsCalls()).toEqual([]);
  });

  it("keeps the placeholder text until the first reading lands", async () => {
    await mount();
    const bob = [...document.querySelectorAll(".member-card")].find((c) => c.textContent?.includes("bob"))!;
    expect(bob.querySelector(".mc-live.muted")?.textContent).toBe("Live usage: reading…");
  });

  it("stops polling a card that scrolls out, pauses while hidden, and ends on unmount", async () => {
    await mount();
    await show("alice@x.com", true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_DWELL_MS);
    });
    expect(statsCalls()).toHaveLength(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_100);
    });
    expect(statsCalls()).toHaveLength(2);

    setHidden(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(statsCalls()).toHaveLength(2);
    setHidden(false);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_DWELL_MS);
    });
    expect(statsCalls()).toHaveLength(3);

    await show("alice@x.com", false);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(statsCalls()).toHaveLength(3);

    await show("alice@x.com", true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_DWELL_MS);
    });
    expect(statsCalls()).toHaveLength(4); // due again after the minute away
    act(() => root!.unmount());
    root = null;
    await vi.advanceTimersByTimeAsync(120_000);
    expect(statsCalls()).toHaveLength(4);
    expect(liveObservers()).toHaveLength(0);
  });

  it("stops when the filter hides the card", async () => {
    await mount();
    await show("alice@x.com", true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_DWELL_MS);
    });
    expect(statsCalls()).toHaveLength(1);
    const stopped = [...document.querySelectorAll(".member-filter button")].find((b) => b.textContent?.startsWith("Stopped"))!;
    expect(stopped).toBeTruthy();
    await act(async () => (stopped as HTMLElement).click());
    expect(document.querySelectorAll(".member-card")).toHaveLength(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(120_000);
    });
    expect(statsCalls()).toHaveLength(1);
  });
});
