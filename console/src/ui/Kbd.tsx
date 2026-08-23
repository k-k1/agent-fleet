// Kbd — keycap chips for shortcut hints (which-key, palette, cheat-sheet, tooltips).
// Takes a chord string ("mod+shift+k") and renders per-platform keycaps (⌘/⌥ on
// macOS, Ctrl/Alt elsewhere). Display only — key matching lives in lib/keys/chords.ts.
import { parseChord } from "../lib/keys/chords.ts";
import { isMac } from "../lib/device.ts";

const SYMBOL: Record<string, string> = {
  arrowup: "↑",
  arrowdown: "↓",
  arrowleft: "←",
  arrowright: "→",
  enter: "⏎",
  escape: "Esc",
  backspace: "⌫",
  space: "Space",
  tab: "Tab",
  pageup: "PgUp",
  pagedown: "PgDn",
  home: "Home",
  end: "End",
};

function keyLabel(key: string): string {
  if (key in SYMBOL) return SYMBOL[key];
  if (/^f\d{1,2}$/.test(key)) return key.toUpperCase();
  return key.length === 1 ? key.toUpperCase() : key;
}

// Punctuation whose shifted form reads clearer as a single cap than "Shift + X"
// (e.g. Shift+/ = ?). Rendered as one keycap without a separate Shift chip.
const SHIFT_SYMBOL: Record<string, string> = { "/": "?" };

export function Kbd({ chord, className }: { chord: string; className?: string }) {
  const mac = isMac();
  const c = parseChord(chord);
  const shifted = c.shift ? SHIFT_SYMBOL[c.key] : undefined;
  const caps: string[] = [];
  if (c.mod) caps.push(mac ? "⌘" : "Ctrl");
  if (c.alt) caps.push(mac ? "⌥" : "Alt");
  if (c.shift && !shifted) caps.push(mac ? "⇧" : "Shift");
  if (c.key) caps.push(shifted ?? keyLabel(c.key));
  return (
    <span className={"ui-kbd" + (className ? " " + className : "")} aria-hidden="true">
      {caps.map((k, i) => (
        <kbd key={i}>{k}</kbd>
      ))}
    </span>
  );
}
