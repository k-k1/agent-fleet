import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const released: unknown[] = [];
const removed: string[] = [];
let releaseFails = false;
let probe: () => Promise<string | null> = () => Promise.resolve("AQAB");
let addDevice: () => Promise<string> = () => Promise.resolve("dev1");
vi.mock("./pushApi.ts", () => ({
  probePushKey: () => probe(),
  addPushDevice: () => addDevice(),
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

import { createHash } from "node:crypto";
import { deviceIdFor, disablePush, enablePush, releasePushForSignOut, subscriptionMatchesKey } from "./webPush.ts";

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
  probe = () => Promise.resolve("AQAB");
  addDevice = () => Promise.resolve("dev1");
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
afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

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
  it("sign-out ends the browser subscription even when the CP call outlasts the deadline", async () => {
    const api = await import("./pushApi.ts");
    vi.spyOn(api, "releasePushEndpoint").mockImplementation(() => new Promise(() => {}));
    await releasePushForSignOut(50);
    expect(unsubscribed).toBe(1);
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

  it("subscribes with the key the CP serves now, not the one the screen loaded earlier", async () => {
    current = null;
    probe = () => Promise.resolve("AgAB"); // rotated since Settings opened
    const reg = (await navigator.serviceWorker.getRegistration())!;
    const subscribe = vi.spyOn(reg.pushManager, "subscribe");
    expect(await enablePush("AQAB")).toBe("ok");
    expect([...(subscribe.mock.calls[0]![0]!.applicationServerKey as Uint8Array)]).toEqual([2, 0, 1]);
  });
  it("replaces a subscription that matches the screen's key but not the rotated one", async () => {
    probe = () => Promise.resolve("AgAB");
    expect(await enablePush("AQAB")).toBe("ok");
    expect(released).toEqual([sub.toJSON()]);
    expect(subscribed).toBe(1);
  });
  it("falls back to the screen's key when the key read fails, and stops when the CP now has none", async () => {
    current = null;
    probe = () => Promise.reject(new Error("503"));
    expect(await enablePush("AQAB")).toBe("ok");
    expect(subscribed).toBe(1);
    subscribed = 0;
    probe = () => Promise.resolve(null);
    expect(await enablePush("AQAB")).toBe("unavailable");
    expect(subscribed).toBe(0);
  });
  it("a sign-out during subscribe() takes the new subscription back and registers nothing", async () => {
    current = null;
    let done!: () => void;
    const reg = (await navigator.serviceWorker.getRegistration())!;
    vi.spyOn(reg.pushManager, "subscribe").mockImplementation(() => new Promise((r) => (done = () => r(sub as never))));
    const add = vi.fn(() => Promise.resolve("dev1"));
    addDevice = add;
    const p = enablePush("AQAB");
    await vi.waitFor(() => expect(done).toBeTypeOf("function"));
    await releasePushForSignOut(); // reads no subscription yet: nothing to release
    done();
    expect(await p).toBe("cancelled");
    expect(add).not.toHaveBeenCalled();
    expect(unsubscribed).toBe(1);
    expect(localStorage.getItem("af-push-sub-id")).toBeNull();
  });
  it("a sign-out while the CP registers the device releases it again once the CP answers", async () => {
    let answer!: (id: string) => void;
    addDevice = () => new Promise((r) => (answer = r));
    const p = enablePush("AQAB");
    await vi.waitFor(() => expect(answer).toBeTypeOf("function"));
    await releasePushForSignOut();
    released.length = 0;
    unsubscribed = 0;
    answer("dev1");
    expect(await p).toBe("cancelled");
    expect(released).toEqual([sub.toJSON()]);
    expect(unsubscribed).toBe(1);
    expect(localStorage.getItem("af-push-sub-id")).toBeNull();
  });
  it("a failed registration takes back only a subscription this call made", async () => {
    addDevice = () => Promise.reject(new Error("cap"));
    await expect(enablePush("AQAB")).rejects.toThrow("cap"); // reuses the existing subscription
    expect(unsubscribed).toBe(0);
    current = null;
    await expect(enablePush("AQAB")).rejects.toThrow("cap"); // made it itself
    expect(unsubscribed).toBe(1);
  });
  it("gives up with an error when the service worker never becomes ready", async () => {
    vi.useFakeTimers();
    Object.defineProperty(navigator, "serviceWorker", {
      value: { getRegistration: () => Promise.resolve(undefined), ready: new Promise(() => {}) },
      configurable: true,
    });
    const p = enablePush("AQAB");
    const failed = expect(p).rejects.toThrow("service worker not ready");
    await vi.advanceTimersByTimeAsync(10_000);
    await failed;
    expect(subscribed).toBe(0);
  });
  it("derives the CP's row id from the endpoint", async () => {
    const want = createHash("sha256").update("https://push.example/x").digest("hex").slice(0, 16);
    expect(await deviceIdFor({ endpoint: "https://push.example/x" } as PushSubscription)).toBe(want);
  });
});
