import { describe, expect, it } from "vitest";
import {
  MASKED,
  NAME_RE,
  bodyOf,
  codexUnsupported,
  emptyForm,
  formOf,
  formValid,
  fromKV,
} from "./mcpWire.ts";
import type { McpServer } from "./mcpWire.ts";

// The MCP registry tab's wire contract (docs/48 P1). These rules are enforced on the
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

describe("codexUnsupported (docs/48 §7)", () => {
  it("flags a remote server carrying a header other than Authorization", () => {
    expect(codexUnsupported("http", [{ k: "X-Api-Key", v: "s" }])).toBe(true);
  });
  it("does not flag a bearer-only remote server", () => {
    expect(codexUnsupported("http", [{ k: "authorization", v: "s" }])).toBe(false);
  });
  it("does not flag a stdio server (env vars are expressible in codex)", () => {
    expect(codexUnsupported("stdio", [{ k: "X-Api-Key", v: "s" }])).toBe(false);
  });
  it("ignores a half-typed blank row", () => {
    expect(codexUnsupported("http", [{ k: "  ", v: "s" }])).toBe(false);
  });
});
