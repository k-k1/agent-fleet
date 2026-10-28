// The in-memory settings and their localStorage copy. One instance per page: everything that
// reads or replaces the state goes through here, so lib/settings.ts and sync.ts share it without
// importing each other.
import { applyCjkFont, applyLocale, applyTheme } from "./appearance.ts";
import { fillRecommendedModelMaps, migrateAiAssistPrefs, normalizeAgentLaunchDefaults } from "./normalize.ts";
import { DEFAULT_MODEL } from "./options.ts";
import { DEFAULTS, settingsFromStored, type Settings } from "./prefs.ts";

export const KEY = "af-display-settings";

function load(): Settings {
  try {
    const saved = JSON.parse(localStorage.getItem(KEY) || "{}");
    // Migrate the old ON/OFF setting to the three-way choice; an explicit new setting wins.
    if (!("ttsBackgroundPlayback" in saved) && typeof saved.ttsQuietWhenHidden === "boolean") {
      saved.ttsBackgroundPlayback = saved.ttsQuietWhenHidden ? "quiet" : "normal";
    }
    delete saved.ttsQuietWhenHidden;
    // Split of AI assist generation (one key per title-suggestion feature, two separate
    // priority/model lists, an independent read-aloud language). Runs the same function as
    // hydrateUIPrefs().
    migrateAiAssistPrefs(saved);
    fillRecommendedModelMaps(saved);
    const legacyClaudeModel = typeof saved.defaultModel === "string" ? saved.defaultModel : DEFAULT_MODEL;
    const rows = saved.agentLaunchDefaults && typeof saved.agentLaunchDefaults === "object"
      ? saved.agentLaunchDefaults
      : {};
    return {
      ...settingsFromStored(saved),
      agentLaunchDefaults: normalizeAgentLaunchDefaults(rows, legacyClaudeModel),
    };
  } catch {
    return { ...DEFAULTS };
  }
}

let state = load();
const subs = new Set<() => void>();

export function getSettings(): Settings {
  return state;
}

export function setState(next: Settings): void {
  state = next;
}

export function persist(): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
}

export function notify(): void {
  subs.forEach((fn) => fn());
}

export function subscribe(fn: () => void): () => void {
  subs.add(fn);
  return () => {
    subs.delete(fn);
  };
}

// The same three places apply all three: first paint, on change, after a server hydrate.
export function applyToPage(s: Settings): void {
  applyTheme(s);
  applyLocale(s);
  applyCjkFont(s);
}

applyToPage(state);
