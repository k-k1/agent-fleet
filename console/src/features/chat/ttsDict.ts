// features/chat/ttsDict - the tenant-wide pronunciation dictionary (docs/log/24). The dictionary an
// admin puts in the CP's SettingsStore (GET /api/tts/dict) is cached in this module, and
// effectiveDict() returns it merged with the user dictionary (the ttsUserDict setting). On the same
// spelling the user dictionary wins. Everything is applied client-side (startTts, startNarration,
// turnTts and ReaderView all use it).

import { api } from "../../core/api/client.ts";
import { getSettings } from "../../lib/settings.ts";
import { parseUserDict, mergeDicts } from "./ttsText.ts";

let tenantPairs: [string, string][] = [];
let loaded = false;
let loading: Promise<void> | null = null;

// loadTenantDict fetches the shared dictionary and caches it. A failure (not signed in, network)
// is given up on quietly; the next effectiveDict() tries again.
export function loadTenantDict(): Promise<void> {
  if (loaded) return Promise.resolve();
  if (loading) return loading;
  loading = api("api/tts/dict")
    .then((d) => {
      if (d && !d.error && typeof d.dict === "string") {
        tenantPairs = parseUserDict(d.dict);
        loaded = true;
      }
    })
    .catch(() => {})
    .finally(() => {
      loading = null;
    });
  return loading;
}

// setTenantDict refreshes the cache right after the admin screen saves, so the change applies
// immediately without a re-fetch.
export function setTenantDict(raw: string): void {
  tenantPairs = parseUserDict(raw);
  loaded = true;
}

// effectiveDict merges the user dictionary with the tenant-wide one: user entries win, longest
// spelling first. When the shared one is not loaded yet it starts the fetch in the background and
// returns the user dictionary alone for now, so the shared entries take effect from the next read.
export function effectiveDict(): [string, string][] {
  if (!loaded) void loadTenantDict();
  return mergeDicts(parseUserDict(getSettings().ttsUserDict), tenantPairs);
}

// Prefetch once at start-up so the shared dictionary applies from the very first read. If that runs
// before sign-in and fails, effectiveDict() retries.
void loadTenantDict();
