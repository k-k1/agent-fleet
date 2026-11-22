import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const released: unknown[] = [];
const removed: string[] = [];
let releaseFails = false;
vi.mock("./pushApi.ts", () => ({
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

import { disablePush, enablePush, releasePushForSignOut, subscriptionMatchesKey } from "./webPush.ts";

let unsubscribed = 0;
const keyBytes = (b64: string) => Uint8Array.from(atob(b64), (c) => c.charCodeAt(0)).buffer;
const sub = {
  options: { applicationServerKey: keyBytes("AQAB") as ArrayBuffer | null },
  toJSON: () => ({ endpoint: "https://push.example/x", keys: { p256dh: "p", auth: "a" } }),
  unsubscribe: () => {
    unsubscribed++;
    return Promise.resolve(true);
  },
};
let current: typeof sub | null = sub;
let subscribed = 0;

beforeEach(() => {
  released.length = 0;
  removed.length = 0;
  unsubscribed = 0;
  releaseFails = false;
  current = sub;
  localStorage.clear();
  sub.options.applicationServerKey = keyBytes("AQAB");
  subscribed = 0;
  const reg = { pushManager: { getSubscription: () => Promise.resolve(current), subscribe: () => (subscribed++, Promise.resolve(sub)) } };
  Object.defineProperty(navigator, "serviceWorker", {
    value: { getRegistration: () => Promise.resolve(reg), ready: Promise.resolve(reg) },
    configurable: true,
  });
  (window as any).PushManager = function () {};
  (window as any).Notification = { permission: "granted", requestPermission: () => Promise.resolve("granted") };
});
afterEach(() => vi.restoreAllMocks());

describe("web push device lifecycle", () => {
  it("a subscription made with another key is replaced, the old row released first", async () => {
    sub.options.applicationServerKey = keyBytes("AgAB");
    expect(subscriptionMatchesKey(sub as unknown as PushSubscription, "AQAB")).toBe(false);
    expect(await enablePush("AQAB")).toBe("ok");
    expect(released).toEqual([sub.toJSON()]);
    expect(unsubscribed).toBe(1);
    expect(subscribed).toBe(1);
  });
  it("a failed release of the old row stops the key swap and keeps the old subscription", async () => {
    sub.options.applicationServerKey = keyBytes("AgAB");
    const api = await import("./pushApi.ts");
    vi.spyOn(api, "releasePushEndpoint").mockRejectedValueOnce(new Error("500"));
    await expect(enablePush("AQAB")).rejects.toThrow("500");
    expect(unsubscribed).toBe(0);
    expect(subscribed).toBe(0);
  });
  it("sign-out retries a subscription read that failed once, and still releases", async () => {
    let n = 0;
    const reg = {
      pushManager: { getSubscription: () => (++n === 1 ? Promise.reject(new Error("flaky")) : Promise.resolve(sub)) },
    };
    Object.defineProperty(navigator, "serviceWorker", { value: { getRegistration: () => Promise.resolve(reg) }, configurable: true });
    await releasePushForSignOut();
    expect(released).toEqual([sub.toJSON()]);
    expect(unsubscribed).toBe(1);
  });
  it("a current subscription is reused, not recreated", async () => {
    expect(await enablePush("AQAB")).toBe("ok");
    expect(subscribed).toBe(0);
    expect(released).toEqual([]);
  });
  it("a localStorage that refuses the id does not undo a registration the CP accepted", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    expect(await enablePush("AQAB")).toBe("ok");
    expect(unsubscribed).toBe(0);
  });
  it("sign-out is bounded as a whole when the browser unsubscribe never settles", async () => {
    sub.unsubscribe = () => new Promise(() => {});
    const t0 = Date.now();
    await releasePushForSignOut(100);
    expect(Date.now() - t0).toBeLessThan(1500);
    sub.unsubscribe = () => (unsubscribed++, Promise.resolve(true));
  });
  it("sign-out is bounded when the release call never answers", async () => {
    const api = await import("./pushApi.ts");
    vi.spyOn(api, "releasePushEndpoint").mockImplementation(() => new Promise(() => {}));
    const t0 = Date.now();
    await releasePushForSignOut(100);
    expect(Date.now() - t0).toBeLessThan(1500);
  });
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
    expect(await enablePush("AQAB")).toBe("ok");
    expect(localStorage.getItem("af-push-sub-id")).toBe("dev1");
    await disablePush();
    expect(removed).toEqual(["dev1"]);
    expect(unsubscribed).toBe(1);
    expect(localStorage.getItem("af-push-sub-id")).toBeNull();
  });
  it("denied permission subscribes nothing", async () => {
    (window as any).Notification = { permission: "denied", requestPermission: () => Promise.resolve("denied") };
    expect(await enablePush("AQAB")).toBe("denied");
    expect(localStorage.getItem("af-push-sub-id")).toBeNull();
  });
});
