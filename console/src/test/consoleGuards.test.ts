// The two Console ratchets of ADR 0124 Phase 0: the css-dup ceiling CI runs, and the lib/ →
// features/ import boundary in .oxlintrc.json. Both only work while their numbers stay honest,
// so this checks the mechanism (a count above the ceiling fails) and that the exception list
// does not keep files that no longer need an exception.
import { describe, it, expect } from "vitest";
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = fileURLToPath(new URL("../..", import.meta.url));

function cssDup(...args: string[]) {
  const r = spawnSync(process.execPath, [join(ROOT, "scripts/css-dup.mjs"), "--json", ...args], {
    encoding: "utf8",
  });
  return { status: r.status, stdout: r.stdout, stderr: r.stderr };
}

describe("css-dup ceilings", () => {
  const all = JSON.parse(cssDup().stdout) as { bareFiles: number }[];
  const dups = all.length;
  const bare = all.filter((d) => d.bareFiles > 1).length;

  it("passes at or above today's counts", () => {
    expect(cssDup(`--max-dups=${dups}`, `--max-bare=${bare}`).status).toBe(0);
    expect(cssDup(`--max-dups=${dups + 1}`, `--max-bare=${bare + 1}`).status).toBe(0);
  });

  it("fails when either count is above its ceiling", () => {
    expect(dups).toBeGreaterThan(0);
    expect(bare).toBeGreaterThan(0);
    const d = cssDup(`--max-dups=${dups - 1}`);
    expect(d.status).toBe(1);
    expect(d.stderr).toContain("--max-dups");
    const b = cssDup(`--max-bare=${bare - 1}`);
    expect(b.status).toBe(1);
    expect(b.stderr).toContain("--max-bare");
  });

  it("rejects a ceiling that is not a non-negative integer", () => {
    expect(cssDup("--max-dups=abc").status).toBe(2);
    expect(cssDup("--max-bare=-1").status).toBe(2);
  });

  it("CI's ceilings in package.json are today's counts", () => {
    // Above today's count would let a new duplicate in unnoticed; the script's own message
    // asks for the ceiling to be lowered, and this makes that stick.
    const pkg = JSON.parse(readFileSync(join(ROOT, "package.json"), "utf8"));
    expect(pkg.scripts["css:dup:ci"]).toBe(
      `node scripts/css-dup.mjs --max-dups=${dups} --max-bare=${bare}`,
    );
  });
});

describe("lib/ import boundary exceptions", () => {
  // .oxlintrc.json is JSONC; its comments are whole lines.
  const cfg = JSON.parse(
    readFileSync(join(ROOT, ".oxlintrc.json"), "utf8").replace(/^\s*\/\/.*$/gm, ""),
  ) as { overrides: { files: string[]; rules: Record<string, unknown> }[] };
  const exceptions = cfg.overrides.find((o) => o.rules["no-restricted-imports"] === "off")?.files;

  it("lists exceptions under src/lib/ only", () => {
    expect(exceptions?.length).toBeGreaterThan(0);
    for (const f of exceptions ?? []) expect(f).toMatch(/^src\/lib\/[^*]+$/);
  });

  it("drops a file from the list once it no longer imports features/", () => {
    for (const f of exceptions ?? []) {
      const path = join(ROOT, f);
      expect(existsSync(path), `${f} is listed but does not exist`).toBe(true);
      expect(readFileSync(path, "utf8"), `${f} no longer imports features/`).toMatch(
        /["'](\.\.\/)+features\//,
      );
    }
  });
});
