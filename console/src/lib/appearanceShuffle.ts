// Random surface colors (the appearanceShuffle setting). On its schedule every surface in
// SURFACE_TARGETS gets a new color; the dark/light theme keys are never written.
//
// The schedule is shared by the tabs of one browser: the time of the last pick lives under one key
// per appearance scope (appearanceScope), so the first tab that finds a pick due makes it and the
// others see it is no longer due. They show the new colors through watchOtherTabs (lib/settings.ts)
// rather than rolling their own, which would leave every tab a different color.
import {
  SURFACE_COLORS,
  SURFACE_TARGETS,
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

function parseStamp(raw: string | null): Stamp | null {
  try {
    const v = JSON.parse(raw || "null");
    return v && typeof v === "object" && typeof v.at === "number" ? { at: v.at } : null;
  } catch {
    return null;
  }
}

function writeStamp(scope: string, stamp: Stamp): void {
  try {
    localStorage.setItem(STAMP_PREFIX + scope, JSON.stringify(stamp));
  } catch {}
}

/** Pick new colors now (the dice button, switching the schedule on, and every due check).
 * `extra` is written in the same update, so turning the schedule on saves once. */
export function shuffleAppearanceNow(now: number = Date.now(), extra: Partial<Settings> = {}): void {
  const colors = pickColors(getSettings());
  setSettings({ ...extra, ...colors });
  // Read the scope after the write: setSettings settles which tenant's look is showing.
  const scope = appearanceScope();
  if (!scope) return;
  writeStamp(scope, { at: now });
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
    // would overwrite a tenant's saved colors the moment it is opened.
    if (mode === "1h" || mode === "daily") writeStamp(scope, { at: now });
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
  const onVisible = () => check();
  const timer = setInterval(check, CHECK_MS);
  document.addEventListener("visibilitychange", onVisible);
  return () => {
    clearInterval(timer);
    document.removeEventListener("visibilitychange", onVisible);
  };
}
