// sshConfigIni — reads an OpenSSH client config (~/.ssh/config) as ssh_config(5) describes it
// (issue #3017, part 1).
//
// This is not the AWS config format (awsConfigIni.ts) and must not share its rules: there are
// no `[sections]` and no continuation lines; a line is `Keyword args` or `Keyword=args`, a
// `Host` or `Match` line opens a block that holds the lines below it, and lines before the first
// such line belong to every host. What makes the file hard to treat as a list of records is that
// OpenSSH evaluates it top to bottom and the FIRST value obtained for each keyword wins, across
// every block that applies to the host being connected to. So this reader keeps the blocks in
// file order and leaves the "which value wins" question to the caller (sshConfigImport.ts).
//
// Pure: no fetch, no React. Values never appear in an error; an error names a line only,
// because the text may hold a command line with a token in it.

export interface SshLine {
  /** The keyword, lower-cased (keywords are case-insensitive). */
  key: string;
  /** Everything after the keyword and its separator, trimmed. For ProxyCommand this is the
   *  whole rest of the line, as OpenSSH takes it. Never shown. */
  args: string;
  line: number;
}

export interface SshBlock {
  kind: "global" | "host" | "match";
  line: number;
  /** The `Host` patterns, quotes removed (empty for global and match blocks: a Match
   *  condition is never evaluated, so it is not kept). */
  patterns: string[];
  lines: SshLine[];
}

export interface SshFile {
  blocks: SshBlock[];
  /** A UTF-8 BOM was removed before reading. */
  bomStripped: boolean;
}

export type SshErrorCode = "bad_line" | "missing_args" | "bad_quote";

export interface SshError {
  code: SshErrorCode;
  line: number;
}

// `Keyword`, then whitespace or one "=" (with optional whitespace around it), then the rest.
const LINE_RE = /^([A-Za-z][A-Za-z0-9]*)(?:\s*=\s*|\s+)(.*)$/;
const BARE_RE = /^([A-Za-z][A-Za-z0-9]*)$/;
// A word of a Host line: double-quoted, or a run of non-space, non-quote characters.
const WORD_RE = /^(?:"([^"]*)"|([^\s"]+))/;

/** Split the arguments of a Host or Match line into words; a word that starts with `#` ends
 *  the line (a trailing comment). Null when a quote is left open. */
function words(args: string): string[] | null {
  const out: string[] = [];
  let rest = args.trim();
  // OpenSSH also strips single quotes and reads backslash escapes in these words; this reader
  // does not, and a name read differently would skip the blocks that really apply to it, so
  // the whole file is refused instead.
  if (/['\\]/.test(rest.split(/(?:^|\s)#/)[0])) return null;
  while (rest) {
    if (rest.startsWith("#")) break;
    const m = WORD_RE.exec(rest);
    if (!m) return null;
    out.push(m[1] ?? m[2]);
    rest = rest.slice(m[0].length);
    // A quote glued to the next word ("a"b) is not a form OpenSSH documents.
    if (rest && !/^\s/.test(rest)) return null;
    rest = rest.trim();
  }
  return out;
}

export function parseSshConfig(text: string): { ok: true; file: SshFile } | { ok: false; error: SshError } {
  let bomStripped = false;
  if (text.charCodeAt(0) === 0xfeff) {
    text = text.slice(1);
    bomStripped = true;
  }
  const blocks: SshBlock[] = [{ kind: "global", line: 0, patterns: [], lines: [] }];
  const lines = text.split(/\r\n|\n|\r/);
  for (let i = 0; i < lines.length; i++) {
    const lineno = i + 1;
    const stripped = lines[i].trim();
    if (stripped === "" || stripped.startsWith("#")) continue;
    let key: string;
    let args: string;
    const m = LINE_RE.exec(stripped);
    const bare = BARE_RE.exec(stripped);
    if (m) [key, args] = [m[1].toLowerCase(), m[2].trim()];
    else if (bare) [key, args] = [bare[1].toLowerCase(), ""];
    else return { ok: false, error: { code: "bad_line", line: lineno } };
    if (key === "host" || key === "match") {
      const w = words(args);
      if (!w) return { ok: false, error: { code: "bad_quote", line: lineno } };
      if (w.length === 0) return { ok: false, error: { code: "missing_args", line: lineno } };
      blocks.push({ kind: key, line: lineno, patterns: key === "host" ? w : [], lines: [] });
      continue;
    }
    blocks[blocks.length - 1].lines.push({ key, args, line: lineno });
  }
  return { ok: true, file: { blocks, bomStripped } };
}

/** ssh's pattern match: `*` and `?` are the only wildcards, and matching is case-sensitive
 *  (ssh_config(5): "arguments are case-sensitive"; a case-folded match would also widen a
 *  negation and drop a block that does apply). */
export function sshGlob(pattern: string, name: string): boolean {
  const re = pattern
    .replace(/[.+^${}()|[\]\\]/g, "\\$&")
    .replace(/\*/g, ".*")
    .replace(/\?/g, ".");
  return new RegExp("^" + re + "$").test(name);
}

export const hasSshWildcard = (p: string): boolean => /[*?]/.test(p);
