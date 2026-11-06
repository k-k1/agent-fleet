import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";

/** A Settings AWS profile the member marked as a model route (ADR 0128 decision 11, option C2):
 *  the only kind the bedrock route accepts. */
export interface ModelRouteProfile {
  id: string;
  label: string;
  region: string;
}

const REGION_RE = /^[a-z]{2}(-[a-z]+)+-[0-9]$/;

/** The marked profiles in a GET api/ssm/profiles answer, in its order. Anything that is not a
 *  well-formed profile row is left out. */
export function markedProfiles(list: unknown): ModelRouteProfile[] {
  if (!Array.isArray(list)) return [];
  const out: ModelRouteProfile[] = [];
  for (const p of list) {
    if (p?.modelRoute !== true || typeof p?.id !== "string" || !/^[A-Za-z0-9_-]+$/.test(p.id)) continue;
    out.push({
      id: p.id,
      label: typeof p.label === "string" && p.label ? p.label : p.id,
      region: typeof p.region === "string" ? p.region : "",
    });
  }
  return out;
}

/** The selection's connection reference, `<profile id>@<region>`. An empty region means the
 *  profile's own, resolved by the Agent at every call. */
export function bedrockConnection(profileId: string, region: string): string {
  return `${profileId}@${region.trim()}`;
}

/** Whether a region typed in the launch dialog can be sent: empty (the profile's own) or one AWS
 *  writes. The Agent checks again; this only keeps a typo from becoming a refused launch. */
export function regionOk(region: string): boolean {
  const r = region.trim();
  return r === "" || (REGION_RE.test(r) && !r.startsWith("cn-"));
}

/** The member's marked profiles, loaded once while enabled. null until the answer arrives, []
 *  when it failed or none is marked: the bedrock route is then not offered at all. */
export function useModelRouteProfiles(enabled: boolean): ModelRouteProfile[] | null {
  const [profiles, setProfiles] = useState<ModelRouteProfile[] | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    api("api/ssm/profiles")
      .then((d) => live && setProfiles(markedProfiles(d)))
      .catch(() => live && setProfiles([]));
    return () => {
      live = false;
    };
  }, [enabled]);
  return enabled ? profiles : null;
}

/** The create body's `route` object: the id, and the connection only when the route takes one. */
export function routeSelectionBody(route: string, connection?: string): { route: string; connection?: string } {
  return connection ? { route, connection } : { route };
}
