import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let key: string | null = "k";
let off: string[] = ["answer-ready"];
let prefsGate: Promise<void> | null = null;
let sub: unknown = null;
let matches = true;
let myId = "d1"; // what webPush derives for this browser's subscription
let matchKey: string | null = null; // when set, the subscription was made with exactly this key
let idGate: Promise<void> | null = null;
let listHangs = false;
let listGate: Promise<void> | null = null;
const puts: string[][] = [];
vi.mock("./pushApi.ts", async (orig) => ({
  ...(await orig<typeof import("./pushApi.ts")>()),
  probePushKey: () => Promise.resolve(key),
  getPushOff: () => (prefsGate ? prefsGate.then(() => off) : Promise.resolve(off)),
  putPushOff: (o: string[]) => {
    puts.push(o);
    return Promise.resolve();
  },
  listPushDevices: () => (listHangs ? new Promise<never>(() => {}) : listGate ? listGate : Promise.resolve()).then(() => [{ id: "d1", userAgent: "Mozilla/5.0 (iPhone) Safari/605", createdAt: "2026-11-22T00:00:00Z", lastOkAt: "" }]),
  removePushDevice: vi.fn(() => Promise.resolve()),
}));
vi.mock("./webPush.ts", () => ({
  pushSupport: () => "ok",
  currentSubscription: () => Promise.resolve(sub),
  subscriptionMatchesKey: (_s: unknown, k: string) => (matchKey !== null ? k === matchKey : matches),
  deviceIdFor: () => (idGate ? idGate : Promise.resolve()).then(() => myId),
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
  prefsGate = null;
  sub = null;
  matches = true;
  myId = "d1";
  matchKey = null;
  idGate = null;
  listHangs = false;
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
    expect(puts).toEqual([["answer-ready", "question"]]);
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
  it("reads the key again after a click, so a rotation since the screen opened does not read as stale", async () => {
    key = "k1";
    sub = {};
    matchKey = "k2"; // enablePush subscribed with the key the CP serves now
    const host = await mount();
    const sw = host.querySelector<HTMLInputElement>("label input[role=switch]")!;
    expect(sw.checked).toBe(false);
    expect(host.textContent).toContain(t("push.stale"));
    key = "k2";
    vi.mocked(webPush.enablePush).mockResolvedValue("ok");
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
    sub = {};
    const host = await mount();
    const btn = host.querySelector<HTMLButtonElement>(".push-devices button")!;
    await act(async () => void btn.click());
    await flush();
    expect(pushApi.removePushDevice).toHaveBeenCalledWith("d1");
    expect(webPush.dropLocalSubscription).toHaveBeenCalledOnce();
    expect(webPush.disablePush).not.toHaveBeenCalled();
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
