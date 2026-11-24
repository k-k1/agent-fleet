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
// The module remembers that a sign-out started (the page never comes back from one), so each test
// loads a fresh copy.
type WebPush = typeof import("./webPush.ts");
let deviceIdFor: WebPush["deviceIdFor"];
let disablePush: WebPush["disablePush"];
let dropLocalSubscription: WebPush["dropLocalSubscription"];
let enablePush: WebPush["enablePush"];
let releasePushForSignOut: WebPush["releasePushForSignOut"];
let subscriptionMatchesKey: WebPush["subscriptionMatchesKey"];

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

beforeEach(async () => {
  vi.resetModules();
  ({ deviceIdFor, disablePush, dropLocalSubscription, enablePush, releasePushForSignOut, subscriptionMatchesKey } = await import("./webPush.ts"));
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

  it("an enable pressed after the sign-out started registers nothing, even while the old release is still out", async () => {
    const api = await import("./pushApi.ts");
    let finish!: () => void;
    vi.spyOn(api, "releasePushEndpoint").mockImplementation(() => new Promise<void>((r) => (finish = r)));
    const out = releasePushForSignOut(5000); // the old subscription's release is held
    await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
    unsubscribed = 0;
    current = null; // the old subscription is already gone
    expect(await enablePush("AQAB")).toBe("cancelled");
    expect(subscribed).toBe(0);
    finish();
    await out;
  });
  it("a key swap whose old release answers after the sign-out neither unsubscribes nor subscribes again", async () => {
    sub.options.applicationServerKey = keyBytes("AgAB");
    const api = await import("./pushApi.ts");
    let finish!: () => void;
    vi.spyOn(api, "releasePushEndpoint").mockImplementationOnce(() => new Promise<void>((r) => (finish = r)));
    localStorage.setItem("af-push-sub-id", "old");
    const p = enablePush("AQAB");
    await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
    const out = releasePushForSignOut(5000);
    await vi.waitFor(() => expect(unsubscribed).toBe(1)); // the sign-out's own unsubscribe
    finish();
    expect(await p).toBe("cancelled");
    await out;
    expect(unsubscribed).toBe(1);
    expect(subscribed).toBe(0);
    expect(localStorage.getItem("af-push-sub-id")).toBe("old");
  });
  it("releases the CP row after a sign-out even when the registration's answer is lost", async () => {
    let fail!: (e: Error) => void;
    addDevice = () => new Promise((_, rej) => (fail = rej));
    const p = enablePush("AQAB");
    await vi.waitFor(() => expect(fail).toBeTypeOf("function"));
    const out = releasePushForSignOut(50);
    await out;
    released.length = 0;
    fail(new Error("network")); // the CP may have committed the row
    expect(await p).toBe("cancelled");
    expect(released).toEqual([sub.toJSON()]);
  });
  it("the sign-out waits for a running enable to take its registration back", async () => {
    let answer!: (id: string) => void;
    addDevice = () => new Promise((r) => (answer = r));
    const p = enablePush("AQAB");
    await vi.waitFor(() => expect(answer).toBeTypeOf("function"));
    let out = false;
    const done = releasePushForSignOut(5000).then(() => (out = true));
    await new Promise((r) => setTimeout(r, 30));
    expect(out).toBe(false); // the enable has not settled
    released.length = 0;
    answer("dev1");
    await done;
    expect(await p).toBe("cancelled");
    expect(released).toEqual([sub.toJSON()]);
  });
  it("reports cancelled, not a denial, when the sign-out comes while the permission prompt is open", async () => {
    let decide!: (v: string) => void;
    (window as any).Notification = { permission: "default", requestPermission: () => new Promise((r) => (decide = r)) };
    const p = enablePush("AQAB");
    await vi.waitFor(() => expect(decide).toBeTypeOf("function"));
    const t0 = Date.now();
    await releasePushForSignOut(); // nothing has been changed yet: it does not wait for the prompt
    expect(Date.now() - t0).toBeLessThan(500);
    const probeCalls = vi.fn(() => Promise.resolve("AQAB"));
    probe = probeCalls;
    decide("granted");
    expect(await p).toBe("cancelled");
    expect(probeCalls).not.toHaveBeenCalled(); // granted after the sign-out goes no further
    expect(subscribed).toBe(0);
  });
  it("a subscribe() that fails after the sign-out started reads as cancelled", async () => {
    current = null;
    let fail!: (e: Error) => void;
    const reg = (await navigator.serviceWorker.getRegistration())!;
    vi.spyOn(reg.pushManager, "subscribe").mockImplementation(() => new Promise((_, rej) => (fail = rej)));
    const p = enablePush("AQAB");
    await vi.waitFor(() => expect(fail).toBeTypeOf("function"));
    await releasePushForSignOut();
    fail(new Error("push service down"));
    expect(await p).toBe("cancelled");
  });
  it("lets the person turn push on again on the same page after the sign-out was called off", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    await releasePushForSignOut(100); // e.g. the browser's "leave this page?" prompt was declined
    expect(await enablePush("AQAB")).toBe("cancelled");
    vi.setSystemTime(Date.now() + 6000);
    expect(await enablePush("AQAB")).toBe("ok");
  });
  it("releases the CP row when the sign-out reads nothing because the failed enable was already ending its subscription", async () => {
    current = null;
    let fail!: (e: Error) => void;
    addDevice = () => new Promise((_, rej) => (fail = rej));
    let finishUnsub!: () => void;
    const made = {
      ...sub,
      unsubscribe: () => {
        unsubscribed++;
        current = null; // the browser no longer lists it
        return new Promise<boolean>((r) => (finishUnsub = () => r(true)));
      },
    };
    const reg = (await navigator.serviceWorker.getRegistration())!;
    vi.spyOn(reg.pushManager, "subscribe").mockResolvedValue(made as never);
    const p = enablePush("AQAB");
    await vi.waitFor(() => expect(fail).toBeTypeOf("function"));
    fail(new Error("network")); // the CP may have committed the row; the answer is lost
    await vi.waitFor(() => expect(finishUnsub).toBeTypeOf("function"));
    await releasePushForSignOut(50); // reads null: nothing to release from its side
    expect(released).toEqual([]);
    finishUnsub();
    expect(await p).toBe("cancelled");
    expect(released).toEqual([sub.toJSON()]);
  });
  it("hands the screen the key an enable subscribed with", async () => {
    probe = () => Promise.resolve("AgAB");
    const seen: string[] = [];
    await enablePush("AQAB", (k) => seen.push(k));
    expect(seen).toEqual(["AgAB"]);
  });
  it("dropLocalSubscription leaves a subscription alone when it is not the one whose row was removed", async () => {
    localStorage.setItem("af-push-sub-id", "dev1");
    await dropLocalSubscription("0000000000000000"); // a different registration took this one's place
    expect(unsubscribed).toBe(0);
    expect(localStorage.getItem("af-push-sub-id")).toBe("dev1");
    const own = await deviceIdFor(sub as unknown as PushSubscription);
    await dropLocalSubscription(own);
    expect(unsubscribed).toBe(1);
  });
  it("asks for the permission before anything else, so it runs inside the tap", async () => {
    const order: string[] = [];
    (window as any).Notification = {
      permission: "default",
      requestPermission: () => (order.push("permission"), Promise.resolve("granted")),
    };
    probe = () => (order.push("key"), Promise.resolve("AQAB"));
    expect(await enablePush("AQAB")).toBe("ok");
    expect(order).toEqual(["permission", "key"]);
  });
  it("uses the screen's key when the key read does not answer in time", async () => {
    vi.useFakeTimers();
    current = null;
    probe = () => new Promise(() => {});
    const p = enablePush("AQAB");
    await vi.advanceTimersByTimeAsync(5000);
    expect(await p).toBe("ok");
    expect(subscribed).toBe(1);
  });
  it("dropLocalSubscription ends the browser side only and forgets the id", async () => {
    localStorage.setItem("af-push-sub-id", "dev1");
    await dropLocalSubscription();
    expect(unsubscribed).toBe(1);
    expect(removed).toEqual([]);
    expect(released).toEqual([]);
    expect(localStorage.getItem("af-push-sub-id")).toBeNull();
  });
  it("falls back to the stored id when the endpoint cannot be hashed, and to none without one", async () => {
    vi.spyOn(crypto.subtle, "digest").mockRejectedValue(new Error("no subtle"));
    const s = { endpoint: "https://push.example/x" } as PushSubscription;
    expect(await deviceIdFor(s)).toBe("");
    localStorage.setItem("af-push-sub-id", "dev1");
    expect(await deviceIdFor(s)).toBe("dev1");
  });
});
