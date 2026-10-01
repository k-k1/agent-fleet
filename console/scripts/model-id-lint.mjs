#!/usr/bin/env node
// Model-id lint, the Console half (issue #1084; the Go half is scripts/model-id-lint). Fails on
// a model-id-shaped string or template literal in src/ outside the fallback registry,
// src/lib/modelFallbacks.ts. Model ids move every few weeks, and a fallback pinned somewhere
// nobody owns keeps naming a retired model long after the catalog has moved on; the registry
// records who owns each pinned id and why discovery cannot supply it.
//
// "Model-id-shaped" is defined once, in scripts/model-id-lint/patterns.txt at the repository
// root, which the Go half reads too. Comments are not AST nodes, so a comment naming an id
// is never a finding. A string built from literals alone ("gpt-" + "5.6-luna", a template
// whose substitutions are such strings) is judged as the string it builds.
//
// Usage:
//   node scripts/model-id-lint.mjs          - exit non-zero on any violation (the CI gate)
//   node scripts/model-id-lint.mjs --list   - every finding with its status
//
// Escape hatch, for a literal that names an id without choosing one (an example in help
// text, a table keyed by id). Only a real comment counts — the same words inside a string or
// JSX text are not a directive. The reason is required:
//   * // model-id-lint:allow <reason>       at end of line, or on the line above
//   * // model-id-lint:allow-file <reason>  anywhere in the file
//
// Before scanning, the lint runs itself over a planted literal and over the shapes it must
// leave alone, and refuses to report "clean" when either control fails — an empty result and
// a scanner that matched nothing look identical otherwise.

// `typescript-ast` is the typescript@6 alias: TypeScript 7 ships no compiler API (see
// scripts/i18n-lint.mjs).
import ts from "typescript-ast";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = fileURLToPath(new URL(".", import.meta.url));
const SRC = join(HERE, "..", "src");
const PATTERNS = join(HERE, "..", "..", "scripts", "model-id-lint", "patterns.txt");

// Relative to src/. The one file allowed to spell a model id.
const REGISTRY = new Set(["lib/modelFallbacks.ts"]);

const ALLOW = "model-id-lint:allow";
const ALLOW_FILE = "model-id-lint:allow-file";

function loadPattern() {
  const alts = readFileSync(PATTERNS, "utf8")
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith("#"));
  if (alts.length === 0) throw new Error(`${PATTERNS}: no patterns`);
  return new RegExp(`\\b(?:${alts.join("|")})`);
}

function walkFiles(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walkFiles(p, out);
    else if (/\.(tsx|ts)$/.test(name)) out.push(p);
  }
  return out;
}

const isTest = (rel) => /\.test\.tsx?$/.test(rel) || rel.includes("/__tests__/") || rel.endsWith(".d.ts");

// The text after a directive, separators trimmed. "" means no reason was given.
const reasonAfter = (line, directive) =>
  line
    .slice(line.indexOf(directive) + directive.length)
    .replace(/\*\/.*$/, "")
    .replace(/^[\s:—-]+/, "")
    .trim();

// scan returns { findings, errors } for one file's text. Exported shape kept small so the
// self-test below can drive it with in-memory sources.
export function scan(rel, text, pattern) {
  const findings = [];
  const errors = [];
  const sf = ts.createSourceFile(rel, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const lineOf = (pos) => sf.getLineAndCharacterOfPosition(pos).line + 1;

  // Directives are read from real comments only. A directive spelled inside a string, template
  // or JSX text (help text explaining the directive, say) must not exempt anything.
  const allowed = new Map(); // 1-indexed line -> reason
  let fileReason = "";
  for (const c of comments(sf, text)) {
    const body = text.slice(c.pos, c.end);
    const line = lineOf(c.pos);
    if (body.includes(ALLOW_FILE)) {
      const r = reasonAfter(body, ALLOW_FILE);
      if (r) fileReason = r;
      else errors.push(`${rel}:${line}: ${ALLOW_FILE} needs a reason`);
    } else if (body.includes(ALLOW)) {
      const r = reasonAfter(body, ALLOW);
      if (!r) {
        errors.push(`${rel}:${line}: ${ALLOW} needs a reason`);
        continue;
      }
      const end = lineOf(c.end);
      allowed.set(end, r);
      allowed.set(end + 1, r);
    }
  }

  const report = (node, raw) => {
    const m = raw && raw.match(pattern);
    if (!m) return;
    const line = lineOf(node.getStart(sf));
    let status = "violation";
    let reason = "";
    if (REGISTRY.has(rel)) status = "registry";
    else if (allowed.has(line)) [status, reason] = ["allowed", allowed.get(line)];
    else if (fileReason) [status, reason] = ["allowed-file", fileReason];
    findings.push({ rel, line, literal: raw.replace(/\s+/g, " ").trim().slice(0, 80), match: m[0], status, reason });
  };
  const visit = (node) => {
    // A string built from literals alone is judged whole — "gpt-" + "5.6-luna" and
    // `gpt-${"5.6-luna"}` pin an id with no single part matching — and its parts are not
    // visited again, so a matching part is reported once.
    if (ts.isBinaryExpression(node) || ts.isTemplateExpression(node) || ts.isParenthesizedExpression(node)) {
      const v = foldString(node);
      if (v !== null) {
        report(node, v);
        return;
      }
    }
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) report(node, node.text);
    else if (ts.isTemplateExpression(node)) {
      report(node.head, node.head.text);
      for (const span of node.templateSpans) report(span.literal, span.literal.text);
    } else if (ts.isJsxText(node)) report(node, node.text);
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return { findings, errors };
}

// foldString evaluates node when it is built from string literals alone: a literal, literals
// joined by + (parentheses allowed), or a template whose substitutions are such strings.
// null for anything that needs a value at run time.
function foldString(node) {
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text;
  if (ts.isParenthesizedExpression(node)) return foldString(node.expression);
  if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken) {
    const l = foldString(node.left);
    const r = l === null ? null : foldString(node.right);
    return r === null ? null : l + r;
  }
  if (ts.isTemplateExpression(node)) {
    let s = node.head.text;
    for (const span of node.templateSpans) {
      const v = foldString(span.expression);
      if (v === null) return null;
      s += v + span.literal.text;
    }
    return s;
  }
  return null;
}

// comments lists every comment in the file once, as { pos, end }: the leading and trailing
// comment ranges of every token, which is where the parser leaves them.
function comments(sf, text) {
  const seen = new Map();
  const add = (ranges) => {
    for (const r of ranges || []) seen.set(r.pos, r);
  };
  const visit = (node) => {
    add(ts.getLeadingCommentRanges(text, node.pos));
    add(ts.getTrailingCommentRanges(text, node.end));
    ts.forEachChild(node, visit);
  };
  visit(sf);
  add(ts.getLeadingCommentRanges(text, sf.endOfFileToken.pos));
  return [...seen.values()];
}

// selfTest is the positive and negative control: a planted id must be found, and the shapes
// the lint must leave alone must not be.
function selfTest(pattern) {
  const src = [
    `// a comment naming gpt-5.6-luna is fine`,
    `export const planted = "claude-opus-4-8";`,
    "export const tpl = `gemini-3.8-flash-low`;",
    `export const alias = "sonnet";`,
    `export const family = "qwen-image-2.1";`,
    `// ${ALLOW} an example in help text`,
    `export const example = "for example gpt-5.6-terra";`,
  ].join("\n");
  const { findings, errors } = scan("features/x.ts", src, pattern);
  const got = findings.map((f) => `${f.line}:${f.status}`).join(",");
  const want = "2:violation,3:violation,7:allowed";
  const reg = scan("lib/modelFallbacks.ts", `export const A = "gpt-5.4-mini";`, pattern).findings;
  const bare = scan("features/y.ts", `// ${ALLOW}\nconst x = "gpt-5.4-mini";`, pattern);
  const failures = [];
  if (errors.length || got !== want) failures.push(`planted source: got ${got} ${errors}, want ${want}`);
  if (reg.length !== 1 || reg[0].status !== "registry") failures.push(`registry: got ${JSON.stringify(reg)}`);
  if (bare.errors.length !== 1 || bare.findings[0]?.status !== "violation")
    failures.push(`a reasonless allow must be an error and leave the violation: ${JSON.stringify(bare)}`);
  // A directive inside a string is text, not a directive.
  const fake = scan("features/z.ts", `const help = "${ALLOW_FILE} ordinary help text";\nconst m = "gpt-5.6-luna";`, pattern);
  if (fake.errors.length || fake.findings.length !== 1 || fake.findings[0].status !== "violation")
    failures.push(`a directive inside a string must not exempt: ${JSON.stringify(fake)}`);
  // A string built from literals is judged whole.
  const folded = scan("features/w.ts", 'const a = "gpt-" + "5.6-luna";\nconst b = `claude-${"sonnet-5"}`;\nconst c = ("gemini-" + ("3.8"));\nconst d = "gpt-" + v;', pattern);
  const fg = folded.findings.map((f) => `${f.line}:${f.literal}`).join(",");
  if (fg !== "1:gpt-5.6-luna,2:claude-sonnet-5,3:gemini-3.8")
    failures.push(`constant concatenation: got ${fg}, want 1:gpt-5.6-luna,2:claude-sonnet-5,3:gemini-3.8`);
  return failures;
}

function main() {
  const pattern = loadPattern();
  const failures = selfTest(pattern);
  if (failures.length) {
    for (const f of failures) console.error(`✖ self-test: ${f}`);
    process.exit(2);
  }

  const findings = [];
  const errors = [];
  for (const abs of walkFiles(SRC)) {
    const rel = relative(SRC, abs).split("\\").join("/");
    if (isTest(rel)) continue;
    const r = scan(rel, readFileSync(abs, "utf8"), pattern);
    findings.push(...r.findings);
    errors.push(...r.errors);
  }
  findings.sort((a, b) => a.rel.localeCompare(b.rel) || a.line - b.line);
  const fmt = (f) => `${f.rel}:${f.line}  ${JSON.stringify(f.literal)} (matched ${f.match})${f.reason ? ` — ${f.reason}` : ""}`;

  if (process.argv.includes("--list")) {
    for (const f of findings) console.log(`${f.status.padEnd(12)} ${fmt(f)}`);
  }
  const bad = findings.filter((f) => f.status === "violation");
  for (const e of errors) console.error(`✖ ${e}`);
  if (bad.length || errors.length) {
    for (const f of bad) console.error(`src/${fmt(f)}`);
    console.error(`\n✖ ${bad.length} model-id-shaped literal(s) outside src/lib/modelFallbacks.ts.`);
    console.error(`  Move the id into the registry, or mark a literal that chooses no model with // ${ALLOW} <reason>.`);
    process.exit(1);
  }
  console.log(`✓ no model-id-shaped literals outside the registry (${findings.length} found, all registry or allowed).`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) main();
