// Ceiling on kind-literal branches (`=== "codex"`, `case "kiro":`) in the Console outside
// agents/registry.ts. A new kind branch belongs in the registry, read through its
// capabilities; this test fails when the count rises (ADR 0124 Phase 0). A PR that removes
// branches lowers KIND_LITERAL_CEILING; nothing may raise it.
import { describe, it, expect } from "vitest";
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { SESSION_KINDS } from "../types/session.ts";

const KIND_LITERAL_CEILING = 66;

const SRC = path.resolve(__dirname, "..");
const REGISTRY = path.join(SRC, "agents", "registry.ts");

// Kinds that are not agents; ADR 0124 does not count branches on them. Every other kind in
// SESSION_KINDS is counted (kindCatalog.test.ts holds that list equal to the Agent's catalog), so
// a new kind is counted from the PR that adds it unless someone lists it here.
const NON_AGENT_KINDS = ["shell", "ssm"];

// The expression of ADR 0124's "Measuring" section, counted per line as grep -E does, built from
// the kind list rather than a typed copy of it.
function branchPattern(kinds: readonly string[]): RegExp {
  const k = kinds.join("|");
  return new RegExp(`(===|!==) *["'](${k})["']|["'](${k})["'] *(===|!==)|case ["'](${k})["']`);
}

function agentKinds(): string[] {
  for (const k of NON_AGENT_KINDS) expect(SESSION_KINDS, `NON_AGENT_KINDS lists ${k}`).toContain(k);
  return SESSION_KINDS.filter((k) => !NON_AGENT_KINDS.includes(k));
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) sourceFiles(p, out);
    else if (/\.tsx?$/.test(e.name) && !e.name.includes(".test.") && p !== REGISTRY) out.push(p);
  }
  return out;
}

function countBranches(dir: string, re: RegExp): { total: number; perFile: Record<string, number> } {
  let total = 0;
  const perFile: Record<string, number> = {};
  for (const f of sourceFiles(dir)) {
    const n = readFileSync(f, "utf8")
      .split("\n")
      .filter((l) => re.test(l)).length;
    if (n > 0) {
      perFile[path.relative(dir, f)] = n;
      total += n;
    }
  }
  return { total, perFile };
}

describe("kind-literal branch ceiling", () => {
  it("counts what the ADR counts", () => {
    const BRANCH = branchPattern(agentKinds());
    for (const l of [`if (kind === "codex") {`, `k !== 'muse'`, `"kiro" === k`, `case "lcpp":`]) {
      expect(BRANCH.test(l), l).toBe(true);
    }
    for (const l of [`const k = "codex";`, `["claude", "codex"]`, `case "bash":`, `kind === "shell"`]) {
      expect(BRANCH.test(l), l).toBe(false);
    }
  });

  it(`stays at or below ${KIND_LITERAL_CEILING} outside agents/registry.ts`, () => {
    const { total, perFile } = countBranches(SRC, branchPattern(agentKinds()));
    expect(total, "the scan found no branch at all; it is not reading src/").toBeGreaterThan(0);
    if (total > KIND_LITERAL_CEILING) {
      throw new Error(
        `kind-literal branches outside agents/registry.ts: ${total}, ceiling ${KIND_LITERAL_CEILING}.\n` +
          `Do not compare against an agent kind here: describe the behaviour in agents/registry.ts ` +
          `and read it from there (ADR 0124 Phase 3).\nPer file: ${JSON.stringify(perFile, null, 1)}`,
      );
    }
    if (total < KIND_LITERAL_CEILING) {
      console.info(
        `kind-literal branches: ${total}, below the ceiling ${KIND_LITERAL_CEILING}; lower KIND_LITERAL_CEILING to ${total} in this PR.`,
      );
    }
  });

  it("counts a kind that does not exist yet once it is in the kind list", () => {
    const dir = mkdtempSync(path.join(tmpdir(), "kind-ceiling-"));
    try {
      writeFileSync(path.join(dir, "x.ts"), `if (k === "pi") {}\nswitch (k) { case 'pi': }\n`);
      expect(countBranches(dir, branchPattern(agentKinds())).total).toBe(0);
      expect(countBranches(dir, branchPattern([...agentKinds(), "pi"])).total).toBe(2);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
