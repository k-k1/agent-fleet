// Recent first prompts for the launch modal, newest first, per base repository. Synced in the
// ui-prefs (Settings.launchHistory) so the history follows the user across devices (#1469), and
// capped like the templates (lib/launchTemplates.ts): one oversized ui-prefs PUT fails the sync
// of every setting.
//
// It used to live in this browser's localStorage only (`af.repo-prompts.<repo>`). Those keys are
// moved into the synced list once the server copy has been read, never before: a write before
// that read is kept over the server's value by the next hydrate, which would replace the history
// other devices saved. Until then, and for any entry that does not fit the synced budget, the
// legacy keys stay where they are and are still shown — moving them must not lose any.
import { getSettings, setSettings, uiPrefsLoaded } from "./settings.ts";
import { byteLength, scopeRepo } from "./launchTemplates.ts";

export interface PromptHistoryEntry {
  /** Base repository (a worktree's history is its base clone's). */
  repo: string;
  text: string;
  /** Launch time, epoch ms (0 = moved from the device-only history, time unknown). */
  at: number;
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

/**
 * Moves the device-only history into the synced list, behind what is already there. A key is
 * removed only once all its entries are in the list; what did not fit stays in it (and is still
 * shown). Returns whether anything moved. Does nothing before the server copy has been read.
 */
export function migrateLegacyPromptHistory(): boolean {
  if (!uiPrefsLoaded()) return false;
  const keys = legacyKeys();
  if (!keys.length) return false;
  const current = synced();
  const moved: PromptHistoryEntry[] = keys.flatMap((k) => {
    const repo = scopeRepo(k.slice(LEGACY_PREFIX.length));
    return readLegacy(k).map((text) => ({ repo, text: text.trim(), at: 0 }));
  });
  const next = normalizeHistory([...current, ...moved]);
  const has = new Set(next.map((e) => e.repo + "\u0000" + e.text));
  // An entry the synced list already had counts as moved; an oversized one is kept in the legacy
  // key, where it was always remembered.
  for (const k of keys) {
    const repo = scopeRepo(k.slice(LEGACY_PREFIX.length));
    writeLegacy(k, readLegacy(k).filter((t) => !has.has(repo + "\u0000" + t.trim())));
  }
  if (JSON.stringify(next) === JSON.stringify(current)) return false;
  setSettings({ launchHistory: next });
  return true;
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
// does a prompt too large for the synced budget, which then stays on this device only.
export function pushPromptHistory(repo: string, prompt: string): void {
  const p = (prompt || "").trim();
  if (!repo || !p) return;
  const base = scopeRepo(repo);
  if (!uiPrefsLoaded() || byteLength(p) > HISTORY_ENTRY_MAX_BYTES) {
    const key = LEGACY_PREFIX + base;
    writeLegacy(key, [p, ...readLegacy(key).filter((s) => s.trim() !== p)].slice(0, HISTORY_PER_REPO));
    return;
  }
  migrateLegacyPromptHistory();
  const rest = synced().filter((e) => !(e.repo === base && e.text === p));
  setSettings({ launchHistory: normalizeHistory([{ repo: base, text: p, at: Date.now() }, ...rest]) });
}

/** Forget one entry, wherever it is stored. */
export function deletePromptHistory(repo: string, text: string): void {
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
  if (next.length !== all.length) setSettings({ launchHistory: next });
}
