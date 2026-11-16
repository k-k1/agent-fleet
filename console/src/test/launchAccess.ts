// A LaunchAccess for render tests that are not about the credential policy: every kind is
// allowed, with the routes the test's kind catalogue (setKindCatalog) declares for it, read at
// render time so a catalogue set after the first render still shows.
import { kindRoutes } from "../agents/kindCatalog.ts";
import type { LiveKindAccess, LiveKinds } from "../agents/launchAccess.ts";

export interface TestLaunchAccess {
  kinds: LiveKinds;
  noRoute: boolean;
  allOff: boolean;
  refresh?: () => void;
}

export function openAccess(extra: Record<string, Partial<LiveKindAccess>> = {}): TestLaunchAccess {
  const entry = (k: string): LiveKindAccess => {
    const routes = kindRoutes(k);
    return { kind: k, ...(routes.length ? { routes } : {}), ...extra[k] };
  };
  const kinds = { get: entry, has: () => true } as unknown as LiveKinds;
  return { kinds, noRoute: false, allOff: false };
}
