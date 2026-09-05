// Pure logic deciding whose font size a pane's zoom command moves.
//
// Font size is held as one global setting per surface - the same value the Settings > Display
// stepper and the read-aloud view's +/- buttons move. The keyboard zoom follows that and moves
// the setting of the surface the active pane belongs to. Adding no new persisted state means
// the settings screen, cross-device sync and reset-to-default all keep working unchanged.
//
// Imports neither the store nor the DOM (the rule for lib); the layout types are borrowed
// type-only.
import type { PaneContent } from "../layout/types.ts";
import { imageFormat } from "./filemeta.ts";

/** The four surfaces that carry a font size (same key names as in lib/settings.ts). */
export type FontSetting = "termSize" | "viewerSize" | "chatSize" | "readerSize";

// Same range as the Settings > Display stepper; both read these constants so they cannot drift.
export const FONT_MIN = 9;
export const FONT_MAX = 28;

/** The setting key that governs this pane's font size. Null means a surface with no text of
 *  its own (browser, image): the caller then lets the key fall through to the terminal. */
export function fontSettingFor(content: PaneContent | null | undefined): FontSetting | null {
  if (!content) return null; // empty cell
  switch (content.kind) {
    // With chat=true the terminal pane draws the mirror (a conversation), so it belongs to
    // the chat surface.
    case "terminal":
      return content.chat ? "chatSize" : "termSize";
    case "chat":
    case "sharedSession":
      return "chatSize";
    case "read":
      return "readerSize";
    // Only images carry no text. drawio stays in scope because it toggles between diagram
    // and source.
    case "file":
      return imageFormat(content.filePath) ? null : "viewerSize";
    case "diff":
    case "wtdiff":
    case "scm":
    case "changes":
    case "commit":
    case "doc":
      return "viewerSize";
    // browser / browserAttach: zooming there is the page's own, not this setting.
    default:
      return null;
  }
}

/** The value one step away, clamped to the allowed range. */
export const stepFontSize = (current: number, delta: number): number =>
  Math.min(FONT_MAX, Math.max(FONT_MIN, current + delta));
