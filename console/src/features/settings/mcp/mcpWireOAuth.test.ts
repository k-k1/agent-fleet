// OAuth as the fourth Authentication mode (ADR 0119): it sends no header and marks the
// definition, for a member's own server and a tenant-distributed one alike.
import { describe, expect, it } from "vitest";
import {
  AUTH_MODES,
  authHeaders,
  bodyOf,
  bodyOfTenant,
  emptyForm,
  emptyTenantForm,
  formOf,
  probeOfAnswer,
  switchAuth,
  tenantFormOf,
  tenantFormValid,
} from "./mcpWire.ts";
import type { McpServer } from "./mcpWire.ts";

const server = (over: Partial<McpServer> = {}): McpServer => ({
  id: "mcp-1", name: "hosted", origin: "user", transport: "http", url: "https://m.example.com/mcp",
  enabled: true, targets: { assistant: true, session: true }, editable: true, ready: true, ...over,
});

describe("OAuth authentication mode", () => {
  it("is offered", () => {
    expect([...AUTH_MODES]).toEqual(["none", "bearer", "headers", "oauth"]);
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

  it("puts auth on a tenant body only in OAuth mode", () => {
    const f = { ...emptyTenantForm(), name: "t", url: "https://m.example.com/mcp" };
    for (const auth of ["none", "bearer", "headers"] as const) {
      expect("auth" in bodyOfTenant({ ...f, auth }, "sales")).toBe(false);
    }
    expect(bodyOfTenant({ ...f, auth: "oauth" }, "sales").auth).toBe("oauth");
  });

  // The CP refuses OAuth with user_secret or with a header, so a tenant form that still holds
  // either from an earlier mode must send neither.
  it("sends a tenant OAuth body with no header and no user_secret", () => {
    const f = {
      ...emptyTenantForm(),
      name: "t",
      url: "https://m.example.com/mcp",
      auth: "oauth" as const,
      userSecret: true,
      headers: [{ k: "Authorization", v: "" }],
    };
    const body = bodyOfTenant(f, "sales");
    expect(body.headers).toEqual({});
    expect(body.user_secret).toBe(false);
    expect(tenantFormValid(f)).toBe(true);
  });

  it("opens a distributed OAuth server in OAuth mode", () => {
    const row = {
      id: "t1", name: "hosted", transport: "http" as const, url: "https://m.example.com/mcp",
      targets: { assistant: true, session: true }, enabled: true,
    };
    expect(tenantFormOf({ ...row, auth: "oauth" }).auth).toBe("oauth");
    expect(tenantFormOf(row).auth).toBe("none");
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
