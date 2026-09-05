// Pure key-chord parsing / matching for the keyboard command system (docs: keymap
// redesign). No DOM mutation, no React — the dispatcher (dispatcher.ts) and the
// command registry (registry.ts) build on these, and vitest covers this file.
//
// A chord is a canonical string: zero or more modifiers then one base key, joined by
// "+", e.g. "mod+k", "alt+1", "shift+f6", "escape". Rules:
//   - "mod" = Ctrl on Windows/Linux OR ⌘ (Meta) on macOS. The whole app treats the
//     two as equivalent (the `e.ctrlKey || e.metaKey` idiom), so one token covers both
//     platforms and a binding written once works everywhere.
//   - The base key comes from KeyboardEvent.code, NOT .key, so Alt/Shift can't mutate
//     it: on macOS ⌥+1 yields e.key "¡" and Shift+k yields "K", but .code stays
//     "Digit1" / "KeyK". Shift is kept as its own modifier, so "k" and "shift+k" are
//     distinct bindings (the pane map uses h/j/k/l to focus vs H/J/K/L to move).
//   - Modifiers are emitted in a fixed order (mod, alt, shift) so a binding string and
//     a live event canonicalize identically and compare by plain string equality.

export interface Chord {
  mod: boolean; // ctrl OR meta (⌘)
  alt: boolean;
  shift: boolean;
  key: string; // canonical base key, lowercase
}

// KeyboardEvent.code → canonical base key. Letters / digits / F-keys / arrows are
// fully layout-stable; the punctuation map uses US-keycap naming (our few symbol
// bindings — [ ] , . ; - = / \ — are rare and rebindable, an accepted limitation on
// exotic layouts).
const CODE_SYMBOL: Record<string, string> = {
  Backslash: "\\",
  BracketLeft: "[",
  BracketRight: "]",
  Comma: ",",
  Period: ".",
  Semicolon: ";",
  Quote: "'",
  Backquote: "`",
  Minus: "-",
  Equal: "=",
  Slash: "/",
  Space: "space",
  Enter: "enter",
  NumpadEnter: "enter",
  Tab: "tab",
  Escape: "escape",
  Backspace: "backspace",
  Delete: "delete",
};

export function codeToKey(code: string): string {
  const letter = /^Key([A-Z])$/.exec(code);
  if (letter) return letter[1].toLowerCase();
  const digit = /^(?:Digit|Numpad)([0-9])$/.exec(code);
  if (digit) return digit[1];
  const fkey = /^F([1-9]|1[0-9]|2[0-4])$/.exec(code);
  if (fkey) return "f" + fkey[1];
  const arrow = /^Arrow(Up|Down|Left|Right)$/.exec(code);
  if (arrow) return "arrow" + arrow[1].toLowerCase();
  if (code in CODE_SYMBOL) return CODE_SYMBOL[code];
  return code.toLowerCase();
}

const MODIFIER_CODES = new Set([
  "ShiftLeft",
  "ShiftRight",
  "ControlLeft",
  "ControlRight",
  "AltLeft",
  "AltRight",
  "MetaLeft",
  "MetaRight",
]);

// eventToChord returns null for a modifier-only keydown (pressing Shift on the way to
// an action letter must not resolve/cancel a chord) — callers read null as "no chord
// yet, keep waiting".
export function eventToChord(e: KeyboardEvent): Chord | null {
  if (MODIFIER_CODES.has(e.code)) return null;
  return {
    mod: e.ctrlKey || e.metaKey,
    alt: e.altKey,
    shift: e.shiftKey,
    key: codeToKey(e.code),
  };
}

export function formatChord(c: Chord): string {
  const parts: string[] = [];
  if (c.mod) parts.push("mod");
  if (c.alt) parts.push("alt");
  if (c.shift) parts.push("shift");
  parts.push(c.key);
  return parts.join("+");
}

export function parseChord(s: string): Chord {
  const c: Chord = { mod: false, alt: false, shift: false, key: "" };
  for (const raw of s.toLowerCase().split("+")) {
    const p = raw.trim();
    if (!p) continue;
    if (p === "mod" || p === "ctrl" || p === "control" || p === "cmd" || p === "meta" || p === "⌘") c.mod = true;
    else if (p === "alt" || p === "opt" || p === "option" || p === "⌥") c.alt = true;
    else if (p === "shift" || p === "⇧") c.shift = true;
    else c.key = p;
  }
  return c;
}

// canonical normalizes any hand-written binding string to its comparable form, so the
// registry can store "Mod+Shift+K" and still match by equality.
export const canonical = (s: string): string => formatChord(parseChord(s));

// eventChordString: the live event's canonical chord, or null if it was modifier-only.
export function eventChordString(e: KeyboardEvent): string | null {
  const c = eventToChord(e);
  return c ? formatChord(c) : null;
}

// eventKeyChordString: a SECONDARY chord built from KeyboardEvent.key instead of .code,
// for punctuation whose physical position differs across keyboard layouts. Our .code map
// uses US-keycap naming, so on a Japanese (JIS) keyboard Alt+] reports .code "Backslash"
// → the primary chord is "alt+\\" and never matches an "alt+]" binding, even though .key
// is still "]". The dispatcher tries this ONLY as a fallback when the .code chord matched
// no command, so US bindings are untouched. Restricted to a single non-alphanumeric
// printable char: letters/digits are already layout-stable via .code, and named keys
// ("Enter", "Tab", "ArrowLeft") come through the .code map. .key already reflects Shift
// (']' vs '}'), so shift is not folded in again.
export function eventKeyChordString(e: KeyboardEvent): string | null {
  if (MODIFIER_CODES.has(e.code)) return null;
  const k = e.key;
  if (!k || k.length !== 1 || /[a-z0-9]/i.test(k)) return null;
  return formatChord({ mod: e.ctrlKey || e.metaKey, alt: e.altKey, shift: false, key: k.toLowerCase() });
}

// shouldIgnore: never dispatch while an IME is composing (Japanese input reports
// isComposing / keyCode 229 while converting), nor on auto-repeat (holding a key must not
// fire a command over and over).
export function shouldIgnore(e: KeyboardEvent): boolean {
  return e.isComposing === true || e.keyCode === 229 || e.repeat === true;
}
