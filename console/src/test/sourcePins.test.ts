import { describe, expect, it } from "vitest";
import { codeOnly, hasCodeLiteral, importsRawClient } from "./sourcePins.ts";

const C = '"../../../core/api/client.ts"';

describe("importsRawClient", () => {
  it.each([
    [`import { api } from ${C};`, true],
    [`import { apiJSON as j } from ${C};`, true],
    [`import {\n  raw,\n  api,\n} from ${C};`, true],
    [`import * as client from ${C};`, true],
    [`export * from ${C};`, true],
    [`export { api } from ${C};`, true],
    [`const m = await import(${C});`, true],
    [`import { raw } from ${C};`, false],
    [`import { apiBase } from ${C};`, false],
    [`import type { api } from ${C};`, false],
    [`import { type api, raw } from ${C};`, false],
    [`// import { api } from ${C};\nconst x = 1;`, false],
    [`/* import * as c from ${C}; */`, false],
    [`import { x } from "./other.ts";`, false],
  ])("%s", (src, want) => {
    expect(importsRawClient(src)).toBe(want);
  });
});

describe("codeOnly / hasCodeLiteral", () => {
  it("drops a path that only a comment mentions", () => {
    expect(hasCodeLiteral('// "api/x"\nconst a = 1;', "api/x")).toBe(false);
    expect(hasCodeLiteral("/* api/x */ const a = 1;", "api/x")).toBe(false);
    expect(hasCodeLiteral('const a = "api/x";', "api/x")).toBe(true);
    expect(hasCodeLiteral("const a = `api/x/${id}`;", "api/x/${id}")).toBe(true);
  });
  it("keeps a // inside a string", () => {
    expect(codeOnly('const u = "http://x"; // tail')).toContain("http://x");
    expect(codeOnly('const u = "http://x"; // tail')).not.toContain("tail");
  });
});
