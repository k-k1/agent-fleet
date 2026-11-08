import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { agentOf } from "../../agents/registry.ts";

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

/** Which of a kind's models the launch dialog offers for the chosen route (ADR 0128 decision 6),
 *  or undefined when the kind has no route-only models. A route with a model prefix (the kind's
 *  routeModelPrefix; opencode on bedrock: amazon-bedrock/) offers only ids under it, so the kind's
 *  own default ("") is left out. With no such route chosen (none picked, none offered on this
 *  launch, a Managed launch, profiles still loading) every route-only id is left out: the Agent
 *  lists them whenever the route is open, and they cannot run without it. */
export function routeModelFilter(kind: string, route: string): ((id: string) => boolean) | undefined {
  const prefixes = Object.values(agentOf(kind).routeModelPrefix ?? {});
  if (!prefixes.length) return undefined;
  const own = agentOf(kind).routeModelPrefix?.[route] ?? "";
  if (own) return (id) => id.startsWith(own) && id.length > own.length;
  return (id) => !prefixes.some((p) => id.startsWith(p));
}

// The system inference profile scopes Claude Code passes through; the Agent's list
// (routes.BedrockScopes) is the same.
const BEDROCK_SCOPES = ["us", "eu", "apac", "jp", "au", "us-gov", "global"];
const PROVIDER_RE = /^[a-z0-9][a-z0-9-]*$/;
const MODEL_SEG_RE = /^[a-z0-9][a-z0-9_-]*$/;
const VERSION_RE = /^[0-9]+(:[0-9]+[km])?$/;

/** Whether `id` reads as an Anthropic model id on Bedrock: a foundation-model id
 *  (anthropic.claude-…) or a system inference profile id (<scope>.anthropic.claude-…). The same
 *  form routes.ParseBedrockAnthropicModelID accepts; aliases ("opus"), first-party ids
 *  ("claude-…") and ARNs are not, because Claude Code would map them or walk a fallback ladder of
 *  other ids instead of calling the one the member picked. Whether the account serves it is only
 *  learnt from Bedrock's answer. */
export function bedrockModelOk(id: string): boolean {
  if (id === "" || id !== id.trim() || id.length > 200 || /^arn:/i.test(id) || id.includes("/")) return false;
  const colon = id.indexOf(":");
  const base = colon < 0 ? id : id.slice(0, colon);
  if (colon >= 0 && !VERSION_RE.test(id.slice(colon + 1))) return false;
  let parts = base.split(".");
  if (parts.length >= 3 && BEDROCK_SCOPES.includes(parts[0])) parts = parts.slice(1);
  if (parts.length < 2 || !PROVIDER_RE.test(parts[0]) || BEDROCK_SCOPES.includes(parts[0])) return false;
  return parts[0] === "anthropic" && parts.slice(1).every((p) => MODEL_SEG_RE.test(p));
}

const modelKey = (profileId: string) => `af.bedrock-model.${profileId}`;

/** The Bedrock model id last launched with this profile on this browser, "" when none. Kept per
 *  profile because the ids an account and Region serve differ between profiles. */
export function rememberedBedrockModel(profileId: string): string {
  try {
    return localStorage.getItem(modelKey(profileId)) ?? "";
  } catch {
    return "";
  }
}

export function rememberBedrockModel(profileId: string, id: string): void {
  try {
    localStorage.setItem(modelKey(profileId), id);
  } catch {
    // Private mode or a full quota: the next launch starts from an empty field, nothing breaks.
  }
}
