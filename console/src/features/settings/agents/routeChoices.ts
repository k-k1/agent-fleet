import { bedrockConnection, type ModelRouteProfile } from "../../repos/bedrockProfiles.ts";
import type { LiveKind } from "../credentials/memberCatalog.ts";

// The default routes a harness may take, shared by the Agents page (HarnessHead) and the schedule
// editor. Kept apart from the page's components so a screen outside Settings can import it.

/** A default route the select offers: the route, and the connection it is saved with. */
export interface RouteChoice {
  route: string;
  connection?: string;
  label: string;
}

/**
 * routeChoices are the default routes a harness may take: each route the policy allows it, with
 * the connection a per-connection route needs. A route whose connection the member has none of
 * (no marked AWS profile, no llama.cpp endpoint) is not offered, since a default the launch cannot
 * hold to would only be refused.
 */
export function routeChoices(
  live: LiveKind | undefined,
  label: (route: string) => string,
  profiles: ModelRouteProfile[] | null,
  lcppBinding: string | undefined,
): RouteChoice[] {
  const routes = [...new Set([...(live?.routes ?? []), ...(live?.fixedRoutes ?? [])])].sort();
  const out: RouteChoice[] = [];
  for (const r of routes) {
    if (r === "bedrock") {
      for (const p of profiles ?? []) {
        out.push({ route: r, connection: bedrockConnection(p.id, p.region), label: `${label(r)} — ${p.label}` });
      }
    } else if (r === "member-llamacpp") {
      if (lcppBinding) out.push({ route: r, connection: lcppBinding, label: label(r) });
    } else {
      out.push({ route: r, label: label(r) });
    }
  }
  return out;
}

/** The select value of a choice: its route and connection. */
export const choiceKey = (c: { route: string; connection?: string }) => `${c.route}|${c.connection ?? ""}`;
