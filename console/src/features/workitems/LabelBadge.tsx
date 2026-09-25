import type { CSSProperties } from "react";
import { labelBadgeColors } from "./labelColor.ts";

/** One label as a filled badge in the tracker's colour (#993). The colours go in as custom
 * properties so the stylesheet keeps the shape, the edge and any theme-specific treatment. */
export function LabelBadge({ name, color }: { name: string; color?: string }) {
  const { bg, fg } = labelBadgeColors(name, color);
  return (
    <span className="wi-label" style={{ "--wi-label-bg": bg, "--wi-label-fg": fg } as CSSProperties}>
      {name}
    </span>
  );
}
