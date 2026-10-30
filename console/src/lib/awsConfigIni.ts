// awsConfigIni — reads an AWS CLI config file the way the CLI does (issue #3010).
//
// The AWS CLI v2 reads ~/.aws/config with Python's configparser.RawConfigParser in its
// default strict mode, then botocore maps sections to profiles. A generic INI reader
// disagrees with it on exactly the files people have hand-edited for years: an indented
// line is a continuation of the previous value (not a new key), `#` mid-line is part of the
// value, a section or key given twice fails the whole file, and [DEFAULT] lends its keys to
// every section. Importing what the CLI would refuse, or reading a value the CLI reads
// differently, would turn the member's working setup into a different one in Settings, so
// this mirrors configparser rather than approximating it. The Agent's own reader
// (workspace/agent/internal/awsx/ini.go) holds the same rules.
//
// Pure: no fetch, no React. Values never appear in an error; an error names a line and,
// at most, a key, because the text may hold secrets.

export interface IniValue {
  /** The value as configparser returns it: continuation lines joined with "\n". */
  value: string;
  line: number;
}

export interface IniSection {
  /** The raw header text between the brackets, e.g. `profile prod`. */
  header: string;
  line: number;
  /** Own keys only (lower-cased, as configparser's optionxform does); [DEFAULT] is separate. */
  options: Map<string, IniValue>;
}

export interface IniFile {
  sections: IniSection[];
  defaults: Map<string, IniValue>;
  /** A UTF-8 BOM was removed before reading. The CLI itself cannot read such a file. */
  bomStripped: boolean;
}

export type IniErrorCode = "no_section_header" | "duplicate_section" | "duplicate_key" | "bad_line" | "bad_nested";

export interface IniError {
  code: IniErrorCode;
  line: number;
  /** The key, for duplicate_key; never a value. */
  key?: string;
}

const COMMENT_PREFIXES = ["#", ";"];
const SECTION_RE = /^\[(.+)\]/;
// configparser's _OPT_TMPL with delimiters "=" and ":": the key is everything up to the first
// delimiter, non-greedy.
const OPTION_RE = /^(.*?)\s*([=:])\s*(.*)$/;

export function parseAwsIni(text: string): { ok: true; file: IniFile } | { ok: false; error: IniError } {
  let bomStripped = false;
  if (text.charCodeAt(0) === 0xfeff) {
    text = text.slice(1);
    bomStripped = true;
  }
  const sections: IniSection[] = [];
  const defaults = new Map<string, IniValue>();
  // Raw values while reading: an array of lines per key, joined at the end, as configparser does.
  const pending = new Map<Map<string, IniValue>, Map<string, string[]>>();
  const seenSections = new Set<string>();
  const added = new Set<string>();
  let cur: Map<string, IniValue> | null = null;
  let curName = "";
  let optname: string | null = null;
  let indentLevel = 0;
  const lines = text.split(/\r\n|\n|\r/);
  for (let i = 0; i < lines.length; i++) {
    const lineno = i + 1;
    const line = lines[i];
    const stripped = line.trim();
    const isComment = COMMENT_PREFIXES.some((p) => stripped.startsWith(p));
    if (stripped === "" || isComment) {
      // A blank line inside a value is kept (empty_lines_in_values); a comment line is not.
      if (!isComment && cur && optname !== null) pending.get(cur)!.get(optname)!.push("");
      continue;
    }
    const indent = line.length - line.trimStart().length;
    if (cur && optname !== null && indent > indentLevel) {
      pending.get(cur)!.get(optname)!.push(stripped);
      continue;
    }
    indentLevel = indent;
    const sm = SECTION_RE.exec(stripped);
    if (sm) {
      const name = sm[1];
      optname = null;
      if (name === "DEFAULT") {
        cur = defaults;
      } else {
        if (seenSections.has(name)) return { ok: false, error: { code: "duplicate_section", line: lineno } };
        seenSections.add(name);
        const sec: IniSection = { header: name, line: lineno, options: new Map() };
        sections.push(sec);
        cur = sec.options;
      }
      curName = name;
      if (!pending.has(cur)) pending.set(cur, new Map());
      continue;
    }
    if (!cur) return { ok: false, error: { code: "no_section_header", line: lineno } };
    const om = OPTION_RE.exec(stripped);
    if (!om || om[1].trimEnd() === "") return { ok: false, error: { code: "bad_line", line: lineno } };
    const key = om[1].trimEnd().toLowerCase();
    const id = curName + "\u0000" + key;
    // [DEFAULT] may be reopened, but a key repeated across its blocks is still a duplicate.
    if (added.has(id)) return { ok: false, error: { code: "duplicate_key", line: lineno, key } };
    added.add(id);
    optname = key;
    pending.get(cur)!.set(key, [om[3].trim()]);
    cur.set(key, { value: "", line: lineno });
  }
  for (const [target, values] of pending) {
    for (const [k, parts] of values) {
      // Trailing blank lines are not part of the value (configparser rstrips the join).
      target.get(k)!.value = parts.join("\n").replace(/\s+$/, "");
    }
  }
  // botocore reads a value that starts on the next line as a nested block of `k = v` lines and
  // fails the whole file when one has no "=" (configloader._parse_nested). It does so on each
  // section's effective values, so a bad [DEFAULT] value a section overrides is never reached.
  const file: IniFile = { sections, defaults, bomStripped };
  for (const sec of sections) {
    for (const v of effectiveOptions(file, sec).values()) {
      if (nestedDict(v.value) === null) return { ok: false, error: { code: "bad_nested", line: v.line } };
    }
  }
  return { ok: true, file };
}

/** A nested value (one that starts on the next line) as botocore reads it: a dict filled line
 *  by line, so a repeated key keeps its last value (configloader._parse_nested). Null when a
 *  line has no "="; undefined for a plain value. */
export function nestedDict(value: string): Map<string, string> | null | undefined {
  if (!value.startsWith("\n")) return undefined;
  const out = new Map<string, string>();
  for (const l of value.split("\n")) {
    const t = l.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i < 0) return null;
    out.set(t.slice(0, i).trim(), t.slice(i + 1).trim());
  }
  return out;
}

/** A value in a form that is equal exactly when botocore's values are: a string and a dict
 *  never match, and a dict compares by its pairs whatever their order or spelling. */
export function botocoreValue(value: string): string {
  const d = nestedDict(value);
  if (!d) return JSON.stringify(["s", value]);
  return JSON.stringify(["d", [...d].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))]);
}

/** Effective options of a section: its own keys over [DEFAULT]'s, as configparser's
 *  options()/get() return them. */
export function effectiveOptions(file: IniFile, sec: IniSection): Map<string, IniValue> {
  const out = new Map(file.defaults);
  for (const [k, v] of sec.options) out.set(k, v);
  return out;
}

// botocore splits a section header with Python's shlex (POSIX mode): `profile "prod app"` names
// `prod app`, and a header that does not split into exactly two words names no profile at all.
// Only space, tab, CR and LF separate words there; a no-break space is part of a name.
function shlexSplit(s: string): string[] | null {
  const out: string[] = [];
  let cur = "";
  let inWord = false;
  let quote: string | null = null;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (quote) {
      if (c === quote) quote = null;
      else if (c === "\\" && quote === '"' && i + 1 < s.length && (s[i + 1] === '"' || s[i + 1] === "\\")) cur += s[++i];
      else cur += c;
      continue;
    }
    if (c === '"' || c === "'") {
      quote = c;
      inWord = true;
    } else if (c === " " || c === "\t" || c === "\r" || c === "\n") {
      if (inWord) out.push(cur);
      cur = "";
      inWord = false;
    } else if (c === "\\") {
      // Python's shlex refuses a trailing backslash ("No escaped character").
      if (i + 1 >= s.length) return null;
      cur += s[++i];
      inWord = true;
    } else {
      cur += c;
      inWord = true;
    }
  }
  if (quote) return null;
  if (inWord) out.push(cur);
  return out;
}

export type SectionRole = { kind: "profile"; name: string } | { kind: "sso-session"; name: string } | { kind: "other" };

/** What botocore makes of a section header. */
export function sectionRole(header: string): SectionRole {
  if (header === "default") return { kind: "profile", name: "default" };
  for (const [prefix, kind] of [
    ["profile", "profile"],
    ["sso-session", "sso-session"],
  ] as const) {
    if (header.startsWith(prefix)) {
      const parts = shlexSplit(header);
      // botocore checks only the prefix and the word count, so `profilex y` names `y` too.
      if (parts && parts.length === 2) return { kind, name: parts[1] };
      return { kind: "other" };
    }
  }
  return { kind: "other" };
}
