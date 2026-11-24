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

type Attr = { name: string; value: string | null };
type Tag = { kind: string; line: number; attrs: Attr[]; spreads: string[] };

// Skips a balanced {…} expression starting at `i` (src[i] === "{"), honouring strings and template
// literals, and returns the index after the closing brace.
function skipBraces(src: string, i: number): number {
  let depth = 0;
  let q: string | null = null;
  for (; i < src.length; i++) {
    const c = src[i];
    if (q) {
      if (c === "\\") i++;
      else if (c === q) q = null;
      continue;
    }
    if (c === '"' || c === "'" || c === "`") q = c;
    else if (c === "{") depth++;
    else if (c === "}" && --depth === 0) return i + 1;
  }
  return i;
}

// Parses every <input>/<textarea> opening tag into real attributes and spreads, so that text inside
// a value (placeholder="autoComplete", data-type="checkbox") is never mistaken for an attribute.
export function parseControls(src: string): Tag[] {
  const res: Tag[] = [];
  const re = /<(input|textarea)(?=[\s/>])/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src))) {
    let i = m.index + m[0].length;
    const attrs: Attr[] = [];
    const spreads: string[] = [];
    for (; i < src.length; ) {
      while (/\s/.test(src[i])) i++;
      if (src.startsWith("//", i)) {
        i = src.indexOf("\n", i);
        continue;
      }
      if (src.startsWith("/*", i)) {
        i = src.indexOf("*/", i) + 2;
        continue;
      }
      if (src[i] === ">" || (src[i] === "/" && src[i + 1] === ">")) break;
      if (src[i] === "{") {
        const e = skipBraces(src, i);
        spreads.push(src.slice(i + 1, e - 1).replace(/^\s*\.\.\./, "").trim());
        i = e;
        continue;
      }
      const n = /^[\w:-]+/.exec(src.slice(i));
      if (!n) break;
      i += n[0].length;
      let value: string | null = null;
      if (src[i] === "=") {
        i++;
        if (src[i] === '"' || src[i] === "'") {
          const e = src.indexOf(src[i], i + 1);
          value = src.slice(i + 1, e);
          i = e + 1;
        } else if (src[i] === "{") {
          const e = skipBraces(src, i);
          value = "{" + src.slice(i + 1, e - 1).trim() + "}";
          i = e;
        }
      }
      attrs.push({ name: n[0], value });
    }
    res.push({ kind: m[1], line: src.slice(0, m.index).split("\n").length, attrs, spreads });
  }
  return res;
}

export function needsOptOut(t: Tag): boolean {
  if (t.attrs.some((a) => a.name === "autoComplete")) return false;
  if (t.spreads.includes("NO_AUTOFILL")) return false;
  if (t.kind === "textarea") return true;
  const ty = t.attrs.find((a) => a.name === "type")?.value;
  if (ty == null) return true;
  const lit = /^\{\s*["']([\w-]+)["']\s*\}$/.exec(ty)?.[1] ?? (ty.startsWith("{") ? null : ty);
  // A dynamic type can render as text, so it counts as text.
  return lit === null || lit === "text" || lit === "search" || lit === "url";
}

const first = (src: string) => parseControls(src)[0];

describe("autofill opt-out guard", () => {
  it("detects an unmarked control and accepts a marked one", () => {
    expect(needsOptOut(first("<input value={a} onChange={(e) => f(e)} />"))).toBe(true);
    expect(needsOptOut(first("<textarea rows={2}>"))).toBe(true);
    expect(needsOptOut(first('<input type="search" />'))).toBe(true);
    expect(needsOptOut(first("<input {...NO_AUTOFILL} />"))).toBe(false);
    expect(needsOptOut(first('<input autoComplete="new-password" />'))).toBe(false);
    expect(needsOptOut(first('<input type="checkbox" />'))).toBe(false);
    expect(needsOptOut(first('<input type={"radio"} />'))).toBe(false);
  });

  it("is not fooled by text inside attribute values or dynamic types", () => {
    expect(needsOptOut(first('<input placeholder="autoComplete" />'))).toBe(true);
    expect(needsOptOut(first('<input placeholder="NO_AUTOFILL" />'))).toBe(true);
    expect(needsOptOut(first('<input data-type="checkbox" />'))).toBe(true);
    expect(needsOptOut(first("<input type={kind} />"))).toBe(true);
    expect(needsOptOut(first("<input {...rest} />"))).toBe(true);
    expect(needsOptOut(first("<input title={`a > b`} onChange={(e) => g(e)} />"))).toBe(true);
    expect(needsOptOut(first("<input\n  // it's a note\n  type=\"checkbox\" />"))).toBe(false);
    expect(parseControls("<input a={1} />\n<textarea />").map((t) => t.kind)).toEqual(["input", "textarea"]);
  });

  it("keeps real autofill on personal-data and credential fields", () => {
    const want: [string, string][] = [
      ["src/features/settings/connect/GitTab.tsx", 'autoComplete="email"'],
      ["src/features/settings/connect/GitTab.tsx", 'autoComplete="name"'],
      ["src/features/settings/connect/GitTab.tsx", 'autoComplete="username"'],
      ["src/features/scm/ChangesView.tsx", 'autoComplete="email"'],
      ["src/features/settings/tenant/tenantMembers.tsx", 'autoComplete="email"'],
    ];
    for (const [f, needle] of want) expect(fs.readFileSync(f, "utf8")).toContain(needle);
  });

  it("has no unmarked free-text control in console/src", () => {
    const bad: string[] = [];
    for (const f of files("src")) {
      const src = fs.readFileSync(f, "utf8");
      parseControls(src).forEach((t, n) => {
        if (needsOptOut(t) && !ALLOW[`${f}:${n}`]) bad.push(`${f}:${t.line}`);
      });
    }
    expect(bad).toEqual([]);
  });
});
