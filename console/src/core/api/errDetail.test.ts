// The errDetail contract. A generic code such as `*_failed` only produces the i18n boilerplate,
// so dropping the cause the server returned (message) removes the only on-screen clue to a
// failure that reproduces on the user's machine alone — agent-memory import showed nothing but
// "import failed" and hid the reason.
import { beforeAll, describe, expect, it, vi } from "vitest";

// client.ts binds window.fetch and reads document.baseURI at import time, so stub first.
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("sessionStorage", {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
});
vi.stubGlobal("document", { baseURI: "http://localhost/" });
vi.stubGlobal("window", { fetch: vi.fn(), addEventListener: () => {}, removeEventListener: () => {} });

let client: typeof import("./client.ts");

beforeAll(async () => {
  client = await import("./client.ts");
});

describe("errText for Go-built error codes", () => {
  it("words interaction_not_pending from the catalogue, not the server's English message", () => {
    const text = client.errText({ code: "interaction_not_pending", message: "interaction x is not pending" });
    expect(text).not.toContain("is not pending");
    expect(text).not.toBe("");
  });
});

describe("errDetail", () => {
  it("appends the server's message even for a code that has a translation", () => {
    const head = client.errText({ code: "memory_import_failed" });
    const got = client.errDetail({
      code: "memory_import_failed",
      message: "apply claude/projects/-x: mkdir /var/lib/af/claude/projects/-x: no such file or directory",
    });
    expect(got.startsWith(head + ": ")).toBe(true);
    expect(got).toContain("no such file or directory");
  });

  it("returns the boilerplate alone when there is no message, with no separator", () => {
    const got = client.errDetail({ code: "memory_import_failed" });
    expect(got).toBe(client.errText({ code: "memory_import_failed" }));
    expect(got.endsWith(":")).toBe(false);
  });

  it("matches errText for an untranslated code, which already shows the message verbatim", () => {
    const e = { code: "no_such_code_zzz", message: "boom" };
    expect(client.errDetail(e)).toBe(client.errText(e));
  });

  it("passes a plain string through unchanged", () => {
    expect(client.errDetail("plain")).toBe("plain");
  });

  // The refusal of a start whose CLI did not install carries its own localized sentence; the
  // server's English message after it would say the same thing twice.
  it("shows the localized cli_install text alone", () => {
    const e = { code: "cli_install_no_space", message: "codex could not be installed when this workspace started because the home disk is full." };
    const got = client.errDetail(e);
    expect(got).toBe(client.errText(e));
    expect(got).not.toContain("codex could not");
    expect(got.length).toBeGreaterThan(20);
  });
});
