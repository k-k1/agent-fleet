// pinFirst returns a copy of `items` with the entries matching `isPinned` moved to
// the front, preserving the relative order of everything else (stable). Used to pin
// the attached session and its repo to the top of the left-pane lists.
export function pinFirst(items, isPinned) {
  return [...items].sort((a, b) => (isPinned(a) ? -1 : 0) - (isPinned(b) ? -1 : 0));
}
