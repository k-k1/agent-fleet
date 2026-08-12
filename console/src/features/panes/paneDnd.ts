export type PaneDropZone = "center" | "down" | "right";

/** A lone Cell cannot be dragged as geometry, but its tab can still create the
 * second Cell. Keep those permissions independent. */
export function acceptsPaneDrag(canDragCell: boolean, hasCellPayload: boolean, hasTabPayload: boolean): boolean {
  return hasTabPayload || (canDragCell && hasCellPayload);
}

/** Tab buttons own center drops (reorder); edge drops bubble to the Cell. */
export const tabOwnsDrop = (zone: PaneDropZone): boolean => zone === "center";
