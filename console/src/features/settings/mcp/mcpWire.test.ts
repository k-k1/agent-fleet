import { describe, expect, it } from "vitest";
import {
  MASKED,
  NAME_RE,
  bodyOf,
  emptyForm,
  formOf,
  formValid,
  fromKV,
  bodyOfTenant,
  emptyTenantForm,
  needsMemberSecrets,
  tenantFormOf,
  tenantFormValid,
} from "./mcpWire.ts";
import type { McpServer, TenantServer } from "./mcpWire.ts";

// The MCP registry tab's wire contract (docs/log/48 P1). These rules are enforced on the
// agent side too — the point here is that the Console never SENDS something the agent
// must reject, and never quietly destroys a stored secret on the way back.

const stdioRow: McpServer = {
  id: "u1",
  name: "filesystem",
  origin: "user",
  transport: "stdio",
  command: "npx",
  args: ["-y", "@modelcontextprotocol/server-filesystem", "/home/dev/repos"],
  env: { API_TOKEN: MASKED },
  enabled: true,
  targets: { assistant: false, session: true },
  kinds: ["claude", "codex"],
  editable: true,
  ready: true,
};

const httpRow: McpServer = {
  id: "u2",
  name: "corp-wiki",
  label: "Corp Wiki",
  origin: "user",
  transport: "http",
  url: "https://mcp.example.com/mcp",
  headers: { Authorization: MASKED },
  enabled: true,
  targets: { assistant: true, session: true },
  editable: true,
  ready: true,
};

describe("name rule (mirrors mcpreg.nameRe)", () => {
  it.each(["a", "my-server", "my_server", "s1", "a".repeat(48)])("accepts %s", (n) => {
    expect(NAME_RE.test(n)).toBe(true);
  });
  // Rejected because codex writes the name as a TOML bare key — the narrowest of the
  // target CLIs' charsets, so anything else breaks a config file rather than this form.
  it.each(["", "-lead", "_lead", "has space", "has.dot", "sla/sh", "a".repeat(49)])(
    "rejects %s",
    (n) => {
      expect(NAME_RE.test(n)).toBe(false);
    },
  );
});

describe("formOf / bodyOf round-trip", () => {
  it("keeps a masked secret masked, so the agent restores the stored value", () => {
    const body = bodyOf(formOf(stdioRow));
    expect(body.env).toEqual({ API_TOKEN: MASKED });
  });

  it("round-trips a stdio definition unchanged", () => {
    const body = bodyOf(formOf(stdioRow));
    expect(body).toMatchObject({
      id: "u1",
      name: "filesystem",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/home/dev/repos"],
      targets: { assistant: false, session: true },
      kinds: ["claude", "codex"],
      enabled: true,
    });
  });

  it("round-trips a remote definition unchanged", () => {
    const body = bodyOf(formOf(httpRow));
    expect(body).toMatchObject({
      transport: "http",
      url: "https://mcp.example.com/mcp",
      headers: { Authorization: MASKED },
      label: "Corp Wiki",
    });
  });

  // The agent refuses a definition carrying both halves (a stdio server with a URL is
  // a mistake, not a merge), so switching the transport must DROP the other half —
  // not send it as an empty string.
  it("sends only the half the transport uses (stdio)", () => {
    const body = bodyOf({ ...formOf(stdioRow), url: "https://leftover.example.com", headers: [{ k: "X", v: "y" }] });
    expect(body).not.toHaveProperty("url");
    expect(body).not.toHaveProperty("headers");
  });

  it("sends only the half the transport uses (http)", () => {
    const body = bodyOf({ ...formOf(httpRow), command: "leftover", args: "-x", env: [{ k: "A", v: "b" }] });
    expect(body).not.toHaveProperty("command");
    expect(body).not.toHaveProperty("args");
    expect(body).not.toHaveProperty("env");
  });

  it("omits the id for a new registration (the agent mints it)", () => {
    expect(bodyOf({ ...emptyForm(), name: "x", command: "true" }).id).toBeUndefined();
  });

  it("splits arguments one per line and keeps embedded spaces", () => {
    const body = bodyOf({ ...emptyForm(), name: "x", command: "sh", args: "-c\necho hello world\n\n" });
    expect(body.args).toEqual(["-c", "echo hello world"]);
  });

  it("sends timeout 0 (= the CLI default) for a blank field", () => {
    expect(bodyOf({ ...emptyForm(), name: "x", command: "true", timeoutMs: "" }).timeoutMs).toBe(0);
  });
});

describe("fromKV", () => {
  it("drops blank keys but keeps a blank value (that is how a value is cleared)", () => {
    expect(fromKV([{ k: "A", v: "1" }, { k: "  ", v: "2" }, { k: "B", v: "" }])).toEqual({ A: "1", B: "" });
  });
  it("trims keys", () => {
    expect(fromKV([{ k: " A ", v: "1" }])).toEqual({ A: "1" });
  });
});

describe("formValid", () => {
  it("requires a command for stdio", () => {
    expect(formValid({ ...emptyForm(), name: "x" })).toBe(false);
    expect(formValid({ ...emptyForm(), name: "x", command: "npx" })).toBe(true);
  });
  it("requires an http(s) URL for a remote server", () => {
    const base = { ...emptyForm(), name: "x", transport: "http" as const };
    expect(formValid(base)).toBe(false);
    expect(formValid({ ...base, url: "ftp://x.example.com" })).toBe(false);
    expect(formValid({ ...base, url: "https://x.example.com/mcp" })).toBe(true);
  });
  it("refuses an invalid name whatever the transport", () => {
    expect(formValid({ ...emptyForm(), name: "bad name", command: "npx" })).toBe(false);
  });
  // Both targets off is a legal staging state (stored, handed to nothing), so it must
  // not block the save — see secrets.MCPTargets.
  it("allows a definition with no target selected", () => {
    expect(formValid({ ...emptyForm(), name: "x", command: "npx", assistant: false, session: false })).toBe(true);
  });
});


// --- tenant distribution (docs/log/48 P4) ---------------------------------------------
//
// The invariant worth a test rather than a review: a tenant definition can never carry
// a command. ADR0031 決定 2 refuses distributed stdio because it is an admin running
// arbitrary code in every member's container, and the wire body is the last place the
// Console could reintroduce one.

const tenantRow: TenantServer = {
  id: "t1",
  name: "corp-wiki",
  label: "Corp Wiki",
  transport: "http",
  url: "https://wiki.corp.example/mcp",
  headers: { Authorization: MASKED, "X-Team": MASKED },
  targets: { assistant: true, session: true },
  kinds: ["claude"],
  enabled: true,
};

describe("bodyOfTenant", () => {
  it("never sends a command, args or env", () => {
    const b = bodyOfTenant(tenantFormOf(tenantRow), "acme");
    for (const k of ["command", "args", "env"]) expect(b).not.toHaveProperty(k);
    expect(b.transport).toBe("http");
  });
  it("keeps masked header values so an untouched edit does not clear the credential", () => {
    const b = bodyOfTenant(tenantFormOf(tenantRow), "acme");
    expect(b.headers).toEqual({ Authorization: MASKED, "X-Team": MASKED });
  });
  it("sends header NAMES with blank values when the member supplies the credential", () => {
    const f = { ...tenantFormOf(tenantRow), userSecret: true };
    const b = bodyOfTenant(f, "acme");
    // A real value must not travel just because it was in the form before the toggle:
    // with user_secret on, nothing stores it and nothing would ever read it.
    expect(b.headers).toEqual({ Authorization: "", "X-Team": "" });
    expect(b.user_secret).toBe(true);
  });
  it("carries the tenant it is being distributed to", () => {
    expect(bodyOfTenant(emptyTenantForm(), "acme").tenant_slug).toBe("acme");
  });
});

describe("tenantFormValid", () => {
  it("requires a valid name and an http(s) URL", () => {
    const base = emptyTenantForm();
    expect(tenantFormValid(base)).toBe(false);
    expect(tenantFormValid({ ...base, name: "wiki" })).toBe(false);
    expect(tenantFormValid({ ...base, name: "wiki", url: "ftp://x/y" })).toBe(false);
    expect(tenantFormValid({ ...base, name: "bad name", url: "https://x/y" })).toBe(false);
    expect(tenantFormValid({ ...base, name: "wiki", url: "https://x/y" })).toBe(true);
  });
});

describe("needsMemberSecrets", () => {
  // An empty value is the tenant saying "you supply this"; MASKED is a value that is
  // already stored. Confusing the two either hides the action or invents one.
  const row = (over: Partial<McpServer>): McpServer =>
    ({ ...tenantRow, origin: "tenant", editable: false, ready: false, ...over }) as McpServer;
  it("is true for a tenant user_secret row with an unfilled value", () => {
    expect(needsMemberSecrets(row({ userSecret: true, headers: { Authorization: "" } }))).toBe(true);
  });
  it("is false once every value is stored", () => {
    expect(needsMemberSecrets(row({ userSecret: true, headers: { Authorization: MASKED } }))).toBe(false);
  });
  it("is false for a plain tenant row — the values are not the member's to enter", () => {
    expect(needsMemberSecrets(row({ headers: { Authorization: "" } }))).toBe(false);
  });
  it("is false for a user row, whose empty value is its own owner's to fill", () => {
    expect(needsMemberSecrets(row({ origin: "user", userSecret: true, headers: { A: "" } }))).toBe(false);
  });
});
