// Stored header values belong to the host they were entered for: moving the URL of an existing
// distribution to another host blanks the masked values in the form (the control plane refuses
// them with mcp_header_secret_reenter), while other edits keep them.
import { describe, expect, it } from "vitest";
import { bodyOfTenant, tenantFormOf, tenantHostMoved, tenantUrlPatch } from "./mcpWire.ts";

const loaded = () =>
  tenantFormOf({
    id: "t1", name: "hosted", transport: "http", url: "https://m.example.com/mcp", enabled: true,
    targets: { assistant: true, session: true },
    headers: { Authorization: "Bearer ***", "X-Key": "***", "X-Plain": "visible" },
  });

describe("tenant form URL host change", () => {
  it("keeps stored values for the same host, another path or the explicit default port", () => {
    const f = loaded();
    for (const url of ["https://m.example.com/other", "https://M.example.com:443/mcp", "https://m.example.com/mcp?x=1"]) {
      expect(tenantHostMoved(f, url)).toBe(false);
      expect(tenantUrlPatch(f, url)).toEqual({ url });
    }
  });

  it("blanks the masked values when the host or port changes, and only those", () => {
    const f = loaded();
    for (const url of ["https://n.example.com/mcp", "https://m.example.com:8443/mcp", "https://m.example.net/mcp"]) {
      const p = tenantUrlPatch(f, url);
      expect(p.url).toBe(url);
      expect(p.headers).toEqual([
        { k: "Authorization", v: "" },
        { k: "X-Key", v: "" },
        { k: "X-Plain", v: "visible" },
      ]);
    }
  });

  it("sends nothing masked to the control plane afterwards", () => {
    const f = loaded();
    const moved = { ...f, ...tenantUrlPatch(f, "https://n.example.com/mcp") };
    const body = bodyOfTenant(moved, "sales");
    expect(JSON.stringify(body.headers)).not.toContain("***");
  });

  it("does not touch a new distribution, a user_secret row, or values the admin typed", () => {
    const fresh = { ...loaded(), id: "", origUrl: "" };
    expect(tenantUrlPatch(fresh, "https://n.example.com")).toEqual({ url: "https://n.example.com" });
    const us = { ...loaded(), userSecret: true };
    expect(tenantUrlPatch(us, "https://n.example.com")).toEqual({ url: "https://n.example.com" });
    const typed = { ...loaded(), headers: [{ k: "X-Key", v: "typed-now" }] };
    expect(tenantUrlPatch(typed, "https://n.example.com")).toEqual({ url: "https://n.example.com" });
  });
});
