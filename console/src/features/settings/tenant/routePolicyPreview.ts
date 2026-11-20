// The pre-save preview on the tenant's Restrictions tab (#4446 item 2): which harnesses, chat
// backends and image providers saving the draft would leave with no usable route.
//
// Only the tenant's own layer is computed here. The layers above it are taken as the CP answered
// them (each route's forbiddenBy, the other layers' rules for the self-hosted check), so the
// preview cannot drift from a rule it does not own.
//
// The rule is held to the CP by the Go-generated table preview-cases.json
// (routepolicysrv TestPreviewCases): for every case the prediction must never allow a route the CP
// then refuses, and must equal the CP's answer unless a host the CP has not seen is involved.
// Whenever the Console cannot tell, it answers "not allowed": a preview may over-warn, never
// under-warn. That holds against the upper layers as loaded; one another administrator narrows
// after the load shows up when the page reloads (a save only checks the tenant layer's revision).
import { allows } from "./routePolicyCeiling.ts";
import { layerRules } from "./routePolicySummary.ts";
import { parseOwners, restricts, type OwnersView, type RouteRule, type TenantPolicyView } from "./routePolicyRule.ts";

type Group = keyof OwnersView;
const GROUPS: Group[] = ["kinds", "chat", "imagegen"];

/** mirrors routepolicy.Rule.allowsSelfHosted: a per-connection route on an approved endpoint. */
function allowsSelfHosted(r: RouteRule, id: string): boolean {
  if (!restricts(r)) return true;
  return (
    !r.deny?.includes(id) &&
    (r.classes === undefined || r.classes.includes("self-hosted")) &&
    (r.destinations === undefined || r.destinations.includes("self-hosted"))
  );
}

export interface Preview {
  /** Per group, the routes the layers would allow with the draft saved (as owners[].allowed). */
  allowed: Record<Group, Record<string, string[]>>;
  /** Per group, the ids usable now that the draft would leave with no usable route. */
  hides: Record<Group, string[]>;
  /** The draft names a host the CP has not judged yet and no host is approved so far, so the
   *  per-connection routes are not counted and `allowed` may hold less than the CP will answer. */
  unsavedHosts: boolean;
  /** unsavedHosts, and an owner listed in `hides` has a route such a host could make usable. */
  unsavedMayRescue: boolean;
}

/** previewTenantRule answers what the layers would allow if the tenant's rule were `draft`; null
 *  when the answer cannot be computed (a CP that sends no owners, or a malformed one). */
export function previewTenantRule(v: TenantPolicyView, draft: RouteRule): Preview | null {
  const owners = parseOwners(v.owners);
  if (!owners || !Array.isArray(v.routes)) return null;
  const layers = layerRules(v);
  const everyLayer = { ...layers, tenant: draft };

  // A route that is not allowed but names no layer is forbidden for a reason this code does not
  // know: it stays forbidden whatever the draft says.
  const forbiddenAbove = new Map(
    v.routes.map((r) => {
      const by = (r.forbiddenBy ?? []).filter((l) => l !== "tenant");
      return [r.id, !r.allowed && !r.forbiddenBy?.length ? ["unknown"] : by] as const;
    }),
  );
  // A route the CP did not list is not allowed.
  const rawAllowed = (id: string) => {
    const by = forbiddenAbove.get(id);
    return by !== undefined && by.length === 0 && allows(draft, id, v.vocabulary);
  };

  // A host is approved only when the CP already judged exactly that host with the layers above
  // the tenant, which the draft cannot change.
  const judged = new Map((Array.isArray(v.endpoints) ? v.endpoints : []).map((e) => [e.host, e.approved] as const));
  const hosts = draft.endpoints ?? [];
  const approvedHosts = hosts.filter((h) => judged.get(h) === true).length;
  const unjudged = hosts.some((h) => !judged.has(h));

  const onHost = new Set(
    v.vocabulary.routes
      .filter((rt) => rt.perConnection)
      .map((rt) => rt.id)
      .filter((id) => Object.values(everyLayer).every((r) => allowsSelfHosted(r, id))),
  );
  // Only an approved host turns per-connection routes on, and one already approved does it for
  // every route: a further unjudged host changes nothing then.
  const unsavedHosts = unjudged && approvedHosts === 0 && onHost.size > 0;
  const ok = (id: string) => rawAllowed(id) || (approvedHosts > 0 && onHost.has(id));

  const allowed = { kinds: {}, chat: {}, imagegen: {} } as Preview["allowed"];
  const hides = { kinds: [], chat: [], imagegen: [] } as unknown as Preview["hides"];
  let unsavedMayRescue = false;
  for (const g of GROUPS) {
    for (const it of owners[g]) {
      const now = it.routes.filter(ok);
      allowed[g][it.id] = now;
      // Only an owner this save would newly hide is a caveat's subject.
      if (now.length > 0 || it.allowed.length === 0) continue;
      hides[g].push(it.id);
      if (unsavedHosts && it.routes.some((r) => onHost.has(r))) unsavedMayRescue = true;
    }
  }
  return { allowed, hides, unsavedHosts, unsavedMayRescue };
}
