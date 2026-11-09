// Guard: a test that mocks core/api/client.ts with its own factory must provide `apiJSON`.
//
// lib/settings/sync.ts saves ui-prefs through `apiJSON` on a 600 ms debounce. In a file whose
// bare factory lacks it, a rendered tree that writes a setting (ReaderView's font buttons, a
// settings tab) and outlasts the debounce calls a missing export, and vitest exits 1 on the
// unhandled error even though every assertion passed (#3672, #3691). A factory that spreads the
// real module (importOriginal) already carries `apiJSON`, so only a factory that does neither
// is flagged. Fix: add `apiJSON: async () => ({})` to the factory.
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";

const SRC = path.resolve(__dirname, "..");
// This file quotes the forbidden shape in its self-test samples.
const EXEMPT = new Set(["test/clientMockApiJSON.test.ts"]);
const MOCK_OPEN = /vi\.mock\(\s*(["'`])[^"'`]*core\/api\/client(?:\.ts)?\1\s*,/g;
const MOCK_CLOSE = /^\}\)\);?[ \t]*$|^\}\);?[ \t]*$/m;
const HAS_REAL_MODULE = /importOriginal|importActual|\borig\b|\bactual\b/;

/** bareClientMocks returns the 1-based line of every client.ts mock factory in `src` that
 *  neither defines `apiJSON` nor spreads the real module. */
export function bareClientMocks(src: string): number[] {
  const lines: number[] = [];
  for (const m of src.matchAll(MOCK_OPEN)) {
    const start = m.index! + m[0].length;
    const rest = src.slice(start);
    const close = rest.search(MOCK_CLOSE);
    const factory = close < 0 ? rest : rest.slice(0, close);
    if (/\bapiJSON\b/.test(factory) || HAS_REAL_MODULE.test(factory)) continue;
    lines.push(src.slice(0, m.index!).split("\n").length);
  }
  return lines;
}

function testFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) testFiles(p, out);
    else if (/\.test\.tsx?$/.test(e.name)) out.push(p);
  }
  return out;
}

describe("client.ts mocks carry apiJSON", () => {
  it("flags a bare factory and passes the safe shapes (scanner self-test)", () => {
    const bare = 'vi.mock("../core/api/client.ts", () => ({\n  api: vi.fn(),\n}));\n';
    expect(bareClientMocks(bare)).toEqual([1]);
    expect(bareClientMocks(bare.replace("api: vi.fn(),", "api: vi.fn(),\n  apiJSON: async () => ({}),"))).toEqual([]);
    expect(bareClientMocks('vi.mock("../core/api/client.ts", async (orig) => ({\n  ...(await orig()),\n}));\n')).toEqual([]);
    expect(bareClientMocks('vi.mock("../core/api/client.ts");\n')).toEqual([]);
  });

  it("no test file mocks core/api/client.ts without apiJSON", () => {
    const offenders: string[] = [];
    for (const f of testFiles(SRC)) {
      const rel = path.relative(SRC, f).split(path.sep).join("/");
      if (EXEMPT.has(rel)) continue;
      for (const line of bareClientMocks(readFileSync(f, "utf8"))) offenders.push(`${rel}:${line}`);
    }
    expect(offenders, "add `apiJSON: async () => ({})` to these factories (see the header)").toEqual([]);
  });
});
