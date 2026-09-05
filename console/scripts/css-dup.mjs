#!/usr/bin/env node
// Detect global class names defined in more than one file.
//
// Why it exists: every CSS file in this Console is global and main.tsx imports them in a fixed
// order (these are not CSS Modules). When two files define the same class name, that is not
// reported as a collision but composed in import order: only the properties from the file read
// later are overridden, the rest stay as the earlier file left them. This has produced looks
// that cannot be explained by reading either file on its own.
//
// The tool exists to take an inventory of the current duplicates before splitting and moving
// files (ADR 0067 Phase 0). By default it only lists them and does not fail; whether it becomes
// a CI gate is a decision for after the inventory.
//
// Usage:
//   node scripts/css-dup.mjs            list them (always exit 0)
//   node scripts/css-dup.mjs --fail     exit 1 if there is any duplicate (for future CI use)
//   node scripts/css-dup.mjs --json     machine readable
//
// There is exactly one severity distinction. A same-named class qualified by an ancestor, as in
// `.parent .active {}`, is effectively local to its file, whereas a selector that is `.active`
// alone (bare) is literally global and composes directly with another file's `.active`. Only
// names whose bare form appears in two or more files are dangerous today, and those get a ⚠.
//
// Deliberate limit: it judges nothing beyond that. Guessing whether a duplicate is an accident
// or an intended override (a theme variant, say) would cost the list its credibility.

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = fileURLToPath(new URL(".", import.meta.url));
const ROOT = join(HERE, "..");
const SRC = join(ROOT, "src");

// Excluded: Marp deck themes never reach the Console's DOM (they are the Marp renderer's own
// stylesheets), so a shared class name there is never composed.
const SKIP = [/\/marp-themes\//];

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (name.endsWith(".css")) out.push(p);
  }
  return out;
}

// Blank out comments and strings so a "." inside them is not picked up. The length is preserved
// so line numbers still hold.
function blank(css) {
  let out = "";
  for (let i = 0; i < css.length; ) {
    if (css.startsWith("/*", i)) {
      const end = css.indexOf("*/", i + 2);
      const stop = end === -1 ? css.length : end + 2;
      out += css.slice(i, stop).replace(/[^\n]/g, " ");
      i = stop;
      continue;
    }
    const q = css[i];
    if (q === '"' || q === "'") {
      let j = i + 1;
      while (j < css.length && css[j] !== q) j += css[j] === "\\" ? 2 : 1;
      const stop = Math.min(j + 1, css.length);
      out += " ".repeat(stop - i);
      i = stop;
      continue;
    }
    out += q;
    i++;
  }
  return out;
}

// The body of a conditional group rule holds rules, not declarations, so descend into it and
// collect the selectors inside. @keyframes bodies are from/to/% and hold no class names, so
// descending there would be harmless anyway.
const NESTED_AT = /^@(media|supports|layer|container|scope|document)\b/;

// List only the selectors, i.e. the text right before a "{". Declaration blocks are not read, so
// a value such as content: ".x" is never mistaken for a class name.
function selectors(css) {
  const s = blank(css);
  const out = [];
  let prelude = "";
  let preludeStart = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === "{") {
      const p = prelude.trim();
      if (p.startsWith("@")) {
        if (NESTED_AT.test(p)) {
          prelude = "";
          preludeStart = i + 1;
          continue; // keep reading the body as rules
        }
      } else if (p) {
        out.push({ text: p, index: preludeStart });
      }
      // Skip the whole declaration block, counting nesting on the way
      let depth = 1;
      while (++i < s.length && depth > 0) {
        if (s[i] === "{") depth++;
        else if (s[i] === "}") depth--;
      }
      i--;
      prelude = "";
      preludeStart = i + 2;
      continue;
    }
    if (c === "}") {
      prelude = "";
      preludeStart = i + 1;
      continue;
    }
    prelude += c;
  }
  return out;
}

const CLASS = /\.(-?[_a-zA-Z][\w-]*)/g;

// Whether one selector (one comma-separated part) is a bare `.name`. Pseudo-classes and
// pseudo-elements are allowed, since `.on:hover` holds the same global name; any descendant or
// combinator makes it not bare.
function bareClassOf(part) {
  const m = /^\.(-?[_a-zA-Z][\w-]*)(::?[\w-]+(\([^)]*\))?)*$/.exec(part.trim());
  return m ? m[1] : null;
}

const files = walk(SRC).filter((p) => !SKIP.some((re) => re.test(p)));
// name -> file -> { lines, bare }
const byName = new Map();
for (const abs of files) {
  const rel = relative(ROOT, abs).split("\\").join("/");
  const text = readFileSync(abs, "utf8");
  const lineOf = (idx) => text.slice(0, idx).split("\n").length;
  for (const sel of selectors(text)) {
    const line = lineOf(sel.index);
    const bare = new Set(sel.text.split(",").map(bareClassOf).filter(Boolean));
    for (const m of sel.text.matchAll(CLASS)) {
      const name = m[1];
      if (!byName.has(name)) byName.set(name, new Map());
      const per = byName.get(name);
      if (!per.has(rel)) per.set(rel, { lines: [], bare: false });
      const rec = per.get(rel);
      rec.lines.push(line);
      if (bare.has(name)) rec.bare = true;
    }
  }
}

const bareFiles = (per) => [...per.values()].filter((r) => r.bare).length;

const dups = [...byName.entries()]
  .filter(([, per]) => per.size > 1)
  .sort((a, b) => bareFiles(b[1]) - bareFiles(a[1]) || b[1].size - a[1].size || a[0].localeCompare(b[0]));
const risky = dups.filter(([, per]) => bareFiles(per) > 1);

if (process.argv.includes("--json")) {
  console.log(
    JSON.stringify(
      dups.map(([name, per]) => ({
        name,
        bareFiles: bareFiles(per),
        files: [...per].map(([f, r]) => ({ file: f, lines: r.lines, bare: r.bare })),
      })),
      null,
      2,
    ),
  );
} else {
  console.log(`# Duplicate global class names (${files.length} files / ${byName.size} distinct names)`);
  console.log(`#   ${dups.length} are defined in two or more files (composed in import order)`);
  console.log(`#   of those, ⚠ ${risky.length} are bare (\`.name {}\` alone) in two or more files = a direct clash\n`);
  for (const [name, per] of dups) {
    const mark = bareFiles(per) > 1 ? "⚠" : " ";
    const where = [...per].map(([f, r]) => `${f}:${r.lines.join(",")}${r.bare ? "(bare)" : ""}`).join("  ");
    console.log(`${mark} .${name}  (${per.size} files)  ${where}`);
  }
}

// --fail is for putting this on CI later; the default only lists, since Phase 0 is an inventory.
if (process.argv.includes("--fail") && risky.length > 0) process.exit(1);
