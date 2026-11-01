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
import type { Settings } from "./settings/prefs.ts";
import { applyToPage, getSettings, notify, persist, setState, subscribe } from "./settings/store.ts";
import {
  currentPrefsOwner,
  markUnsaved,
  recordOwnerAfterLocalWrite,
  scheduleServerSave,
  setNotifyFallbackPinner,
} from "./settings/sync.ts";

export {
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
  normalizeMemoryReview,
  streamReplies,
  type AgentLaunchDefault,
  type AgentLaunchDefaults,
  type ExternalMemoryMode,
  type MemoryReviewMode,
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
  if (!syncAppearanceOwner(currentPrefsOwner())) return;
  persist();
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

// The generic signature ties key and value together in the type system, preventing mismatches
// such as passing a boolean for "theme".
export function setSetting<K extends keyof Settings>(key: K, value: Settings[K]): void {
  setSettings({ [key]: value } as Partial<Settings>);
}

// setSettings — update several keys at once, coalescing them into one render and one server
// save. Used where many keys change together, such as a reset: calling setSetting 17 times would
// run that many re-renders and debounced saves.
export function setSettings(patch: Partial<Settings>): void {
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
  persist();
  recordOwnerAfterLocalWrite();
  applyToPage(state);
  scheduleServerSave();
  notify();
}

export function useSettings(): Settings {
  return useSyncExternalStore(subscribe, getSettings, getSettings);
}
