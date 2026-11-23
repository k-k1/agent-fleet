// The activity bar's context menu (ADR 0142 decision 11): how it opens, what it lists and locks,
// that the choice reaches the settings and the swipe, and the dot for a hidden view that waits.
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { api } = vi.hoisted(() => ({ api: vi.fn((..._args: unknown[]) => Promise.resolve({})) }));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
  memoList: () => Promise.resolve([]),
}));

import { ActivityBar } from "./ActivityBar.tsx";
import { useLeftRail } from "../core/store/leftRail.ts";
import type { RailView } from "../core/store/leftRail.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { useNotificationStore } from "../features/notifications/store.ts";
import { useHandoffStore } from "../features/sharing/handoffStore.ts";
import type { HandoffOffer } from "../features/sharing/handoffStore.ts";
import { getSettings, hydrateUIPrefs, setSettings } from "../lib/settings.ts";
import { LONG_PRESS_MS } from "../lib/longPress.ts";
import { BAR_STEP_DIST, BAR_SWIPE_UP_IS_NEXT } from "./barSwipe.ts";
import type { Session } from "../types/session.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

// The saved choice has been read, as it has by the time anyone opens the menu.
beforeAll(async () => {
  await hydrateUIPrefs();
});

beforeEach(() => {
  localStorage.clear();
  setSettings({ railViewsOff: [] });
  useLeftRail.setState({ open: true, view: "sessions", swipeOverlay: false });
  useSessionsStore.setState({ sessions: [] });
  useNotificationStore.setState({ items: [] });
  useHandoffStore.setState({ owned: [], received: [], receivedAt: 0 });
  api.mockImplementation(() => Promise.resolve({}));
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  document.querySelectorAll(".actbar-menu").forEach((m) => m.remove());
  vi.useRealTimers();
});

type Props = Partial<Parameters<typeof ActivityBar>[0]>;
const Shell = (props: Props) => {
  const view = useLeftRail((s) => s.view);
  return <ActivityBar view={view} shown foldable={false} schedulerEnabled {...props} />;
};
async function mount(props: Props = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Shell {...props} />));
}
const bar = () => host!.querySelector<HTMLElement>(".actbar")!;
const icons = () => [...host!.querySelectorAll<HTMLElement>(".actbar-btn[data-rail-view]")].map((b) => b.dataset.railView);
const btn = (v: RailView) => host!.querySelector<HTMLButtonElement>(`.actbar-btn[data-rail-view="${v}"]`)!;
const menu = () => document.querySelector<HTMLElement>(".actbar-menu");
const row = (v: RailView) => menu()!.querySelector<HTMLButtonElement>(`[data-menu-view="${v}"]`)!;
const rightClick = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 30, clientY: 80 })));

describe("opening the menu", () => {
  it("opens on a right-click on the bar and lists every view with a check for the shown ones", async () => {
    await mount();
    await rightClick(btn("memo"));
    expect(menu()).not.toBeNull();
    const rows = [...menu()!.querySelectorAll<HTMLElement>("[data-menu-view]")].map((r) => r.dataset.menuView);
    expect(rows).toEqual(["sessions", "shared", "assistant", "workitems", "memo", "autorun", "memory", "schedules", "files"]);
    expect(row("memo").getAttribute("aria-checked")).toBe("true");
    expect(row("memo").getAttribute("role")).toBe("menuitemcheckbox");
  });

  it("opens on the context-menu key, anchored on the focused icon, and puts focus back on close", async () => {
    await mount();
    const b = btn("files");
    b.focus();
    await act(async () => void b.dispatchEvent(new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true, cancelable: true })));
    expect(menu()).not.toBeNull();
    await act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(b);
  });

  it("opens on a long press, and the click that follows the lift does not pick an icon", async () => {
    vi.useFakeTimers();
    await mount();
    const target = btn("files");
    const touch = (type: string, x: number, y: number) => {
      const e = new Event(type, { bubbles: true, cancelable: true });
      Object.defineProperty(e, "touches", { value: type === "touchend" ? [] : [{ clientX: x, clientY: y }] });
      target.dispatchEvent(e);
    };
    await act(async () => {
      target.dispatchEvent(new Event("pointerdown", { bubbles: true }));
      Object.defineProperty(Event.prototype, "pointerType", { value: "touch", configurable: true });
      touch("touchstart", 20, 200);
      vi.advanceTimersByTime(LONG_PRESS_MS + 20);
    });
    delete (Event.prototype as unknown as Record<string, unknown>).pointerType;
    expect(menu()).not.toBeNull();
    await act(async () => {
      touch("touchend", 20, 200);
      target.click();
    });
    expect(useLeftRail.getState().view).toBe("sessions");
  });
});

describe("turning icons off", () => {
  it("hides the icon, stores the choice with the settings, and reset brings it back", async () => {
    await mount();
    await rightClick(bar());
    await act(async () => row("memory").click());
    expect(icons()).not.toContain("memory");
    expect(getSettings().railViewsOff).toEqual(["memory"]);
    expect(row("memory").getAttribute("aria-checked")).toBe("false");
    await act(async () => menu()!.querySelector<HTMLButtonElement>("[data-menu-reset]")!.click());
    expect(icons()).toContain("memory");
    expect(getSettings().railViewsOff).toEqual([]);
  });

  it("turns an icon back on from the menu", async () => {
    setSettings({ railViewsOff: ["files"] });
    await mount();
    expect(icons()).not.toContain("files");
    await rightClick(bar());
    await act(async () => row("files").click());
    expect(icons()).toContain("files");
  });

  it("cannot turn off the view on screen, and says why", async () => {
    await mount();
    await rightClick(bar());
    expect(row("sessions").getAttribute("aria-disabled")).toBe("true");
    await act(async () => row("sessions").click());
    expect(getSettings().railViewsOff).toEqual([]);
    expect(row("sessions").textContent).toContain("on screen");
  });

  it("keeps the icon of the view on screen even when it is stored as off", async () => {
    setSettings({ railViewsOff: ["sessions", "files"] });
    await mount();
    expect(icons()).toContain("sessions");
    expect(icons()).not.toContain("files");
  });

  it("cannot turn off the last icon", async () => {
    // Every icon but the one on screen is off.
    const others = ["shared", "assistant", "workitems", "memo", "autorun", "memory", "schedules", "files"];
    setSettings({ railViewsOff: others });
    useLeftRail.setState({ view: "sessions" });
    await mount();
    expect(icons()).toEqual(["sessions"]);
    await rightClick(bar());
    expect(row("sessions").getAttribute("aria-disabled")).toBe("true");
    await act(async () => row("files").click());
    expect(icons()).toEqual(["sessions", "files"]);
  });

  it("lists Schedules greyed out with the reason where there is no scheduler", async () => {
    await mount({ schedulerEnabled: false });
    expect(icons()).not.toContain("schedules");
    await rightClick(bar());
    expect(row("schedules").getAttribute("aria-disabled")).toBe("true");
    expect(row("schedules").getAttribute("aria-checked")).toBe("false");
    expect(row("schedules").textContent).toContain("no scheduler");
    await act(async () => row("schedules").click());
    expect(getSettings().railViewsOff).toEqual([]);
  });

  it("offers no reset while nothing is off", async () => {
    await mount();
    await rightClick(bar());
    expect(menu()!.querySelector("[data-menu-reset]")!.getAttribute("aria-disabled")).toBe("true");
  });
});

describe("the vertical swipe follows the shown icons only", () => {
  it("steps over an icon that is off", async () => {
    setSettings({ railViewsOff: ["shared"] });
    useLeftRail.setState({ view: "sessions" });
    await mount();
    const next = BAR_SWIPE_UP_IS_NEXT ? -1 : 1;
    const target = btn("sessions");
    const touch = (type: string, y: number) => {
      const e = new Event(type, { bubbles: true, cancelable: true });
      Object.defineProperty(e, "touches", { value: type === "touchend" ? [] : [{ clientX: 20, clientY: y }] });
      target.dispatchEvent(e);
    };
    await act(async () => {
      touch("touchstart", 200);
      touch("touchmove", 200 + next * (BAR_STEP_DIST + 10));
      touch("touchend", 200 + next * (BAR_STEP_DIST + 10));
    });
    expect(useLeftRail.getState().view).toBe("assistant");
  });
});

describe("a hidden view that waits on the member", () => {
  const offer = (id: string) => ({ id, status: "pending" }) as unknown as HandoffOffer;
  const waitingSession = { name: "w", state: "question", alive: true, kind: "claude" } as unknown as Session;

  it("shows one dot on the bar and names the view in the menu", async () => {
    useHandoffStore.setState({ received: [offer("a"), offer("b")] });
    setSettings({ railViewsOff: ["shared"] });
    await mount();
    expect(icons()).not.toContain("shared");
    const dot = host!.querySelector<HTMLElement>("[data-hidden-waiting]")!;
    expect(dot.dataset.hiddenWaiting).toBe("shared");
    expect(dot.getAttribute("aria-label")).toContain("Shared sessions");
    await act(async () => dot.click());
    expect(menu()).not.toBeNull();
    expect(row("shared").textContent).toContain("2 handoffs waiting for your decision");
  });

  it("shows no dot for news or unread of a hidden view", async () => {
    useNotificationStore.setState({
      items: [{ seq: 1, id: "n", kind: "answer-ready", target: { type: "session", id: "s1" }, displayName: "", payload: {}, createdAt: "", seen: false }],
    });
    useSessionsStore.setState({ sessions: [{ name: "s1", state: "working", alive: true, kind: "claude" } as unknown as Session] });
    useLeftRail.setState({ view: "files" });
    setSettings({ railViewsOff: ["sessions", "assistant"] });
    await mount();
    expect(icons()).not.toContain("sessions");
    expect(host!.querySelector("[data-hidden-waiting]")).toBeNull();
  });

  it("does not repeat the badge on a view whose icon is shown", async () => {
    useSessionsStore.setState({ sessions: [waitingSession] });
    useLeftRail.setState({ view: "files" });
    await mount();
    expect(host!.querySelector("[data-hidden-waiting]")).toBeNull();
    expect(btn("sessions").querySelector(".actbar-badge")?.textContent).toBe("1");
  });

  it("has the dot for a session stopped on an answer when the Sessions icon is off", async () => {
    useSessionsStore.setState({ sessions: [waitingSession] });
    useLeftRail.setState({ view: "files" });
    setSettings({ railViewsOff: ["sessions"] });
    await mount();
    expect(host!.querySelector("[data-hidden-waiting]")?.getAttribute("data-hidden-waiting")).toBe("sessions");
  });
});

describe("keyboard focus over the icons", () => {
  const key = (el: Element, k: string) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));

  it("moves focus over the shown view icons only, in both directions, skipping hidden icons and launchers", async () => {
    setSettings({ railViewsOff: ["shared"] });
    await mount();
    btn("sessions").focus();
    await key(btn("sessions"), "ArrowDown");
    expect(document.activeElement).toBe(btn("assistant"));
    await key(btn("assistant"), "ArrowUp");
    expect(document.activeElement).toBe(btn("sessions"));
    // Past the first icon it stays; past the last view icon it does not enter the launchers.
    await key(btn("sessions"), "ArrowUp");
    expect(document.activeElement).toBe(btn("sessions"));
    btn("files").focus();
    await key(btn("files"), "ArrowDown");
    expect(document.activeElement).toBe(btn("files"));
    // Moving focus does not choose the view.
    expect(useLeftRail.getState().view).toBe("sessions");
  });
});

describe("the left pane folded away", () => {
  it("lets the view that was last chosen go, since nothing is on screen", async () => {
    await mount({ shown: false });
    await rightClick(bar());
    expect(row("sessions").getAttribute("aria-disabled")).toBeNull();
    await act(async () => row("sessions").click());
    expect(icons()).not.toContain("sessions");
  });

  it("still keeps the last icon, saying so, and does not turn it off", async () => {
    setSettings({ railViewsOff: ["shared", "assistant", "workitems", "memo", "autorun", "memory", "schedules", "files"] });
    await mount({ shown: false });
    expect(icons()).toEqual(["sessions"]);
    await rightClick(bar());
    expect(row("sessions").getAttribute("aria-disabled")).toBe("true");
    expect(row("sessions").textContent).toContain("the last icon");
    await act(async () => row("sessions").click());
    expect(icons()).toEqual(["sessions"]);
  });

  it("drops a stored-off view's icon while folded, and shows it again once on screen", async () => {
    setSettings({ railViewsOff: ["sessions"] });
    await mount({ shown: false });
    expect(icons()).not.toContain("sessions");
    await act(async () => root!.render(<Shell shown />));
    expect(icons()).toContain("sessions");
  });
});

describe("focus when the focused icon leaves the bar", () => {
  it("moves to a remaining icon when folding the pane takes away the icon of a view stored as off", async () => {
    setSettings({ railViewsOff: ["files"] });
    useLeftRail.setState({ view: "files" });
    await mount();
    expect(icons()).toContain("files");
    btn("files").focus();
    expect(document.activeElement).toBe(btn("files"));
    // Folding the pane: nothing is on screen, so the stored choice applies and the icon goes.
    await act(async () => root!.render(<Shell shown={false} />));
    expect(icons()).not.toContain("files");
    expect(host!.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).toBe(btn("sessions"));
  });

  it("leaves focus alone when it was moved on purpose", async () => {
    setSettings({ railViewsOff: ["files"] });
    useLeftRail.setState({ view: "files" });
    await mount();
    btn("files").focus();
    const other = document.createElement("input");
    document.body.append(other);
    other.focus();
    await act(async () => root!.render(<Shell shown={false} />));
    expect(document.activeElement).toBe(other);
    other.remove();
  });
});

describe("focus after the menu closes", () => {
  const key = (el: Element, k: string) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
  const esc = () => act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));

  it("lands on the view on screen when the icon the menu was opened from was turned off", async () => {
    await mount();
    btn("files").focus();
    await key(btn("files"), "ContextMenu");
    await act(async () => row("files").click());
    expect(icons()).not.toContain("files");
    await esc();
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(btn("sessions"));
  });

  it("lands on the view on screen when the dot it was opened from went away", async () => {
    useSessionsStore.setState({ sessions: [{ name: "w", state: "question", alive: true, kind: "claude" } as unknown as Session] });
    useLeftRail.setState({ view: "files" });
    setSettings({ railViewsOff: ["sessions"] });
    await mount();
    const dot = host!.querySelector<HTMLButtonElement>("[data-hidden-waiting]")!;
    dot.focus();
    await key(dot, "ContextMenu");
    await act(async () => row("sessions").click());
    expect(host!.querySelector("[data-hidden-waiting]")).toBeNull();
    await esc();
    expect(document.activeElement).toBe(btn("files"));
  });

  it("anchors a contextmenu with no pointer position (the keyboard's) on the focused icon", async () => {
    await mount();
    const b = btn("memo");
    b.getBoundingClientRect = () => ({ left: 100, right: 140, top: 250, bottom: 300, width: 40, height: 50, x: 100, y: 250, toJSON: () => ({}) }) as DOMRect;
    b.focus();
    await act(async () => void b.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 0, clientY: 0 })));
    expect(menu()!.style.left).toBe("120px");
    expect(menu()!.style.top).toBe("296px");
  });
});

describe("long press and the clicks after it", () => {
  // The order a touch screen sends: pointerdown, then touchstart, a hold, then touchend.
  const down = (target: Element, type: string, x: number, y: number) => {
    const e = new Event(type, { bubbles: true, cancelable: true });
    if (type === "pointerdown") Object.defineProperty(e, "pointerType", { value: "touch" });
    else Object.defineProperty(e, "touches", { value: type === "touchend" ? [] : [{ clientX: x, clientY: y }] });
    target.dispatchEvent(e);
  };

  async function longPress(target: Element) {
    await act(async () => {
      down(target, "pointerdown", 20, 200);
      down(target, "touchstart", 20, 200);
      vi.advanceTimersByTime(LONG_PRESS_MS + 20);
    });
  }

  it("drops the click on lift, and a later mouse click on a menu row is not eaten", async () => {
    vi.useFakeTimers();
    await mount();
    await longPress(btn("files"));
    expect(menu()).not.toBeNull();
    await act(async () => {
      down(btn("files"), "touchend", 20, 200);
      btn("files").click();
    });
    expect(useLeftRail.getState().view).toBe("sessions");
    // The browser sent no click after touchend; a mouse click on the menu is a new action.
    await act(async () => void vi.advanceTimersByTime(1000));
    await act(async () => row("memory").click());
    expect(getSettings().railViewsOff).toEqual(["memory"]);
  });

  it("does not eat the first menu click even when the lift's click never came and no time has passed", async () => {
    vi.useFakeTimers();
    await mount();
    await longPress(btn("files"));
    await act(async () => down(btn("files"), "touchend", 20, 200));
    // The menu is outside the bar, so its click is not the one being dropped.
    await act(async () => row("memory").click());
    expect(getSettings().railViewsOff).toEqual(["memory"]);
  });

  it("treats a press that began on the icon inside the button as a press on the button", async () => {
    vi.useFakeTimers();
    await mount();
    const icon = btn("files").querySelector(".codicon")!;
    await longPress(icon);
    await act(async () => down(icon, "touchend", 20, 200));
    // The compatibility click lands on the button itself, not on the icon.
    await act(async () => btn("files").click());
    expect(useLeftRail.getState().view).toBe("sessions");
  });

  it("drops only the click on the button the press began on, and none after a touchcancel", async () => {
    vi.useFakeTimers();
    await mount();
    await longPress(btn("files"));
    await act(async () => down(btn("files"), "touchend", 20, 200));
    await act(async () => btn("memo").click());
    expect(useLeftRail.getState().view).toBe("memo");
    await act(async () => void vi.advanceTimersByTime(1000));
    await longPress(btn("files"));
    await act(async () => down(btn("files"), "touchcancel", 20, 200));
    await act(async () => btn("files").click());
    expect(useLeftRail.getState().view).toBe("files");
  });

  it("does not drop an icon click that comes well after the lift", async () => {
    vi.useFakeTimers();
    await mount();
    await longPress(btn("files"));
    await act(async () => down(btn("files"), "touchend", 20, 200));
    await act(async () => void vi.advanceTimersByTime(1000));
    await act(async () => btn("memo").click());
    expect(useLeftRail.getState().view).toBe("memo");
  });
});

describe("re-measuring the bar when the dot appears", () => {
  it("drops data-fits when the dot makes the column overflow, and restores it when the dot goes", async () => {
    await mount();
    const el = bar();
    Object.defineProperty(el, "clientHeight", { value: 300, configurable: true });
    Object.defineProperty(el, "scrollHeight", { configurable: true, get: () => (el.querySelector("[data-hidden-waiting]") ? 340 : 300) });
    await act(async () => root!.render(<Shell />));
    expect(el.hasAttribute("data-fits")).toBe(true);
    useLeftRail.setState({ view: "files" });
    setSettings({ railViewsOff: ["sessions"] });
    await act(async () => void useSessionsStore.setState({ sessions: [{ name: "w", state: "question", alive: true, kind: "claude" } as unknown as Session] }));
    expect(el.querySelector("[data-hidden-waiting]")).not.toBeNull();
    expect(el.hasAttribute("data-fits")).toBe(false);
    await act(async () => void useSessionsStore.setState({ sessions: [] }));
    expect(el.querySelector("[data-hidden-waiting]")).toBeNull();
    expect(el.hasAttribute("data-fits")).toBe(true);
  });
});

describe("several hidden views waiting at once", () => {
  const offer = (id: string) => ({ id, status: "pending" }) as unknown as HandoffOffer;
  it("shows one dot, each row counts only its own view, and a view turned on moves its count to the icon", async () => {
    useSessionsStore.setState({ sessions: [{ name: "w", state: "question", alive: true, kind: "claude" } as unknown as Session] });
    useHandoffStore.setState({ received: [offer("a"), offer("b")] });
    useLeftRail.setState({ view: "files" });
    setSettings({ railViewsOff: ["sessions", "shared"] });
    await mount();
    expect(host!.querySelectorAll("[data-hidden-waiting]")).toHaveLength(1);
    expect(host!.querySelector("[data-hidden-waiting]")!.getAttribute("data-hidden-waiting")).toBe("sessions shared");
    await act(async () => host!.querySelector<HTMLButtonElement>("[data-hidden-waiting]")!.click());
    expect(row("sessions").textContent).toContain("1 session waiting for your answer");
    expect(row("shared").textContent).toContain("2 handoffs waiting for your decision");
    await act(async () => row("sessions").click());
    expect(host!.querySelectorAll("[data-hidden-waiting]")).toHaveLength(1);
    expect(host!.querySelector("[data-hidden-waiting]")!.getAttribute("data-hidden-waiting")).toBe("shared");
    expect(row("sessions").textContent).not.toContain("waiting for your answer");
    expect(btn("sessions").querySelector(".actbar-badge")?.textContent).toBe("1");
    expect(row("shared").textContent).toContain("2 handoffs waiting for your decision");
  });
});

describe("ids this build does not know", () => {
  it("Reset clears only the ids it knows, and is off when there are none to clear", async () => {
    setSettings({ railViewsOff: ["future-view", "files"] });
    await mount();
    await rightClick(bar());
    const reset = () => menu()!.querySelector<HTMLButtonElement>("[data-menu-reset]")!;
    expect(reset().getAttribute("aria-disabled")).toBeNull();
    await act(async () => reset().click());
    expect(getSettings().railViewsOff).toEqual(["future-view"]);
    expect(reset().getAttribute("aria-disabled")).toBe("true");
    await act(async () => reset().click());
    expect(getSettings().railViewsOff).toEqual(["future-view"]);
  });
  it("keeps an unknown id when an icon is turned off", async () => {
    setSettings({ railViewsOff: ["future-view"] });
    await mount();
    await rightClick(bar());
    await act(async () => row("files").click());
    expect(getSettings().railViewsOff).toEqual(["future-view", "files"]);
  });
});
