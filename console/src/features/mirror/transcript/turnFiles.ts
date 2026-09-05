// turnFiles — which files THIS turn edited (docs/log/68 P1).
//
// The head's changed-files strip (「変更ファイル」) answers "what did this session change"; this
// answers "what did this reply change", which is the question a reader has while scrolling.
// Until now the only route to that was to expand a collapsed ToolRun and read tool
// traces, and edits to one file spread over several calls looked like several things.
//
// Folded per file so a turn that rewrites one file five times shows one chip.
import type { Part } from "./types.ts";

export interface TurnFile {
  file: string; // the path as the agent wrote it (display + the diff pane's title)
  name: string; // basename
  tool: string; // the first tool that touched it, for the diff pane's header
  verb: string; // "" | "edit" | "add" | "delete"
  edits: NonNullable<Part["edits"]>;
}

const baseName = (p: string) => p.split("/").filter(Boolean).pop() || p;

/**
 * Collect the turn's edit-family parts, in the order the files were first touched.
 *
 * A part with a `file` but no `edits` still counts: that is how a delete arrives (codex's
 * patch header, cursor's Delete) and how kinds that carry no diff bodies report an edit.
 * The chip is then a coordinate without a diff — which is still more than the reader had.
 */
export function turnFiles(parts: Part[] | undefined): TurnFile[] {
  const order: string[] = [];
  const byFile = new Map<string, TurnFile>();
  for (const p of parts || []) {
    if (p.kind !== "tool" || !p.file) continue;
    let f = byFile.get(p.file);
    if (!f) {
      f = { file: p.file, name: baseName(p.file), tool: p.tool || "", verb: p.verb || "", edits: [] };
      byFile.set(p.file, f);
      order.push(p.file);
    }
    if (p.edits?.length) f.edits = [...f.edits, ...p.edits];
    // The last call is what the file IS now — a file written and then deleted is deleted.
    if (p.verb) f.verb = p.verb;
  }
  return order.map((k) => byFile.get(k)!);
}

/** The synthetic part a chip hands to the diff pane: one file's edits across the turn. */
export const chipPart = (f: TurnFile): Part => ({
  kind: "tool",
  tool: f.tool,
  file: f.file,
  edits: f.edits,
});
