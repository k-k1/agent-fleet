import { beforeEach, describe, expect, it, vi } from "vitest";

// Runs the wrappers against a mocked client: the URL, method, body and argument count each one
// sends (a DELETE must not grow a body argument), and what a null body does.
const { api, apiJSON } = vi.hoisted(() => ({ api: vi.fn(), apiJSON: vi.fn() }));
vi.mock("../../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => api(...a),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));

import * as A from "./tenantApi.ts";

beforeEach(() => {
  api.mockReset().mockResolvedValue({ ok: 1 });
  apiJSON.mockReset().mockResolvedValue({ ok: 1 });
});

const TT = "api/admin/tenants/a%2Fb";
const M = TT + "/members/k%2F1";
const writes: [string, () => Promise<unknown>, unknown[]][] = [
  ["putTenantWatchSources", () => A.putTenantWatchSources("a/b", true), [TT + "/watch-sources", "PUT", { enabled: true }]],
  ["putTenantMemoQueues", () => A.putTenantMemoQueues("a/b", false), [TT + "/memo-queues", "PUT", { enabled: false }]],
  ["putTenantExternalMemory", () => A.putTenantExternalMemory("a/b", true), [TT + "/external-memory", "PUT", { enabled: true }]],
  ["putTenantMcpOAuth", () => A.putTenantMcpOAuth("a/b", true), [TT + "/mcp-oauth", "PUT", { enabled: true }]],
  ["putTenantBranchRules", () => A.putTenantBranchRules("a/b", [{ x: 1 }]), [TT + "/branch-rules", "PUT", { rules: [{ x: 1 }] }]],
  ["putTenantEngineAccess role", () => A.putTenantEngineAccess("a/b", "", { on: 1 }), [TT + "/engine-access", "PUT", { on: 1 }]],
  ["putTenantEngineAccess members", () => A.putTenantEngineAccess("a/b", "/members", { on: 1 }), [TT + "/engine-access/members", "PUT", { on: 1 }]],
  ["putTenantNetwork", () => A.putTenantNetwork("a/b", "10.0.0.0/8"), [TT + "/network", "PUT", { allowed_cidrs: "10.0.0.0/8" }]],
  ["putTenantMachine", () => A.putTenantMachine("a/b", "g"), [TT + "/slot-class", "PUT", { slot_class: "g" }]],
  ["putTenantBoost", () => A.putTenantBoost("a/b", { enabled: true }), [TT + "/boost", "PUT", { enabled: true }]],
  ["putTenantMemberDefaults", () => A.putTenantMemberDefaults("a/b", { d: 1 }), [TT + "/member-defaults", "PUT", { d: 1 }]],
  ["putGitOAuthApp", () => A.putGitOAuthApp("a/b", "git/hub", { c: 1 }), [TT + "/git-oauth/git%2Fhub", "PUT", { c: 1 }]],
  ["putTenantLimits", () => A.putTenantLimits("a/b", { m: 1 }), [TT + "/limits", "PUT", { m: 1 }]],
  ["putTenantLogin", () => A.putTenantLogin("a/b", { m: 1 }), [TT + "/login", "PUT", { m: 1 }]],
  ["deleteTenant", () => A.deleteTenant("a/b"), [TT, "DELETE", {}]],
  ["saveTenantIdp create", () => A.saveTenantIdp("a/b", undefined, { n: 1 }), [TT + "/idp", "POST", { n: 1 }]],
  ["saveTenantIdp update", () => A.saveTenantIdp("a/b", "i/1", { n: 1 }), [TT + "/idp/i%2F1", "PUT", { n: 1 }]],
  ["deleteTenantIdp", () => A.deleteTenantIdp("a/b", "i/1"), [TT + "/idp/i%2F1", "DELETE"]],
  ["setTenantIdpStatus", () => A.setTenantIdpStatus("a/b", "i/1", "active"), [TT + "/idp/i%2F1/status", "POST", { status: "active" }]],
  ["setTenantIdpStatus confirm", () => A.setTenantIdpStatus("a/b", "i/1", "suspended", true), [TT + "/idp/i%2F1/status?confirm=1", "POST", { status: "suspended" }]],
  ["putProvidedGrant", () => A.putProvidedGrant("p", { g: 1 }), ["p/grant", "PUT", { g: 1 }]],
  ["putProvidedPolicy", () => A.putProvidedPolicy("p", { g: 1 }), ["p/policy", "PUT", { g: 1 }]],
  ["putProvidedMembers", () => A.putProvidedMembers("p", { g: 1 }), ["p/members", "PUT", { g: 1 }]],
  ["putRoutePolicyRule", () => A.putRoutePolicyRule("p", { r: 1 } as never, 7), ["p", "PUT", { r: 1 }, { "If-Match": '"7"' }]],
  ["deleteMemberHomeBackups", () => A.deleteMemberHomeBackups("a/b", "k/1"), [M + "/home-backups", "DELETE", {}]],
  ["deleteMemberRow", () => A.deleteMemberRow("a/b", "k/1"), [M, "DELETE", {}]],
  ["stopMemberWorkspace", () => A.stopMemberWorkspace("a/b", "k"), ["api/admin/stop-workspace", "POST", { tenant_slug: "a/b", user_key: "k" }]],
  ["rotateMemberGitToken", () => A.rotateMemberGitToken("a/b", "k"), ["api/admin/rotate-git-token", "POST", { tenant_slug: "a/b", user_key: "k" }]],
  ["cleanMemberHome", () => A.cleanMemberHome("a/b", "k"), ["api/admin/clean-home", "POST", { tenant_slug: "a/b", user_key: "k" }]],
  ["destroyMemberWorkspace", () => A.destroyMemberWorkspace("a/b", "k"), ["api/admin/workspaces", "DELETE", { tenant_slug: "a/b", user_key: "k" }]],
  ["removeMembership", () => A.removeMembership("a/b", "k", true), ["api/admin/memberships", "DELETE", { tenant_slug: "a/b", user_key: "k", purge: true }]],
  ["putUserLimits", () => A.putUserLimits({ u: 1 }), ["api/admin/user-limits", "PUT", { u: 1 }]],
  ["putMembershipRole", () => A.putMembershipRole("a/b", "k", "admin"), ["api/admin/membership-role", "PUT", { user_key: "k", tenant_slug: "a/b", role: "admin" }]],
  ["returnPoolSlot", () => A.returnPoolSlot("i/1", "t"), ["api/admin/ec2-pool/slots/i%2F1/return", "POST", { token: "t" }]],
  ["forcePoolSlotStop", () => A.forcePoolSlotStop("i/1", false), ["api/admin/ec2-pool/slots/i%2F1/force-stop", "POST", {}]],
  ["forcePoolSlotStop override", () => A.forcePoolSlotStop("i/1", true), ["api/admin/ec2-pool/slots/i%2F1/force-stop", "POST", { override_healthy: true }]],
  ["reserveOutdatedSlots", () => A.reserveOutdatedSlots(["a", "b"]), ["api/admin/ec2-pool/reserve-outdated", "POST", { instance_ids: ["a", "b"] }]],
];

describe("tenant wrappers send what the components used to", () => {
  it.each(writes)("%s", async (_n, call, want) => {
    await call();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(apiJSON.mock.calls[0]).toEqual(want);
    expect(apiJSON.mock.calls[0]).toHaveLength(want.length);
  });

  it("reads and the pool's api()-based writes hit the literal paths", async () => {
    const ctl = new AbortController();
    await A.getTenantMembers("a/b");
    await A.getTenantEngineAccess("a/b");
    await A.getTenantMachine("a/b");
    await A.getTenantBoost("a/b");
    await A.getTenantNetwork("a/b");
    await A.getTenantBranchRules("a/b");
    await A.getTenantMcpOAuth("a/b");
    await A.getTenantMemberDefaults("a/b");
    await A.getTenantGitOAuth("a/b");
    await A.getTenantWatchSources("a/b");
    await A.getTenantMemoQueues("a/b");
    await A.getTenantExternalMemory("a/b");
    await A.getTenantRoutePolicy("a/b");
    await A.getProvided("p");
    await A.getTenantIdps("a/b");
    await A.getAllIdps();
    await A.getDeploymentProviders();
    await A.getMemberLiveStats("a/b", "k/1", ctl.signal);
    await A.getMemberStats("a/b", "k/1");
    await A.getMemberSessions("a/b", "k/1");
    await A.getMemberHomeBackups("a/b", "k/1");
    await A.getEc2PoolStatus();
    await A.terminatePoolSlot("i/1");
    await A.setPoolSlotReserved("i/1", true);
    await A.setPoolSlotReserved("i/1", false);
    expect(api.mock.calls).toEqual([
      [TT + "/members"],
      [TT + "/engine-access"],
      [TT + "/slot-class"],
      [TT + "/boost"],
      [TT + "/network"],
      [TT + "/branch-rules"],
      [TT + "/mcp-oauth"],
      [TT + "/member-defaults"],
      [TT + "/git-oauth"],
      [TT + "/watch-sources"],
      [TT + "/memo-queues"],
      [TT + "/external-memory"],
      [TT + "/route-policy"],
      ["p"],
      [TT + "/idp"],
      ["api/admin/idp"],
      ["api/admin/providers"],
      [M + "/stats?disk=0", { signal: ctl.signal }],
      [M + "/stats"],
      [M + "/sessions"],
      [M + "/home-backups"],
      ["api/admin/ec2-pool"],
      ["api/admin/ec2-pool/slots/i%2F1", { method: "DELETE" }],
      ["api/admin/ec2-pool/slots/i%2F1/replace", { method: "PUT" }],
      ["api/admin/ec2-pool/slots/i%2F1/replace", { method: "DELETE" }],
    ]);
  });

  it("keeps a null body as null where the caller guarded it, and rejects where it dereferenced", async () => {
    api.mockResolvedValue(null);
    apiJSON.mockResolvedValue(null);
    expect(await A.getTenantNetwork("a/b")).toBeNull();
    expect(await A.putTenantLimits("a/b", {})).toBeNull();
    expect(await A.getTenantBranchRules("a/b")).toBeNull();
    await expect(A.getTenantMembers("a/b")).rejects.toThrow();
    // The ones whose caller stores the reply before reading it stay nullable; assertBody throws.
    expect(await A.getTenantMcpOAuth("a/b")).toBeNull();
    expect(await A.putTenantMcpOAuth("a/b", true)).toBeNull();
    expect(await A.putTenantBranchRules("a/b", [])).toBeNull();
    expect(await A.putTenantMemberDefaults("a/b", {})).toBeNull();
    expect(() => A.assertBody(null)).toThrow();
    expect(() => A.assertBody({})).not.toThrow();
  });

  it("hands an error body through unchanged", async () => {
    apiJSON.mockResolvedValue({ error: { code: "x" } });
    expect(await A.putTenantNetwork("a/b", "")).toEqual({ error: { code: "x" } });
  });
});
