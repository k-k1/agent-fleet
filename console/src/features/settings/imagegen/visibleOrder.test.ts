import { describe, it, expect } from "vitest";
import { mergeVisibleOrder } from "./visibleOrder.ts";

// The Agent's reading of a stored order (normalizeStoredProviderOrder, then effectiveOrder's
// placement of what is unmentioned), narrowed to `declared`, the ids it knows: a legacy alias
// expands at its slot to every row of its kind not already ranked; an unmentioned fleet row goes
// first, an unmentioned vendor route last.
function agentReads(stored: string[], rows: Record<string, string>, vendors: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  const push = (id: string) => {
    if (!seen.has(id)) {
      seen.add(id);
      out.push(id);
    }
  };
  for (const id of stored) {
    if (id === "comfy" || id === "openai-compat") {
      Object.keys(rows).filter((k) => rows[k] === id).forEach(push);
      continue;
    }
    if (id in rows || vendors.includes(id)) push(id);
  }
  const fleet = Object.keys(rows).filter((k) => !seen.has(k));
  const rest = vendors.filter((v) => !seen.has(v));
  return [...fleet, ...out, ...rest];
}

describe("mergeVisibleOrder", () => {
  it("keeps a hidden provider before the visible ones in its slot", () => {
    expect(mergeVisibleOrder(["agy", "image", "codex"], ["image", "codex"], ["codex", "image"])).toEqual([
      "agy",
      "codex",
      "image",
    ]);
  });

  it("keeps a hidden provider between the visible ones in its slot", () => {
    expect(mergeVisibleOrder(["image", "agy", "codex"], ["image", "codex"], ["codex", "image"])).toEqual([
      "codex",
      "agy",
      "image",
    ]);
  });

  // Allowed again, the hidden provider still ranks where the member put it.
  it("leaves the hidden provider's rank intact for when the policy allows it again", () => {
    const rows = { image: "comfy" };
    const merged = mergeVisibleOrder(["agy", "image", "codex"], ["image", "codex"], ["codex", "image"], rows);
    expect(agentReads(merged, rows, ["agy", "codex"])).toEqual(["agy", "codex", "image"]);
  });

  it("places a visible id the stored list does not name where it was shown", () => {
    expect(mergeVisibleOrder(["agy", "codex"], ["image", "codex"], ["codex", "image"])).toEqual([
      "codex",
      "agy",
      "image",
    ]);
  });

  it("writes nothing new for an answer that is not a permutation of what was shown", () => {
    expect(mergeVisibleOrder(["agy", "codex"], ["codex"], ["agy"])).toEqual(["agy", "codex"]);
  });
});

// A preference saved before ADR 0082 names the fleet's engine by its kind. Whatever the member
// does, the order the Agent then runs is the order the member last saw.
describe("mergeVisibleOrder with a legacy alias, round-tripped through the Agent's reading", () => {
  const vendors = ["agy", "codex"];

  it("moves the aliased row to the end, and the Agent runs it last", () => {
    const rows = { image: "comfy" };
    const stored = ["comfy", "agy", "codex"];
    const shown = agentReads(stored, rows, vendors);
    expect(shown).toEqual(["image", "agy", "codex"]);
    const merged = mergeVisibleOrder(stored, shown, ["agy", "codex", "image"], rows);
    expect(agentReads(merged, rows, vendors)).toEqual(["agy", "codex", "image"]);
  });

  it("keeps a hidden row of the alias's kind with the alias, after the visible rows of that kind", () => {
    const all = { image: "comfy", "comfy-lan": "comfy" };
    const visibleRows = { image: "comfy" };
    const stored = ["comfy", "agy", "codex"];
    const merged = mergeVisibleOrder(stored, ["image", "codex"], ["codex", "image"], visibleRows);
    // Allowed again, agy keeps its slot and comfy-lan comes right after image, where the alias now stands.
    expect(agentReads(merged, all, vendors)).toEqual(["codex", "agy", "image", "comfy-lan"]);
  });

  it("leaves an alias whose rows are all hidden in its slot", () => {
    expect(mergeVisibleOrder(["comfy", "agy", "codex"], ["agy", "codex"], ["codex", "agy"])).toEqual([
      "comfy",
      "codex",
      "agy",
    ]);
  });
});
