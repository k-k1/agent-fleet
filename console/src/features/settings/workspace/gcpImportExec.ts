// gcpImportExec — creates the rows of a gcloud-configuration import through the CP's existing
// POST api/gcp/profiles, one at a time (issue #3017, part 2). The same rules as ssmImportExec:
//   - The current list must be read first. `api()` answers a failed GET with the error body, not
//     a throw, and the CP has no unique constraint on a label, so reading a failure as "nothing
//     exists yet" would create every row a second time.
//   - A request that never came back, or a success whose body did not arrive, leaves its row
//     "unknown" and stops the run: the row may exist, and the next import's re-read tells.
//   - A failure the CP answered (a 400) is that row's alone; the run goes on.
// The body is built here from the plan's fields and nothing else.
import { api, rawJSON } from "../../../core/api/client.ts";
import type { ExistingGcpProfile, GcpCreate } from "../../../lib/gcloudConfigImport.ts";

export type GcpRowOutcome = "created" | "failed" | "unknown" | "not_run";

export interface GcpExecRow {
  label: string;
  outcome: GcpRowOutcome;
  /** The CP's error code for "failed" (never its message or the request body). */
  code?: string;
}

/** The member's current profiles, or null when they could not be read. */
export async function loadGcpProfiles(): Promise<ExistingGcpProfile[] | null> {
  try {
    const rows = await api("api/gcp/profiles");
    return Array.isArray(rows) ? rows : null;
  } catch {
    return null;
  }
}

export async function executeGcpPlan(
  plan: GcpCreate[],
  opts: { cancelled?: () => boolean; onProgress?: (done: number, total: number) => void } = {},
): Promise<GcpExecRow[]> {
  const out: GcpExecRow[] = [];
  let stopped = false;
  for (const p of plan) {
    if (stopped || opts.cancelled?.()) {
      stopped = true;
      out.push({ label: p.label, outcome: "not_run" });
      continue;
    }
    const body = {
      loginMethod: "google",
      label: p.label,
      project: p.fields.project,
      quotaProject: p.fields.quotaProject,
      account: p.fields.account,
      region: p.fields.region,
      zone: p.fields.zone,
      impersonateServiceAccount: p.fields.impersonateServiceAccount,
    };
    let res: Response;
    try {
      res = await rawJSON("api/gcp/profiles", "POST", body);
    } catch {
      stopped = true;
      out.push({ label: p.label, outcome: "unknown" });
      continue;
    }
    if (!res.ok) {
      const j = await res.json().catch(() => null);
      out.push({ label: p.label, outcome: "failed", code: typeof j?.error?.code === "string" ? j.error.code : String(res.status) });
    } else {
      let id = "";
      try {
        const j = await res.json();
        id = typeof j?.id === "string" ? j.id : "";
      } catch {
        // falls through to "unknown"
      }
      if (!id) stopped = true;
      out.push({ label: p.label, outcome: id ? "created" : "unknown" });
    }
    opts.onProgress?.(out.length, plan.length);
  }
  return out;
}
