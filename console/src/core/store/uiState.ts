// uiState — the member's Console-only UI state, kept in the control plane (ADR 0114) so it is
// readable while the workspace is stopped and follows the member to another device. Settings
// the Agent acts on belong in ui-prefs (lib/settings.ts), never here as well.
//
// localStorage holds a copy for the first paint only, keyed per (user, tenant). The CP answer
// replaces it as soon as it arrives.
import { create } from "zustand";
import { api, getTenant, getUser } from "../api/client.ts";

/** The first-run state. Mirrors the CP's onboardingStateWire (control-plane/member_ui_state.go). */
export interface OnboardingState {
  /** The member put the setup checklist away ("later"). */
  dismissed: boolean;
  /** The goal track they opened: "dev" = the development steps are expanded. */
  track: "" | "dev";
}

/** Launch defaults: the tenant's, and the member's own override ("" / null = not set at that layer).
 *  Mirrors launchDefaultsWire. They only prefill the launch dialog; the Agent's ui-prefs are never
 *  written from them (ADR 0114 P3 note). */
export interface LaunchDefaults {
  agent: string;
  model: string;
  /** "", "managed" or "terminal". */
  driver: string;
  /** "", "ask" or "skip". */
  permission_mode: string;
  /** null = unset, 0 = no budget. */
  budget_usd: number | null;
}

/** Non-secret ops settings. Mirrors opsSettingsWire. */
export interface OpsSettings {
  grafana_url: string;
  aws_sso_start_url: string;
  aws_account_id: string;
  aws_role_name: string;
}

/** The tenant's defaults for its members (ADR 0114 decision 8). Mirrors tenantMemberDefaultsWire
 *  (control-plane/tenant_member_defaults.go). Read-only here: only a tenant admin sets them. */
export interface TenantMemberDefaults {
  /** The agent the first-run card names and the launch dialog starts on; "" = none. */
  agent: string;
  launch?: LaunchDefaults;
  /** owner/name paths offered first in Start. */
  repos?: string[];
  ops?: OpsSettings;
}

/** What the member's Console uses: built-in < tenant < the member's own, merged by the CP
 *  (effectiveWire). Never re-derive it here. */
export interface EffectiveDefaults {
  launch: LaunchDefaults;
  repos: string[];
  ops: OpsSettings;
}

/** The whole document. Mirrors memberUIStateWire; a PATCH replaces the top-level keys it carries.
 *  `defaults` and `effective` come back on every answer and are never sent. `launch` is the
 *  member's own override: `{}`-like (all empty) clears it. */
export interface MemberUIState {
  onboarding?: OnboardingState;
  launch?: LaunchDefaults;
  ops?: OpsSettings;
  defaults?: TenantMemberDefaults;
  effective?: EffectiveDefaults;
}

// The keys the first-run card used before ADR 0114. Read once to migrate, then removed.
const LEGACY_DISMISS = "af.onboarding.dismissed";
const LEGACY_TRACK = "af.onboarding.track";

const cacheKey = () => `af.ui-state:${getUser()}:${getTenant()}`;

function readCache(key: string): MemberUIState {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as MemberUIState) : {};
  } catch {
    return {};
  }
}

function writeCache(key: string, s: MemberUIState) {
  try {
    localStorage.setItem(key, JSON.stringify(s));
  } catch {}
}

/** The pre-ADR-0114 onboarding keys, or null when this browser never wrote any. */
export function legacyOnboarding(): OnboardingState | null {
  try {
    const d = localStorage.getItem(LEGACY_DISMISS);
    const t = localStorage.getItem(LEGACY_TRACK);
    if (d === null && t === null) return null;
    return { dismissed: d === "1", track: t === "dev" ? "dev" : "" };
  } catch {
    return null;
  }
}

function clearLegacy() {
  try {
    localStorage.removeItem(LEGACY_DISMISS);
    localStorage.removeItem(LEGACY_TRACK);
  } catch {}
}

/** A CP answer is a document only when it is an object without `error`: api() resolves HTTP
 *  failures as `{error}` instead of throwing. */
const isDoc = (v: unknown): v is MemberUIState => !!v && typeof v === "object" && !("error" in v);

// Request bookkeeping, outside the store so it never re-renders anything.
//
// `owner` is the (user, tenant) cache key the state belongs to, and `epoch` changes every time the
// owner does. A request remembers the epoch it was made in and its answer is used only in that
// same epoch: comparing the key alone would let a stale answer from the first visit to a tenant
// land after switching away and back (A, B, A).
//
// `pending` holds keys patched locally and not yet confirmed. Every answer is shown with them on
// top, so neither an earlier PATCH's answer nor a GET started before a PATCH can roll a newer
// choice back. A key leaves `pending` when an answer confirms the value it still holds, or when
// its PATCH failed: the failed value stays on screen until the next answer, which then shows what
// the CP holds instead of a choice that was never saved.
//
// `writes` counts confirmed PATCHes in the epoch: a GET that started before one of them carries
// an older document than the PATCH answer already shown, so its document is not used.
let owner = "";
let epoch = 0;
let pending: MemberUIState = {};
let writes = 0;

// A new owner starts a new epoch with nothing pending.
function claim(key: string) {
  if (key === owner) return;
  owner = key;
  epoch++;
  pending = {};
  writes = 0;
}
// PATCHes go one at a time, so their answers arrive in the order the choices were made.
let queue: Promise<unknown> = Promise.resolve();

function adopt(key: string, doc: MemberUIState, set: (s: Partial<UIStateStore>) => void) {
  const shown = { ...doc, ...pending };
  set({ state: shown });
  writeCache(key, shown);
}

interface UIStateStore {
  state: MemberUIState;
  /** The CP has answered for the current (user, tenant). Until then `state` is the cache. */
  loaded: boolean;
  /** Re-reads the cache for the current (user, tenant) and asks the CP. Call on boot, on a
   *  tenant switch and when the signed-in user is resolved. */
  load(): Promise<void>;
  /** Applies locally at once, then stores. Resolves true once the CP confirmed it. A failed
   *  store keeps the local value for this tab; the next load brings back what the CP holds.
   *  `ifAbsent` asks the CP to fill only keys it does not hold yet. */
  patch(p: MemberUIState, opts?: { ifAbsent?: boolean }): Promise<boolean>;
}

export const useUIStateStore = create<UIStateStore>((set, get) => ({
  state: readCache(cacheKey()),
  loaded: false,
  async load() {
    const key = cacheKey();
    if (key !== owner) {
      claim(key);
      set({ state: readCache(key), loaded: false });
    }
    const ep = epoch;
    const startWrites = writes;
    let res: unknown;
    try {
      res = await api("api/me/ui-state");
    } catch {
      return; // CP unreachable: keep the cache, stay unloaded so nobody reads it as fresh
    }
    // Another epoch took over, or the CP answered an error: keep what is shown, unloaded.
    if (epoch !== ep || !isDoc(res)) return;
    if (writes === startWrites) adopt(key, res, set);
    set({ loaded: true });
    const legacy = legacyOnboarding();
    // A choice made in this tab since supersedes the browser's old value. Uploading it now would
    // replace that choice on screen; the next load finds the CP holding it and clears the keys
    // (or, if that write failed, still has them to upload).
    if (!legacy || pending.onboarding) return;
    // The old keys go only once the CP holds an onboarding value: a failed upload keeps them
    // for the next load. if_absent keeps a late upload from another tab from undoing a value
    // stored in the meantime.
    if (get().state.onboarding && !pending.onboarding) clearLegacy();
    else if (await get().patch({ onboarding: legacy }, { ifAbsent: true })) clearLegacy();
  },
  patch(p, opts) {
    const key = cacheKey();
    claim(key);
    const ep = epoch;
    // Pinned now: the tenant header is otherwise filled in when the request is sent, and a
    // queued PATCH sent after a switch would write this tenant's choice into the other's row.
    const tenant = getTenant();
    pending = { ...pending, ...p };
    const shown = { ...get().state, ...p };
    set({ state: shown });
    writeCache(key, shown);
    const url = "api/me/ui-state" + (opts?.ifAbsent ? "?if_absent=1" : "");
    // On failure the key leaves `pending` (see the bookkeeping comment) unless a newer choice
    // replaced it meanwhile.
    const forget = () => {
      if (epoch !== ep) return;
      for (const k of Object.keys(p) as (keyof MemberUIState)[]) {
        if (pending[k] === p[k]) delete pending[k];
      }
    };
    const send = async (): Promise<boolean> => {
      // A PATCH still queued when the owner changed belongs to the previous one: never send it.
      if (epoch !== ep || cacheKey() !== key) return false;
      let res: unknown;
      try {
        res = await api(url, {
          method: "PATCH",
          headers: { "Content-Type": "application/json", ...(tenant ? { "X-AF-Tenant": tenant } : {}) },
          body: JSON.stringify(p),
        });
      } catch {
        forget();
        return false;
      }
      if (epoch !== ep) return false;
      if (!isDoc(res)) {
        forget();
        return false;
      }
      writes++;
      for (const k of Object.keys(p) as (keyof MemberUIState)[]) {
        if (pending[k] === p[k]) delete pending[k];
      }
      adopt(key, res, set);
      return true;
    };
    const done = queue.then(send, send);
    queue = done.catch(() => {});
    return done;
  },
}));

/** The CP-computed effective defaults: the last answer, from the per-(user, tenant) cache while the
 *  workspace is stopped or the CP is unreachable. undefined until one was ever received. */
export const useEffectiveDefaults = (): EffectiveDefaults | undefined => useUIStateStore((s) => s.state.effective);
