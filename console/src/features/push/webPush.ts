// Browser side of Web Push (#4750): subscribe / unsubscribe this device through the service worker
// (public/sw.js), and release it on sign-out. Nothing here caches or intercepts a request.
import { addPushDevice, probePushKey, releasePushEndpoint, removePushDevice } from "./pushApi.ts";

// The CP's id of this browser's subscription, kept to mark "this device" in the list. Best effort:
// without it the list simply marks nothing. The af prefix makes clearLocalState drop it at sign-out.
const ID_KEY = "af-push-sub-id";

export type PushSupport = "ok" | "unsupported";

export function pushSupport(): PushSupport {
  return "serviceWorker" in navigator && "PushManager" in window && "Notification" in window ? "ok" : "unsupported";
}

async function registration(): Promise<ServiceWorkerRegistration | null> {
  if (pushSupport() !== "ok") return null;
  try {
    return (await navigator.serviceWorker.getRegistration()) ?? null;
  } catch {
    return null;
  }
}

export async function currentSubscription(): Promise<PushSubscription | null> {
  const reg = await registration();
  try {
    return (await reg?.pushManager.getSubscription()) ?? null;
  } catch {
    return null;
  }
}

/** Like currentSubscription, but a failed read rejects instead of reading as "none". */
async function readSubscription(): Promise<PushSubscription | null> {
  if (pushSupport() !== "ok") return null;
  const reg = await navigator.serviceWorker.getRegistration();
  return (await reg?.pushManager.getSubscription()) ?? null;
}

export const thisDeviceId = (): string => {
  try {
    return localStorage.getItem(ID_KEY) || "";
  } catch {
    return "";
  }
};
const storeDeviceId = (id: string) => {
  try {
    localStorage.setItem(ID_KEY, id);
  } catch {
    /* quota / private mode: the list just marks no device */
  }
};
const forgetDeviceId = () => {
  try {
    localStorage.removeItem(ID_KEY);
  } catch {
    /* ignore */
  }
};

/**
 * The CP's id for a subscription: the first 8 bytes of SHA-256 over the endpoint, hex (the CP's
 * store.PushSubscriptionID). "" where the browser has no SubtleCrypto.
 */
async function derivedDeviceId(endpoint: string): Promise<string> {
  try {
    const sum = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(endpoint));
    return [...new Uint8Array(sum).slice(0, 8)].map((b) => b.toString(16).padStart(2, "0")).join("");
  } catch {
    return "";
  }
}

/**
 * The CP row id of this browser's subscription, "" when it can be told by neither means. Derived from the endpoint rather than read from
 * localStorage alone: the stored id is lost with the storage (cleared site data, another profile
 * mode) while the subscription and its CP row live on.
 */
export async function deviceIdFor(sub: PushSubscription): Promise<string> {
  return (await derivedDeviceId(sub.endpoint)) || thisDeviceId();
}

function b64urlToBytes(s: string): Uint8Array<ArrayBuffer> {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const bin = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(new ArrayBuffer(bin.length));
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function bytesToB64url(buf: ArrayBuffer | null | undefined): string {
  if (!buf) return "";
  let bin = "";
  for (const b of new Uint8Array(buf)) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Whether the browser's subscription was made with the CP's current key (false after a key rotation). */
export function subscriptionMatchesKey(sub: PushSubscription, key: string): boolean {
  return bytesToB64url(sub.options?.applicationServerKey) === key.replace(/=+$/, "");
}

// "cancelled": the member signed out while the enable was running; nothing is left registered.
export type EnableResult = "ok" | "denied" | "unsupported" | "unavailable" | "cancelled";

// A sign-out stops the enables that are running (epoch) and keeps new ones from starting for a
// window (blockedUntil): a subscription made after the release read would be registered under the
// account that is leaving and reach whoever signs in next. The block is a window, not a latch,
// because a sign-out can be called off after it started (the browser's "leave this page?" prompt
// on a terminal pane): the person then stays and must be able to turn push on again.
let epoch = 0;
let blockedUntil = 0;
const SIGN_OUT_BLOCK_EXTRA_MS = 5_000;
// Enables that have started changing something; the sign-out waits for them (inside its own
// deadline) so that what they take back is done before the page goes. One that is still asking for
// permission or reading the key has changed nothing and is not waited for.
const running = new Set<Promise<unknown>>();

const KEY_READ_TIMEOUT_MS = 5_000;

const READY_TIMEOUT_MS = 10_000;
// serviceWorker.ready never settles when the worker failed to register; bound it so the UI recovers.
function swReady(): Promise<ServiceWorkerRegistration> {
  return Promise.race([
    navigator.serviceWorker.ready,
    new Promise<never>((_, rej) => setTimeout(() => rej(new Error("service worker not ready")), READY_TIMEOUT_MS)),
  ]);
}

/**
 * Asks permission, subscribes and registers this browser with the CP. `key` is the CP's public key
 * as the screen last saw it, loaded before the click: requestPermission is the first await so it
 * still runs inside the user's tap (iOS refuses the prompt once the activation has lapsed). The key
 * is read again right after the prompt, since the CP may have rotated it while the screen was open;
 * the screen's copy is used only when that read fails or takes too long. Throws the CP's message on
 * refusal. Resolves "cancelled" (never an error) once a sign-out has started.
 */
export function enablePush(key: string | null, onKey?: (used: string) => void): Promise<EnableResult> {
  let p: Promise<EnableResult>;
  const begin = () => {
    running.add(p);
    const done = () => void running.delete(p);
    p.then(done, done);
  };
  p = doEnable(key, begin, onKey);
  return p;
}

async function doEnable(key: string | null, begin: () => void, onKey?: (used: string) => void): Promise<EnableResult> {
  if (Date.now() < blockedUntil) return "cancelled";
  const mine = epoch;
  const gone = () => epoch !== mine;
  if (pushSupport() !== "ok") return "unsupported";
  if (!key) return "unavailable";
  let created: PushSubscription | null = null;
  try {
    const perm = Notification.permission === "default" ? await Notification.requestPermission() : Notification.permission;
    if (gone()) return "cancelled"; // an answer that arrives while the page is leaving says nothing worth showing
    if (perm !== "granted") return "denied";
    const fresh = await readKey(key);
    if (gone()) return "cancelled";
    if (!fresh) return "unavailable";
    onKey?.(fresh);
    const reg = await swReady();
    if (gone()) return "cancelled";
    let sub = await reg.pushManager.getSubscription();
    if (gone()) return "cancelled";
    begin();
    if (sub && !subscriptionMatchesKey(sub, fresh)) {
      // Made with a key the CP no longer sends with (rotation): its pushes would be refused, so end
      // it on both sides first (the old row also frees a slot at the device cap) and start over.
      // The CP row goes first and its failure stops the swap: the old keys are needed to retry.
      await releasePushEndpoint(sub.toJSON());
      if (gone()) return "cancelled";
      await sub.unsubscribe().catch(() => {});
      forgetDeviceId();
      sub = null;
    }
    if (gone()) return "cancelled";
    // Only a subscription this call made is taken back on a failure: one that was already there
    // belongs to an earlier enable and is not this call's to end.
    if (!sub) {
      sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64urlToBytes(fresh) });
      created = sub;
    }
    if (gone()) {
      if (created) await created.unsubscribe().catch(() => {});
      return "cancelled";
    }
    // The endpoint and keys are taken before the POST: whatever the POST's fate, a sign-out that
    // started meanwhile can still release this exact subscription.
    const json = sub.toJSON();
    let id: string;
    try {
      id = await addPushDevice(json);
    } catch (e) {
      // A subscription the CP does not know would be a device that silently gets nothing. When the
      // answer was lost the CP may have the row anyway, so a sign-out also releases it.
      if (gone()) {
        await takeBack(json, sub);
        return "cancelled";
      }
      if (created) await created.unsubscribe().catch(() => {});
      if (gone()) {
        // The sign-out started while the local end was being ended: its own read found nothing,
        // so the CP row (if the lost answer hid one) is released here with the keys taken above.
        await releasePushEndpoint(json).catch(() => {});
        return "cancelled";
      }
      throw e;
    }
    if (gone()) {
      // The CP accepted it after the sign-out released this browser: take it off again.
      await takeBack(json, sub);
      return "cancelled";
    }
    storeDeviceId(id);
    return "ok";
  } catch (e) {
    // A failure on the way out (a permission or key read, a worker that is gone) is not news.
    if (gone()) {
      if (created) await created.unsubscribe().catch(() => {});
      return "cancelled";
    }
    throw e;
  }
}

/** The CP's key now; `cached` when the read fails or does not answer in time (a hung request must not hold the switch). */
async function readKey(cached: string): Promise<string | null> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      probePushKey(),
      new Promise<string>((res) => (timer = setTimeout(() => res(cached), KEY_READ_TIMEOUT_MS))),
    ]);
  } catch {
    return cached;
  } finally {
    clearTimeout(timer);
  }
}

/** Ends a subscription on both sides at once; neither half waits for the other. */
async function takeBack(json: PushSubscriptionJSON, sub: PushSubscription): Promise<void> {
  await Promise.allSettled([releasePushEndpoint(json), sub.unsubscribe()]);
  forgetDeviceId();
}

/**
 * Drops this browser's own subscription after its CP row is already gone (the device list's
 * "remove" on this very device): nothing more to tell the CP.
 */
export async function dropLocalSubscription(rowId = ""): Promise<void> {
  const sub = await currentSubscription();
  if (!sub) return void forgetDeviceId();
  // Another subscription may have replaced the one whose row was removed while the CP was
  // answering; that one is a different device registration and stays.
  if (rowId && (await deviceIdFor(sub)) !== rowId) return;
  forgetDeviceId();
  await sub?.unsubscribe().catch(() => {});
}

/** Removes this browser's subscription from the CP and the push service. */
export async function disablePush(): Promise<void> {
  const sub = await currentSubscription();
  if (!sub) return;
  // Server first: if it fails the browser keeps the subscription and the UI still shows the device.
  const id = thisDeviceId();
  if (id) await removePushDevice(id);
  else await releasePushEndpoint(sub.toJSON());
  forgetDeviceId();
  await sub.unsubscribe().catch(() => {});
}

/**
 * Sign-out: the CP stops pushing to this browser before the session ends. The whole call is bounded
 * (a slow network or a push service that never answers must not hold the sign-out); whatever is
 * still pending at the deadline is left to finish on its own, and the CP prunes on the next 410.
 */
export async function releasePushForSignOut(timeoutMs = 2500): Promise<void> {
  epoch++;
  blockedUntil = Date.now() + timeoutMs + SIGN_OUT_BLOCK_EXTRA_MS;
  const ctl = new AbortController();
  const work = (async () => {
    // Enables already running take back what they made; waiting for them also keeps the page here
    // until they have (the deadline below still bounds all of it).
    const pending = Promise.allSettled([...running]);
    // One retry for a read that failed: reading nothing must not be taken for "no subscription".
    const sub = (await readSubscription().catch(() => readSubscription().catch(() => undefined))) ?? null;
    if (!sub) return void (await pending);
    // Both start now, side by side: a CP call that outlasts the deadline must not postpone ending
    // the browser's own subscription until after the page has navigated away.
    const json = sub.toJSON();
    await Promise.allSettled([releasePushEndpoint(json, ctl.signal), sub.unsubscribe(), pending]);
  })();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<void>((res) => {
    timer = setTimeout(() => {
      ctl.abort();
      res();
    }, timeoutMs);
  });
  try {
    await Promise.race([work, deadline]);
  } finally {
    clearTimeout(timer);
  }
}
