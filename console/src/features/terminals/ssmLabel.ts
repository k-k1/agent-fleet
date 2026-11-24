// How the Terminal view names a machine: the alias of the registered host that points at it, or
// the instance id when none does (ADR 0142 decision 10).
import type { SsmHost } from "../../types/session.ts";

/** The part of a registered SSM profile the match needs (GET /api/ssm/profiles). */
export interface SsmProfileRef {
  id: string;
  /** The account the profile reaches (for a role-chained profile, the role's). */
  accountId?: string;
  /** The profile's own default region. */
  region?: string;
}

/** The registered host for the machine a group stands for: the instance id, the account and the
 *  region all have to agree, as the group's own key does. A host with no region of its own uses
 *  its profile's, which is what the session's region was resolved from. Where a side is unknown
 *  (a list from an older Agent carries no account, a profile with no region) it does not rule the
 *  host out, but a side that is known and differs does. */
export function hostFor(
  target: string,
  region: string,
  accountId: string,
  hosts: readonly SsmHost[],
  profiles: readonly SsmProfileRef[],
): SsmHost | undefined {
  if (!target) return undefined;
  const profile = (h: SsmHost) => profiles.find((p) => p.id === h.profileId);
  const same = hosts.filter((h) => {
    if (h.instanceId !== target) return false;
    const p = profile(h);
    if (accountId && p?.accountId && p.accountId !== accountId) return false;
    const eff = h.region || p?.region || "";
    return !region || !eff || eff === region;
  });
  // An exact region beats a host that only might be there.
  return same.find((h) => !!region && (h.region || profile(h)?.region) === region) ?? same[0];
}

/** The group's name: the host's alias, else the instance id. */
export function machineLabel(
  target: string,
  region: string,
  accountId: string,
  hosts: readonly SsmHost[],
  profiles: readonly SsmProfileRef[],
): string {
  return hostFor(target, region, accountId, hosts, profiles)?.alias || target;
}

/** What a row says beyond its group: the profile and the run-as document, when it has them. */
export function ssmRowDetail(s: { ssm?: { profile?: string; document?: string } }): string {
  return [s.ssm?.profile, s.ssm?.document].filter(Boolean).join(" · ");
}
