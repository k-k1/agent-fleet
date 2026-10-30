// The gcloud import executor's guarantees (issue #3017, part 2): a failed read of the current
// list sends nothing, a request that never came back stops the run, and a CP refusal is one
// row's alone. The request body carries the profile fields and nothing else.
import { describe, it, expect, beforeEach, vi } from "vitest";

const api = vi.fn();
const rawJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  rawJSON: (...a: unknown[]) => rawJSON(...a),
}));

const { executeGcpPlan, loadGcpProfiles } = await import("./gcpImportExec.ts");

const create = (label: string) => ({
  label,
  fields: { project: "my-project-123", quotaProject: "", account: "me@example.com", region: "", zone: "", impersonateServiceAccount: "" },
});
const ok = (id = "x") => new Response(JSON.stringify({ id }), { status: 201 });

beforeEach(() => {
  api.mockReset();
  rawJSON.mockReset();
});

describe("loadGcpProfiles", () => {
  it("returns null for an error body or a rejected GET, the list otherwise", async () => {
    api.mockResolvedValue({ error: { code: "internal" } });
    expect(await loadGcpProfiles()).toBeNull();
    api.mockRejectedValue(new Error("net"));
    expect(await loadGcpProfiles()).toBeNull();
    api.mockResolvedValue([]);
    expect(await loadGcpProfiles()).toEqual([]);
  });
});

describe("executeGcpPlan", () => {
  it("sends exactly the profile fields", async () => {
    rawJSON.mockResolvedValue(ok());
    const rows = await executeGcpPlan([create("a")]);
    expect(rows).toEqual([{ label: "a", outcome: "created" }]);
    expect(rawJSON).toHaveBeenCalledWith("api/gcp/profiles", "POST", {
      loginMethod: "google",
      label: "a",
      project: "my-project-123",
      quotaProject: "",
      account: "me@example.com",
      region: "",
      zone: "",
      impersonateServiceAccount: "",
    });
  });

  it("goes on after a CP refusal, reporting its code only", async () => {
    rawJSON.mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: "bad_project", message: "SECRETSENTINEL" } }), { status: 400 })).mockResolvedValueOnce(ok());
    const rows = await executeGcpPlan([create("a"), create("b")]);
    expect(rows).toEqual([
      { label: "a", outcome: "failed", code: "bad_project" },
      { label: "b", outcome: "created" },
    ]);
    expect(JSON.stringify(rows)).not.toContain("SECRETSENTINEL");
  });

  it("stops at a request that never came back, and at a success without an id", async () => {
    rawJSON.mockRejectedValueOnce(new Error("net"));
    expect((await executeGcpPlan([create("a"), create("b")])).map((r) => r.outcome)).toEqual(["unknown", "not_run"]);
    expect(rawJSON).toHaveBeenCalledTimes(1);
    rawJSON.mockReset();
    rawJSON.mockResolvedValueOnce(new Response("{}", { status: 201 }));
    expect((await executeGcpPlan([create("a"), create("b")])).map((r) => r.outcome)).toEqual(["unknown", "not_run"]);
  });

  it("does not send once cancelled", async () => {
    rawJSON.mockResolvedValue(ok());
    let n = 0;
    const rows = await executeGcpPlan([create("a"), create("b")], { cancelled: () => n++ >= 1 });
    expect(rows.map((r) => r.outcome)).toEqual(["created", "not_run"]);
  });
});
