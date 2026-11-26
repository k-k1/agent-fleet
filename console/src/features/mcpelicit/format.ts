import type { McpElicitField } from "./store.ts";

/** indentJSON lays out JSON text over lines without parsing it: every string, number and name
 *  stays exactly as written, because the card must show what the server runs (a parse would
 *  merge a member written twice and respell numbers). Text it cannot follow is returned as is. */
export function indentJSON(raw: string): string {
  let out = "";
  let depth = 0;
  let inString = false;
  let escaped = false;
  const nl = () => "\n" + "  ".repeat(depth);
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i];
    if (inString) {
      out += c;
      if (escaped) escaped = false;
      else if (c === "\\") escaped = true;
      else if (c === '"') inString = false;
      continue;
    }
    switch (c) {
      case '"':
        inString = true;
        out += c;
        break;
      case "{":
      case "[": {
        let j = i + 1;
        while (j < raw.length && /\s/.test(raw[j])) j++;
        if (raw[j] === (c === "{" ? "}" : "]")) {
          out += c + raw[j];
          i = j;
          break;
        }
        depth++;
        out += c + nl();
        break;
      }
      case "}":
      case "]":
        depth--;
        if (depth < 0) return raw;
        out += nl() + c;
        break;
      case ",":
        out += c + nl();
        break;
      case ":":
        out += ": ";
        break;
      case " ":
      case "\t":
      case "\n":
      case "\r":
        break;
      default:
        out += c;
    }
  }
  return depth === 0 && !inString ? out : raw;
}

/** optionTexts is what the card shows for each of a field's options. The relay cuts long labels,
 *  so two options can carry the same label; those, and an option with no label, also show their
 *  value, or the member could not tell which one they pick (#5282). */
export function optionTexts(field: McpElicitField): string[] {
  const seen = new Map<string, number>();
  for (const o of field.options) seen.set(looks(o.label), (seen.get(looks(o.label)) ?? 0) + 1);
  return field.options.map((o) => {
    const key = looks(o.label);
    if (!key) return o.value;
    return (seen.get(key) ?? 0) > 1 ? `${o.label} (${o.value})` : o.label;
  });
}

// looks is a label as it reads on screen: compatibility forms folded, invisible characters
// dropped and white space collapsed, so labels that differ only there count as the same.
function looks(label: string): string {
  return label
    .normalize("NFKC")
    .replace(/[\u00AD\u034F\u061C\u115F\u1160\u17B4\u17B5\u180B-\u180F\u200B-\u200F\u202A-\u202E\u2060-\u206F\u3164\uFE00-\uFE0F\uFEFF\uFFA0]/g, "")
    .replace(/\s+/g, " ")
    .trim();
}
