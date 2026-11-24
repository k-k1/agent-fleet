// Guard: a free-text <input> (text/search/url/no type) or <textarea> must opt out of browser
// autofill, otherwise Chrome may pop the saved e-mail/address list over it. Spread NO_AUTOFILL
// (src/ui/noAutofill.ts) or set `autoComplete` explicitly (e.g. "new-password" on a credential).
import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// Intentional exceptions: "<file>:<nth control in that file>" with the reason. Keep empty unless
// a control really wants autofill.
const ALLOW: Record<string, string> = {};

function files(dir: string, out: string[] = []): string[] {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) files(p, out);
    else if (/\.tsx$/.test(e.name) && !/\.test\./.test(e.name)) out.push(p);
  }
  return out;
}

// Returns the opening tag text of every <input>/<textarea> in `src`, brace- and quote-aware so
// `=>` and `>` inside `{…}` attribute expressions do not end the tag early.
export function openingTags(src: string): { kind: string; tag: string; line: number }[] {
  const res: { kind: string; tag: string; line: number }[] = [];
  const re = /<(input|textarea)\b/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src))) {
    let i = m.index + m[0].length;
    let depth = 0;
    let q: string | null = null;
    for (; i < src.length; i++) {
      const c = src[i];
      if (q) {
        if (c === q) q = null;
        continue;
      }
      if (c === '"' || c === "'" || c === "`") q = c;
      else if (c === "{") depth++;
      else if (c === "}") depth--;
      else if (c === ">" && depth === 0) break;
    }
    res.push({ kind: m[1], tag: src.slice(m.index, i + 1), line: src.slice(0, m.index).split("\n").length });
  }
  return res;
}

export function needsOptOut(kind: string, tag: string): boolean {
  if (/autoComplete|NO_AUTOFILL/.test(tag)) return false;
  if (kind === "textarea") return true;
  const t = tag.match(/\btype=["{]+([a-z-]+)/)?.[1] ?? "text";
  return t === "text" || t === "search" || t === "url";
}

describe("autofill opt-out guard", () => {
  it("detects an unmarked control and accepts a marked one", () => {
    expect(needsOptOut("input", '<input value={a} onChange={(e) => f(e)} />')).toBe(true);
    expect(needsOptOut("textarea", "<textarea rows={2}>")).toBe(true);
    expect(needsOptOut("input", '<input type="search" />')).toBe(true);
    expect(needsOptOut("input", "<input {...NO_AUTOFILL} />")).toBe(false);
    expect(needsOptOut("input", '<input autoComplete="new-password" />')).toBe(false);
    expect(needsOptOut("input", '<input type="checkbox" />')).toBe(false);
    const tags = openingTags('<input onChange={(e) => x(e)} value={v} />\n<textarea />');
    expect(tags.map((t) => t.kind)).toEqual(["input", "textarea"]);
  });

  it("has no unmarked free-text control in console/src", () => {
    const bad: string[] = [];
    for (const f of files("src")) {
      const src = fs.readFileSync(f, "utf8");
      openingTags(src).forEach((t, n) => {
        if (needsOptOut(t.kind, t.tag) && !ALLOW[`${f}:${n}`]) bad.push(`${f}:${t.line}`);
      });
    }
    expect(bad).toEqual([]);
  });
});
