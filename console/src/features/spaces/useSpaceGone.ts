// Whether the space a launch was pointed at is known to be gone: the list is this workspace's own
// and no longer holds it. A launch aimed at it is stopped and says so, never sent to another space
// (or a new one) in its place: the draft and its files were written for that space's work.
import { spacesFor, spacesOwner, useSpacesStore } from "./store.ts";

export function useSpaceGone(folder: string): boolean {
  const owner = spacesOwner();
  const spaces = useSpacesStore((s) => spacesFor(s, owner));
  const loaded = useSpacesStore((s) => s.owner === owner);
  return !!folder && loaded && !spaces.some((s) => s.folder === folder);
}
