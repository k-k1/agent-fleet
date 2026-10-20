// The workspace's preview state as the port controls need it (docs/log/81, ADR 0062): the
// issued subdomain URLs, which ports are listed, which unlisted ones the owner has open right
// now, and who else can see the listed ones. One store, so the session rows and the
// workspace bar agree on what a click does without each fetching it.
import { create } from "zustand";
import { api, getTenant } from "../../core/api/client.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useTenantStore } from "../../core/store/tenant.ts";

export interface PreviewInfo {
  /** The deployment's preview domain; "" when host-based previews are off. */
  domain: string;
  /** port → URL, only for ports with a URL issued for this start (empty while stopped). */
  urls: Record<string, string>;
  /** Listed ports (the allow-list). */
  ports: number[];
  /** Unlisted ports the owner has opened for this start (grants). */
  tempPorts: number[];
  public: boolean;
  tenantShare: boolean;
  maxPorts: number;
}

/** Reads the ws-settings payload; null when it carries no preview block. */
export function previewInfoFrom(res: any): PreviewInfo | null {
  if (!res || res.error || typeof res !== "object") return null;
  return {
    domain: typeof res.previewDomain === "string" ? res.previewDomain : "",
    urls: res.previewUrls && typeof res.previewUrls === "object" ? res.previewUrls : {},
    ports: Array.isArray(res.previewPorts) ? res.previewPorts : [],
    tempPorts: Array.isArray(res.previewTempPorts) ? res.previewTempPorts : [],
    public: !!res.previewPublic,
    tenantShare: !!res.previewTenantShare,
    maxPorts: Number(res.previewMaxPorts) || 8,
  };
}

/** Whose answer this is: the tenant AND the identity the Console is acting as. The same tenant
 *  with another owner (identityRev bumps) has other workspaces and other slugs. */
export function currentScope(): string {
  return getTenant() + "|" + useTenantStore.getState().identityRev;
}

interface PreviewStore {
  /** The last answer, and the scope (tenant|identity) it was read for. */
  info: PreviewInfo | null;
  scope: string;
  /** Store a settled answer (a read, a save, a reissue). It voids every older read in flight.
   *  `from` is what the caller captured when it STARTED (previewToken()): if the scope or the
   *  generation moved since, the answer is dropped instead of being filed under the new one. */
  set(info: PreviewInfo | null, from?: PreviewToken): void;
  /** Forget everything (workspace stopped / scope changed) and ignore any answer in flight. */
  reset(): void;
  /** Re-read from the CP. Without `force`, calls closer than MIN_GAP_MS, or while one is in
   *  flight for this scope, share the previous answer. `force` starts a new read. */
  refresh(opts?: { force?: boolean }): Promise<void>;
}

export interface PreviewToken {
  scope: string;
  /** The epoch (see below), not the read generation: a legitimate write must survive other writes. */
  epoch: number;
}

const MIN_GAP_MS = 4000;
let lastAt = 0;
let inflight: Promise<void> | null = null;
let inflightScope = "";
// Two counters, because they answer different questions.
//  - epoch: which workspace start / tenant / identity the held state belongs to. Bumped only
//    by reset(). A write (save, reissue) that began under an older epoch is refused; two writes
//    that began under the same epoch are both legitimate, whatever order they settle in.
//  - readGen: which read is the newest. Bumped by every forced read, reset and settled write,
//    so a slow older READ can never bring old URLs back over a newer answer.
let epoch = 0;
let gen = 0;

/** Whether the tenant and identity are still the ones the token was taken under. A queued
 *  write checks this right before it is SENT: the request carries the tenant current at send
 *  time, so a write typed under another tenant must be dropped, not sent to this one. (Only the
 *  scope, not the epoch: settings are editable while the workspace is stopped, and a stop must
 *  not eat a pending save.) */
export function previewScopeUnchanged(t: PreviewToken): boolean {
  return t.scope === currentScope();
}

/** Capture before an async call that will `set` its result. */
export function previewToken(): PreviewToken {
  return { scope: currentScope(), epoch };
}

export const usePreviewStore = create<PreviewStore>((set, get) => ({
  info: null,
  scope: "",
  set(info, from) {
    if (from && (from.scope !== currentScope() || from.epoch !== epoch)) return;
    gen++;
    inflight = null;
    set({ info, scope: currentScope() });
  },
  reset() {
    epoch++;
    gen++;
    inflight = null;
    lastAt = 0;
    set({ info: null, scope: "" });
  },
  refresh({ force = false } = {}) {
    const scope = currentScope();
    if (!force && inflight && inflightScope === scope) return inflight;
    if (!force && get().scope === scope && get().info && Date.now() - lastAt < MIN_GAP_MS) return Promise.resolve();
    const mine = ++gen;
    lastAt = Date.now();
    inflightScope = scope;
    const p: Promise<void> = api("api/env/ws-settings")
      .then(
        (res: any) => {
          if (mine === gen) set({ info: previewInfoFrom(res), scope });
        },
        () => {
          if (mine === gen) set({ info: null, scope });
        },
      )
      .finally(() => {
        if (inflight === p) inflight = null;
      });
    inflight = p;
    return p;
  },
}));

/** The info that is valid for the tenant and identity the Console is on now, else null (so a
 *  control falls back to the pane / lightweight preview instead of opening another owner's slug). */
export function useCurrentPreviewInfo(): PreviewInfo | null {
  // Subscribed so a tenant / identity change re-renders the control and re-evaluates the scope.
  useTenantStore((s) => s.tenant + "|" + s.identityRev);
  return usePreviewStore((s) => (s.scope === currentScope() ? s.info : null));
}

/** The non-hook form of useCurrentPreviewInfo, for a handler that decides at click time. */
export function currentPreviewInfo(): PreviewInfo | null {
  const s = usePreviewStore.getState();
  return s.scope === currentScope() ? s.info : null;
}

// A workspace that stops, restarts or is replaced issues a new slug, and a tenant or identity
// switch moves to other workspaces: drop what was read, and read again if one is running.
// One subscription here rather than one per control.
let lastWsState = useWorkspaceStore.getState().state;
useWorkspaceStore.subscribe((s) => {
  if (s.state === lastWsState) return;
  lastWsState = s.state;
  if (s.state === "running") void usePreviewStore.getState().refresh({ force: true });
  else usePreviewStore.getState().reset();
});
let lastScope = useTenantStore.getState().tenant + "|" + useTenantStore.getState().identityRev;
useTenantStore.subscribe((s) => {
  const now = s.tenant + "|" + s.identityRev;
  if (now === lastScope) return;
  lastScope = now;
  usePreviewStore.getState().reset();
  if (useWorkspaceStore.getState().state === "running") void usePreviewStore.getState().refresh({ force: true });
});
