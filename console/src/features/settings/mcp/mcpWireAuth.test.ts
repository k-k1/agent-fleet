import { describe, expect, it } from "vitest";
import {
  MASKED,
  NAME_RE,
  advancedIsDefault,
  authOf,
  bodyOf,
  bodyOfTenant,
  deriveId,
  emptyForm,
  emptyTenantForm,
  formOf,
  formValid,
  isInsecureRemote,
  memberTokenOnly,
  memberTokenValue,
  stripBearer,
  switchAuth,
  tenantFormOf,
  tenantFormValid,
  withDerivedId,
} from "./mcpWire.ts";
import type { McpServer, TenantServer } from "./mcpWire.ts";

// ADR 0119 decision 8 will add an OAuth mode to the Authentication selector; these tests pin
// the three modes it sits beside, and the scheme-preserving mask round trip (`Bearer ***`)
// that lets an edit tell a Bearer token from any other Authorization value.

const remote = (headers?: Record<string, string>): McpServer => ({
  id: "u9",
  name: "wiki",
  label: "Wiki",
  origin: "user",
  transport: "http",
  url: "https://mcp.example.com/mcp",
  headers,
  enabled: true,
  targets: { assistant: true, session: true },
  editable: true,
  ready: true,
});

describe("defaults", () => {
  it("a new form is remote with no authentication", () => {
    const f = emptyForm();
    expect(f.transport).toBe("http");
    expect(f.auth).toBe("none");
    expect(advancedIsDefault(f)).toBe(true);
  });
});

describe("ID derivation", () => {
  it("derives from an ASCII display name", () => {
    expect(deriveId("Internal Wiki", "")).toBe("internal-wiki");
    expect(NAME_RE.test(deriveId("  My  Server!! ", ""))).toBe(true);
  });
  it("falls back to the URL host when the display name has no usable characters", () => {
    const id = deriveId("社内ウィキ", "https://mcp.example.com/mcp");
    expect(id).toBe("mcp-example-com");
    expect(NAME_RE.test(id)).toBe(true);
    expect(deriveId("", "https://mcp.example.com:8443/x")).toBe("mcp-example-com");
    expect(deriveId("", "https://")).toBe("");
  });
  it("is cut to the 48-character limit without a trailing dash", () => {
    const id = deriveId("a".repeat(47) + " b", "");
    expect(id.length).toBeLessThanOrEqual(48);
    expect(NAME_RE.test(id)).toBe(true);
  });
  it("follows the label and URL until the user edits the ID", () => {
    let f = withDerivedId({ ...emptyForm(), label: "Wiki" });
    expect(f.name).toBe("wiki");
    f = withDerivedId({ ...f, label: "Wiki 2" });
    expect(f.name).toBe("wiki-2");
    f = withDerivedId({ ...f, name: "mine", nameEdited: true, label: "Other" });
    expect(f.name).toBe("mine");
  });
  it("never regenerates the ID of an existing server", () => {
    const f = withDerivedId({ ...formOf(remote()), label: "Renamed", nameEdited: false });
    expect(f.name).toBe("wiki");
    const t = withDerivedId({ ...tenantFormOf(tenantRow()), label: "Renamed", nameEdited: false });
    expect(t.name).toBe("wiki");
  });
});

describe("insecure remote warning", () => {
  it.each(["http://mcp.example.com/mcp", "HTTP://10.0.0.5/mcp"])("warns on %s", (u) => {
    expect(isInsecureRemote(u)).toBe(true);
  });
  it.each([
    "https://mcp.example.com/mcp",
    "http://localhost:8080/mcp",
    "http://127.0.0.1:3000/mcp",
    "http://[::1]:3000/mcp",
    "http://dev.localhost/mcp",
    "",
  ])("does not warn on %s", (u) => {
    expect(isInsecureRemote(u)).toBe(false);
  });
});

describe("authOf: which editor a stored header set opens in", () => {
  it("no headers is None", () => {
    expect(formOf(remote()).auth).toBe("none");
  });
  it("`Bearer ***` is Bearer, in any scheme case", () => {
    expect(formOf(remote({ Authorization: "Bearer ***" })).auth).toBe("bearer");
    expect(formOf(remote({ Authorization: "bearer ***" })).auth).toBe("bearer");
    expect(formOf(remote({ authorization: "BEARER ***" })).auth).toBe("bearer");
  });
  it("a non-Bearer Authorization (masked `***`) stays Custom headers", () => {
    expect(formOf(remote({ Authorization: MASKED })).auth).toBe("headers");
  });
  it("Bearer plus another header stays Custom headers, so nothing is lost", () => {
    expect(formOf(remote({ Authorization: "Bearer ***", "X-Team": MASKED })).auth).toBe("headers");
  });
  it("an unset Authorization is Bearer only for a user_secret row", () => {
    expect(authOf([{ k: "Authorization", v: "" }])).toBe("headers");
    expect(authOf([{ k: "Authorization", v: "" }], true)).toBe("bearer");
  });
});

describe("Bearer round trip (member form)", () => {
  it("a new Bearer server stores Authorization: Bearer <token>", () => {
    const f = { ...emptyForm(), name: "x", url: "https://e.com/mcp", auth: "bearer" as const, token: "abc123" };
    expect(formValid(f)).toBe(true);
    expect(bodyOf(f).headers).toEqual({ Authorization: "Bearer abc123" });
  });
  it("strips a pasted Bearer prefix", () => {
    expect(stripBearer("  Bearer   abc ")).toBe("abc");
    expect(stripBearer("bearer abc")).toBe("abc");
    const f = { ...emptyForm(), name: "x", url: "https://e.com/mcp", auth: "bearer" as const, token: "Bearer abc" };
    expect(bodyOf(f).headers).toEqual({ Authorization: "Bearer abc" });
  });
  it("a new Bearer server needs a token", () => {
    expect(formValid({ ...emptyForm(), name: "x", url: "https://e.com/mcp", auth: "bearer" })).toBe(false);
  });
  it("an untouched token sends `Bearer ***` back, keeping the stored value", () => {
    const f = formOf(remote({ Authorization: "Bearer ***" }));
    expect(formValid(f)).toBe(true);
    expect(bodyOf(f).headers).toEqual({ Authorization: "Bearer ***" });
  });
  it("keeps the stored key spelling and the scheme case as stored", () => {
    expect(bodyOf(formOf(remote({ authorization: "bearer ***" }))).headers).toEqual({ authorization: "bearer ***" });
  });
  it("a typed token replaces the stored one", () => {
    const f = { ...formOf(remote({ Authorization: "Bearer ***" })), token: "fresh" };
    expect(bodyOf(f).headers).toEqual({ Authorization: "Bearer fresh" });
  });
  it("switching to None removes the Authorization header", () => {
    const f = { ...formOf(remote({ Authorization: "Bearer ***" })), auth: "none" as const };
    expect(bodyOf(f).headers).toEqual({});
  });
  it("switching to Bearer from a non-Bearer Authorization does not send the old mask", () => {
    const f = { ...formOf(remote({ Authorization: MASKED })), auth: "bearer" as const };
    expect(formValid(f)).toBe(false);
    expect(bodyOf(f).headers).toEqual({});
  });
  it("Custom headers sends the rows as typed", () => {
    const f = formOf(remote({ Authorization: MASKED, "X-Team": MASKED }));
    expect(bodyOf(f).headers).toEqual({ Authorization: MASKED, "X-Team": MASKED });
  });
  it("stdio never carries headers whatever the auth mode was", () => {
    const f = { ...formOf(remote({ Authorization: "Bearer ***" })), transport: "stdio" as const, command: "npx" };
    expect(bodyOf(f).headers).toBeUndefined();
  });
});

const tenantRow = (headers?: Record<string, string>, user_secret = false): TenantServer => ({
  id: "t1",
  name: "wiki",
  transport: "http",
  url: "https://mcp.example.com/mcp",
  headers,
  targets: { assistant: true, session: true },
  enabled: true,
  user_secret,
});

describe("tenant form", () => {
  it("round-trips a distributed Bearer token", () => {
    const f = tenantFormOf(tenantRow({ Authorization: "Bearer ***" }));
    expect(f.auth).toBe("bearer");
    expect(bodyOfTenant(f, "acme").headers).toEqual({ Authorization: "Bearer ***" });
    expect(bodyOfTenant({ ...f, token: "new" }, "acme").headers).toEqual({ Authorization: "Bearer new" });
    expect(bodyOfTenant({ ...f, auth: "none" }, "acme").headers).toEqual({});
  });
  it("a non-Bearer Authorization opens as Custom headers", () => {
    expect(tenantFormOf(tenantRow({ Authorization: MASKED })).auth).toBe("headers");
  });
  it("user_secret distributes the Authorization NAME only, with no token required", () => {
    const f = { ...emptyTenantForm(), name: "x", url: "https://e.com/mcp", auth: "bearer" as const, userSecret: true };
    expect(tenantFormValid(f)).toBe(true);
    expect(bodyOfTenant(f, "acme").headers).toEqual({ Authorization: "" });
    expect(tenantFormOf(tenantRow({ Authorization: "" }, true)).auth).toBe("bearer");
  });
  it("without user_secret a new Bearer distribution needs a token", () => {
    const f = { ...emptyTenantForm(), name: "x", url: "https://e.com/mcp", auth: "bearer" as const };
    expect(tenantFormValid(f)).toBe(false);
    expect(tenantFormValid({ ...f, token: "t" })).toBe(true);
  });
});

describe("member value form (user_secret rows)", () => {
  it("offers a token-only field for a Bearer Authorization, raw otherwise", () => {
    expect(memberTokenOnly("Authorization", "Bearer ***")).toBe(true);
    expect(memberTokenOnly("Authorization", "")).toBe(true);
    expect(memberTokenOnly("Authorization", MASKED)).toBe(false); // stored `Token abc`
    expect(memberTokenOnly("X-Api-Key", "")).toBe(false);
  });
  it("adds the scheme to a bare token, strips a pasted one, keeps an explicit other scheme", () => {
    expect(memberTokenValue("abc", "")).toBe("Bearer abc");
    expect(memberTokenValue("Bearer abc", "")).toBe("Bearer abc");
    expect(memberTokenValue("Token abc", "")).toBe("Token abc");
  });
  it("untouched keeps what was stored", () => {
    expect(memberTokenValue("", "Bearer ***")).toBe("Bearer ***");
    expect(memberTokenValue("  ", "")).toBe("");
  });
});

describe("switchAuth carries the entered credential across modes", () => {
  it("new Bearer token -> Custom headers keeps Authorization: Bearer <token>", () => {
    const f = switchAuth({ ...emptyForm(), url: "https://e.com/mcp", name: "x", auth: "bearer" as const, token: "fresh" }, "headers");
    expect(f.headers).toEqual([{ k: "Authorization", v: "Bearer fresh" }]);
    expect(bodyOf({ ...f, headers: [...f.headers, { k: "X-Team", v: "ops" }] }).headers).toEqual({
      Authorization: "Bearer fresh",
      "X-Team": "ops",
    });
  });
  it("replacing a stored token, then switching, sends the new token, not the old mask", () => {
    const base = { ...formOf(remote({ Authorization: "Bearer ***" })), token: "fresh" };
    expect(bodyOf(switchAuth(base, "headers")).headers).toEqual({ Authorization: "Bearer fresh" });
  });
  it("an untouched stored token stays a mask across the switch", () => {
    const base = formOf(remote({ Authorization: "Bearer ***" }));
    expect(bodyOf(switchAuth(base, "headers")).headers).toEqual({ Authorization: "Bearer ***" });
  });
  it("Custom headers -> Bearer takes a typed `Bearer <token>` row as the token", () => {
    const f = switchAuth(
      { ...emptyForm(), auth: "headers" as const, headers: [{ k: "Authorization", v: "Bearer typed" }] },
      "bearer",
    );
    expect(f.token).toBe("typed");
    expect(bodyOf({ ...f, name: "x", url: "https://e.com" }).headers).toEqual({ Authorization: "Bearer typed" });
  });
  it("None keeps nothing on the wire but the rows survive a switch back", () => {
    const f = switchAuth({ ...emptyForm(), auth: "bearer" as const, token: "t" }, "none");
    expect(bodyOf({ ...f, name: "x", url: "https://e.com" }).headers).toEqual({});
    expect(switchAuth(f, "bearer").token).toBe("t");
  });
  it("Bearer -> None -> Custom headers keeps a new token", () => {
    const f = switchAuth(switchAuth({ ...emptyForm(), auth: "bearer" as const, token: "fresh" }, "none"), "headers");
    expect(bodyOf({ ...f, name: "x", url: "https://e.com" }).headers).toEqual({ Authorization: "Bearer fresh" });
  });
  it("replacing a stored token -> None -> Custom headers does not bring the old mask back", () => {
    const base = { ...formOf(remote({ Authorization: "Bearer ***" })), token: "fresh" };
    expect(bodyOf(switchAuth(switchAuth(base, "none"), "headers")).headers).toEqual({ Authorization: "Bearer fresh" });
    expect(bodyOf(switchAuth(switchAuth(base, "none"), "bearer")).headers).toEqual({ Authorization: "Bearer fresh" });
  });
  it("Custom headers -> None -> Bearer takes a typed Bearer row as the token", () => {
    const f = switchAuth(
      switchAuth({ ...emptyForm(), auth: "headers" as const, headers: [{ k: "Authorization", v: "Bearer typed" }] }, "none"),
      "bearer",
    );
    expect(f.token).toBe("typed");
  });
  it("user_secret Bearer -> Custom keeps the Authorization name with no value", () => {
    const f = switchAuth({ ...emptyTenantForm(), auth: "bearer" as const, userSecret: true }, "headers", true);
    expect(f.headers).toEqual([{ k: "Authorization", v: "" }]);
  });
});

describe("member value form: clearing", () => {
  it("a blank keeps; clearing is a separate action handled by the form", () => {
    expect(memberTokenValue("", "Bearer ***")).toBe("Bearer ***");
  });
});
