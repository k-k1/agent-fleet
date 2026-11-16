// What the member may launch under the credential policy (ADR 0138 D10a, D11b), from the live,
// policy-filtered kind catalogue (GET /agents/kinds, read by memberCatalog.ts). Pure functions
// over that answer, so the launch dialog, the repo quick-launch, the schedule editor and the
// assistant's backend order all decide the same way. Nothing here imports a store or the API
// client: registry-level code is imported by most screens and their tests.
import { AGENTS, type AvailCtx } from "./registry.ts";
import type { SessionKind } from "./kindTable.ts";

/** One kind of the live catalogue, as far as launching reads it. */
export interface LiveKindAccess {
  kind: string;
  /** Selectable routes the policy allows. */
  routes?: string[];
  /** Routes the kind runs on with no selection, narrowed to the allowed ones. */
  fixedRoutes?: string[];
  /** Allowed routes whose credential the Workspace holds now; absent when the kind cannot tell. */
  usableRoutes?: string[];
  /** The kind's assistant chat backend is offered (absent: the kind has none). */
  chat?: boolean;
}

export type LiveKinds = ReadonlyMap<string, LiveKindAccess>;

/** A saved default route (agentLaunchDefaults[kind].route). */
export interface RouteSel {
  route: string;
  connection?: string;
}

// The route a create with no selection runs on (AgentDescriptor.noSelectionRoute). A kind without
// one resolves its route from its login (codex, the fixed-route kinds) or per model (opencode), so
// the catalogue's own answer for it is the one to trust.
const noSelectionRoute = (kind: string): string | undefined => AGENTS[kind as SessionKind]?.noSelectionRoute;

// Kinds that run no model are outside the credential policy (the Agent's sessionx.modelKind): a
// policy that cannot be confirmed must not take a shell away.
const MODEL_FREE = new Set(["shell", "ssm"]);

const allowedOf = (k: LiveKindAccess): string[] => [...(k.routes ?? []), ...(k.fixedRoutes ?? [])];

/** Whether the Workspace holds route's credential; true while the kind cannot tell. */
export function routeUsable(k: LiveKindAccess | undefined, route: string): boolean {
  return !k?.usableRoutes || k.usableRoutes.includes(route);
}

/**
 * kindLaunchable: the kind may be offered in a launch menu. A kind that runs no model keeps its own
 * check alone. Without a confirmed catalogue
 * (live null) nothing is: the policy in force is unknown. A kind the catalogue leaves out is
 * forbidden by the policy or switched off by the member. Where the catalogue says which routes
 * are usable, a kind with none is not offered. A kind whose own check is its no-selection login
 * alone (claude) is judged on the usable routes alone, since an Anthropic key or a marked AWS
 * profile launches it as well as the subscription login; every other kind also keeps its own
 * connection check, so nothing is offered that was not before.
 */
export function kindLaunchable(kind: string, ctx: AvailCtx, live: LiveKinds | null): boolean {
  if (MODEL_FREE.has(kind)) return AGENTS[kind as SessionKind].available(ctx);
  if (!live) return false;
  const k = live.get(kind);
  if (!k) return false;
  const own = AGENTS[kind as SessionKind]?.available(ctx) ?? false;
  if (!k.usableRoutes) return own;
  if (k.usableRoutes.length === 0) return false;
  return !!noSelectionRoute(kind) || own;
}

/**
 * emptyRouteOk: a launch of kind may leave its route unselected. Not when the kind is known to
 * run on one route with no selection (claude: the subscription login) and that route is not
 * allowed or its credential is not held: the launch would be refused, or start a CLI with no
 * login. The member then chooses a route; the Console never picks one for them (D11b).
 */
export function emptyRouteOk(kind: string, k: LiveKindAccess | undefined): boolean {
  const r = noSelectionRoute(kind);
  // An entry with no route facts at all is a kind that declares none: nothing to choose from.
  if (!r || (k && !k.routes && !k.fixedRoutes)) return true;
  return !!k && allowedOf(k).includes(r) && routeUsable(k, r);
}

/**
 * defaultRouteUsable: the saved default route can be launched on now, as far as the catalogue
 * shows: the policy allows it for the kind and its credential is held. A per-connection default
 * (a Bedrock profile) is also checked against the connections the caller lists, when it has
 * them; null means not known yet.
 */
export function defaultRouteUsable(k: LiveKindAccess | undefined, saved: RouteSel, connections?: string[] | null): boolean {
  if (!k || !allowedOf(k).includes(saved.route) || !routeUsable(k, saved.route)) return false;
  if (saved.connection && connections) return connections.includes(saved.connection);
  return true;
}

/** What a quick launch (no dialog) sends as its route, or "dialog" when the member has to choose. */
export type QuickRoute = { route?: RouteSel } | "dialog";

/**
 * quickLaunchRoute decides a repo row's one-click launch. The saved default route is sent when it
 * is selectable and usable, and held to by the Agent when it is a fixed one. A default that
 * cannot be used now, or a kind that cannot run without a route choice, opens the launch dialog
 * instead: a quick launch never runs on a route the member did not choose. A selectable default
 * on Bedrock opens it too: the route needs a model of its own (a Bedrock id for claude, an
 * amazon-bedrock/ model for opencode), which only the dialog asks for.
 */
export function quickLaunchRoute(kind: string, live: LiveKinds | null, saved: RouteSel | undefined): QuickRoute {
  const k = live?.get(kind);
  if (!k) return "dialog";
  if (saved) {
    if (!defaultRouteUsable(k, saved)) return "dialog";
    if (!(k.routes ?? []).includes(saved.route)) return {};
    if (saved.route === "bedrock") return "dialog";
    return { route: saved };
  }
  return emptyRouteOk(kind, k) ? {} : "dialog";
}

/** Whether kind's assistant chat backend may be offered: only when the catalogue says so. */
export function chatOffered(kind: string, live: LiveKinds | null): boolean {
  return live?.get(kind)?.chat === true;
}

/**
 * mergeVisibleOrder writes a reordering of the visible entries back into the full stored order:
 * each visible slot takes the next entry of the new order, and hidden entries keep their places.
 * A hidden backend is never shown, and never moved by a reorder of the ones around it.
 */
export function mergeVisibleOrder(full: string[], visible: string[]): string[] {
  const shown = new Set(visible);
  let i = 0;
  return full.map((k) => (shown.has(k) ? visible[i++] : k));
}
