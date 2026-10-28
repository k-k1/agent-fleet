// The ui-prefs server sync: the debounced PUT of this tab's settings, the hydrate that merges the
// server copy in, the record of whose copy this browser holds, and the resync across an identity
// switch. Every rule about which key may cross which way is read off the descriptors' scopes
// (prefs.ts). Re-exported from lib/settings.ts.
//
// ui-prefs sync has lost user data before: settingsEquivalence.dom.test.tsx pins every PUT body,
// merge result and timer here, and a change to any of them is a behaviour change, not a refactor.
import { useSyncExternalStore } from "react";
import { api, apiJSON } from "../../core/api/client.ts";
import {
  DEFAULTS,
  isAccumulatedSetting,
  isAgentDefaultedSetting,
  isDeviceLocalSetting,
  migrateIncoming,
  normalizeMerged,
  sameValue,
  type Settings,
} from "./prefs.ts";
import { fillRecommendedModelMaps, migrateAiAssistPrefs, normalizeAgentLaunchDefaults, normalizeAssistantOrder } from "./normalize.ts";
import { KEY, applyToPage, getSettings, notify, persist, setState } from "./store.ts";

// The notification table pins its device-local cells off a synced key on every hydrate
// (features/notifications/prefs.ts pinSyncedFallbacks). lib/ may not import features/ (ADR 0124),
// so lib/settings.ts hands the function in at load; until then nothing is pinned.
let pinNotifyFallbacks: (s: Settings) => Record<string, boolean> | null = () => null;

export function setNotifyFallbackPinner(fn: (s: Settings) => Record<string, boolean> | null): void {
  pinNotifyFallbacks = fn;
}

// Debounced mirror of the full settings object to the per-user server store. Best
// effort: if the workspace is stopped / agent unreachable, localStorage still holds it.
let saveTimer: ReturnType<typeof setTimeout> | null = null;
let saveInFlight: Promise<void> | null = null;
// Only the newest PUT's answer speaks for the sync state: an older one landing late (or the
// previous owner's, across an identity switch) must not overwrite it.
let saveGen = 0;
// Server-synced keys changed on this tab and not yet confirmed saved, with a per-key change
// count so a save only confirms the version it carried. A hydrate keeps these local values
// over the server's: without that, the only ways out of a failed save were to lose the change
// (server wins) or to PUT the whole tab's state blind over what other devices saved since.
const unsaved = new Map<keyof Settings, number>();
let changeSeq = 0;

/** Whether this tab's settings have reached the Agent's ui-prefs. The Console applies a change at
 *  once and saves it later, so while a save is failing the screen shows one setting and the Agent
 *  acts on another (a model un-hidden on screen is refused at launch; one hidden still runs) —
 *  the member has to be told, not just the console. */
export type PrefsSyncState = "synced" | "pending" | "failed";
let syncState: PrefsSyncState = "synced";
const syncSubs = new Set<() => void>();

function setSyncState(next: PrefsSyncState): void {
  if (syncState === next) return;
  syncState = next;
  syncSubs.forEach((fn) => fn());
}

export const prefsSyncState = (): PrefsSyncState => syncState;

/** Called on every sync-state change; returns the unsubscribe. */
export function subscribePrefsSync(fn: () => void): () => void {
  syncSubs.add(fn);
  return () => void syncSubs.delete(fn);
}

export function usePrefsSyncState(): PrefsSyncState {
  return useSyncExternalStore(
    (fn) => {
      syncSubs.add(fn);
      return () => void syncSubs.delete(fn);
    },
    prefsSyncState,
    prefsSyncState,
  );
}

/** Try again now: read the server copy (the unsaved keys stay local over it), then send. */
export function retryPrefsSync(): void {
  if (saveTimer || saveInFlight) return; // already on its way
  hydrateAttempt = 0;
  void hydrateUIPrefs();
}

// Whose server copy this browser's localStorage was last merged with, as "tenant|user". Read at
// the first hydrate: localStorage outlives a sign-out, so without it a previous account's
// accumulated data (working sets, hidden models, …) cannot be told from this account's
// unsynced data. The source is set by the app shell; unset, or with the user not yet resolved,
// the owner is unknown and every owner-dependent step takes its conservative branch.
const OWNER_KEY = "af-display-settings-owner";
let ownerSource: () => string = () => "";

export function setPrefsOwnerSource(fn: () => string): void {
  ownerSource = fn;
}

function readStoredOwner(): string {
  try {
    return localStorage.getItem(OWNER_KEY) || "";
  } catch {
    return "";
  }
}

function recordOwner(owner: string): void {
  if (!owner) return;
  try {
    localStorage.setItem(OWNER_KEY, owner);
  } catch {}
}

// Has the server's ui-prefs been read even once? Saving is a whole-object PUT where the last
// writer wins, so nothing may be sent before that read succeeds: an unhydrated state is only
// this browser's localStorage or DEFAULTS, not necessarily the user's settings, and PUTting it
// wipes the real server data (learned suggestions, pins, usage tallies, keybindings) with
// defaults. Damage: a GET returning 502 right after a workspace restart, together with an empty
// localStorage, let the learning from a single mirror reply PUT the full default set over the
// server, and it propagated from there to every other device.
let prefsLoaded = false;
// Local changes made before that read succeeded. Sent once, all together, as soon as it does.
let savePending = false;
let hydrateRetry: ReturnType<typeof setTimeout> | null = null;
let hydrateAttempt = 0;
// Left unfetched, changes would never sync, so retry a few times independently, starting from a
// short interval. Once those are used up, App's focus / visibilitychange (refreshUIPrefs) picks
// it up.
const HYDRATE_RETRY_MS = [2_000, 5_000, 15_000, 30_000, 60_000];

function scheduleHydrateRetry(): void {
  if (prefsLoaded || hydrateRetry) return;
  if (hydrateAttempt >= HYDRATE_RETRY_MS.length) {
    // Out of automatic retries with a change still waiting: it has not reached the Agent.
    if (savePending) setSyncState("failed");
    return;
  }
  const wait = HYDRATE_RETRY_MS[hydrateAttempt++];
  hydrateRetry = setTimeout(() => {
    hydrateRetry = null;
    void hydrateUIPrefs();
  }, wait);
}

// Called the moment the server's current values have been read. Pending local changes are sent
// out here, and not before.
function markPrefsLoaded(): void {
  prefsLoaded = true;
  hydrateAttempt = 0;
  if (hydrateRetry) {
    clearTimeout(hydrateRetry);
    hydrateRetry = null;
  }
  if (savePending) {
    savePending = false;
    scheduleServerSave();
  } else if (syncState === "failed" && !saveTimer && !saveInFlight && unsaved.size === 0) {
    // The read that failed is now done and nothing is waiting to go out.
    setSyncState("synced");
  }
}

export function scheduleServerSave(): void {
  if (!prefsLoaded) {
    savePending = true;
    // A failure stays shown until something lands; otherwise the change is waiting for the read.
    if (syncState !== "failed") setSyncState("pending");
    scheduleHydrateRetry();
    return;
  }
  if (syncState !== "failed") setSyncState("pending");
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    saveTimer = null;
    const gen = ++saveGen;
    const carried = new Map(unsaved);
    // Device-local keys are not sent to the server; they never leave this device.
    const flight: Promise<void> = apiJSON("api/env/ui-prefs", "PUT", serverPrefs(getSettings()))
      // Swallowing a failure means believing the settings are synced when they are not. A
      // permanent failure such as exceeding the 64 KiB limit (413) is only visible here, so it
      // must always be logged — and shown (usePrefsSyncState).
      .then((res) => {
        if (res && typeof res === "object" && res.error) {
          warnPrefsSaveFailed(res.error);
          if (gen === saveGen) setSyncState("failed");
          return;
        }
        for (const [k, seq] of carried) if (unsaved.get(k) === seq) unsaved.delete(k);
        if (gen === saveGen && !saveTimer && unsaved.size === 0) setSyncState("synced");
      })
      .catch((e) => {
        warnPrefsSaveFailed(e);
        if (gen === saveGen) setSyncState("failed");
      })
      .finally(() => {
        if (saveInFlight === flight) saveInFlight = null;
      });
    saveInFlight = flight;
  }, 600);
}

/** Test hook: cancel the debounced save and the hydrate retry, drop the queued change, and
 *  return what was still pending. A timer left running fires after its test, in another file's
 *  module environment (a different `apiJSON` mock, or none). Never called in production. */
export function cancelPrefsTimersForTest(): { save: boolean; hydrateRetry: boolean } {
  const pending = { save: saveTimer !== null, hydrateRetry: hydrateRetry !== null };
  if (saveTimer) clearTimeout(saveTimer);
  if (hydrateRetry) clearTimeout(hydrateRetry);
  saveTimer = null;
  hydrateRetry = null;
  savePending = false;
  // An answer still in flight (a GET or PUT this test started) must not re-arm the retry or
  // move the sync state after the test is over.
  hydrateGen++;
  saveGen++;
  return pending;
}

// Registry for src/test/prefsTimersSetup.ts. It cannot import this module itself: a test's
// vi.mock / vi.resetModules decides which instance (and which `api` binding) this file gets, so
// each instance registers its own hook here. Vite folds MODE at build time, so the production
// bundle carries neither the registration nor the hook.
if (import.meta.env.MODE === "test") {
  const key = Symbol.for("af.prefsTimerHooks");
  const g = globalThis as any;
  (g[key] ??= new Set<typeof cancelPrefsTimersForTest>()).add(cancelPrefsTimersForTest);
}

function warnPrefsSaveFailed(err: unknown): void {
  try {
    console.warn("[settings] failed to save ui-prefs to the server (kept locally)", err);
  } catch {}
}

/** Exported for tests: has the server copy been read at least once this session? */
export const uiPrefsLoaded = (): boolean => prefsLoaded;

// Pull changes made on another device. Never race a local debounced/in-flight save:
// an older server snapshot must not overwrite the value this tab is currently writing.
export async function refreshUIPrefs(): Promise<void> {
  if (saveTimer || saveInFlight) return;
  hydrateAttempt = 0; // back in the foreground = conditions changed; restore the retry budget
  await hydrateUIPrefs();
}

// "Has no content" — null/undefined, empty string, empty array, empty object. Numbers and
// booleans are out of scope: 0 and false are chosen values, not lost ones.
export function isEmptyPref(v: unknown): boolean {
  if (v == null || v === "") return true;
  if (Array.isArray(v)) return v.length === 0;
  if (typeof v === "object") return Object.keys(v as object).length === 0;
  return false;
}

// The Agent's cap on the ui-prefs blob (uiprefs.MaxBytes). A PUT over it is refused whole, so
// every synced setting stops saving — not just the one that grew.
export const SERVER_PREFS_MAX_BYTES = 64 * 1024;
// Room left for the keys that grow without a check of their own (counters, learned suggestions).
const SERVER_PREFS_HEADROOM = 2 * 1024;

/** Whether the server copy would still fit the Agent's cap with `patch` applied. For writers of
 *  user-sized content to refuse a change before it breaks the sync of everything else. */
export function serverPrefsFit(patch: Partial<Settings>): boolean {
  const body = JSON.stringify(serverPrefs({ ...getSettings(), ...patch }));
  return new TextEncoder().encode(body).length <= SERVER_PREFS_MAX_BYTES - SERVER_PREFS_HEADROOM;
}

// serverPrefs is a shallow copy of only the settings that may be stored on the server, i.e.
// everything except the device-local keys.
function serverPrefs(s: Settings): Partial<Settings> {
  const out: Partial<Settings> = {};
  for (const k of Object.keys(s) as (keyof Settings)[]) {
    if (!isDeviceLocalSetting(k)) (out as any)[k] = s[k];
  }
  return out;
}

// Each hydrate's number; only the newest one's answer is merged. api() sends the tenant current at
// call time, so an answer asked before an identity switch is the previous owner's copy, and
// merging it after the switch's resync would show — and could send — that owner's settings as
// the new owner's.
let hydrateGen = 0;

// hydrateUIPrefs pulls the server-stored prefs (if any) and merges the known keys
// over the local state, so a fresh browser inherits the user's settings. Called once
// at boot after the tenant is resolved (App.tsx). Server wins over localStorage —
// EXCEPT device-local keys, which stay whatever this browser's localStorage holds.
export async function hydrateUIPrefs(): Promise<boolean> {
  const gen = ++hydrateGen;
  const owner = ownerSource();
  let srv: any;
  try {
    srv = await api("api/env/ui-prefs");
  } catch {
    if (gen === hydrateGen) scheduleHydrateRetry();
    return false;
  }
  if (gen !== hydrateGen || ownerSource() !== owner) return false; // superseded or asked as someone else
  // api() does not throw on an HTTP error; it returns {error:{code:"http_502"}} — exactly what
  // the CP does while a workspace is starting. Mistaking "could not fetch" for "the server is
  // empty" lets the next save overwrite the server with defaults, so treat a failure as a
  // failure and leave prefsLoaded unset.
  if (!srv || typeof srv !== "object" || srv.error) {
    scheduleHydrateRetry();
    return false;
  }
  // Users whose server copy still holds only the old boolean are migrated once to the three-way choice.
  if (!("ttsBackgroundPlayback" in srv) && typeof srv.ttsQuietWhenHidden === "boolean") {
    srv.ttsBackgroundPlayback = srv.ttsQuietWhenHidden ? "quiet" : "normal";
  }
  // Users left with only the legacy single pin (assistantAgent) migrate to a priority list with
  // that pin promoted to the front (the same rule as the Agent's assistantAgentOrderPref).
  if (!("assistantAgentOrder" in srv) && typeof srv.assistantAgent === "string" && srv.assistantAgent !== "auto") {
    srv.assistantAgentOrder = normalizeAssistantOrder([srv.assistantAgent]);
  }
  // Split of AI assist generation (same function as load(), hence the same rules). Must run
  // AFTER the single-pin promotion: aiAssistOrder inherits from assistantAgentOrder, so calling
  // it first would leave a user who has only the legacy pin on the default order.
  migrateAiAssistPrefs(srv);
  // Display only — nothing is written back: the Agent reads a missing entry as "recommended"
  // too (ui_prefs.go's aiModelPref / assistantChatModelPref), and a PUT here would be the
  // hydrate-time write the owner-switch guards below forbid.
  fillRecommendedModelMaps(srv);
  // The opencode setting changed from "how to shape the list" to "which billing route to use".
  // Of the legacy values, "hide Zen" means intending to use Go only, so it maps to go; "Go
  // first" and "show all" mean wanting to see both, so they map to zen (the previous
  // appearance). Same rule as the Agent's CatalogPref. Only touch it when the server actually
  // has a value: writing a default into a missing key would let the merge below overwrite the
  // local choice, sending an unsaved selection back to zen every time.
  // migrateIncoming applies each descriptor's migrate (opencodeCatalog's is this one).
  migrateIncoming(srv);
  let changed = false;
  const merged: Settings = { ...getSettings() };
  // Whose accumulated data this browser holds. Another known owner's is dropped before the merge,
  // so neither restore path below can send it to this account (the identity-switch resync does
  // the same within one page load; this covers a boot after a sign-out). Only a known,
  // matching owner may push back a key the server has never held.
  const storedOwner = readStoredOwner();
  // With no record (a copy written before the record existed, or by a browser that never finished
  // a hydrate) only the keys the server has never held are dropped: recording this owner over
  // them would make the next boot push them as this account's, while the empty-server self-heal
  // below keeps working as it did before the record.
  if (owner && storedOwner !== owner) {
    if (storedOwner) unsaved.clear();
    for (const k of Object.keys(DEFAULTS) as (keyof Settings)[]) {
      if (!isAccumulatedSetting(k) || (!storedOwner && k in srv)) continue;
      unsaved.delete(k);
      if (!sameValue(merged[k], DEFAULTS[k])) {
        (merged as any)[k] = DEFAULTS[k];
        changed = true;
      }
    }
  }
  const sameOwner = !!owner && storedOwner === owner;
  // If the server lost accumulated data that this device still has, push it back instead of
  // taking the server's value (self-repair).
  let restore = false;
  for (const k of Object.keys(DEFAULTS)) {
    const key = k as keyof Settings;
    if (isDeviceLocalSetting(key)) continue; // device-local keys are never restored
    if (!(k in srv)) {
      // A key the server has never held while this device holds a non-default value — say,
      // hidden models set while every save failed. Pushed back only for the recorded owner.
      if (unsaved.has(key) || (sameOwner && isAccumulatedSetting(key) && !sameValue((merged as any)[k], DEFAULTS[key]))) restore = true;
      // A key the Agent reads with its own default when absent: showing this device's value while
      // the server holds none would show one thing and enforce another. The recorded owner's
      // choice goes back to the server; a known other owner's device takes the Agent's answer.
      // With the owner not known yet, neither — the next hydrate decides.
      else if (isAgentDefaultedSetting(key) && !sameValue((merged as any)[k], DEFAULTS[key])) {
        if (sameOwner) restore = true;
        else if (owner) {
          (merged as any)[k] = DEFAULTS[key];
          changed = true;
        }
      }
      continue;
    }
    if (sameValue(srv[k], (merged as any)[k])) {
      unsaved.delete(key); // the server already holds it (a save that landed but answered an error)
      continue;
    }
    // Changed here and not yet saved (a save pending or failed): this tab's value stands and goes
    // out with the save below, over the server copy that is otherwise taken as it is.
    if (unsaved.has(key)) {
      restore = true;
      continue;
    }
    if (isAccumulatedSetting(key) && isEmptyPref(srv[k]) && !isEmptyPref((merged as any)[k])) {
      restore = true;
      continue;
    }
    (merged as any)[k] = srv[k];
    changed = true;
  }
  const serverRows = srv.agentLaunchDefaults;
  const legacyClaudeModel = typeof srv.defaultModel === "string" ? srv.defaultModel : merged.defaultModel;
  // A server written by an older Console has only defaultModel. Do not let the
  // already-normalized local map mask that server-side value during migration.
  // Once the new map exists on the server it is authoritative for every agent.
  // An unsaved local edit stands over the server's map, as the loop above does for other keys.
  const rows = unsaved.has("agentLaunchDefaults")
    ? merged.agentLaunchDefaults
    : serverRows && typeof serverRows === "object"
    ? serverRows
    : {
        ...merged.agentLaunchDefaults,
        claude: { ...merged.agentLaunchDefaults.claude, model: legacyClaudeModel },
      };
  const normalized = normalizeAgentLaunchDefaults(rows, legacyClaudeModel);
  if (JSON.stringify(normalized) !== JSON.stringify(merged.agentLaunchDefaults)) {
    merged.agentLaunchDefaults = normalized;
    changed = true;
  }
  // The notification table's device-local cells must not follow a synced key from here on.
  const pinned = pinNotifyFallbacks(merged);
  if (pinned) {
    merged.notifyDevice = pinned;
    changed = true;
  }
  // Each descriptor's normaliser over the merged value: the Agent enables AF memory only on a
  // boolean true (uiprefs.AgentMemory) and so on, and anything else shown as on would show one
  // thing and enforce another.
  if (normalizeMerged(merged)) changed = true;
  // Reaching here means the server's current values really were read. A pending save flows after
  // this: markPrefsLoaded → scheduleServerSave reads state 600ms later, so the `state = merged`
  // below takes effect first.
  // The record and the copy are written together: another tab may have written the shared copy
  // since this one last did, and recording this owner over that copy would vouch for its values.
  // When the copy is recorded as this owner's already it is left alone: another tab of the same
  // owner may hold a newer unsaved edit there.
  if (owner && storedOwner !== owner) {
    try {
      localStorage.setItem(KEY, JSON.stringify(changed ? merged : getSettings()));
    } catch {}
    recordOwner(owner);
  }
  markPrefsLoaded();
  // Accumulated data the server did not have is written back from here to restore it.
  if (restore) scheduleServerSave();
  if (!changed) return true;
  setState(merged);
  persist();
  applyToPage(merged);
  notify();
  return true;
}

// Resync across an identity boundary (a tenant switch, or a user switch inside one tenant).
// ACCUMULATED is designed to protect a non-empty local copy against an empty server value, but
// that premise is about one user's several devices; it must NOT apply just after the owner
// changed while the previous owner's data is still in memory. If it did, the previous owner's
// accumulated data would be written back into the new owner's ui-prefs.json — exactly how
// working sets once leaked into another account. Iterating through isAccumulatedSetting instead
// of listing ACCUMULATED again means keys added to that set later are covered automatically,
// with no per-key fix. The returned Promise is fire-and-forget for the caller (App.tsx), but is
// awaitable so tests can wait for completion.
export function resyncAccumulatedForIdentitySwitch(): Promise<boolean> {
  // A pending save (the 600ms debounce) may be carrying the previous owner's values, so drop it
  // rather than send it under the new owner.
  if (saveTimer) {
    clearTimeout(saveTimer);
    saveTimer = null;
  }
  savePending = false;
  // What failed or waited was the previous owner's save; the new owner starts from their own copy,
  // and an answer to the previous owner's PUT still in flight no longer speaks for it.
  saveGen++;
  unsaved.clear();
  setSyncState("synced");
  const cleared: Partial<Settings> = {};
  for (const k of Object.keys(DEFAULTS) as (keyof Settings)[]) {
    if (isAccumulatedSetting(k)) (cleared as any)[k] = DEFAULTS[k];
  }
  setState({ ...getSettings(), ...cleared });
  persist();
  notify();
  // Local is already empty, so the restore guard above does not fire and the new owner's server
  // values are taken as they are, empty or not.
  return hydrateUIPrefs();
}

/** setSettings' half of the bookkeeping: the synced keys it changed wait for a confirmed save. */
export function markUnsaved(keys: (keyof Settings)[]): void {
  for (const k of keys) {
    if (!isDeviceLocalSetting(k)) unsaved.set(k, ++changeSeq);
  }
}

/** The owner the app shell reports right now ("" while unknown). */
export const currentPrefsOwner = (): string => ownerSource();

// localStorage is shared by every tab, whatever tenant each shows: after a local write the copy
// holds this tab's owner's values, so the record has to say so, or a tab on another tenant would
// later vouch for them as its own. Only once this tab has read its server copy — before that,
// what it holds is not known to be this owner's.
export function recordOwnerAfterLocalWrite(): void {
  if (prefsLoaded) recordOwner(ownerSource());
}
