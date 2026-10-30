// The member roster as cards (#2385 phase 1): a state badge with a word, needs-attention first,
// filter and search, and still one GET …/members for the whole page.
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
import { setLocale } from "../../../lib/i18n/index.ts";
import { en } from "../../../lib/i18n/locales/en.ts";

const AUTO_STOP = { kind: "start-deadline", phase: "x", limit_minutes: 30, stopped_at: "2026-10-01T10:00:00Z" };
const ROSTER = [
  {
    user_key: "u-alice", email: "alice@x.com", role: "member", state: "running", status: "active", max_sessions: 4,
    last_active_at: new Date().toISOString(),
    sessions: { live: 3, total: 5, observed_at: new Date(Date.now() - 2 * 60_000).toISOString() },
  },
  {
    user_key: "u-bob", email: "bob@x.com", role: "member", state: "stopped", status: "active",
    last_active_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
    sessions: { live: 0, total: 4, observed_at: new Date(Date.now() - 3 * 3600_000).toISOString() },
  },
  {
    // The CP omits sessions when the mirror read failed; last_active_at and a limit remain.
    user_key: "u-frank", email: "frank@x.com", role: "member", state: "stopped", status: "active", max_sessions: 3,
    last_active_at: new Date(Date.now() - 86400_000).toISOString(),
  },
  {
    user_key: "u-gina", email: "gina@x.com", role: "member", state: "stopped", status: "active",
    disk: { used: 19 * 1024 ** 3, total: 20 * 1024 ** 3, observed_at: new Date(Date.now() - 2 * 86400_000).toISOString() },
  },
  { user_key: "u-hank", email: "hank@x.com", role: "member", state: "running", status: "active", disk: { used: 3 * 1024 ** 3 } },
  { user_key: "u-carol", email: "carol@x.com", role: "member", state: "stopped", status: "active", auto_stop: AUTO_STOP },
  { user_key: "u-dave", email: "dave@x.com", role: "member", state: "none", status: "active", has_workspace: false },
  { user_key: "u-erin", email: "erin@x.com", role: "member", state: "stopped", status: "removed" },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const onOpen = vi.fn();

async function mount() {
  api.mockImplementation((p: string) =>
    p === "api/admin/workspace-sizing" ? Promise.resolve({}) : Promise.resolve({ members: ROSTER }),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MembersPanel slug="acme" isSuper={false} onOpenMember={onOpen} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const cards = () => Array.from(document.querySelectorAll<HTMLButtonElement>(".member-card"));
const titles = () => cards().map((c) => c.querySelector(".mc-title")?.textContent);
const badge = (c: Element) => c.querySelector(".member-state")?.textContent;

beforeEach(() => setLocale("en"));

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
  setLocale("ja");
});

describe("member cards", () => {
  it("says each state in words, not by colour alone", async () => {
    await mount();
    const byTitle = new Map(cards().map((c) => [c.querySelector(".mc-title")?.textContent, badge(c)]));
    expect(byTitle.get("alice@x.com")).toBe(en["admin.roster_state_running"]);
    expect(byTitle.get("bob@x.com")).toBe(en["admin.roster_state_stopped"]);
    expect(byTitle.get("carol@x.com")).toBe(en["admin.roster_state_auto_stopped"]);
    expect(byTitle.get("dave@x.com")).toBe(en["admin.roster_state_never"]);
    expect(byTitle.get("erin@x.com")).toBe(en["admin.roster_state_removed"]);
  });

  it("puts the member who needs attention first and counts them", async () => {
    await mount();
    // gina's disk is at 95 %: a full disk leads, then the other reasons (carol's auto-stop).
    expect(titles().slice(0, 2)).toEqual(["gina@x.com", "carol@x.com"]);
    expect(cards()[0].classList.contains("attn")).toBe(true);
    const summary = document.querySelector(".member-summary")?.textContent || "";
    expect(summary).toContain("2 of 8 running");
    expect(summary).toContain("2 need attention");
  });

  it("filters and searches", async () => {
    await mount();
    const filter = (label: string) =>
      Array.from(document.querySelectorAll<HTMLButtonElement>(".member-filter .seg-btn")).find((b) =>
        b.textContent?.startsWith(label),
      )!;
    await act(async () => filter(en["admin.roster_filter_removed"]).click());
    expect(titles()).toEqual(["erin@x.com"]);
    await act(async () => filter(en["admin.roster_filter_all"]).click());
    const search = document.querySelector<HTMLInputElement>(".member-search")!;
    await act(async () => {
      // React tracks the value through the native setter; assigning .value alone is ignored.
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, "u-bo");
      search.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(titles()).toEqual(["bob@x.com"]);
  });

  it("opens the member detail from a card", async () => {
    await mount();
    await act(async () => cards()[0].click());
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ user_key: "u-gina" }));
  });

  it("shows sessions against the limit with the numbers as text, and last active on a stopped card", async () => {
    await mount();
    const card = (t: string) => cards().find((c) => c.querySelector(".mc-title")?.textContent === t)!;
    const alice = card("alice@x.com");
    expect(alice.querySelector(".mc-sess-text")?.textContent).toBe("3 live / 5 total (limit 4)");
    const bar = alice.querySelector<HTMLElement>(".mc-bar")!;
    expect(bar.getAttribute("aria-valuenow")).toBe("3");
    expect(bar.getAttribute("aria-valuemax")).toBe("4");
    expect(alice.textContent).not.toContain("Last active");
    const bob = card("bob@x.com");
    expect(bob.querySelector(".mc-sess-text")?.textContent).toBe("0 live / 4 total");
    expect(bob.querySelector(".mc-bar")).toBeNull(); // no limit set: no bar, the numbers remain
    expect(bob.querySelector(".mc-last-active")?.textContent).toMatch(/^Last active /);
    // No sessions but a limit and a last-active time: still renders, without a bar.
    const frank = card("frank@x.com");
    expect(frank.querySelector(".mc-bar")).toBeNull();
    expect(frank.querySelector(".mc-last-active")?.textContent).toMatch(/^Last active /);
    // A member with no mirror and no activity gets no line at all.
    expect(card("dave@x.com").querySelector(".mc-sessions")).toBeNull();
  });

  it("draws the disk as a gauge with its numbers, its age on a stopped card, and tolerates missing fields", async () => {
    await mount();
    const card = (t: string) => cards().find((c) => c.querySelector(".mc-title")?.textContent === t)!;
    const gina = card("gina@x.com");
    expect(gina.querySelector(".mc-disk-text")?.textContent).toBe("Disk 19.0 / 20.0 GiB (95%)");
    expect(gina.querySelector(".mc-disk")?.classList.contains("error")).toBe(true);
    expect(gina.querySelector(".mc-disk")?.textContent).toContain("as of");
    expect(gina.classList.contains("attn")).toBe(true);
    // No total (no quota): the used figure alone, no bar, no level.
    const hank = card("hank@x.com");
    expect(hank.querySelector(".mc-disk-text")?.textContent).toBe("Disk 3.0 GiB used");
    expect(hank.querySelector(".mc-disk .mc-bar")).toBeNull();
    expect(hank.classList.contains("attn")).toBe(false);
    // Never measured: no disk line.
    expect(card("dave@x.com").querySelector(".mc-disk")).toBeNull();
  });

  // Thirty cards must not mean thirty Agent calls: the page reads one roster response.
  // An observer that never reports a card on screen: nothing is visible yet, so nothing is fetched
  // (tenantMemberLive.dom.test.tsx covers the cards that do become visible).
  it("makes no per-member calls", async () => {
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
    await mount();
    expect(api.mock.calls.map((c) => c[0]).sort()).toEqual(["api/admin/tenants/acme/members", "api/admin/workspace-sizing"]);
    vi.unstubAllGlobals();
  });
});
