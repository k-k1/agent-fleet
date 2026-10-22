// What comes back in the address bar after the authorization server is untrusted text: it is
// read against closed sets and never shown as received.
import { describe, expect, it } from "vitest";
import {
  RETURN_REASONS,
  normalizeStatus,
  parseOAuthReturn,
  safeAuthorizeUrl,
  stripOAuthReturn,
} from "./mcpOAuth.ts";
import { tools as en } from "../../../lib/i18n/locales/en/tools.ts";

describe("parseOAuthReturn", () => {
  it("is null for any other URL", () => {
    expect(parseOAuthReturn("")).toBeNull();
    expect(parseOAuthReturn("?mcp_oauth=maybe")).toBeNull();
    expect(parseOAuthReturn("?server=mcp-1")).toBeNull();
  });

  it("reads a success and a failure", () => {
    expect(parseOAuthReturn("?mcp_oauth=ok&server=mcp-1&registration=cimd")).toEqual({
      ok: true, server: "mcp-1", reason: undefined, registration: "cimd",
    });
    expect(parseOAuthReturn("?mcp_oauth=failed&server=mcp-1&reason=iss_mismatch&registration=dcr")).toEqual({
      ok: false, server: "mcp-1", reason: "iss_mismatch", registration: "dcr",
    });
  });

  it("reads anything outside the closed sets as nothing", () => {
    const r = parseOAuthReturn(
      "?mcp_oauth=failed&server=" + encodeURIComponent("<img src=x onerror=1>") +
        "&reason=" + encodeURIComponent("https://evil.example.com") + "&registration=%2F%2Fevil.example.com",
    );
    expect(r).toEqual({ ok: false, server: "", reason: "failed", registration: undefined });
  });

  it("has a text for every reason, in the file the toast reads", () => {
    const src = JSON.stringify(en);
    for (const r of RETURN_REASONS) {
      if (r === "failed") continue; // the generic text is mcp.oauth_return_failed_generic
      expect(src).toContain('"mcp.oauth_return_' + r + '"');
    }
  });
});

describe("stripOAuthReturn", () => {
  it("removes only our four parameters", () => {
    expect(stripOAuthReturn("?mcp_oauth=ok&server=a&reason=x&registration=dcr&keep=1")).toBe("?keep=1");
    expect(stripOAuthReturn("?mcp_oauth=ok&server=a")).toBe("");
  });
});

describe("safeAuthorizeUrl", () => {
  it("navigates only to http(s)", () => {
    expect(safeAuthorizeUrl("https://as.example.com/authorize?x=1")).toBe("https://as.example.com/authorize?x=1");
    expect(safeAuthorizeUrl("javascript:alert(1)")).toBeNull();
    expect(safeAuthorizeUrl("data:text/html,x")).toBeNull();
    expect(safeAuthorizeUrl("//evil.example.com")).toBeNull();
    expect(safeAuthorizeUrl(undefined)).toBeNull();
  });
});

describe("normalizeStatus", () => {
  it("reads an unknown state as not signed in and drops bad fields", () => {
    const s = normalizeStatus({ state: "weird", registration: "magic", scopes: ["a", 3], expiresAt: "soon" }, "mcp-1");
    expect(s.state).toBe("not_signed_in");
    expect(s.registration).toBeUndefined();
    expect(s.scopes).toEqual(["a"]);
    expect(s.expiresAt).toBeUndefined();
  });
});
