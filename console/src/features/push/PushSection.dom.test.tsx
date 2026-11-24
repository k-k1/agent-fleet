import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let key: string | null = "k";
let off: string[] = ["answer-ready"];
let offOrigins: string[] = [];
let prefsGate: Promise<void> | null = null;
let sub: unknown = null;
let matches = true;
let myId = "d1"; // what webPush derives for this browser's subscription
let matchKey: string | null = null; // when set, the subscription was made with exactly this key
let idGate: Promise<void> | null = null;
let listHangs = false;
let subHangs = false;
let idHangs = false;
let firstListGate: Promise<void> | null = null; // holds the first list read only, which then answers with no devices
let listCalls = 0;
const row = (id: string) => ({ id, userAgent: "Mozilla/5.0 (iPhone) Safari/605", createdAt: "2026-11-22T00:00:00Z", lastOkAt: "" });
let rows = [row("d1")];
let listGate: Promise<void> | null = null;
const puts: { off: string[]; offOrigins?: string[] }[] = [];
vi.mock("./pushApi.ts", async (orig) => ({
  ...(await orig<typeof import("./pushApi.ts")>()),
  probePushKey: () => Promise.resolve(key),
  getPushPrefs: () => (prefsGate ? prefsGate.then(() => ({ off, offOrigins })) : Promise.resolve({ off, offOrigins })),
  putPushPrefs: (o: { off: string[]; offOrigins?: string[] }) => {
    puts.push(o);
    return Promise.resolve();
  },
  listPushDevices: async () => {
    if (listCalls++ === 0 && firstListGate) {
      await firstListGate;
      return []; // the late first read: it must never be what the screen ends up showing
    }
    if (listHangs) return new Promise<never>(() => {});
    await (listGate ?? Promise.resolve());
    return rows;
  },
  removePushDevice: vi.fn(() => Promise.resolve()),
}));
vi.mock("./webPush.ts", () => ({
  pushSupport: () => "ok",
  // Read when the gate opens, like the list: a read that was started earlier sees the browser as it is then.
  currentSubscription: async () => {
    await (listGate ?? Promise.resolve());
    return subHangs ? new Promise(() => {}) : sub;
  },
  subscriptionMatchesKey: (_s: unknown, k: string) => (matchKey !== null ? k === matchKey : matches),
  deviceIdFor: () => (idHangs ? new Promise<never>(() => {}) : idGate ? idGate : Promise.resolve()).then(() => myId),
  thisDeviceId: () => "",
  dropLocalSubscription: vi.fn(() => Promise.resolve()),
  enablePush: vi.fn(),
  disablePush: vi.fn(),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { t } from "../../lib/i18n/index.ts";
import { PushSection, shortAgent } from "./PushSection.tsx";
import * as webPush from "./webPush.ts";
import * as pushApi from "./pushApi.ts";

let root: Root | null = null;
const flush = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
async function mount() {
  const host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<PushSection />));
  await flush();
  return host;
}
beforeEach(() => {
  key = "k";
  off = ["answer-ready"];
  offOrigins = [];
  prefsGate = null;
  sub = null;
  matches = true;
  myId = "d1";
  matchKey = null;
  idGate = null;
  listHangs = false;
  subHangs = false;
  idHangs = false;
  firstListGate = null;
  listCalls = 0;
  rows = [row("d1")];
  listGate = null;
  puts.length = 0;
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.innerHTML = "";
});

describe("PushSection", () => {
  it("is absent when the deployment has no Web Push", async () => {
    key = null;
    const host = await mount();
    expect(host.querySelector("[data-testid=push-section]")).toBeNull();
  });
  it("shows the per-kind choices from the server and the device list", async () => {
    const host = await mount();
    const boxes = [...host.querySelectorAll<HTMLInputElement>(".push-kind input")];
    expect(boxes.map((b) => b.checked)).toEqual([true, true, true, false]);
    expect(host.querySelector(".push-devices")?.textContent).toContain("Safari / iOS");
  });
  it("saves a turned-off kind as the full off list", async () => {
    const host = await mount();
    const q = host.querySelector<HTMLInputElement>(".push-kind input")!;
    await act(async () => void q.click());
    await flush();
    expect(puts).toEqual([{ off: ["answer-ready", "question"] }]);
  });
  it("shows the origin switches as three rows of two, from the saved list", async () => {
    offOrigins = ["autorun-child"];
    const host = await mount();
    const rows = [...host.querySelectorAll(".push-origin-row")];
    expect(rows).toHaveLength(3);
    const states = rows.map((r) => [...r.querySelectorAll<HTMLInputElement>("input")].map((b) => b.checked));
    expect(states).toEqual([[true, true], [true, false], [true, true]]);
  });
  it("gives each of the six origin switches a distinct accessible name", async () => {
    const host = await mount();
    const names = [...host.querySelectorAll(".push-origin-row input")].map((b) => b.getAttribute("aria-label"));
    expect(new Set(names).size).toBe(6);
    expect(names.every((n) => n && n.includes(": "))).toBe(true);
  });
  it("saves origins with the kinds, and leaves origins out when only a kind changes (a stale screen must not undo them)", async () => {
    const host = await mount();
    const inputs = host.querySelectorAll<HTMLInputElement>(".push-origin-row input");
    await act(async () => void inputs[2].click()); // auto-run: the session itself
    await flush();
    expect(puts).toEqual([{ off: ["answer-ready"], offOrigins: ["autorun"] }]);
    const q = host.querySelector<HTMLInputElement>(".push-kind input")!;
    await act(async () => void q.click());
    await flush();
    expect(puts[1]).toEqual({ off: ["answer-ready", "question"] });
  });
  it("keeps the kind switches locked until the saved choices arrive, so nothing is overwritten", async () => {
    let open!: () => void;
    prefsGate = new Promise<void>((r) => (open = r));
    const host = await mount();
    const q = host.querySelector<HTMLInputElement>(".push-kind input")!;
    expect(host.querySelector<HTMLFieldSetElement>(".push-kinds")!.disabled).toBe(true);
    await act(async () => void q.click());
    expect(puts).toEqual([]);
    open();
    await flush();
    expect(host.querySelector<HTMLFieldSetElement>(".push-kinds")!.disabled).toBe(false);
  });
  it("shows a device whose subscription uses an old key as off, with a hint", async () => {
    sub = {};
    matches = false;
    const host = await mount();
    const sw = host.querySelector<HTMLInputElement>("label input[role=switch]")!;
    expect(sw.checked).toBe(false);
    expect(host.textContent).toContain(t("push.stale"));
  });
  it("shows this browser as on when the CP lists the row its endpoint maps to, with nothing stored locally", async () => {
    sub = {};
    const host = await mount();
    expect(host.querySelector<HTMLInputElement>("label input[role=switch]")!.checked).toBe(true);
    expect(host.textContent).toContain(t("push.this_badge"));
  });
  it("shows this browser as off when the CP has no row for its subscription", async () => {
    sub = {};
    myId = "gone";
    const host = await mount();
    expect(host.querySelector<HTMLInputElement>("label input[role=switch]")!.checked).toBe(false);
  });
  it("holds the device switch until the device list has loaded", async () => {
    sub = {};
    let open!: () => void;
    listGate = new Promise<void>((r) => (open = r));
    const host = await mount();
    const sw = host.querySelector<HTMLInputElement>("label input[role=switch]")!;
    expect(sw.disabled).toBe(true);
    open();
    await flush();
    expect(sw.disabled).toBe(false);
    expect(sw.checked).toBe(true);
  });
  it("compares with the key the enable subscribed with, so a rotation since the screen opened does not read as stale", async () => {
    key = "k1";
    sub = {};
    matchKey = "k2"; // enablePush subscribed with the key the CP serves now
    const host = await mount();
    const sw = host.querySelector<HTMLInputElement>("label input[role=switch]")!;
    expect(sw.checked).toBe(false);
    expect(host.textContent).toContain(t("push.stale"));
    key = "k2";
    vi.mocked(webPush.enablePush).mockImplementation(async (_k, onKey) => (onKey?.("k2"), "ok"));
    await act(async () => void sw.click());
    await flush();
    expect(sw.checked).toBe(true);
    expect(host.textContent).not.toContain(t("push.stale"));
  });
  it("shows no device list until this browser's row id is known, so a removal cannot miss it", async () => {
    sub = {};
    let open!: () => void;
    idGate = new Promise<void>((r) => (open = r));
    const host = await mount();
    expect(host.querySelector(".push-devices")).toBeNull();
    open();
    await flush();
    expect(host.querySelector(".push-devices")).not.toBeNull();
  });
  it("removing this very device ends its subscription locally without asking the CP twice", async () => {
    sub = { endpoint: "https://push.example/x" };
    const host = await mount();
    const btn = host.querySelector<HTMLButtonElement>(".push-devices button")!;
    await act(async () => void btn.click());
    await flush();
    expect(pushApi.removePushDevice).toHaveBeenCalledWith("d1");
    expect(webPush.dropLocalSubscription).toHaveBeenCalledWith("https://push.example/x");
    expect(webPush.disablePush).not.toHaveBeenCalled();
  });
  it("removing a device ends the subscription it was shown with, even when a newer read replaced it meanwhile", async () => {
    sub = { endpoint: "https://push.example/A" };
    const host = await mount();
    let release!: () => void;
    listGate = new Promise<void>((r) => (release = r));
    const sw = host.querySelector<HTMLInputElement>("label input[role=switch]")!;
    await act(async () => void sw.click()); // off: its reload (the read) is now waiting on the gate
    await flush();
    let answerDelete!: () => void;
    vi.mocked(pushApi.removePushDevice).mockImplementationOnce(() => new Promise<void>((r) => (answerDelete = r)));
    const btn = host.querySelector<HTMLButtonElement>(".push-devices button")!; // device A's row
    await act(async () => void btn.click());
    // The key swap finished meanwhile: this browser now holds B, and the newer read shows only B.
    sub = { endpoint: "https://push.example/B" };
    myId = "d2";
    rows = [row("d2")];
    await act(async () => {
      release();
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(host.querySelector(".push-devices")?.textContent).toContain(t("push.this_badge"));
    await act(async () => {
      answerDelete();
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(webPush.dropLocalSubscription).toHaveBeenCalledWith("https://push.example/A");
    expect(webPush.dropLocalSubscription).not.toHaveBeenCalledWith("https://push.example/B");
  });
  it("leaves the switch usable, and off, when the device list never answers", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    sub = {};
    listHangs = true;
    const host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<PushSection />));
    await act(async () => void (await vi.advanceTimersByTimeAsync(100)));
    const sw = host.querySelector<HTMLInputElement>("label input[role=switch]")!;
    expect(sw.disabled).toBe(true);
    await act(async () => void (await vi.advanceTimersByTimeAsync(10_000)));
    expect(sw.disabled).toBe(false);
    expect(sw.checked).toBe(false);
    vi.useRealTimers();
  });
  it("leaves the switch usable when the browser's own subscription read never answers", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    sub = {};
    subHangs = true;
    const host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<PushSection />));
    await act(async () => void (await vi.advanceTimersByTimeAsync(100)));
    await act(async () => void (await vi.advanceTimersByTimeAsync(10_000)));
    expect(host.querySelector<HTMLInputElement>("label input[role=switch]")!.disabled).toBe(false);
    vi.useRealTimers();
  });
  it("shows the device list even when deriving this browser's row id never answers", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    sub = {};
    idHangs = true;
    const host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<PushSection />));
    await act(async () => void (await vi.advanceTimersByTimeAsync(100)));
    expect(host.querySelector(".push-devices")).toBeNull();
    await act(async () => void (await vi.advanceTimersByTimeAsync(2000)));
    expect(host.querySelector(".push-devices")).not.toBeNull();
    expect(host.querySelector<HTMLInputElement>("label input[role=switch]")!.disabled).toBe(false);
    vi.useRealTimers();
  });
  it("turns push on again with the key an earlier enable on this screen used, not the one the screen opened with", async () => {
    key = "k1";
    sub = {};
    matchKey = "k2";
    const enable = vi.mocked(webPush.enablePush);
    enable.mockReset();
    enable.mockImplementation(async (_k, onKey) => (onKey?.("k2"), "ok"));
    const host = await mount();
    const sw = host.querySelector<HTMLInputElement>("label input[role=switch]")!;
    await act(async () => void sw.click()); // off -> on, subscribed with k2
    await flush();
    expect(sw.checked).toBe(true);
    sub = null;
    await act(async () => void sw.click()); // on -> off
    await flush();
    expect(sw.checked).toBe(false);
    await act(async () => void sw.click()); // on again
    await flush();
    expect(enable.mock.calls.map((c) => c[0])).toEqual(["k1", "k2"]);
  });
  it("drops a read that answers after its limit instead of letting it overwrite a newer one", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    sub = {};
    let late!: () => void;
    firstListGate = new Promise<void>((r) => (late = r));
    const host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    act(() => root!.render(<PushSection />));
    await act(async () => void (await vi.advanceTimersByTimeAsync(100)));
    await act(async () => void (await vi.advanceTimersByTimeAsync(10_100))); // the first read gave up
    const sw = host.querySelector<HTMLInputElement>("label input[role=switch]")!;
    expect(sw.disabled).toBe(false);
    matchKey = "k"; // a newer read: the subscription is current and the CP lists it
    vi.mocked(webPush.enablePush).mockResolvedValue("ok");
    await act(async () => void sw.click());
    await act(async () => void (await vi.advanceTimersByTimeAsync(100)));
    expect(sw.checked).toBe(true);
    expect(host.querySelector(".push-devices")).not.toBeNull();
    await act(async () => {
      late(); // the first read now answers with an empty list
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(sw.checked).toBe(true);
    expect(host.querySelector(".push-devices")).not.toBeNull();
    vi.useRealTimers();
  });
  it("counts a matching subscription as on when its row id cannot be told at all", async () => {
    sub = {};
    myId = "";
    const host = await mount();
    expect(host.querySelector<HTMLInputElement>("label input[role=switch]")!.checked).toBe(true);
  });
  it("labels a device from its user agent", () => {
    expect(shortAgent("Mozilla/5.0 (Linux; Android 14) Chrome/120 Mobile Safari/537")).toBe("Chrome / Android");
  });
});
