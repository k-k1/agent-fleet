import { describe, it, expect } from "vitest";
import { hostOf, hostsOf, checkQuery, egressLevel } from "./egressCheck.ts";
import type { EgressCheck } from "./egressCheck.ts";

// The rules that decide WHETHER the MCP screens warn about a destination (docs/log/48 §9).
// The failure mode being guarded is crying wolf: a warning shown on a deployment with no
// egress control, or while the check has not answered, teaches the user to ignore it.

describe("hostOf", () => {
  it("extracts the host the proxy would match on", () => {
    expect(hostOf("https://mcp.example.com/mcp")).toBe("mcp.example.com");
    expect(hostOf("http://MCP.Example.COM:8443/x")).toBe("mcp.example.com");
    expect(hostOf(" https://mcp.example.com ")).toBe("mcp.example.com");
  });

  it("unwraps an IPv6 literal, which the allowlist stores bare", () => {
    expect(hostOf("http://[::1]:8080/mcp")).toBe("::1");
  });

  it("returns '' — say nothing — for anything not checkable", () => {
    expect(hostOf("")).toBe("");
    expect(hostOf(undefined)).toBe("");
    expect(hostOf("not a url")).toBe("");
    expect(hostOf("mcp.example.com")).toBe(""); // no scheme: a half-typed URL
    expect(hostOf("ftp://mcp.example.com")).toBe("");
  });
});

describe("hostsOf", () => {
  it("collects distinct hosts and skips the unusable ones", () => {
    expect(hostsOf(["https://b.example/mcp", "https://a.example/x", "https://b.example/y", "", undefined])).toEqual([
      "a.example",
      "b.example",
    ]);
  });
});

describe("checkQuery", () => {
  it("encodes one host param per destination", () => {
    expect(checkQuery(["a.example", "b.example"])).toBe("host=a.example&host=b.example");
    expect(checkQuery(["a b.example"])).toBe("host=a%20b.example");
  });
});

describe("egressLevel", () => {
  const check = (over: Partial<EgressCheck>): EgressCheck => ({
    configured: true,
    mode: "log-only",
    enforce: false,
    hosts: {},
    ...over,
  });
  const verdict = (allowed: boolean, proposed = false) => ({
    "x.example": { host: "x.example", allowed, proposed },
  });

  it("says nothing when the deployment has no egress control", () => {
    expect(egressLevel(check({ configured: false, hosts: verdict(false) }), "x.example")).toBe("none");
  });

  it("says nothing while the check has not answered", () => {
    expect(egressLevel(null, "x.example")).toBe("none");
    expect(egressLevel(check({}), "x.example")).toBe("none"); // no verdict for this host
    expect(egressLevel(check({ hosts: verdict(false) }), "")).toBe("none");
  });

  it("says nothing for an allowed destination", () => {
    expect(egressLevel(check({ hosts: verdict(true) }), "x.example")).toBe("none");
  });

  it("distinguishes log-only from enforce", () => {
    expect(egressLevel(check({ hosts: verdict(false) }), "x.example")).toBe("would_block");
    expect(egressLevel(check({ enforce: true, mode: "enforce", hosts: verdict(false) }), "x.example")).toBe("blocked");
  });

  it("a pending request outranks both — the action is to wait, not to ask again", () => {
    expect(egressLevel(check({ hosts: verdict(false, true) }), "x.example")).toBe("pending");
    expect(egressLevel(check({ enforce: true, hosts: verdict(false, true) }), "x.example")).toBe("pending");
  });
});
