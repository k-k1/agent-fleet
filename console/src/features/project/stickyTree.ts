export interface StickyTreeRow {
  path: string;
  type: string;
  depth: number;
}

/** Return the open directory lineage at `through` in a pre-order flat tree. */
export function stickyAncestors<T extends StickyTreeRow>(rows: T[], through: number, limit = 7): T[] {
  if (through < 0 || limit <= 0) return [];
  const stack: T[] = [];
  for (let i = 0; i <= Math.min(through, rows.length - 1); i++) {
    const row = rows[i];
    while (stack.length && stack[stack.length - 1].depth >= row.depth) stack.pop();
    if (row.type === "dir") stack.push(row);
  }
  return stack.slice(0, limit);
}
