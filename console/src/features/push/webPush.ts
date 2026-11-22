// Browser side of Web Push (#4750): subscribe / unsubscribe this device through the service worker
// (public/sw.js), and release it on sign-out. Nothing here caches or intercepts a request.
import { addPushDevice, releasePushEndpoint, removePushDevice } from "./pushApi.ts";

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

export type EnableResult = "ok" | "denied" | "unsupported" | "unavailable";

const READY_TIMEOUT_MS = 10_000;
// serviceWorker.ready never settles when the worker failed to register; bound it so the UI recovers.
function swReady(): Promise<ServiceWorkerRegistration> {
  return Promise.race([
    navigator.serviceWorker.ready,
    new Promise<never>((_, rej) => setTimeout(() => rej(new Error("service worker not ready")), READY_TIMEOUT_MS)),
  ]);
}

/**
 * Asks permission, subscribes and registers this browser with the CP. `key` is the CP's public key,
 * loaded before the click: requestPermission is the first await so it still runs inside the user's
 * tap (iOS refuses the prompt once the activation has lapsed). Throws the CP's message on refusal.
 */
export async function enablePush(key: string | null): Promise<EnableResult> {
  if (pushSupport() !== "ok") return "unsupported";
  if (!key) return "unavailable";
  const perm = Notification.permission === "default" ? await Notification.requestPermission() : Notification.permission;
  if (perm !== "granted") return "denied";
  const reg = await swReady();
  let sub = await reg.pushManager.getSubscription();
  if (sub && !subscriptionMatchesKey(sub, key)) {
    // Made with a key the CP no longer sends with (rotation): its pushes would be refused, so end
    // it on both sides first (the old row also frees a slot at the device cap) and start over.
    // The CP row goes first and its failure stops the swap: the old keys are needed to retry.
    await releasePushEndpoint(sub.toJSON());
    await sub.unsubscribe().catch(() => {});
    forgetDeviceId();
    sub = null;
  }
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64urlToBytes(key) });
  try {
    storeDeviceId(await addPushDevice(sub.toJSON()));
  } catch (e) {
    // A subscription the CP does not know would be a device that silently gets nothing.
    await sub.unsubscribe().catch(() => {});
    throw e;
  }
  return "ok";
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
  const ctl = new AbortController();
  const work = (async () => {
    // One retry for a read that failed: reading nothing must not be taken for "no subscription".
    const sub = (await readSubscription().catch(() => readSubscription().catch(() => undefined))) ?? null;
    if (!sub) return;
    // Both start now, side by side: a CP call that outlasts the deadline must not postpone ending
    // the browser's own subscription until after the page has navigated away.
    const json = sub.toJSON();
    await Promise.allSettled([releasePushEndpoint(json, ctl.signal), sub.unsubscribe()]);
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
