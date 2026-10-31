// ssmImportExec — applies an SSM import plan (planSsmImport) to the CP, one row at a time.
// Shared by the settings bundle import (BackupTab) and the ~/.aws/config import (issue #3010).
//
// Why the rules below:
//   - The current lists must be read successfully first. `api()` answers a failed GET with the
//     error body, not a throw, and the CP has no unique constraint on a profile's label
//     (store_ssm.go CreateSSMProfile), so reading a failure as "nothing exists yet" creates every
//     profile a second time.
//   - Rows run in order, because a chained profile and a host need their source's new id. A row
//     whose source did not get created is not sent: it would be a 400, or worse, bind to a
//     same-named row the plan did not mean.
//   - A request that never came back (network) leaves its row "unknown" and stops the run: the
//     row may exist, and the next import's re-read of the lists is what tells.
import { api, rawJSON } from "../../../core/api/client.ts";
import type { ExistingSignIn } from "../../../lib/awsConfigImport.ts";
import type { SsmPlan } from "../../../lib/settingsBundle.ts";

export type RowOutcome = "created" | "failed" | "unknown" | "skipped_source" | "not_run";

export interface ExecRow {
  kind: "profile" | "host";
  label: string;
  outcome: RowOutcome;
  /** The CP's error code for "failed" (never its message or the request body). */
  code?: string;
}

/** The member's current profiles and hosts, or null when either could not be read. */
export async function loadSsmLists(): Promise<{ profiles: any[]; hosts: any[] } | null> {
  try {
    const [profiles, hosts] = await Promise.all([api("api/ssm/profiles"), api("api/ssm/hosts")]);
    if (!Array.isArray(profiles) || !Array.isArray(hosts)) return null;
    return { profiles, hosts };
  } catch {
    return null;
  }
}

/** The member's shared sign-ins, or null when they could not be read (an older Control Plane
 *  answers 404, a failure answers an error body): the import then proposes no sign-in. */
export async function loadSignIns(): Promise<ExistingSignIn[] | null> {
  try {
    const d = await api("api/ssm/sign-ins");
    if (!Array.isArray(d)) return null;
    return d
      .filter((x) => typeof x?.name === "string")
      .map((x) => ({ name: String(x.name), startUrl: String(x.startUrl ?? ""), ssoRegion: String(x.ssoRegion ?? "") }));
  } catch {
    return null;
  }
}

export async function executeSsmPlan(
  plan: SsmPlan,
  curProfiles: any[],
  opts: { cancelled?: () => boolean; onProgress?: (done: number, total: number) => void } = {},
): Promise<ExecRow[]> {
  const out: ExecRow[] = [];
  // Exact label -> id of the sso profiles a chain may name (the plan already checked label and
  // sign-in), and case-folded label -> id for hosts (the planner's rule).
  const ssoIds = new Map<string, string>();
  const anyIds = new Map<string, string>();
  for (const p of curProfiles) {
    if (p?.kind !== "assume_role") ssoIds.set(String(p.label), String(p.id));
    const k = String(p?.label ?? "")
      .trim()
      .toLowerCase();
    if (k && !anyIds.has(k)) anyIds.set(k, String(p.id));
  }
  const total = plan.profiles.length + plan.hosts.length;
  let stopped = false;
  const send = async (kind: ExecRow["kind"], label: string, path: string, body: unknown): Promise<string | null> => {
    if (stopped || opts.cancelled?.()) {
      stopped = true;
      out.push({ kind, label, outcome: "not_run" });
      return null;
    }
    let res: Response;
    try {
      res = await rawJSON(path, "POST", body);
    } catch {
      stopped = true;
      out.push({ kind, label, outcome: "unknown" });
      return null;
    }
    if (!res.ok) {
      const j = await res.json().catch(() => null);
      out.push({ kind, label, outcome: "failed", code: typeof j?.error?.code === "string" ? j.error.code : String(res.status) });
      return null;
    }
    // A success whose body never arrived (or a profile without its id) is as uncertain as no
    // answer at all: the row probably exists, but the next row cannot be chained to it.
    let id = "";
    let bodyRead = true;
    try {
      const j = await res.json();
      id = typeof j?.id === "string" ? j.id : "";
    } catch {
      bodyRead = false;
    }
    if (!bodyRead || (!id && kind === "profile")) {
      stopped = true;
      out.push({ kind, label, outcome: "unknown" });
      return null;
    }
    out.push({ kind, label, outcome: "created" });
    return id;
  };
  for (const p of plan.profiles) {
    const { source, sourceKey: _k, sourceId, ...rest } = p;
    let body: unknown = rest;
    if (p.kind === "assume_role") {
      const sourceProfileId = sourceId || ssoIds.get(source || "");
      if (!sourceProfileId) {
        out.push({ kind: "profile", label: p.label, outcome: stopped ? "not_run" : "skipped_source" });
        continue;
      }
      body = { ...rest, sourceProfileId };
    }
    const id = await send("profile", p.label, "api/ssm/profiles", body);
    opts.onProgress?.(out.length, total);
    if (id) {
      if (p.kind !== "assume_role") ssoIds.set(p.label, id);
      anyIds.set(p.label.trim().toLowerCase(), id);
    }
  }
  for (const h of plan.hosts) {
    const profileId = anyIds.get(h.profile.trim().toLowerCase());
    if (!profileId) {
      out.push({ kind: "host", label: h.alias, outcome: stopped ? "not_run" : "skipped_source" });
      continue;
    }
    await send("host", h.alias, "api/ssm/hosts", {
      alias: h.alias,
      profileId,
      instanceId: h.instanceId,
      documentName: h.documentName,
      region: h.region,
    });
    opts.onProgress?.(out.length, total);
  }
  return out;
}

/** Counts per outcome, for a one-line summary. */
export function countOutcomes(rows: ExecRow[]): Record<RowOutcome, number> & { profiles: number; hosts: number } {
  const c = { created: 0, failed: 0, unknown: 0, skipped_source: 0, not_run: 0, profiles: 0, hosts: 0 };
  for (const r of rows) {
    c[r.outcome]++;
    if (r.outcome === "created") c[r.kind === "profile" ? "profiles" : "hosts"]++;
  }
  return c;
}
