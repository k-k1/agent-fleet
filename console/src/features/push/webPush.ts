// Browser side of Web Push (#4750): subscribe / unsubscribe this device through the service worker
// (public/sw.js), and release it on sign-out. Nothing here caches or intercepts a request.
import { addPushDevice, getPushKey, releasePushEndpoint, removePushDevice } from "./pushApi.ts";

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

export const thisDeviceId = (): string => {
  try {
    return localStorage.getItem(ID_KEY) || "";
  } catch {
    return "";
  }
};

function b64urlToBytes(s: string): Uint8Array<ArrayBuffer> {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const bin = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(new ArrayBuffer(bin.length));
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export type EnableResult = "ok" | "denied" | "unsupported" | "unavailable";

/** Asks permission, subscribes and registers this browser with the CP. Throws the CP's message on refusal. */
export async function enablePush(): Promise<EnableResult> {
  if (pushSupport() !== "ok") return "unsupported";
  const key = await getPushKey();
  if (!key) return "unavailable";
  const perm = Notification.permission === "default" ? await Notification.requestPermission() : Notification.permission;
  if (perm !== "granted") return "denied";
  const reg = await navigator.serviceWorker.ready;
  let sub = await reg.pushManager.getSubscription();
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64urlToBytes(key) });
  try {
    const id = await addPushDevice(sub.toJSON());
    localStorage.setItem(ID_KEY, id);
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
  const json = sub.toJSON();
  const id = thisDeviceId();
  if (id) await removePushDevice(id);
  else await releasePushEndpoint(json);
  await sub.unsubscribe().catch(() => {});
  try {
    localStorage.removeItem(ID_KEY);
  } catch {
    /* ignore */
  }
}

/**
 * Sign-out: the CP stops pushing to this browser before the session ends. Bounded, because a slow
 * network must not hold the sign-out; the unsubscribe also happens locally.
 */
export async function releasePushForSignOut(timeoutMs = 2500): Promise<void> {
  const sub = await currentSubscription();
  if (!sub) return;
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), timeoutMs);
  try {
    await releasePushEndpoint(sub.toJSON(), ctl.signal);
  } catch {
    /* offline or slow: the unsubscribe below still ends delivery, the CP prunes on the next 410 */
  } finally {
    clearTimeout(timer);
  }
  await sub.unsubscribe().catch(() => {});
}
