import type { McpElicitField } from "./store.ts";

// Characters that change how the text around them reads without showing themselves: everything
// Unicode says a renderer may ignore (direction controls, which can reorder an address on screen,
// joiners, variation selectors, tags, the fillers some scripts render as blank) and every other
// format character. Matched by code point, so supplementary-plane ones (tags) count too.
const HIDDEN_RE = /[\p{Default_Ignorable_Code_Point}\p{Cf}]/gu;
const isHidden = (c: string) => {
  HIDDEN_RE.lastIndex = 0;
  return HIDDEN_RE.test(c);
};

const hex = (n: number) => n.toString(16).toUpperCase().padStart(4, "0");

/** visible shows every hidden character of s as a marker, so text an agent or a server wrote
 *  cannot read differently from what it is. */
export function visible(s: string): string {
  return s.replace(HIDDEN_RE, (c) => `⟨U+${hex(c.codePointAt(0)!)}⟩`);
}

/** jsonEscape is c as a JSON string escape: the server reads it as the same character. */
function jsonEscape(c: string): string {
  let out = "";
  for (let i = 0; i < c.length; i++) out += "\\u" + hex(c.charCodeAt(i)); // a surrogate pair is two
  return out;
}

/** escapeHidden writes every hidden character of JSON text as its escape. Inside a string the
 *  server reads it as the same value; valid JSON has no such character outside one. The result
 *  shows each of them, and nothing in it can be mistaken for one (a marker spelled out in a
 *  string stays as written). */
export function escapeHidden(raw: string): string {
  let out = "";
  for (const c of raw) out += isHidden(c) ? jsonEscape(c) : c;
  return out;
}

// The layout gives up on text it would grow past this factor, and shows it unlaid: each level
// adds indentation to every line inside it, so a deeply nested argument would otherwise grow to
// many times its size and take the card down with it.
const MAX_GROWTH = 4;

/** indentJSON lays out JSON text over lines without parsing it: every string, number and name
 *  stays as written, because the card must show what the server runs (a parse would merge a
 *  member written twice and respell numbers). Hidden characters are written as their escapes
 *  first (escapeHidden), so they show the same whether or not the layout succeeds. */
export function indentJSON(raw: string): string {
  const text = escapeHidden(raw);
  const budget = text.length * MAX_GROWTH + 4096;
  const parts: string[] = [];
  let size = 0;
  let depth = 0;
  let inString = false;
  let escaped = false;
  const put = (s: string) => {
    parts.push(s);
    size += s.length;
  };
  const nl = () => "\n" + "  ".repeat(depth);
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (inString) {
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') inString = false;
      put(c);
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
        while (j < text.length && /\s/.test(text[j])) j++;
        if (text[j] === (c === "{" ? "}" : "]")) {
          put(c + text[j]);
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
        if (depth < 0) return text;
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
    if (size > budget) return text;
  }
  return depth === 0 && !inString ? parts.join("") : text;
}

/** valueText is a choice's value as the card shows it: hidden characters marked, and quoted when
 *  nothing of it would show (an empty or blank value). */
function valueText(v: string): string {
  return looks(v) ? visible(v) : visible(JSON.stringify(v));
}

/** optionTexts is what the card shows for each of a field's options, hidden characters marked.
 *  The relay cuts long labels, so two options can read the same (#5282): those also show their
 *  value, and when the result still has two alike (a label that spells another's label and
 *  value), every option is numbered too. An option with no label shows its value. */
export function optionTexts(field: McpElicitField): string[] {
  const labels = field.options.map((o) => (looks(o.label) ? visible(o.label) : valueText(o.value)));
  const counts = tally(labels);
  const texts = labels.map((l, i) => ((counts.get(looks(l)) ?? 0) > 1 ? `${l} (${valueText(field.options[i].value)})` : l));
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
