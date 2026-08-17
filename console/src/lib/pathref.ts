// Path references an agent writes as INLINE CODE in a reply — `docs/65-drawio-viewer.md`,
// `console/src/lib/filemeta.ts:73`, `_act-parts/`. They are the coordinates a reader most
// wants to open, so MarkdownView turns the ones that actually EXIST into links.
//
// pathRefCandidate is the cheap pre-filter in front of that existence check: it decides
// whether a token is even worth asking the filesystem about. It is deliberately
// conservative, because every accepted token that is not a path costs a directory listing.
//
//   - one token, no whitespace — a shell line (`npm run build`) is not a path reference.
//   - no shell / expression punctuation — `rm -rf *.log`, `foo(bar)`, `--path=x` are
//     commands and code, not coordinates. `@` stays legal: worktree directories here are
//     named "agent-fleet@wip-sighxwi".
//   - path-SHAPED: it contains a "/" or ends in a file extension. A bare word (`develop`,
//     `main`) is a branch far more often than a file, and "is there a file called develop"
//     is not a question worth a request per mention.
//   - never a URL — those are wired as external links long before this runs.
//
// What it deliberately does NOT decide is whether the path exists, or WHICH file it names.
// Both are the Workspace agent's job (POST fs/resolve): the agent knows the working copy's
// real root, the read-only roots outside home, and the denylist, and it answers cwd-first /
// repository-root-second in one round trip. A candidate it can't place stays plain text, on
// the same contract as an unknown session slug (存在しない→リンクにしない).

import { isExternalUrl } from "./filemeta.ts";

const MAX_REF_LEN = 512;

// Characters that say "this is code or a command, not a filename". A real name may legally
// contain most of them, but in an agent's reply they mark a shell snippet or an expression
// far more often than a path — and a false accept costs a request, while a false reject
// only leaves the text as it is today.
const CODEY = /[\s"'`$;&|*?<>(){}[\]\\^=,!]/;

// A trailing extension: a dot, then a letter-led token. Letter-led on purpose — it keeps
// version strings ("1.2.3", "v0.14.2") out, which are the most common dotted non-paths.
const EXT_RE = /\.[A-Za-z][A-Za-z0-9_+-]{0,11}$/;

// A "…:12" / "…:12:3" line-column suffix — the coordinate an agent cites a source line
// with. Split off here: the agent resolves the path, the pane needs the line.
const LINE_SUFFIX = /:(\d+)(?::(\d+))?$/;

export interface PathRef {
  /** The path to resolve: no line suffix, no trailing slash. */
  ref: string;
  line?: number;
  column?: number;
}

/**
 * pathRefCandidate returns the path reference a piece of inline code carries, or null when
 * the token is not worth resolving. A trailing "/" (a directory, as agents write them) is
 * dropped — fs paths carry none, and the resolver reports file vs directory anyway.
 */
export function pathRefCandidate(raw: string | null | undefined): PathRef | null {
  const text = (raw ?? "").trim();
  if (!text || text.length > MAX_REF_LEN) return null;
  if (CODEY.test(text)) return null;
  if (isExternalUrl(text)) return null;

  const trimmed = text.replace(/\/+$/, "");
  // Nothing left to resolve: "/", "./", "..", "~" name a place, not a file.
  if (!trimmed || trimmed === "~" || /^[./]+$/.test(trimmed)) return null;

  const suffix = trimmed.match(LINE_SUFFIX);
  const ref = suffix ? trimmed.slice(0, -suffix[0].length) : trimmed;
  if (!ref || ref === "~" || /^[./]+$/.test(ref)) return null;
  // Path-shaped: it has a separator, ended in one (`_act-parts/` — how a directory is
  // written), or carries an extension.
  if (!ref.includes("/") && trimmed === text && !EXT_RE.test(ref)) return null;

  const line = suffix ? Number(suffix[1]) : 0;
  const column = suffix?.[2] ? Number(suffix[2]) : 0;
  return { ref, ...(line > 0 ? { line } : {}), ...(column > 0 ? { column } : {}) };
}
