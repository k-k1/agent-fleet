// keyHint — the shortcut for a command id as plain text, for button tooltips (P4
// inline hints). Reads the registry, so a button's hint always matches the real
// binding. e.g. keyHint("pane.splitRight") → "⌘K P R" (mac) / "Ctrl+K P R".
// hintSuffix wraps it in the parenthesized form apps append to a title string.
import { isMac } from "../../lib/device.ts";
import { getLocale } from "../../lib/i18n/index.ts";
import { parseChord } from "../../lib/keys/chords.ts";
import { effectiveCommands, boundChord, APP_LEADER } from "./bindings.ts";

function chordText(chord: string): string {
  const c = parseChord(chord);
  const parts: string[] = [];
  if (c.mod) parts.push(isMac() ? "⌘" : "Ctrl");
  if (c.alt) parts.push(isMac() ? "⌥" : "Alt");
  // "?" reads clearer than "Shift+/".
  if (c.shift && c.key === "/") return [...parts, "?"].join("+");
  if (c.shift) parts.push(isMac() ? "⇧" : "Shift");
  parts.push(c.key.length === 1 ? c.key.toUpperCase() : c.key);
  return parts.join("+");
}

export function keyHint(id: string): string | null {
  const c = effectiveCommands().find((x) => x.id === id);
  if (!c) return null;
  if (c.keys && c.keys.length) return chordText(c.keys[0]);
  if (c.seq) {
    const leader = boundChord(APP_LEADER) || "mod+k";
    return [chordText(leader), ...c.seq.split(" ").map(chordText)].join(" ");
  }
  return null; // no direct key and (if it had one) the user unbound it
}

/** "（<hint>）" for appending to a button title, or "" when the command has no key.
 * The parens follow the locale: full-width（）for Japanese, " (…)" for English —
 * hardcoding the full-width pair would leak it into English titles like
 * "Toggle…（Ctrl+K W B）". */
export function hintSuffix(id: string): string {
  const h = keyHint(id);
  if (!h) return "";
  return getLocale() === "ja" ? `（${h}）` : ` (${h})`;
}
