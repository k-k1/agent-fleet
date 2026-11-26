import type { McpElicitField } from "./store.ts";

// Characters that change how the text around them reads without showing themselves: direction
// controls (which can reorder an address on screen), joiners and other format characters,
// variation selectors and the fillers some scripts render as blank.
const HIDDEN_RE =
  /[\u00AD\u034F\u061C\u115F\u1160\u17B4\u17B5\u180B-\u180F\u200B-\u200F\u202A-\u202E\u2060-\u206F\u3164\uFE00-\uFE0F\uFEFF\uFFA0]/g;

const hex = (c: string) => c.charCodeAt(0).toString(16).toUpperCase().padStart(4, "0");

/** visible shows every hidden character of s as a marker, so text an agent or a server wrote
 *  cannot read differently from what it is. */
export function visible(s: string): string {
  return s.replace(HIDDEN_RE, (c) => `⟨U+${hex(c)}⟩`);
}

// The layout gives up on text it would grow past this factor: each level adds indentation to
// every line inside it, so a deeply nested argument would otherwise grow to many times its size
// and take the card down with it.
const MAX_GROWTH = 4;

/** indentJSON lays out JSON text over lines without parsing it: every string, number and name
 *  stays as written, because the card must show what the server runs (a parse would merge a
 *  member written twice and respell numbers). A hidden character inside a string is written as
 *  its JSON escape, which the server reads as the same string. Text it cannot follow, or that
 *  would grow too large, is returned as it is, with hidden characters marked (visible). */
export function indentJSON(raw: string): string {
  const parts: string[] = [];
  let size = 0;
  let depth = 0;
  let inString = false;
  let escaped = false;
  const fallback = () => visible(raw);
  const put = (s: string) => {
    parts.push(s);
    size += s.length;
  };
  const nl = () => "\n" + "  ".repeat(depth);
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i];
    if (inString) {
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') inString = false;
      HIDDEN_RE.lastIndex = 0;
      put(HIDDEN_RE.test(c) ? "\\u" + hex(c) : c);
      continue;
    }
    switch (c) {
      case '"':
        inString = true;
        put(c);
        break;
      case "{":
      case "[": {
        let j = i + 1;
        while (j < raw.length && /\s/.test(raw[j])) j++;
        if (raw[j] === (c === "{" ? "}" : "]")) {
          put(c + raw[j]);
          i = j;
          break;
        }
        depth++;
        put(c + nl());
        break;
      }
      case "}":
      case "]":
        depth--;
        if (depth < 0) return fallback();
        put(nl() + c);
        break;
      case ",":
        put(c + nl());
        break;
      case ":":
        put(": ");
        break;
      case " ":
      case "\t":
      case "\n":
      case "\r":
        break;
      default:
        put(c);
    }
    if (size > raw.length * MAX_GROWTH + 4096) return fallback();
  }
  return depth === 0 && !inString ? parts.join("") : fallback();
}

/** optionTexts is what the card shows for each of a field's options, hidden characters marked.
 *  The relay cuts long labels, so two options can read the same (#5282): those also show their
 *  value, and when the result still has two alike (a label that spells another's label and
 *  value), every option is numbered too. An option with no label shows its value. */
export function optionTexts(field: McpElicitField): string[] {
  const labels = field.options.map((o) => (looks(o.label) ? visible(o.label) : visible(o.value)));
  const counts = tally(labels);
  const texts = labels.map((l, i) => ((counts.get(looks(l)) ?? 0) > 1 ? `${l} (${visible(field.options[i].value)})` : l));
  if ([...tally(texts).values()].every((n) => n === 1)) return texts;
  return texts.map((t, i) => `${i + 1}. ${t}`);
}

function tally(texts: string[]): Map<string, number> {
  const m = new Map<string, number>();
  for (const t of texts) m.set(looks(t), (m.get(looks(t)) ?? 0) + 1);
  return m;
}

// looks is a text as it reads on screen: compatibility forms folded, hidden characters dropped
// and white space collapsed, so texts that differ only there count as the same.
function looks(s: string): string {
  return s.normalize("NFKC").replace(HIDDEN_RE, "").replace(/\s+/g, " ").trim();
}
