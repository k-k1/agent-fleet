import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let key: string | null = "k";
let off: string[] = ["answer-ready"];
let prefsGate: Promise<void> | null = null;
let sub: unknown = null;
let matches = true;
const puts: string[][] = [];
vi.mock("./pushApi.ts", async (orig) => ({
  ...(await orig<typeof import("./pushApi.ts")>()),
  getPushKey: () => Promise.resolve(key),
  getPushOff: () => (prefsGate ? prefsGate.then(() => off) : Promise.resolve(off)),
  putPushOff: (o: string[]) => {
    puts.push(o);
    return Promise.resolve();
  },
  listPushDevices: () => Promise.resolve([{ id: "d1", userAgent: "Mozilla/5.0 (iPhone) Safari/605", createdAt: "2026-11-22T00:00:00Z", lastOkAt: "" }]),
  removePushDevice: vi.fn(() => Promise.resolve()),
}));
vi.mock("./webPush.ts", () => ({
  pushSupport: () => "ok",
  currentSubscription: () => Promise.resolve(sub),
  subscriptionMatchesKey: () => matches,
  thisDeviceId: () => "",
  enablePush: vi.fn(),
  disablePush: vi.fn(),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { t } from "../../lib/i18n/index.ts";
import { PushSection, shortAgent } from "./PushSection.tsx";

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
  it("labels a device from its user agent", () => {
    expect(shortAgent("Mozilla/5.0 (Linux; Android 14) Chrome/120 Mobile Safari/537")).toBe("Chrome / Android");
  });
});
