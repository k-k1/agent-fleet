// The capability table in guide/ref/agents.md against the caps in registry.ts.
//
// Why this file exists: until now the only rows checked against code were the fork rows and the
// permission-skip rows, so a cap could be turned off — or a guide cell flipped — with every test
// still green. Measured while closing ADR 0095 P2-21: reverting muse's `headlessChat` to `false`
// passed all 318 Console test files. The table's own front matter says "this table; the columns
// and the marked rows are checked against the code", and this is what makes that true of more
// than two rows.
//
// 🔴 A row is mapped here only when the row and the cap mean the SAME thing for every kind.
// Several rows deliberately do not, and UNMAPPED_ROWS names them with the reason — a row whose
// `—` is qualified by a footnote ("no transcript under Managed, but the CLI route keeps one") is
// not the negation of a boolean, and pinning it would encode a claim nobody verified.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { AGENTS } from "./registry.ts";
import type { AgentCaps } from "./registry.ts";
import type { SessionKind } from "../types/session.ts";

// Resolved from this file (src/agents/), not from the working directory, so the test does not
// depend on where vitest was started.
const EN = "../../../guide/ref/agents.md";
const JA = "../../../guide/ref/agents.ja.md";

/** Guide row label (English) → the cap that row is a statement about. */
const ROW_TO_CAP: Record<string, keyof AgentCaps> = {
  "Live chat mirror": "chat",
  "Reasoning effort": "effort",
  "Image paste": "imagePaste",
  "Fork from a past message": "forkAt",
  "Choosing to skip permission prompts": "permissionChoice",
  "Scheduled (unattended) runs": "scheduledRuns",
  "Usable as the assistant chat": "headlessChat",
};

/** Rows this file does NOT check, and why. Listed rather than omitted so that the reason is
 *  reviewable and a renamed row is still noticed (the test below asserts they exist). */
const UNMAPPED_ROWS: Record<string, string> = {
  "Managed execution (no terminal)": "a driver route, not a cap (managedDriver/terminalDriver)",
  "Terminal (CLI) execution": "a driver route, not a cap",
  "Read-only history while stopped":
    "cursor's — is qualified (footnote 3: nothing under Managed, a readable history over the CLI route), while caps.transcript is the Console affordance and is true",
  "Model choice at launch": "lcpp: caps.model is true (DynamicModel) and the guide cell is — (unresolved, ADR 0093)",
  "Plan mode": "caps.planMode is the TUI mode-cycle toggle; the row is the feature, which copilot/cursor/lcpp have by another route",
  "Context usage gauge": "lcpp: caps.contextBar is true (ADR 0093 decision 8) and the guide cell is — (unresolved)",
  "Copy the conversation into a new session": "server-side fork, no Console cap",
  "Skill / command picker": "the row is NATIVE enumeration (footnote 4); caps.slashSkills is also true for foreign-only kinds",
  "Handoff to another session": "a server capability, not a Console cap",
  "Start in a git worktree": "a repo/launch option, not a cap",
  "Chat bridge (Discord / Slack)": "keyed on notification kind, never on the agent kind",
  "Usage / remaining-quota chip": "drawn from the usage ledger, no cap behind it",
  "Receives your agent instructions": "an Agent-side apply path",
  "Receives integration (MCP) servers": "an Agent-side materialiser",
  "Agent memory is version-managed": "a repo convention, no cap",
};

interface Table {
  kinds: string[];
  rows: { label: string; cells: string[] }[];
}

/** Reads the one capability table out of a guide file. Cells keep only ✓ or — (footnote
 *  superscripts and emphasis are not part of the claim). */
function readTable(path: string): Table {
  const lines = readFileSync(new URL(path, import.meta.url), "utf8").split("\n");
  const head = lines.findIndex((l) => l.startsWith("| Capability |") || l.startsWith("| 機能 |"));
  if (head < 0) throw new Error(`no capability table in ${path}`);
  const split = (l: string): string[] =>
    l
      .split("|")
      .slice(1, -1)
      .map((c) => c.replace(/[⁰¹²³⁴⁵⁶⁷⁸⁹🔴*_`]/gu, "").trim());
  const kinds = split(lines[head]).slice(1);
  const rows: Table["rows"] = [];
  for (let i = head + 2; i < lines.length && lines[i].startsWith("|"); i++) {
    const cells = split(lines[i]);
    rows.push({ label: cells[0], cells: cells.slice(1) });
  }
  return { kinds, rows };
}

const en = readTable(EN);

describe("guide capability table", () => {
  it("has a column for every kind the Console knows, and no other", () => {
    expect([...en.kinds].sort()).toEqual(Object.keys(AGENTS).sort());
  });

  it("marks every cell with ✓ or —", () => {
    for (const row of en.rows) {
      for (const [i, cell] of row.cells.entries()) {
        expect(`${row.label}/${en.kinds[i]}: ${cell}`).toMatch(/: (✓|—)$/u);
      }
    }
  });

  // The mapping is only worth having if every name in it is a real row: a renamed row would
  // otherwise silently stop being checked, which is the failure this whole file is about. The
  // unmapped names are asserted for the same reason — a rename must not turn an excluded row
  // into a row nobody ever looked at again.
  it("accounts for every row of the table", () => {
    const labels = en.rows.map((r) => r.label);
    const known = [...Object.keys(ROW_TO_CAP), ...Object.keys(UNMAPPED_ROWS)];
    for (const label of known) expect(labels).toContain(label);
    for (const label of labels) expect(known).toContain(label);
  });

  it.each(Object.entries(ROW_TO_CAP))("row %s matches caps.%s for every kind", (label, cap) => {
    const row = en.rows.find((r) => r.label === label)!;
    const fromGuide = Object.fromEntries(en.kinds.map((k, i) => [k, row.cells[i] === "✓"]));
    const fromCode = Object.fromEntries(
      en.kinds.map((k) => [k, AGENTS[k as SessionKind].caps[cap]]),
    );
    expect(fromCode).toEqual(fromGuide);
  });

  // The two languages are one table in two files, and a ✓ that exists in only one of them is a
  // translation that changed a claim.
  it("says the same thing in Japanese", () => {
    const ja = readTable(JA);
    expect(ja.kinds).toEqual(en.kinds);
    expect(ja.rows.length).toBe(en.rows.length);
    for (const [i, row] of en.rows.entries()) {
      expect(`${row.label}: ${ja.rows[i].cells.join(" ")}`).toBe(
        `${row.label}: ${row.cells.join(" ")}`,
      );
    }
  });
});
