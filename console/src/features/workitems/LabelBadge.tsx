import type { CSSProperties } from "react";
import { labelColor } from "./labelColor.ts";

/** One label as a badge in the tracker's colour (#993). The colour goes in as a custom property
 * so the stylesheet decides how it is drawn per theme. */
export function LabelBadge({ name, color }: { name: string; color?: string }) {
  return (
    <span className="wi-label" style={{ "--wi-label-color": labelColor(name, color) } as CSSProperties}>
      {name}
    </span>
  );
}
