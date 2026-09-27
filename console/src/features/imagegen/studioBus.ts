// studioBus — "the list of studios changed" within this window. Panes each hold their own list
// (useStudioList); this is how a create, rename or delete in one pane reaches the others.
// Other windows and devices catch up on their next mount.
const listeners = new Set<() => void>();

export function studiosChanged(): void {
  for (const l of [...listeners]) l();
}

export function onStudiosChanged(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
