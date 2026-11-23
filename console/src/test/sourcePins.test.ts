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
    // Declarations without semicolons must not be read across each other.
    [`export type Foo = string\nimport { api } from ${C}`, true],
    [`export { a }\nimport { api } from ${C}`, true],
    [`export const x = 1\nimport { raw } from ${C}`, false],
    // Example text in a string or template is not an import.
    ["const example = `import { api } from " + C + "`;", false],
    [`const example = 'import { api } from ${C}';`, false],
    [`const s = \`\${ 1 }\`;\nimport { api } from ${C}`, true],
    [`const m = "import(" + ${C};`, false],
    // A quote inside a regex literal does not open a string.
    [`const re = /"/;\nimport { api } from ${C};`, true],
    [`const re = /[/"]+/g;\nimport { api } from ${C};`, true],
    [`const half = a / b;\nimport { api } from ${C};`, true],
    // An interpolation-free template is a plain specifier.
    ["const m = await import(`../../../core/api/client.ts`);", true],
    ["const m = await import(`../../../core/api/${x}.ts`);", false],
    // A type reference or someone else's require() loads nothing.
    [`type Client = typeof import(${C});`, false],
    [`custom.require(${C});`, false],
    [`const m = require(${C});`, true],
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
  it("drops a comment inside a template expression", () => {
    expect(hasCodeLiteral("const a = `hello ${ /* api/old */ v }`;", "api/old")).toBe(false);
    expect(hasCodeLiteral("const a = `hello ${ v // api/old\n }`;", "api/old")).toBe(false);
    expect(hasCodeLiteral("const a = `x/${ v }/api/new`;", "api/new")).toBe(true);
    expect(hasCodeLiteral("const a = `x/${ `n/${ w }` }/api/new`;", "${ w }")).toBe(true);
  });
  it("keeps a // inside a string", () => {
    expect(codeOnly('const u = "http://x"; // tail')).toContain("http://x");
    expect(codeOnly('const u = "http://x"; // tail')).not.toContain("tail");
  });
});
