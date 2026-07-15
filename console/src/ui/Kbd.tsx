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
  space: "Space",
  tab: "Tab",
};

function keyLabel(key: string): string {
  if (key in SYMBOL) return SYMBOL[key];
  if (/^f\d{1,2}$/.test(key)) return key.toUpperCase();
  return key.length === 1 ? key.toUpperCase() : key;
}

export function Kbd({ chord, className }: { chord: string; className?: string }) {
  const mac = isMac();
  const c = parseChord(chord);
  const caps: string[] = [];
  if (c.mod) caps.push(mac ? "⌘" : "Ctrl");
  if (c.alt) caps.push(mac ? "⌥" : "Alt");
  if (c.shift) caps.push(mac ? "⇧" : "Shift");
  if (c.key) caps.push(keyLabel(c.key));
  return (
    <span className={"ui-kbd" + (className ? " " + className : "")} aria-hidden="true">
      {caps.map((k, i) => (
        <kbd key={i}>{k}</kbd>
      ))}
    </span>
  );
}
