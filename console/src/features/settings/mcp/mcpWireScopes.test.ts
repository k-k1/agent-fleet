// Definition scopes on a tenant-distributed OAuth server (ADR 0146 decision 2). The control plane
// refuses a list the Agent would refuse, so the form must not offer to save one either; the cases
// are the Agent's own testdata, which the control plane's test reads too.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { bodyOfTenant, emptyTenantForm, tenantFormOf, tenantFormValid, tenantScopes, tenantScopesValid } from "./mcpWire.ts";

const base = { ...emptyTenantForm(), name: "gmail", url: "https://gmailmcp.googleapis.com/mcp/v1self", auth: "oauth" as const };
const GMAIL_RO = "https://www.googleapis.com/auth/gmail.readonly";

describe("tenant definition scopes", () => {
  it("sends the typed scopes as a list, separated by spaces, tabs or new lines", () => {
    const f = { ...base, oauthScopes: ` ${GMAIL_RO}\n\topenid  email\r\n` };
    expect(tenantScopes(f)).toEqual([GMAIL_RO, "openid", "email"]);
    expect(bodyOfTenant(f, "sales").scopes).toEqual([GMAIL_RO, "openid", "email"]);
    expect(tenantFormValid(f)).toBe(true);
  });

  it("sends no scopes when none are typed or the server is not OAuth", () => {
    expect("scopes" in bodyOfTenant({ ...base, oauthScopes: " \n " }, "sales")).toBe(false);
    for (const auth of ["none", "bearer", "headers"] as const) {
      const f = { ...base, auth, token: "t", headers: [{ k: "X-Key", v: "v" }], oauthScopes: GMAIL_RO };
      expect("scopes" in bodyOfTenant(f, "sales")).toBe(false);
      expect(tenantScopesValid({ ...f, oauthScopes: "a,b" })).toBe(true);
    }
  });

  it("reads stored scopes back one per line, and none as empty", () => {
    const row = {
      id: "t1", name: "gmail", transport: "http" as const, url: base.url, enabled: true,
      targets: { assistant: true, session: true }, auth: "oauth" as const,
    };
    const f = tenantFormOf({ ...row, scopes: [GMAIL_RO, "openid"] });
    expect(f.oauthScopes).toBe(`${GMAIL_RO}\nopenid`);
    expect(bodyOfTenant(f, "sales").scopes).toEqual([GMAIL_RO, "openid"]);
    expect(tenantFormOf(row).oauthScopes).toBe("");
  });

  it("splits at every ASCII whitespace, form feed and vertical tab included", () => {
    const f = { ...base, oauthScopes: "read\fwrite\vopenid" };
    expect(tenantScopes(f)).toEqual(["read", "write", "openid"]);
    expect(tenantFormValid(f)).toBe(true);
  });

  it("keeps a non-ASCII space inside a token, where it is refused", () => {
    const f = { ...base, oauthScopes: "a\u00a0b" };
    expect(tenantScopes(f)).toEqual(["a\u00a0b"]);
    expect(tenantFormValid(f)).toBe(false);
  });

  it("agrees with the Agent's validDefScopes cases", () => {
    const path = fileURLToPath(new URL("../../../../../workspace/agent/internal/mcpreg/testdata/def-scopes-cases.json", import.meta.url));
    const { cases } = JSON.parse(readFileSync(path, "utf8")) as {
      cases: { name: string; scopes: string[]; tokenOf?: number; valid: boolean }[];
    };
    let checked = 0;
    for (const c of cases) {
      const list = [...c.scopes, ...(c.tokenOf ? ["a".repeat(c.tokenOf)] : [])];
      // The form splits at ASCII whitespace, so a case about whitespace inside one scope is a
      // different list here; the control plane is what refuses that shape.
      if (list.some((s) => s === "" || /[\x09-\x0d\x20]/.test(s))) continue;
      checked++;
      expect([c.name, tenantScopesValid({ ...base, oauthScopes: list.join("\n") })]).toEqual([c.name, c.valid]);
    }
    expect(checked).toBeGreaterThan(10);
  });
});
