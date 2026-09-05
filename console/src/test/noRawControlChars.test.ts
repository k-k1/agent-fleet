// Static check that no raw control character appears in a source file.
//
// Using NUL as a composite-key separator (`repo + "\u0000" + rel`) is fine, but the raw 0x00
// byte has ended up in files instead of the escape (six places across five files). It parses,
// it behaves identically at runtime, so nobody notices. The tools that read the code are what
// suffer:
//   - git / ripgrep / grep treat the file as binary from that moment and drop it from search
//     entirely, so `rg foo` answers "not found";
//   - diffs and review screens stop showing the contents.
// The result is code that exists but never appears in a grep. Writing the escape avoids all
// of it.
//
// Tab, newline and carriage return are ordinary whitespace and are out of scope.
import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";

const SRC = path.resolve(__dirname, "..");

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) sourceFiles(p, out);
    else if (/\.(ts|tsx|css)$/.test(e.name)) out.push(p);
  }
  return out;
}

describe("raw control characters", () => {
  it("never appear in a source file (write the escape instead)", () => {
    const bad: string[] = [];
    for (const file of sourceFiles(SRC)) {
      const text = readFileSync(file, "utf8");
      text.split("\n").forEach((line, i) => {
        // eslint-disable-next-line no-control-regex -- finding these is the point
        const m = line.match(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/);
        if (m) {
          const code = "\\u" + m[0].charCodeAt(0).toString(16).padStart(4, "0");
          bad.push(`${path.relative(SRC, file)}:${i + 1} has a raw ${code} (write it as "${code}")`);
        }
      });
    }
    expect(bad).toEqual([]);
  });
});
