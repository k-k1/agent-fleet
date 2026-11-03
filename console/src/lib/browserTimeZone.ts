// Publishes the browser's resolved IANA zone as the machine-written ui-prefs key
// `browserTimeZone`, so the Agent can follow the browser for the text it renders when the member's
// `timeZone` setting is "auto" (it cannot see the browser itself). The Agent uses it only after
// an explicit `timeZone`, so a stale value never overrides a choice.
//
// Several devices share the one key and the last writer wins: a laptop that changed zone updates
// it when its page is next foregrounded, and a phone left elsewhere replaces it the same way.
// Writing only on a difference, and only from load and foreground events (App.tsx), keeps this
// from becoming a write storm or echoing against the sync's own hydrate.
import { getSettings, setSetting } from "./settings.ts";
import { isValidTimeZone } from "./intl.ts";
import { BROWSER_TIME_ZONE_MAX } from "./settings/normalize.ts";

/** The zone this browser resolves right now, or "" when it reports none that Intl accepts. */
export function resolveBrowserTimeZone(): string {
  try {
    const z = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return isValidTimeZone(z) && z.length <= BROWSER_TIME_ZONE_MAX ? z : "";
  } catch {
    return "";
  }
}

/** Store the browser's zone in the settings when it differs from the stored one. */
export function publishBrowserTimeZone(): void {
  const zone = resolveBrowserTimeZone();
  if (zone === "" || getSettings().browserTimeZone === zone) return;
  setSetting("browserTimeZone", zone);
}
