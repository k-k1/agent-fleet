// Recent first prompts for the launch modal, newest first, per base repository. Synced in the
// ui-prefs (Settings.launchHistory) so the history follows the user across devices (#1469), and
// capped like the templates (lib/launchTemplates.ts): one oversized ui-prefs PUT fails the sync
// of every setting.
//
// It used to live in this browser's localStorage only (`af.repo-prompts.<repo>`). Those keys are
// copied into the synced list once the server copy has been read, never before: a write before
// that read is kept over the server's value by the next hydrate, which would replace the history
// other devices saved. A legacy entry is removed only once the server has CONFIRMED a list holding
// it (no save pending or failed): until then a failed PUT followed by a reload hydrates the
// server's older list over the local one, and the legacy key is the only copy left. Entries that
// do not fit the synced budget stay in the legacy key for good and are still shown.
import { getSettings, prefsSyncState, serverPrefsFit, setSettings, subscribePrefsSync, uiPrefsLoaded } from "./settings.ts";
import { byteLength, scopeRepo } from "./launchTemplates.ts";

export interface PromptHistoryEntry {
  /** Base repository (a worktree's history is its base clone's). */
  repo: string;
  text: string;
  /** Launch time, epoch ms (0 = moved from the device-only history, time unknown). */
  at: number;
}

/** The stored shape; see LaunchTemplateStore for why it is wrapped. */
export interface PromptHistoryStore {
  items?: PromptHistoryEntry[];
  at?: number;
}

const LEGACY_PREFIX = "af.repo-prompts.";
export const HISTORY_PER_REPO = 8;
/** A prompt larger than this is remembered on this device only (it would eat the synced budget). */
export const HISTORY_ENTRY_MAX_BYTES = 3 * 1024;
export const HISTORY_MAX_BYTES = 8 * 1024;

function isEntry(v: unknown): v is PromptHistoryEntry {
  if (!v || typeof v !== "object") return false;
  const e = v as Record<string, unknown>;
  return typeof e.repo === "string" && typeof e.text === "string";
}

/** Order kept (newest first); duplicates, oversized entries and the overflow past each cap dropped. */
export function normalizeHistory(v: unknown): PromptHistoryEntry[] {
  if (v && typeof v === "object" && !Array.isArray(v)) v = (v as PromptHistoryStore).items;
  if (!Array.isArray(v)) return [];
  const seen = new Set<string>();
  const perRepo = new Map<string, number>();
  const out: PromptHistoryEntry[] = [];
  for (const x of v) {
    if (!isEntry(x)) continue;
    const text = x.text.trim();
    if (!text || !x.repo || byteLength(text) > HISTORY_ENTRY_MAX_BYTES) continue;
    const k = x.repo + "\u0000" + text;
    if (seen.has(k)) continue;
    const n = perRepo.get(x.repo) || 0;
    if (n >= HISTORY_PER_REPO) continue;
    seen.add(k);
    perRepo.set(x.repo, n + 1);
    out.push({ repo: x.repo, text, at: typeof x.at === "number" ? x.at : 0 });
  }
  while (out.length && byteLength(JSON.stringify(out)) > HISTORY_MAX_BYTES) out.pop();
  return out;
}

const synced = (): PromptHistoryEntry[] => normalizeHistory(getSettings().launchHistory);
const storeOf = (items: PromptHistoryEntry[]): PromptHistoryStore => ({ items, at: Date.now() });

/** Writes the synced list, unless it would push the whole ui-prefs over the Agent's cap. */
function writeSynced(items: PromptHistoryEntry[]): boolean {
  const launchHistory = storeOf(items);
  if (!serverPrefsFit({ launchHistory })) return false;
  setSettings({ launchHistory });
  return true;
}

function legacyKeys(): string[] {
  const keys: string[] = [];
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && k.startsWith(LEGACY_PREFIX) && k.length > LEGACY_PREFIX.length) keys.push(k);
    }
  } catch {
    /* storage unavailable — nothing to move */
  }
  return keys;
}

function readLegacy(key: string): string[] {
  try {
    const raw = localStorage.getItem(key);
    const arr = raw ? JSON.parse(raw) : [];
    return Array.isArray(arr) ? arr.filter((s): s is string => typeof s === "string" && !!s.trim()) : [];
  } catch {
    return [];
  }
}

function writeLegacy(key: string, list: string[]): void {
  try {
    if (list.length) localStorage.setItem(key, JSON.stringify(list));
    else localStorage.removeItem(key);
  } catch {
    /* private mode / quota — best effort */
  }
}

const legacyFor = (base: string): string[] =>
  legacyKeys()
    .filter((k) => scopeRepo(k.slice(LEGACY_PREFIX.length)) === base)
    .flatMap(readLegacy);

/** Drops from the legacy keys what the server-confirmed list holds. Only while nothing is pending
 *  or failed — then this tab's list IS the server's. Runs the moment a save is confirmed, too: left
 *  for later, an entry another device deletes meanwhile would be copied back from here. */
function pruneLegacy(): void {
  if (!uiPrefsLoaded() || prefsSyncState() !== "synced") return;
  const has = new Set(synced().map((e) => e.repo + "\u0000" + e.text));
  for (const k of legacyKeys()) {
    const repo = scopeRepo(k.slice(LEGACY_PREFIX.length));
    const list = readLegacy(k);
    const kept = list.filter((t) => !has.has(repo + "\u0000" + t.trim()));
    if (kept.length !== list.length) writeLegacy(k, kept);
  }
}
subscribePrefsSync(pruneLegacy);

/**
 * Copies the device-only history into the synced list, behind what is already there, and removes
 * from the legacy keys what a server-confirmed list already holds (see the header). Returns
 * whether the synced list changed. Does nothing before the server copy has been read.
 */
export function migrateLegacyPromptHistory(): boolean {
  if (!uiPrefsLoaded()) return false;
  const keys = legacyKeys();
  if (!keys.length) return false;
  pruneLegacy();
  const current = synced();
  const moved: PromptHistoryEntry[] = keys.flatMap((k) => {
    const repo = scopeRepo(k.slice(LEGACY_PREFIX.length));
    return readLegacy(k).map((text) => ({ repo, text: text.trim(), at: 0 }));
  });
  const next = normalizeHistory([...current, ...moved]);
  if (JSON.stringify(next) === JSON.stringify(current)) return false;
  return writeSynced(next);
}

/** The history shown for `repo`, newest first: the synced entries, then any not moved yet. */
export function readPromptHistory(repo: string): string[] {
  if (!repo) return [];
  const base = scopeRepo(repo);
  const out = synced()
    .filter((e) => e.repo === base)
    .map((e) => e.text);
  for (const t of legacyFor(base)) if (!out.includes(t.trim())) out.push(t.trim());
  return out;
}

// pushPromptHistory records a just-launched prompt at the front, dropping any earlier identical
// entry so re-running the same prompt doesn't pile up duplicates. Before the server copy has been
// read it goes to the device-only key instead (see the header), to be moved on the next read; so
// does a prompt too large for the synced budget (or for the whole ui-prefs), which then stays on
// this device only.
export function pushPromptHistory(repo: string, prompt: string): void {
  const p = (prompt || "").trim();
  if (!repo || !p) return;
  const base = scopeRepo(repo);
  const local = () => {
    const key = LEGACY_PREFIX + base;
    writeLegacy(key, [p, ...readLegacy(key).filter((s) => s.trim() !== p)].slice(0, HISTORY_PER_REPO));
  };
  if (!uiPrefsLoaded() || byteLength(p) > HISTORY_ENTRY_MAX_BYTES) return local();
  migrateLegacyPromptHistory();
  const rest = synced().filter((e) => !(e.repo === base && e.text === p));
  if (!writeSynced(normalizeHistory([{ repo: base, text: p, at: Date.now() }, ...rest]))) local();
}

/** Forget one entry, wherever it is stored. False when it is in the synced list and the server
 *  copy has not been read yet (a write now would replace other devices' history). */
export function deletePromptHistory(repo: string, text: string): boolean {
  const base = scopeRepo(repo);
  const t = text.trim();
  for (const k of legacyKeys()) {
    if (scopeRepo(k.slice(LEGACY_PREFIX.length)) !== base) continue;
    const list = readLegacy(k);
    const kept = list.filter((s) => s.trim() !== t);
    if (kept.length !== list.length) writeLegacy(k, kept);
  }
  const all = synced();
  const next = all.filter((e) => !(e.repo === base && e.text === t));
  if (next.length === all.length) return true;
  if (!uiPrefsLoaded()) return false;
  setSettings({ launchHistory: storeOf(next) });
  return true;
}
