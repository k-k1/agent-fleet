// Fetches the kind catalog into kindCatalog.ts. Kept apart so registry.ts, which every screen
// imports, does not pull the API client in with it.
import { api, getTenant } from "../core/api/client.ts";
import { isAuthExpired } from "../core/auth/authExpired.ts";
import { hasKindCatalog, parseKindCatalog, setKindCatalog } from "./kindCatalog.ts";

// ── loading ──────────────────────────────────────────────────────────────────────────
// The answer is fixed per Agent build, so a success lives for the page. It is dropped on a
// tenant switch (a different workspace may run a different Agent build). A failure is not
// cached: the fallback answers meanwhile and the request is retried with a growing delay,
// because the usual cause is a workspace that is still starting.
const RETRY_MS = [3_000, 10_000, 30_000, 60_000];

let inflight: Promise<void> | null = null;
let retryTimer: ReturnType<typeof setTimeout> | null = null;
let attempt = 0;
let epoch = 0;
let owner: string | null = null; // the tenant the catalog, the request or the pending retry belongs to

// A failure that asking again cannot fix: the login has expired, or the request itself was
// refused (4xx other than the "slow down / try later" pair). Network errors, 5xx, an
// unparsable body and an empty catalog are the retryable kind.
function isTerminal(body: unknown): boolean {
  if (isAuthExpired()) return true;
  const st = (body as { error?: { status?: unknown } } | null)?.error?.status;
  return typeof st === "number" && st >= 400 && st < 500 && st !== 408 && st !== 429;
}

export function loadKindCatalog(): Promise<void> {
  if (hasKindCatalog()) return Promise.resolve();
  if (inflight) return inflight;
  if (retryTimer) return Promise.resolve(); // a backoff is already pending: a second chain would double every retry
  const mine = epoch;
  const tenant = getTenant();
  owner = tenant;
  const p: Promise<void> = Promise.resolve()
    .then(() => api("api/agents/kinds")) // relative: the Console may be served under a sub-path
    .catch(() => null)
    .then((raw) => {
      if (mine !== epoch) return; // reset while in flight: this answer belongs to the old tenant
      inflight = null;
      if (getTenant() !== tenant) {
        // The tenant moved without a reset (boot picks it after the first request could start).
        void loadKindCatalog();
        return;
      }
      const cat = parseKindCatalog(raw);
      if (cat) {
        attempt = 0;
        setKindCatalog(cat);
        return;
      }
      if (isTerminal(raw)) return; // the next reset / tenant switch asks again
      const delay = RETRY_MS[Math.min(attempt++, RETRY_MS.length - 1)];
      retryTimer = setTimeout(() => {
        retryTimer = null;
        if (!isAuthExpired()) void loadKindCatalog();
      }, delay);
    });
  inflight = p;
  return p;
}

/** Makes the catalog belong to the current tenant: drops one fetched for another (or a request or
 *  retry backoff running for another) and loads again.
 *  Called from an effect keyed on the tenant, because the tenant can change at any moment of
 *  boot (a retried whoami picking the ?tenant= hint late) and no ordering of one-shot calls
 *  covers that. Does nothing before the first load has been asked for. */
export function syncKindCatalogTenant(): void {
  if (owner !== null && owner !== getTenant()) resetKindCatalog(); // answer, request or retry alike
  void loadKindCatalog();
}

/** Loads once `ready` (the tenant settling) resolves, unless the caller has been torn down by
 *  then: a cleanup's reset must not be followed by a load from the effect it belonged to. */
export async function loadKindCatalogAfter(ready: Promise<unknown>, alive: () => boolean): Promise<void> {
  await ready;
  if (alive()) void loadKindCatalog();
}

/** Forgets the catalog and any pending retry; the next loadKindCatalog() asks again. */
export function resetKindCatalog(): void {
  epoch++;
  owner = null;
  inflight = null;
  attempt = 0;
  if (retryTimer) clearTimeout(retryTimer);
  retryTimer = null;
  setKindCatalog(null);
}
