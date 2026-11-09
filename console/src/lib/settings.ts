// Display settings (theme / fonts / file-viewer options / icon set). Persisted in
// localStorage for instant load + offline, AND mirrored to the server per-user
// (GET/PUT /api/env/ui-prefs) so they follow the user across browsers/devices.
// Shared across React (useSettings) and non-React code (term.js via getSettings +
// subscribe). Terminal and viewer fonts are independent — they default to different
// families so they're visibly distinct out of the box.
//
// This file is the public entry point; the parts live in lib/settings/ (ADR 0124 Phase 2):
//   prefs.ts          one descriptor per preference: Settings, DEFAULTS, scopes, normalisation
//   normalize.ts      the normalisers and legacy migrations the descriptors and the sync use
//   options.ts        the choices each preference offers
//   imageProviders.ts image provider ordering
//   appearance.ts     what settings do to the page (theme, fonts, locale)
//   store.ts          the in-memory state and its localStorage copy
//   sync.ts           the ui-prefs server sync and owner switching
// Import from here, not from those files.
import { useSyncExternalStore } from "react";
import { pinSyncedFallbacks } from "../features/notifications/prefs.ts";
import { applyTheme } from "./settings/appearance.ts";
import { sameValue, type Settings } from "./settings/prefs.ts";
import { APPEARANCE_SHUFFLE_MODES, REGION_THEMES, SURFACE_COLORS, THEMES } from "./settings/options.ts";
import { KEY as SETTINGS_KEY, applyToPage, getSettings, notify, persist, setState, subscribe } from "./settings/store.ts";
import {
  currentPrefsOwner,
  markUnsaved,
  recordOwnerAfterLocalWrite,
  scheduleServerSave,
  setNotifyFallbackPinner,
} from "./settings/sync.ts";

export {
  APPEARANCE_SHUFFLE_MODES,
  ASSISTANT_AGENT_KINDS,
  ASSISTANT_RECOMMENDED_MODEL,
  CHAT_FONTS,
  CJK_FAMILY,
  CJK_FAMILY_PROSE,
  CJK_FONT_AUTO,
  CJK_FONT_OFF,
  CJK_FONTS,
  CJK_UNICODE_RANGE,
  CJK_UNICODE_RANGE_PROSE,
  CLAUDE_MODELS,
  CODE_FONTS,
  DEFAULT_MODEL,
  ICON_SETS,
  LOCALES,
  MIRROR_SEND_MODES,
  OUTPUT_LANGUAGES,
  PANE_LAYOUTS,
  READER_FONTS,
  REGION_FORMATS,
  REGION_THEMES,
  SPAWN_CHILD_LIMITS,
  STOPPED_ARCHIVE_DAYS,
  STOPPED_ARCHIVE_NEVER,
  SURFACE_COLORS,
  SURFACE_TARGETS,
  THEMES,
  TTS_BACKGROUND_PLAYBACK_MODES,
  TTS_CACHE_SIZES,
  TTS_LANGS,
  TTS_POLLY_VOICES,
  TTS_PROVIDERS,
  TTS_SPEEDS,
  TTS_WORK_READ_MODES,
  VOICEVOX_ZUNDAMON,
  stoppedArchiveChoice,
  type AppearanceShuffleMode,
} from "./settings/options.ts";
export {
  IMAGE_PROVIDER_FLEET_GROUP,
  IMAGE_PROVIDERS,
  IMAGE_PROVIDERS_RANKED,
  collapseImageProviderOrder,
  expandImageProviderOrder,
  imageProviderIsFleet,
  imageProviderLabel,
  normalizeImageProviderOrder,
  type ImageFleetRow,
} from "./settings/imageProviders.ts";
export {
  applyCjkFont,
  applyLocale,
  applyTheme,
  chatFontStack,
  effectiveTheme,
  fontStack,
  readerFontStack,
  surfaceAccent,
  surfaceBg,
  surfaceValue,
  termFontStack,
} from "./settings/appearance.ts";
export {
  agentLaunchDefault,
  expandThinking,
  fillRecommendedModelMaps,
  migrateAiAssistPrefs,
  migrateOpencodeCatalog,
  normalizeAgentLaunchDefaults,
  normalizeAgentMemory,
  normalizeAssistantOrder,
  normalizeClaudeCustomModels,
  normalizeExternalMemory,
  streamReplies,
  type AgentLaunchDefault,
  type AgentLaunchDefaults,
  type ExternalMemoryMode,
  type StreamRepliesMode,
  type TtsCharConf,
} from "./settings/normalize.ts";
export {
  TTS_RESET,
  defaultSetting,
  isAccumulatedSetting,
  isDeviceLocalSetting,
  settingsDefaults,
  type Settings,
} from "./settings/prefs.ts";
export { getSettings, subscribe } from "./settings/store.ts";
export {
  SERVER_PREFS_MAX_BYTES,
  cancelPrefsTimersForTest,
  hydrateUIPrefs,
  isEmptyPref,
  prefsSyncState,
  refreshUIPrefs,
  resyncAccumulatedForIdentitySwitch,
  retryPrefsSync,
  serverPrefsFit,
  setPrefsOwnerSource,
  subscribePrefsSync,
  uiPrefsLoaded,
  usePrefsSyncState,
  type PrefsSyncState,
} from "./settings/sync.ts";

setNotifyFallbackPinner(pinSyncedFallbacks);

// Per-tenant appearance. The snapshots live in localStorage under "<tenant>|<user>" (the same
// owner string as the prefs record) and never reach ui-prefs: putting them on the server would
// make them follow the member onto every device, and unreadable while the workspace is stopped.
// An empty owner (user not resolved yet) means "unknown": every step below does nothing then,
// rather than filing a look under the wrong tenant.
const APPEARANCE_KEYS = [
  "theme", "mirrorTheme", "sharedTheme", "assistantTheme",
  "topbarColor", "leftpaneColor", "viewerColor", "chatColor", "sharedColor", "assistantColor",
] as const satisfies readonly (keyof Settings)[];
const APPEARANCE_SNAPSHOT_PREFIX = "af-appearance-tenant:";

function readAppearanceSnapshot(owner: string): Partial<Settings> | null {
  try {
    const raw = JSON.parse(localStorage.getItem(APPEARANCE_SNAPSHOT_PREFIX + owner) || "null");
    if (!raw || typeof raw !== "object") return null;
    const out: Record<string, string> = {};
    for (const k of APPEARANCE_KEYS) if (typeof raw[k] === "string") out[k] = raw[k];
    return Object.keys(out).length ? (out as Partial<Settings>) : null;
  } catch {
    return null;
  }
}

function writeAppearanceSnapshot(owner: string): void {
  const snap: Record<string, unknown> = {};
  const state = getSettings();
  for (const k of APPEARANCE_KEYS) snap[k] = state[k];
  try {
    localStorage.setItem(APPEARANCE_SNAPSHOT_PREFIX + owner, JSON.stringify(snap));
  } catch {}
}

/** Show the current tenant's saved appearance (boot, tenant switch). A tenant with no snapshot
 * keeps what is shown and files it as its first snapshot. Does nothing with the switch off. */
export function applyTenantAppearance(): void {
  catchUpWithOtherTabs();
  if (!syncAppearanceOwner(currentPrefsOwner())) return;
  persistSeen();
  applyTheme(getSettings());
  notify();
}

// Whose snapshot the appearance in `state` currently is. The tenant API's owner changes the
// moment setTenant runs, but App re-applies in a later effect; a write in between (a theme
// hotkey) must not file the previous tenant's look under the new owner, so setSettings swaps
// the owner first through this function. Returns whether `state` changed.
let appearanceOwner = "";

function syncAppearanceOwner(owner: string): boolean {
  if (!getSettings().appearancePerTenant || !owner || owner === appearanceOwner) return false;
  appearanceOwner = owner;
  const snap = readAppearanceSnapshot(owner);
  if (!snap) {
    writeAppearanceSnapshot(owner);
    return false;
  }
  setState({ ...getSettings(), ...snap });
  return true;
}

/** Which appearance a schedule shared between this device's tabs belongs to: "*" while every
 * tenant shares one look, the "<tenant>|<user>" owner while each keeps its own, and null when that
 * owner is not known yet (nothing may be filed under it then). */
export function appearanceScope(): string | null {
  if (!getSettings().appearancePerTenant) return "*";
  return currentPrefsOwner() || null;
}

// Other tabs of this browser. Each tab holds the settings in memory and reads localStorage only at
// load, so without this a change made in one tab stays invisible in the others until they reload,
// and the next write from a stale tab puts its old values back.
//
// Only the appearance keys cross between tabs. paneLayout is device-local too but switching it
// re-lays the main area (with an unsaved-edits confirm), which a tab must not do behind the member's
// back; synced keys already converge through the server copy.
const CROSS_TAB_KEYS = [...APPEARANCE_KEYS, "appearancePerTenant", "appearanceShuffle"] as const;

// The values each cross-tab key may take in this build. Another tab may run another Console
// version; a value this one does not know is left out rather than stored and saved again.
const SURFACE_IDS = SURFACE_COLORS.map((c) => c.id);
const REGION_THEME_IDS = REGION_THEMES.map((t) => t.id);
const CROSS_TAB_ALLOWED: Record<(typeof CROSS_TAB_KEYS)[number], readonly unknown[]> = {
  theme: THEMES.map((t) => t.id),
  mirrorTheme: REGION_THEME_IDS,
  sharedTheme: REGION_THEME_IDS,
  assistantTheme: REGION_THEME_IDS,
  topbarColor: SURFACE_IDS,
  leftpaneColor: SURFACE_IDS,
  viewerColor: SURFACE_IDS,
  chatColor: SURFACE_IDS,
  sharedColor: SURFACE_IDS,
  assistantColor: SURFACE_IDS,
  appearancePerTenant: [true, false],
  appearanceShuffle: APPEARANCE_SHUFFLE_MODES.map((m) => m.id),
};

function crossTabValue(k: (typeof CROSS_TAB_KEYS)[number], v: unknown): unknown {
  return CROSS_TAB_ALLOWED[k].includes(v) ? v : undefined;
}

function parseObject(raw: string | null): Record<string, unknown> {
  try {
    const v = JSON.parse(raw || "null");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}

// What another tab's write of the shared settings record changes here. Only keys whose stored
// value the write itself changed are taken: the rest of that record is the writing tab's own
// copy, possibly older than ours.
function crossTabPatch(oldRaw: string | null, newRaw: string | null): Partial<Settings> {
  const before = parseObject(oldRaw);
  const after = parseObject(newRaw);
  const cur = getSettings();
  const patch: Record<string, unknown> = {};
  for (const k of CROSS_TAB_KEYS) {
    if (!(k in after) || sameValue(before[k], after[k])) continue;
    const v = crossTabValue(k, after[k]);
    if (v !== undefined && !sameValue(v, cur[k])) patch[k] = v;
  }
  const perTenant = "appearancePerTenant" in patch ? !!patch.appearancePerTenant : cur.appearancePerTenant;
  if (perTenant) {
    // The record holds the writing tab's tenant's look, which need not be ours; our tenant's
    // look comes from its snapshot (on the switch turning on) or its snapshot key's own events.
    for (const k of APPEARANCE_KEYS) delete patch[k];
  } else if (patch.appearancePerTenant === false) {
    // Switched off elsewhere: the look that tab showed is now every tenant's.
    for (const k of APPEARANCE_KEYS) {
      const v = crossTabValue(k, after[k]);
      if (v !== undefined) patch[k] = v;
    }
  }
  return patch as Partial<Settings>;
}

function showFromOtherTab(patch: Partial<Settings>): void {
  if (!Object.keys(patch).length) return;
  setState({ ...getSettings(), ...patch });
  // The switch turned on elsewhere: show this tab's own tenant snapshot (or file the current look
  // as its first one), as turning it on here would.
  if (patch.appearancePerTenant) {
    appearanceOwner = "";
    syncAppearanceOwner(currentPrefsOwner());
  } else if (patch.appearancePerTenant === false) {
    appearanceOwner = "";
  }
  // No persist and no server save: the writing tab did both, and these keys never reach ui-prefs.
  applyTheme(getSettings());
  notify();
}

function onOtherTabStorage(e: StorageEvent): void {
  if (!e.key) return;
  // An event can arrive after this tab or a third one has already written the key again. Taking
  // it then would leave two tabs that wrote at the same moment each showing the other's value;
  // skipping it leaves both on whatever was written last.
  try {
    if (localStorage.getItem(e.key) !== e.newValue) return;
  } catch {
    return;
  }
  if (e.key === SETTINGS_KEY) {
    seenRecord = e.newValue;
    showFromOtherTab(crossTabPatch(e.oldValue, e.newValue));
    return;
  }
  const owner = currentPrefsOwner();
  if (!owner || e.key !== APPEARANCE_SNAPSHOT_PREFIX + owner || !getSettings().appearancePerTenant) return;
  const snap = parseObject(e.newValue);
  const cur = getSettings();
  const patch: Record<string, unknown> = {};
  for (const k of APPEARANCE_KEYS) {
    const v = crossTabValue(k, snap[k]);
    if (v !== undefined && !sameValue(v, cur[k])) patch[k] = v;
  }
  showFromOtherTab(patch as Partial<Settings>);
}

// The settings record as this tab last wrote or took it. A tab writes the whole record, so one
// whose storage event from another tab has not arrived yet would put that tab's change back with
// its own older value. Before writing, the tab first takes what changed since (catchUpWithOtherTabs).
let seenRecord: string | null = readRecord();

function readRecord(): string | null {
  try {
    return localStorage.getItem(SETTINGS_KEY);
  } catch {
    return null;
  }
}

// Take the other tabs' changes this tab has not yet seen. The caller's own write goes on top.
function catchUpWithOtherTabs(): void {
  const stored = readRecord();
  if (stored === seenRecord) return;
  const patch = crossTabPatch(seenRecord, stored);
  seenRecord = stored;
  showFromOtherTab(patch);
}

function persistSeen(): void {
  persist();
  seenRecord = readRecord();
}

/** Follow appearance changes made in the other tabs of this browser; returns the stop function. */
export function watchOtherTabs(): () => void {
  window.addEventListener("storage", onOtherTabStorage);
  return () => window.removeEventListener("storage", onOtherTabStorage);
}

// The generic signature ties key and value together in the type system, preventing mismatches
// such as passing a boolean for "theme".
export function setSetting<K extends keyof Settings>(key: K, value: Settings[K]): void {
  setSettings({ [key]: value } as Partial<Settings>);
}

// setSettings — update several keys at once, coalescing them into one render and one server
// save. Used where many keys change together, such as a reset: calling setSetting 17 times would
// run that many re-renders and debounced saves.
export function setSettings(patch: Partial<Settings>): void {
  catchUpWithOtherTabs();
  // Turning the switch on brings back this tenant's earlier snapshot, if any, over the shared
  // look; with none, the look shown becomes the snapshot (written below).
  const owner = currentPrefsOwner();
  if (patch.appearancePerTenant && !getSettings().appearancePerTenant && owner) {
    patch = { ...readAppearanceSnapshot(owner), ...patch };
    appearanceOwner = owner;
  } else if (patch.appearancePerTenant === false) {
    appearanceOwner = "";
  } else {
    syncAppearanceOwner(owner);
  }
  const state = { ...getSettings(), ...patch };
  setState(state);
  if (state.appearancePerTenant && owner && (
    "appearancePerTenant" in patch || APPEARANCE_KEYS.some((k) => k in patch)
  )) {
    writeAppearanceSnapshot(owner);
  }
  markUnsaved(Object.keys(patch) as (keyof Settings)[]);
  persistSeen();
  recordOwnerAfterLocalWrite();
  applyToPage(state);
  scheduleServerSave();
  notify();
}

export function useSettings(): Settings {
  return useSyncExternalStore(subscribe, getSettings, getSettings);
}
