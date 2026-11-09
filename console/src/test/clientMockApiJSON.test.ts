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
// "Calls the original loader", not just "has a variable called orig/actual".
const HAS_REAL_MODULE = /importOriginal|importActual|await\s+\(?\s*(?:orig|actual)\b/;
// A line-start // comment is blanked, newlines kept so line numbers stay right. Not handled, by
// choice: a block comment between the path and the comma, and CRLF files (the repo is LF).
const stripLineComments = (s: string) => s.replace(/^[ \t]*\/\/.*$/gm, "");

/** bareClientMocks returns the 1-based line of every client.ts mock factory in `src` that
 *  neither defines `apiJSON` nor spreads the real module. */
export function bareClientMocks(src: string): number[] {
  const lines: number[] = [];
  src = stripLineComments(src);
  for (const m of src.matchAll(MOCK_OPEN)) {
    const start = m.index! + m[0].length;
    const rest = src.slice(start);
    // A one-line factory ends on its own line; judging it by the rest of the file would let a
    // later mention of apiJSON excuse it.
    const nl = rest.indexOf("\n");
    const head = nl < 0 ? rest : rest.slice(0, nl);
    const close = rest.search(MOCK_CLOSE);
    const factory = /\}\)\)?;?\s*$/.test(head) ? head : close < 0 ? rest : rest.slice(0, close);
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
    // One-line factories are judged on their own line, not on later mentions of apiJSON.
    const one = 'vi.mock("../core/api/client.ts", () => ({ api: vi.fn() }));\nconst apiJSONMock = 1;\n';
    expect(bareClientMocks(one)).toEqual([1]);
    expect(bareClientMocks(one.replace("api: vi.fn()", "api: vi.fn(), apiJSON: async () => ({})"))).toEqual([]);
    // An unrelated argument named actual is not the real module; a commented-out mock is no mock.
    expect(bareClientMocks('vi.mock("../core/api/client.ts", () => ({\n  check: (actual) => actual,\n}));\n')).toEqual([1]);
    expect(bareClientMocks('// vi.mock("../core/api/client.ts", () => ({}));\n')).toEqual([]);
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
