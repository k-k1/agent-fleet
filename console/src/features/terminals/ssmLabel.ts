// How the Terminal view names a machine: the alias of the registered host that points at it, or
// the instance id when none does (ADR 0142 decision 10).
import type { SsmHost } from "../../types/session.ts";

/** The registered host for an instance. A host with no region follows the profile's default, so it
 *  matches any region; one with a region has to match the session's. An exact region wins. */
export function hostFor(target: string, region: string, hosts: readonly SsmHost[]): SsmHost | undefined {
  if (!target) return undefined;
  const same = hosts.filter((h) => h.instanceId === target);
  return same.find((h) => !!region && h.region === region) ?? same.find((h) => !h.region || !region);
}

/** The group's name: the host's alias, else the instance id. */
export function machineLabel(target: string, region: string, hosts: readonly SsmHost[]): string {
  return hostFor(target, region, hosts)?.alias || target;
}

/** What a row says beyond its group: the profile and the run-as document, when it has them. */
export function ssmRowDetail(s: { ssm?: { profile?: string; document?: string } }): string {
  return [s.ssm?.profile, s.ssm?.document].filter(Boolean).join(" · ");
}
