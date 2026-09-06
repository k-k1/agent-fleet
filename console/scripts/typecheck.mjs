#!/usr/bin/env node
// The `npm run typecheck` gate — `tsc --noEmit` on TypeScript 7 (the native compiler).
//
// ## Why this is a script and not `"typecheck": "tsc --noEmit"`
//
// Two packages in this project provide a `tsc` binary: `typescript` (7, the checker) and
// `typescript-ast` (an alias of `typescript@6`, kept only because TypeScript 7 ships no
// compiler API — see src/test/siblingKeys.test.ts). npm links `node_modules/.bin/tsc` to
// ONE of them, and which one it picks depends on install order: a plain `npm i -D` of the
// alias pointed it at 6, a later `npm ci` pointed it at 7 (both measured, same lockfile
// state). So a bare `tsc` is a coin flip.
//
// Losing that flip is silent. TypeScript 6 checks the same code and reports the same
// errors, so the gate still passes — it just takes ~32s instead of ~3s and stops being a
// check of the compiler we ship. Nothing prints the version.
//
// Hence: resolve the package (not the bin), and refuse to run if it is not the major we
// mean. A check that cannot say which tool it ran is not a check.
import { spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(import.meta.url);
// Resolve through package.json: TypeScript 7 has an "exports" map, and bin/ is not in it, so
// require.resolve("typescript/bin/tsc") is ERR_PACKAGE_PATH_NOT_EXPORTED. "./package.json" is.
const manifest = require.resolve("typescript/package.json");
const pkg = require(manifest);
const WANT_MAJOR = 7;

if (Number(pkg.version.split(".")[0]) !== WANT_MAJOR) {
  console.error(
    `typecheck: expected TypeScript ${WANT_MAJOR}.x, resolved ${pkg.version}.\n` +
      "If the upgrade to a new major is deliberate, change WANT_MAJOR in this file.",
  );
  process.exit(1);
}

const tsc = path.join(path.dirname(manifest), pkg.bin.tsc);
const { status } = spawnSync(process.execPath, [tsc, "--noEmit", ...process.argv.slice(2)], {
  stdio: "inherit",
});
process.exit(status ?? 1);
