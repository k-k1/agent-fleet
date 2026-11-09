// Random surface colors (the appearanceShuffle setting). On its schedule every surface in
// SURFACE_TARGETS gets a new color; the dark/light theme keys are never written.
//
// The tabs of one browser share the colors through localStorage but each holds its own copy in
// memory, so the schedule is shared too: the time of the last pick and the colors it chose live
// under one key per appearance scope (appearanceScope). Whichever tab finds the pick due makes it
// and writes that key; the others adopt the colors from the storage event instead of rolling
// their own, which would leave every tab a different color.
import {
  APPEARANCE_SHUFFLE_MODES,
  SETTINGS_STORAGE_KEY,
  SURFACE_COLORS,
  SURFACE_TARGETS,
  adoptAppearance,
  appearanceScope,
  getSettings,
  setSettings,
  type AppearanceShuffleMode,
  type Settings,
} from "./settings.ts";

const STAMP_PREFIX = "af-appearance-shuffle:";
const HOUR_MS = 60 * 60 * 1000;
// How often a visible tab checks whether a pick is due. A minute late on an hourly change is
// not noticeable; returning to the tab also checks at once.
const CHECK_MS = 60 * 1000;

type SurfaceKey = (typeof SURFACE_TARGETS)[number]["key"];
type Colors = Partial<Pick<Settings, SurfaceKey>>;
interface Stamp {
  at: number;
  colors: Colors;
}

// "default" is no color at all, so a pick that lands on it looks like nothing happened.
const PICKABLE = SURFACE_COLORS.map((c) => c.id).filter((id) => id !== "default");

/** A color for every surface: no two surfaces share one, and none keeps the color it has now.
 * With 10 pickable colors and 6 surfaces the pool never runs dry (at most 5 taken + 1 current);
 * should the palette ever shrink below that, the rules relax rather than write an undefined. */
export function pickColors(current: Settings, rand: () => number = Math.random): Colors {
  const used = new Set<string>();
  const out: Record<string, string> = {};
  for (const t of SURFACE_TARGETS) {
    let pool = PICKABLE.filter((id) => !used.has(id) && id !== current[t.key]);
    if (!pool.length) pool = PICKABLE;
    const id = pool[Math.min(pool.length - 1, Math.floor(rand() * pool.length))];
    used.add(id);
    out[t.key] = id;
  }
  return out as Colors;
}

/** Whether a pick is due under `mode`, given the last one. "reload" is never due on a check: it
 * fires once when the page starts (startAppearanceShuffle). */
export function shuffleDue(mode: AppearanceShuffleMode, last: number, now: number): boolean {
  if (mode !== "1h" && mode !== "daily") return false;
  // A stamp from the future means the clock moved back; waiting for it to come round again
  // could freeze the colors for days.
  if (last > now) return true;
  if (mode === "1h") return now - last >= HOUR_MS;
  return new Date(last).toDateString() !== new Date(now).toDateString();
}

function readStamp(scope: string): Stamp | null {
  try {
    return parseStamp(localStorage.getItem(STAMP_PREFIX + scope));
  } catch {
    return null;
  }
}

// Only known surface keys with known color ids are taken: the value comes from another tab,
// possibly of a newer or older Console build.
function parseStamp(raw: string | null): Stamp | null {
  try {
    const v = JSON.parse(raw || "null");
    if (!v || typeof v !== "object" || typeof v.at !== "number") return null;
    const colors: Record<string, string> = {};
    for (const t of SURFACE_TARGETS) {
      const id = v.colors?.[t.key];
      if (typeof id === "string" && SURFACE_COLORS.some((c) => c.id === id)) colors[t.key] = id;
    }
    return { at: v.at, colors: colors as Colors };
  } catch {
    return null;
  }
}

function writeStamp(scope: string, stamp: Stamp): void {
  try {
    localStorage.setItem(STAMP_PREFIX + scope, JSON.stringify(stamp));
  } catch {}
}

// When this page last picked. Two tabs that find the same pick due at once both pick and both
// receive the other's stamp; each adopting the other would leave them swapped until the next
// pick, so a stamp no newer than our own pick is ignored and both settle on the later one.
let lastOwnPickAt = 0;

/** Pick new colors now (the dice button, switching the schedule on, and every due check).
 * `extra` is written in the same update, so turning the schedule on saves once. */
export function shuffleAppearanceNow(now: number = Date.now(), extra: Partial<Settings> = {}): void {
  const colors = pickColors(getSettings());
  setSettings({ ...extra, ...colors });
  // Read the scope after the write: setSettings settles which tenant's look is showing.
  const scope = appearanceScope();
  if (!scope) return;
  lastOwnPickAt = now;
  writeStamp(scope, { at: now, colors });
}

/** Change the schedule. Switching it on picks at once, so the choice visibly does something. */
export function setAppearanceShuffle(mode: AppearanceShuffleMode): void {
  if (getSettings().appearanceShuffle === "off" && mode !== "off") {
    shuffleAppearanceNow(Date.now(), { appearanceShuffle: mode });
  } else {
    setSettings({ appearanceShuffle: mode });
  }
}

// "reload" picks once per page, not once per mount: StrictMode and a remount must not reroll.
// Pending until the scope is known: with per-tenant appearance on, the owner can still be
// unresolved at boot (a failed whoami), and the pick then waits for a later check.
let loadPickPending = false;
let loadPickClaimed = false;

function check(): void {
  if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
  const scope = appearanceScope();
  if (!scope) return;
  const mode = getSettings().appearanceShuffle;
  if (loadPickPending) {
    loadPickPending = false;
    if (mode === "reload") {
      shuffleAppearanceNow();
      return;
    }
  }
  const now = Date.now();
  const stamp = readStamp(scope);
  if (!stamp) {
    // A scope with no schedule yet (a tenant seen for the first time with per-tenant appearance
    // on, or cleared storage) starts its clock from the look it already has: picking at once
    // would overwrite a tenant's saved colors the moment it is opened. No colors in the stamp,
    // so the other tabs have nothing to adopt from it.
    if (mode === "1h" || mode === "daily") writeStamp(scope, { at: now, colors: {} });
    return;
  }
  if (shuffleDue(mode, stamp.at, now)) shuffleAppearanceNow(now);
}

/** Run the schedule for this page; returns the stop function. Call once the per-tenant
 * appearance has been applied (after boot), or the snapshot would overwrite the first pick.
 * `pickOnLoad` is false for a pop-out window: opening one is not opening the Console, and its
 * "reload" pick would recolor the main window too. A pick that falls due on the hourly or daily
 * schedule is made by whichever window checks first, a pop-out included. */
export function startAppearanceShuffle({ pickOnLoad = true }: { pickOnLoad?: boolean } = {}): () => void {
  if (pickOnLoad && !loadPickClaimed) {
    loadPickClaimed = true;
    loadPickPending = true;
  }
  check();
  const onStorage = (e: StorageEvent) => {
    // The schedule itself, changed in another tab: without this a tab still holding "1h" keeps
    // picking (and every tab adopting) after the member switched it off elsewhere.
    if (e.key === SETTINGS_STORAGE_KEY) {
      const mode = readMode(e.newValue);
      if (mode && mode !== getSettings().appearanceShuffle) adoptAppearance({ appearanceShuffle: mode });
      return;
    }
    const scope = appearanceScope();
    if (!scope || e.key !== STAMP_PREFIX + scope) return;
    const stamp = parseStamp(e.newValue);
    if (stamp && stamp.at > lastOwnPickAt && Object.keys(stamp.colors).length) adoptAppearance(stamp.colors);
  };
  const onVisible = () => check();
  const timer = setInterval(check, CHECK_MS);
  window.addEventListener("storage", onStorage);
  document.addEventListener("visibilitychange", onVisible);
  return () => {
    clearInterval(timer);
    window.removeEventListener("storage", onStorage);
    document.removeEventListener("visibilitychange", onVisible);
  };
}

function readMode(raw: string | null): AppearanceShuffleMode | null {
  try {
    const v = JSON.parse(raw || "null")?.appearanceShuffle;
    return APPEARANCE_SHUFFLE_MODES.find((m) => m.id === v)?.id ?? null;
  } catch {
    return null;
  }
}
