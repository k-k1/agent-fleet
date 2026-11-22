import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let key: string | null = "k";
let putFails = false;
let getFails = 0;
const puts: unknown[] = [];
const toasts: string[] = [];
vi.mock("../../push/pushApi.ts", () => ({ probePushKey: () => Promise.resolve(key) }));
vi.mock("./tenantApi.ts", () => ({
  getTenantPushSettings: () => (getFails-- > 0 ? Promise.reject(new Error("503")) : Promise.resolve({ tenant: "acme", includeText: false })),
  putTenantPushSettings: (slug: string, body: unknown) => {
    if (putFails) return Promise.reject(new Error("offline"));
    puts.push([slug, body]);
    return Promise.resolve({ tenant: slug, ...(body as object) });
  },
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toasts.push(m) }));

import { TenantPushView } from "./tenantPush.tsx";

let root: Root | null = null;
const flush = () =>
  act(async () => {
    for (let i = 0; i < 8; i++) await Promise.resolve();
  });
async function mount() {
  const host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<TenantPushView slug="acme" />));
  await flush();
  return host;
}
beforeEach(() => {
  key = "k";
  putFails = false;
  getFails = 0;
  puts.length = 0;
  toasts.length = 0;
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.innerHTML = "";
});

describe("TenantPushView", () => {
  it("is hidden on a deployment without Web Push", async () => {
    key = null;
    expect((await mount()).querySelector(".push-policy")).toBeNull();
  });
  it("reads the setting again after a transient failure, without reopening the screen", async () => {
    vi.useFakeTimers();
    try {
      getFails = 1;
      const host = await mount();
      expect(host.querySelector(".push-policy")).toBeNull();
      await act(async () => void (await vi.advanceTimersByTimeAsync(15_100)));
      expect(host.querySelector(".push-policy")).not.toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
  it("saves the switch for the tenant", async () => {
    const host = await mount();
    const box = host.querySelector<HTMLInputElement>("input[type=checkbox]")!;
    await act(async () => void box.click());
    await flush();
    expect(puts).toEqual([["acme", { includeText: true }]]);
  });
  it("tells the admin when the save fails and keeps the saved value", async () => {
    putFails = true;
    const host = await mount();
    const box = host.querySelector<HTMLInputElement>("input[type=checkbox]")!;
    await act(async () => void box.click());
    await flush();
    expect(toasts.length).toBe(1);
    expect(host.querySelector<HTMLInputElement>("input[type=checkbox]")!.checked).toBe(false);
  });
});
