import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let key: string | null = "k";
let off: string[] = ["answer-ready"];
const puts: string[][] = [];
vi.mock("./pushApi.ts", async (orig) => ({
  ...(await orig<typeof import("./pushApi.ts")>()),
  getPushKey: () => Promise.resolve(key),
  getPushOff: () => Promise.resolve(off),
  putPushOff: (o: string[]) => {
    puts.push(o);
    return Promise.resolve();
  },
  listPushDevices: () => Promise.resolve([{ id: "d1", userAgent: "Mozilla/5.0 (iPhone) Safari/605", createdAt: "2026-11-22T00:00:00Z", lastOkAt: "" }]),
  removePushDevice: vi.fn(() => Promise.resolve()),
}));
vi.mock("./webPush.ts", () => ({
  pushSupport: () => "ok",
  currentSubscription: () => Promise.resolve(null),
  thisDeviceId: () => "",
  enablePush: vi.fn(),
  disablePush: vi.fn(),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

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
  it("labels a device from its user agent", () => {
    expect(shortAgent("Mozilla/5.0 (Linux; Android 14) Chrome/120 Mobile Safari/537")).toBe("Chrome / Android");
  });
});
