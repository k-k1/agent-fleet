// The executor's guarantees (issue #3010): a failed read of the current lists sends nothing,
// a source that was not created stops its dependents, and a request that never came back
// stops the run instead of guessing.
import { describe, it, expect, beforeEach, vi } from "vitest";

const api = vi.fn();
const rawJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  rawJSON: (...a: unknown[]) => rawJSON(...a),
}));

const { executeSsmPlan, loadSsmLists, countOutcomes } = await import("./ssmImportExec.ts");
import type { SsmPlan } from "../../../lib/settingsBundle.ts";

const sso = (label: string) => ({ label, startUrl: "https://c.awsapps.com/start", ssoRegion: "us-east-1", accountId: "1", roleName: "R", region: "" });
const role = (label: string, source: string) => ({
  kind: "assume_role" as const,
  label,
  startUrl: "",
  ssoRegion: "",
  accountId: "",
  roleName: "",
  region: "",
  source,
  sourceKey: "k",
  roleArn: "arn:aws:iam::222222222222:role/D",
  externalId: "",
  sessionName: "",
});
const plan = (profiles: any[], hosts: any[] = []): SsmPlan => ({ profiles, hosts, skippedProfiles: [], skippedHosts: [] });
const ok = (id: string) => new Response(JSON.stringify({ id }), { status: 201 });

beforeEach(() => {
  api.mockReset();
  rawJSON.mockReset();
});

describe("loadSsmLists", () => {
  it("returns null when a GET answers with an error body instead of a list", async () => {
    api.mockImplementation((p: string) => Promise.resolve(p.endsWith("profiles") ? { error: { code: "internal" } } : []));
    expect(await loadSsmLists()).toBeNull();
    api.mockImplementation(() => Promise.reject(new Error("net")));
    expect(await loadSsmLists()).toBeNull();
    api.mockImplementation(() => Promise.resolve([]));
    expect(await loadSsmLists()).toEqual({ profiles: [], hosts: [] });
  });
});

describe("executeSsmPlan", () => {
  it("chains to the id the source just got, and to an existing source by exact label", async () => {
    rawJSON.mockResolvedValueOnce(ok("new-a")).mockResolvedValueOnce(ok("r1")).mockResolvedValueOnce(ok("r2"));
    const rows = await executeSsmPlan(plan([sso("a"), role("ra", "a"), role("rb", "B")]), [{ id: "old-b", label: "B", kind: "sso" }]);
    expect(rows.map((r) => r.outcome)).toEqual(["created", "created", "created"]);
    expect(rawJSON.mock.calls[1][2]).toMatchObject({ sourceProfileId: "new-a", label: "ra" });
    expect(rawJSON.mock.calls[1][2]).not.toHaveProperty("source");
    expect(rawJSON.mock.calls[1][2]).not.toHaveProperty("sourceKey");
    expect(rawJSON.mock.calls[2][2]).toMatchObject({ sourceProfileId: "old-b" });
  });

  it("does not send a role whose source failed, and reports the CP's code only", async () => {
    rawJSON.mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: "bad_start_url", message: "secret-ish text" } }), { status: 400 }));
    const rows = await executeSsmPlan(plan([sso("a"), role("ra", "a")]), []);
    expect(rawJSON).toHaveBeenCalledTimes(1);
    expect(rows).toEqual([
      { kind: "profile", label: "a", outcome: "failed", code: "bad_start_url" },
      { kind: "profile", label: "ra", outcome: "skipped_source" },
    ]);
  });

  it("stops at a request that never came back", async () => {
    rawJSON.mockRejectedValueOnce(new Error("network"));
    const rows = await executeSsmPlan(plan([sso("a"), sso("b")], [{ alias: "h", profile: "a", instanceId: "i-1", documentName: "", region: "" }]), []);
    expect(rawJSON).toHaveBeenCalledTimes(1);
    expect(rows.map((r) => r.outcome)).toEqual(["unknown", "not_run", "not_run"]);
  });

  it("stops when cancelled and keeps what was done", async () => {
    let cancel = false;
    rawJSON.mockImplementation(async () => {
      cancel = true;
      return ok("x");
    });
    const rows = await executeSsmPlan(plan([sso("a"), sso("b")]), [], { cancelled: () => cancel });
    expect(rows.map((r) => r.outcome)).toEqual(["created", "not_run"]);
    expect(countOutcomes(rows)).toMatchObject({ created: 1, not_run: 1, profiles: 1 });
  });
});
