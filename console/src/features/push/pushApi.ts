// Web Push endpoints of the Control Plane (#4750). Every call answers the Response so a caller can
// tell "push is not configured on this deployment" (503) from a real failure. The paths stay
// string literals: control-plane/console_routes_test.go reads them as text.
import { raw, rawJSON } from "../../core/api/client.ts";

export interface PushDevice {
  id: string;
  userAgent: string;
  createdAt: string;
  lastOkAt: string;
}

/** The kinds a member may turn off for push; the CP refuses any other (pushsrv.PrefKinds). */
export const PUSH_KINDS = ["question", "plan-approval", "permission-request", "answer-ready"] as const;
export type PushKind = (typeof PUSH_KINDS)[number];

/** The applicationServerKey, or null when this deployment has no Web Push. */
export async function getPushKey(): Promise<string | null> {
  try {
    const r = await raw("api/push/config");
    if (!r.ok) return null;
    const j = await r.json();
    return typeof j?.publicKey === "string" && j.publicKey ? j.publicKey : null;
  } catch {
    return null;
  }
}

export async function listPushDevices(): Promise<PushDevice[]> {
  const r = await raw("api/push/subscriptions");
  if (!r.ok) throw new Error(String(r.status));
  const j = await r.json();
  return Array.isArray(j?.subscriptions) ? j.subscriptions : [];
}

export async function addPushDevice(sub: PushSubscriptionJSON): Promise<string> {
  const r = await rawJSON("api/push/subscriptions", "POST", { endpoint: sub.endpoint, keys: sub.keys });
  if (!r.ok) {
    let msg = String(r.status);
    try {
      msg = (await r.json())?.error?.message || msg;
    } catch {
      /* keep the status */
    }
    throw new Error(msg);
  }
  return String((await r.json())?.id || "");
}

export async function removePushDevice(id: string): Promise<void> {
  const r = await raw(`api/push/subscriptions/${encodeURIComponent(id)}`, { method: "DELETE" });
  if (!r.ok && r.status !== 404) throw new Error(String(r.status));
}

export async function getPushOff(): Promise<string[]> {
  const r = await raw("api/push/prefs");
  if (!r.ok) throw new Error(String(r.status));
  const j = await r.json();
  return Array.isArray(j?.off) ? j.off : [];
}

export async function putPushOff(off: string[]): Promise<void> {
  const r = await rawJSON("api/push/prefs", "PUT", { off });
  if (!r.ok) throw new Error(String(r.status));
}

export async function postViewing(device: string, session: string): Promise<void> {
  await rawJSON("api/push/viewing", "POST", { device, session });
}

/** Sign-out: drops the browser's subscription server-side; the keys are the proof (no session needed). */
export async function releasePushEndpoint(sub: PushSubscriptionJSON, signal?: AbortSignal): Promise<void> {
  await raw("api/push/release", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ endpoint: sub.endpoint, keys: sub.keys }),
    signal,
  });
}
