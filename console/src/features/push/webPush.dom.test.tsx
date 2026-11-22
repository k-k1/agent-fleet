import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const released: unknown[] = [];
const removed: string[] = [];
let releaseFails = false;
vi.mock("./pushApi.ts", () => ({
  getPushKey: () => Promise.resolve("AQAB"),
  addPushDevice: () => Promise.resolve("dev1"),
  removePushDevice: (id: string) => {
    removed.push(id);
    return Promise.resolve();
  },
  releasePushEndpoint: (sub: unknown) => {
    if (releaseFails) return Promise.reject(new Error("offline"));
    released.push(sub);
    return Promise.resolve();
  },
}));

import { disablePush, enablePush, releasePushForSignOut } from "./webPush.ts";

let unsubscribed = 0;
const sub = {
  toJSON: () => ({ endpoint: "https://push.example/x", keys: { p256dh: "p", auth: "a" } }),
  unsubscribe: () => {
    unsubscribed++;
    return Promise.resolve(true);
  },
};
let current: typeof sub | null = sub;

beforeEach(() => {
  released.length = 0;
  removed.length = 0;
  unsubscribed = 0;
  releaseFails = false;
  current = sub;
  localStorage.clear();
  const reg = { pushManager: { getSubscription: () => Promise.resolve(current), subscribe: () => Promise.resolve(sub) } };
  Object.defineProperty(navigator, "serviceWorker", {
    value: { getRegistration: () => Promise.resolve(reg), ready: Promise.resolve(reg) },
    configurable: true,
  });
  (window as any).PushManager = function () {};
  (window as any).Notification = { permission: "granted", requestPermission: () => Promise.resolve("granted") };
});
afterEach(() => vi.restoreAllMocks());

describe("web push device lifecycle", () => {
  it("sign-out releases the subscription by its keys and unsubscribes the browser", async () => {
    await releasePushForSignOut();
    expect(released).toEqual([sub.toJSON()]);
    expect(unsubscribed).toBe(1);
  });
  it("sign-out still unsubscribes locally when the CP cannot be reached", async () => {
    releaseFails = true;
    await releasePushForSignOut();
    expect(unsubscribed).toBe(1);
  });
  it("sign-out does nothing for a browser with no subscription", async () => {
    current = null;
    await releasePushForSignOut();
    expect(released).toEqual([]);
  });
  it("enable remembers the device id and disable removes it from the CP before the browser", async () => {
    expect(await enablePush()).toBe("ok");
    expect(localStorage.getItem("af-push-sub-id")).toBe("dev1");
    await disablePush();
    expect(removed).toEqual(["dev1"]);
    expect(unsubscribed).toBe(1);
    expect(localStorage.getItem("af-push-sub-id")).toBeNull();
  });
  it("denied permission subscribes nothing", async () => {
    (window as any).Notification = { permission: "denied", requestPermission: () => Promise.resolve("denied") };
    expect(await enablePush()).toBe("denied");
    expect(localStorage.getItem("af-push-sub-id")).toBeNull();
  });
});
