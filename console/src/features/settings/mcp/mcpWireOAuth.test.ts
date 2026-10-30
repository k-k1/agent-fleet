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
  tenantOAuthSecretReentry,
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

describe("the tenant's pre-registered client", () => {
  const base = {
    ...emptyTenantForm(), name: "t", url: "https://m.example.com/mcp", auth: "oauth" as const,
    oauthIssuer: "https://as.example.com", oauthClientId: "af-corp",
  };
  it("sends a public client with no secret, and none at all without a client id", () => {
    expect(bodyOfTenant(base, "sales").oauth_client).toEqual({ issuer: "https://as.example.com", client_id: "af-corp", client_secret: "" });
    expect("oauth_client" in bodyOfTenant({ ...base, oauthClientId: " " }, "sales")).toBe(false);
    expect("oauth_client" in bodyOfTenant({ ...base, auth: "none" }, "sales")).toBe(false);
  });
  it("sends a typed secret, keeps a stored one masked, and drops it on request", () => {
    expect(bodyOfTenant({ ...base, oauthSecret: "s3" }, "sales").oauth_client).toMatchObject({ client_secret: "s3" });
    expect(bodyOfTenant({ ...base, oauthSecretStored: true }, "sales").oauth_client).toMatchObject({ client_secret: "***" });
    expect(bodyOfTenant({ ...base, oauthSecretStored: true, oauthSecretDrop: true }, "sales").oauth_client).toMatchObject({ client_secret: "" });
  });
  it("reads a stored client back without its secret", () => {
    const f = tenantFormOf({
      id: "t1", name: "t", transport: "http", url: "https://m.example.com/mcp", enabled: true,
      targets: { assistant: true, session: true }, auth: "oauth",
      oauth_client: { issuer: "https://as.example.com", client_id: "af-corp", client_secret: "***" },
    });
    expect(f).toMatchObject({ oauthIssuer: "https://as.example.com", oauthClientId: "af-corp", oauthSecret: "", oauthSecretStored: true });
  });
  it("asks for the secret again when the issuer or client id changes under a stored one", () => {
    const loaded = { ...base, oauthSecretStored: true, oauthOrigIssuer: base.oauthIssuer, oauthOrigClientId: base.oauthClientId };
    expect(tenantOAuthSecretReentry(loaded)).toBe(false);
    expect(tenantFormValid(loaded)).toBe(true);
    for (const changed of [{ oauthIssuer: "https://other-as.example.com" }, { oauthClientId: "other" }]) {
      const f = { ...loaded, ...changed };
      expect(tenantOAuthSecretReentry(f)).toBe(true);
      expect(tenantFormValid(f)).toBe(false);
      expect(tenantFormValid({ ...f, oauthSecret: "new" })).toBe(true);
      expect(tenantFormValid({ ...f, oauthSecretDrop: true })).toBe(true);
    }
  });
  it("refuses an issuer with credentials, a query or a fragment", () => {
    for (const issuer of ["https://user@as.example.com", "https://as.example.com/?a=b", "https://as.example.com/#x"]) {
      expect(tenantFormValid({ ...base, oauthIssuer: issuer })).toBe(false);
    }
  });
  it("needs an https issuer once a client id is given", () => {
    expect(tenantFormValid(base)).toBe(true);
    expect(tenantFormValid({ ...base, oauthIssuer: "" })).toBe(false);
    expect(tenantFormValid({ ...base, oauthIssuer: "http://as.example.com" })).toBe(false);
    expect(tenantFormValid({ ...base, oauthIssuer: "", oauthClientId: "" })).toBe(true);
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
