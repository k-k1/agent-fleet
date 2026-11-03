// Guard: no native window.confirm() in the Console.
//
// The native dialog shows the browser's host name as its title, uses browser buttons and ignores
// the dark theme, so a confirmation that still calls it reads as a different product from every
// other one (#3307). Use `useConfirm()` (ui/ConfirmProvider.tsx), or `askConfirm` from
// ui/confirmBridge.ts outside React. confirmBridge.ts is the one deliberate caller: its fallback
// covers a call that arrives before the provider has mounted.
import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";

const SRC = path.resolve(__dirname, "..");
// This file is exempt only because its scanner self-test quotes the forbidden call as sample text.
const ALLOWED = new Set(["ui/confirmBridge.ts", "test/noNativeConfirm.test.ts"]);
const NATIVE_CONFIRM = /\b(?:window|globalThis|self)\s*\.\s*confirm\s*\(/;

/** stripComments blanks // and block comments so prose that names the API is not a call. */
export function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, " ")).replace(/(^|[^:"'`\\])\/\/.*$/gm, "$1");
}

export function usesNativeConfirm(src: string): boolean {
  return NATIVE_CONFIRM.test(stripComments(src));
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) sourceFiles(p, out);
    else if (/\.(ts|tsx)$/.test(e.name)) out.push(p);
  }
  return out;
}

describe("no native window.confirm()", () => {
  it("recognises the call forms and ignores comments", () => {
    expect(usesNativeConfirm('if (!window.confirm("x")) return;')).toBe(true);
    expect(usesNativeConfirm("window . confirm(`x`)")).toBe(true);
    expect(usesNativeConfirm("globalThis.confirm(msg)")).toBe(true);
    expect(usesNativeConfirm("// replaces window.confirm(x)\nconst a = 1;")).toBe(false);
    expect(usesNativeConfirm("/* window.confirm(x) */")).toBe(false);
    expect(usesNativeConfirm('vi.spyOn(window, "confirm")')).toBe(false);
  });

  it("is absent from console/src outside ui/confirmBridge.ts", () => {
    const files = sourceFiles(SRC);
    expect(files.length).toBeGreaterThan(100);
    // The allowed file must still be the one that calls it, or the allow-list is stale.
    expect(usesNativeConfirm(readFileSync(path.join(SRC, "ui/confirmBridge.ts"), "utf8"))).toBe(true);
    const offenders = files
      .map((f) => path.relative(SRC, f).split(path.sep).join("/"))
      .filter((rel) => !ALLOWED.has(rel))
      .filter((rel) => usesNativeConfirm(readFileSync(path.join(SRC, rel), "utf8")));
    expect(offenders, "use useConfirm() from ui/ConfirmProvider.tsx").toEqual([]);
  });
});
