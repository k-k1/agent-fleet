// Web Push endpoints of the Control Plane (#4750). Every call answers the Response so a caller can
// tell "push is not configured on this deployment" (503) from a real failure. The paths stay
// string literals: control-plane/console_routes_test.go reads them as text.
import { raw, rawJSON } from "../../core/api/client.ts";

// The CP's release token for this browser's registration (answered by the add). Sent back with a
// release so a late release for an earlier registration cannot delete a later member's. The af
// prefix makes clearLocalState drop it at sign-out.
// webPush.ts removes it by the same key when it forgets the device id.
const RELEASE_TOKEN_KEY = "af-push-release-token";
/** The token and the subscription (by endpoint) it was issued for. */
export interface ReleaseBinding {
  endpoint: string;
  token: string;
}
/** What storage holds right now; take it when a release is decided on, not when it is sent. */
export const snapshotReleaseBinding = (): ReleaseBinding | null => {
  try {
    const j = JSON.parse(localStorage.getItem(RELEASE_TOKEN_KEY) || "null");
    return j && typeof j.endpoint === "string" && typeof j.token === "string" && j.token ? j : null;
  } catch {
    return null;
  }
};
const storeReleaseBinding = (b: ReleaseBinding | null) => {
  try {
    if (b) localStorage.setItem(RELEASE_TOKEN_KEY, JSON.stringify(b));
    else localStorage.removeItem(RELEASE_TOKEN_KEY);
  } catch {
    // The new binding could not be written, and the old one names a subscription this call has just
    // replaced: leaving it would make the next sign-out skip the new subscription. Without a
    // binding the release falls back to the keys alone.
    try {
      localStorage.removeItem(RELEASE_TOKEN_KEY);
    } catch {
      /* storage is unusable; nothing more to do */
    }
  }
};

export interface PushDevice {
  id: string;
  userAgent: string;
  createdAt: string;
  lastOkAt: string;
}

/** The kinds a member may turn off for push; the CP refuses any other (pushsrv.PrefKinds). */
export const PUSH_KINDS = ["question", "plan-approval", "permission-request", "answer-ready"] as const;
export type PushKind = (typeof PUSH_KINDS)[number];

/**
 * The applicationServerKey; null when this deployment has no Web Push (503) or the member cannot
 * use it (4xx). Throws on a transient failure (network, 5xx other than 503), so a caller can tell
 * "not configured" from "ask again later".
 */
export async function probePushKey(): Promise<string | null> {
  const r = await raw("api/push/config");
  if (r.status >= 400 && r.status < 500) return null;
  // Only the CP's own "not configured" answer is definite; a gateway's bare 503 during a restart is not.
  if (r.status === 503) {
    const code = await r.json().then((j) => j?.error?.code, () => "");
    if (code === "push_unavailable") return null;
    throw new Error("503");
  }
  if (!r.ok) throw new Error(String(r.status));
  const j = await r.json();
  return typeof j?.publicKey === "string" && j.publicKey ? j.publicKey : null;
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
  const j = await r.json();
  storeReleaseBinding(typeof j?.releaseToken === "string" && j.releaseToken ? { endpoint: String(sub.endpoint), token: j.releaseToken } : null);
  return String(j?.id || "");
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

/**
 * `tenant` pins the report to the tenant it was made for: a later tenant switch must not re-aim it.
 * `seq` rises with every report the device makes; the CP drops one that arrives after a later one.
 */
export async function postViewing(device: string, session: string, tenant?: string, seq?: number): Promise<void> {
  await raw("api/push/viewing", {
    method: "POST",
    headers: { "Content-Type": "application/json", ...(tenant ? { "X-AF-Tenant": tenant } : {}) },
    body: JSON.stringify({ device, session, ...(seq ? { seq } : {}) }),
    // A POST that never answers must not hold the reports queued behind it.
    signal: AbortSignal.timeout(10_000),
  });
}

/** Sign-out: drops the browser's subscription server-side; the keys are the proof (no session needed). */
export async function releasePushEndpoint(
  sub: PushSubscriptionJSON,
  signal?: AbortSignal,
  binding: ReleaseBinding | null = snapshotReleaseBinding(),
): Promise<void> {
  // A token issued for another subscription is not this one's. Callers that decide on a release
  // before an asynchronous step pass the binding they took first, so a registration made in
  // between (another tab, another member) is not the one released.
  const releaseToken = binding && binding.endpoint === sub.endpoint ? binding.token : "";
  const r = await raw("api/push/release", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ endpoint: sub.endpoint, keys: sub.keys, ...(releaseToken ? { releaseToken } : {}) }),
    signal,
  });
  // A 500 is not "released": the caller must not report the device as gone.
  if (!r.ok) throw new Error(String(r.status));
}
