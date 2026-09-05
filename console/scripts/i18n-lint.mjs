#!/usr/bin/env node
// i18n regression guard (docs/log/28-i18n.md P5). Detects raw Japanese (CJK) left inside JSX text
// and string/template literals via the TypeScript AST. Comments are not AST nodes, so they are
// excluded automatically and only text that reaches the screen is picked up (fewer false
// positives than a regex).
//
// Usage:
//   node scripts/i18n-lint.mjs           - exit non-zero on any violation (the CI gate)
//   node scripts/i18n-lint.mjs --list     - per-file inventory summary (pending included)
//   node scripts/i18n-lint.mjs --pending  - regenerate the pending list from every current offender
//
// Ratchet: secondary screens still carry bare Japanese. Violations in files listed in
// `i18n-lint-pending.json` are reported as backlog and are not fatal; only violations in OTHER
// files fail CI. A file that reaches zero is dropped from pending, so it can never regress.
//
// Exemptions (text deliberately never translated: the LLM prompts of docs/log/28 §4/§6.4, the TTS
// reading dictionary, answer-matching regexes, proper nouns. Unlike pending, this states "never
// translate this"):
//   * // i18n-exempt[: reason] at end of line or on the line above  - that one node
//   * // i18n-exempt-start ... // i18n-exempt-end                   - a range (an LLM prompt block)
//   * // i18n-exempt-file[: reason] anywhere in the file            - the whole file
//   * ALLOW_FILES (below)                                           - catalogues, reading dictionaries

import ts from "typescript";
import { readFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = fileURLToPath(new URL(".", import.meta.url));
const ROOT = join(HERE, "..");
const SRC = join(ROOT, "src");
const PENDING_FILE = join(HERE, "i18n-lint-pending.json");

// CJK characters (hiragana, katakana, kanji). ASCII-only strings pass straight through.
const HAS_LETTER = /[぀-ヿ㐀-鿿]/;

// Directories exempted wholesale (relative to src/). In the catalogues, CJK is the content itself.
// This is a prefix match on purpose: the catalogues are split per domain (locales/ja/*.ts,
// locales/en/*.ts, ADR 0067 decision 4), and listing file names one by one means the lint reports
// a new domain file as full of bare Japanese the moment it is added.
const ALLOW_DIRS = ["lib/i18n/locales/"];

// Files exempted wholesale (relative to src/). Logic/data rather than translatable text.
const ALLOW_FILES = new Set([
  "features/chat/ttsText.ts", // internals of TTS reading conversion (pronunciation dictionary logic, docs/log/28 §4)
  // Unlike ALLOW_DIRS this is an exact match (ALLOW_FILES.has(rel) below). Splitting a file takes
  // the split-off part out of the exemption, so add each new file here one by one. Do not make it
  // a prefix: parts/ will also hold UI files in future, and a prefix would silently exempt them
  // (ALLOW_DIRS can be a prefix because every new catalogue file is guaranteed to be CJK; here the
  // property is the opposite).
  "features/chat/parts/ttsVoices.ts", // VOICEVOX speaker and emotion style names (§6.4, untranslated)
  "features/chat/parts/ttsAudio.ts", // per-character output volume (keyed on the speaker name)
  "features/chat/parts/ttsPlay.ts", // preview boilerplate (the Japanese sample a character reads)
  "features/chat/parts/ttsAbbrev.ts", // filler words for abbreviated readings
  "features/chat/parts/ttsReadings.ts", // built-in reading-correction table
  "features/viewer/readerText.ts", // Narou ruby parsing logic (《》｜ are syntax characters)
]);

function walkFiles(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) walkFiles(p, out);
    else if (/\.(tsx|ts)$/.test(name)) out.push(p);
  }
  return out;
}

const isTest = (rel) => /\.test\.tsx?$/.test(rel) || rel.includes("/__tests__/");

// Build the set of exempt lines for a file (1-indexed): the i18n-exempt line itself, the line
// after a comment-only marker (which targets the next node), and any start/end range.
function exemptLines(lines) {
  const set = new Set();
  let blockOpen = false;
  for (let i = 0; i < lines.length; i++) {
    const ln = lines[i];
    if (/i18n-exempt-end/.test(ln)) blockOpen = false;
    if (blockOpen) set.add(i + 1);
    if (/i18n-exempt-start/.test(ln)) {
      blockOpen = true;
      continue;
    }
    if (/i18n-exempt\b/.test(ln) && !/i18n-exempt-(file|start|end)/.test(ln)) {
      set.add(i + 1); // the node on this line
      // A comment-only i18n-exempt line is taken to target the next line of real code.
      if (ln.trim().startsWith("//")) set.add(i + 2);
    }
  }
  return set;
}

function loadPending() {
  if (!existsSync(PENDING_FILE)) return new Set();
  const raw = readFileSync(PENDING_FILE, "utf8").trim();
  return raw ? new Set(JSON.parse(raw)) : new Set();
}
const pending = loadPending();

const findings = [];
for (const abs of walkFiles(SRC)) {
  const rel = relative(SRC, abs).split("\\").join("/");
  if (isTest(rel) || ALLOW_FILES.has(rel) || ALLOW_DIRS.some((d) => rel.startsWith(d))) continue;

  const text = readFileSync(abs, "utf8");
  if (text.includes("i18n-exempt-file")) continue;
  const lines = text.split("\n");
  const exempt = exemptLines(lines);

  const sf = ts.createSourceFile(rel, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const report = (node, raw, kind) => {
    if (!raw || !HAS_LETTER.test(raw)) return;
    const { line, character } = sf.getLineAndCharacterOfPosition(node.getStart(sf));
    if (exempt.has(line + 1)) return;
    findings.push({
      rel,
      line: line + 1,
      col: character + 1,
      kind,
      snippet: raw.replace(/\s+/g, " ").trim().slice(0, 50),
    });
  };
  const visit = (node) => {
    if (ts.isStringLiteral(node)) report(node, node.text, "string");
    else if (ts.isNoSubstitutionTemplateLiteral(node)) report(node, node.text, "template");
    else if (ts.isTemplateExpression(node)) {
      report(node.head, node.head.text, "template");
      for (const span of node.templateSpans) report(span.literal, span.literal.text, "template");
    } else if (ts.isJsxText(node)) report(node, node.text, "jsx");
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

findings.sort((a, b) => a.rel.localeCompare(b.rel) || a.line - b.line);
const offenders = new Set(findings.map((f) => f.rel));

// --pending: print the current offender files as a pending list (for seeding and stocktaking).
if (process.argv.includes("--pending")) {
  console.log(JSON.stringify([...offenders].sort(), null, 2));
  process.exit(0);
}

const blocking = findings.filter((f) => !pending.has(f.rel));
const backlog = findings.filter((f) => pending.has(f.rel));

// --all: print every violation as line:col regardless of pending state (briefing for migration).
if (process.argv.includes("--all")) {
  for (const f of findings) console.log(`${f.rel}:${f.line}:${f.col}  [${f.kind}]  ${f.snippet}`);
  process.exit(0);
}

if (process.argv.includes("--list")) {
  const byFile = new Map();
  for (const f of findings) byFile.set(f.rel, (byFile.get(f.rel) || 0) + 1);
  const rows = [...byFile.entries()].sort((a, b) => b[1] - a[1]);
  console.log(`# i18n bare-Japanese literal inventory (${findings.length} total / ${rows.length} files)`);
  console.log(`#   backlog(pending): ${backlog.length} / blocking(new): ${blocking.length}\n`);
  for (const [file, n] of rows) {
    const tag = pending.has(file) ? "backlog" : "BLOCK  ";
    console.log(`${tag} ${String(n).padStart(4)}  ${file}`);
  }
  process.exit(0);
}

// The backlog is summarised for information only (not fatal).
if (backlog.length > 0) {
  const files = new Set(backlog.map((f) => f.rel)).size;
  console.log(`ℹ backlog: ${backlog.length} in ${files} files (listed in i18n-lint-pending.json).`);
}

// A file listed in pending that is now at zero has finished migrating; nudge to drop it (not fatal).
const cleaned = [...pending].filter((p) => !offenders.has(p));
if (cleaned.length > 0) {
  console.log(`✎ migration complete, please remove from i18n-lint-pending.json: ${cleaned.join(", ")}`);
}

if (blocking.length > 0) {
  for (const f of blocking) console.error(`${f.rel}:${f.line}:${f.col}  [${f.kind}]  ${f.snippet}`);
  console.error(`\n✖ found ${blocking.length} bare Japanese literals outside pending.`);
  console.error(`  Move them into the catalogue with t()/useT(), or mark them // i18n-exempt if they are not to be translated.`);
  console.error(`  Full inventory: node scripts/i18n-lint.mjs --list`);
  process.exit(1);
}
console.log("✓ no bare Japanese literals outside pending.");
