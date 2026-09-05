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
});
