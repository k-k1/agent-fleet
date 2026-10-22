// OAuth as the fourth Authentication mode (ADR 0119): it sends no header, marks the
// definition, and stays out of the tenant form.
import { describe, expect, it } from "vitest";
import {
  AUTH_MODES,
  TENANT_AUTH_MODES,
  authHeaders,
  bodyOf,
  bodyOfTenant,
  emptyForm,
  emptyTenantForm,
  formOf,
  probeOfAnswer,
  switchAuth,
} from "./mcpWire.ts";
import type { McpServer } from "./mcpWire.ts";

const server = (over: Partial<McpServer> = {}): McpServer => ({
  id: "mcp-1", name: "hosted", origin: "user", transport: "http", url: "https://m.example.com/mcp",
  enabled: true, targets: { assistant: true, session: true }, editable: true, ready: true, ...over,
});

describe("OAuth authentication mode", () => {
  it("is offered to members and not to tenant distribution", () => {
    expect(AUTH_MODES).toContain("oauth");
    expect(TENANT_AUTH_MODES).not.toContain("oauth");
    expect([...TENANT_AUTH_MODES]).toEqual(["none", "bearer", "headers"]);
  });

  it("sends no headers, even when the rows hold an Authorization", () => {
    expect(authHeaders("oauth", "tok", [{ k: "Authorization", v: "Bearer x" }])).toEqual({});
  });

  it("marks the definition auth=oauth and no other mode does", () => {
    const f = { ...emptyForm(), name: "hosted", url: "https://m.example.com/mcp", auth: "oauth" as const };
    const body = bodyOf({ ...f, headers: [{ k: "Authorization", v: "Bearer x" }] });
    expect(body.auth).toBe("oauth");
    expect(body.headers).toEqual({});
    for (const auth of ["none", "bearer", "headers"] as const) {
      expect("auth" in bodyOf({ ...f, auth })).toBe(false);
    }
  });

  it("opens a saved OAuth server in OAuth mode, and others as before", () => {
    expect(formOf(server({ auth: "oauth" })).auth).toBe("oauth");
    expect(formOf(server({ headers: { Authorization: "Bearer ***" } })).auth).toBe("bearer");
    expect(formOf(server()).auth).toBe("none");
  });

  it("keeps a typed token across a round trip through OAuth", () => {
    let f = { ...emptyForm(), auth: "bearer" as const, token: "abc" };
    f = switchAuth(f, "oauth");
    expect(f.auth).toBe("oauth");
    f = switchAuth(f, "bearer");
    expect(authHeaders(f.auth, f.token, f.headers)).toEqual({ Authorization: "Bearer abc" });
  });

  it("never puts auth on a tenant body", () => {
    const f = { ...emptyTenantForm(), name: "t", url: "https://m.example.com/mcp" };
    expect("auth" in bodyOfTenant(f, "sales")).toBe(false);
  });
});

describe("probeOfAnswer", () => {
  const msg = (e: unknown) => "E:" + (e as { code: string }).code;
  it("keeps the OAuth report that rides with a probe's own error string", () => {
    const res = { ok: false, toolCount: 0, elapsedMs: 3, error: "OAuth required", oauth: { required: true } };
    expect(probeOfAnswer(res, msg)).toBe(res);
  });
  it("turns a refused request (an error envelope) into a failed probe", () => {
    expect(probeOfAnswer({ error: { code: "mcp_invalid" } }, msg)).toEqual({ ok: false, toolCount: 0, elapsedMs: 0, error: "E:mcp_invalid" });
  });
  it("passes a successful probe through", () => {
    const res = { ok: true, toolCount: 2, elapsedMs: 9 };
    expect(probeOfAnswer(res, msg)).toBe(res);
  });
});
