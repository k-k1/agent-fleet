#!/usr/bin/env node
// 同名グローバルクラスの重複検出。
//
// なぜ要るか。この Console の CSS は**全部グローバル**で、main.tsx が固定の順序で import
// する（CSS Modules ではない）。だから 2 つのファイルが同じクラス名を定義すると、両者は
// 衝突として報告されるのではなく **import 順で合成**される —— 後から読まれた側のプロパティ
// だけが上書きされ、残りは前のファイルのまま残る。実際に「片方のファイルだけ見ても
// 説明のつかない見た目」になる事故が起きている。
//
// 分割・移設の前に**現状の重複一覧**を取るための道具（ADR 0067 Phase 0）。既定では
// 列挙するだけで落とさない。CI ゲートにするかは棚卸しのあとの判断。
//
// 使い方:
//   node scripts/css-dup.mjs            … 一覧を出す（常に exit 0）
//   node scripts/css-dup.mjs --fail     … 重複が 1 件でもあれば exit 1（将来の CI 用）
//   node scripts/css-dup.mjs --json     … 機械可読
//
// 危険度は 1 段だけ分ける。`.parent .active {}` のような**祖先で限定された**同名クラスは
// 実質そのファイルの中の話だが、セレクタが `.active` 単体（= bare）のものは文字どおり
// グローバルで、他ファイルの `.active` と直接合成される。bare が 2 ファイル以上にある
// 名前だけが「いま危ないもの」なので ⚠ を付ける。
//
// 限界（承知の上）: それ以上は判定しない。事故なのか意図した上書き（テーマ差分など）
// なのかを当てにいくと、一覧そのものが信用されなくなる。

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = fileURLToPath(new URL(".", import.meta.url));
const ROOT = join(HERE, "..");
const SRC = join(ROOT, "src");

// 対象外: Marp のデッキテーマは Console の DOM に載らない（Marp レンダラ側の独立した
// スタイルシート）ので、同名クラスがあっても合成されない。
const SKIP = [/\/marp-themes\//];

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (name.endsWith(".css")) out.push(p);
  }
  return out;
}

// コメントと文字列を潰す（中身の . を拾わないため）。長さは変えない＝行番号が保てる。
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

// 条件付きグループ規則の中身は「宣言」ではなく「規則」なので、掘って中の
// セレクタも拾う。@keyframes の中は from/to/% でクラス名を含まない（拾っても無害）。
const NESTED_AT = /^@(media|supports|layer|container|scope|document)\b/;

// セレクタ（{ の直前のテキスト）だけを列挙する。宣言ブロックの中は見ないので、
// content: ".x" のような値をクラス名と誤認しない。
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
          continue; // 中身を規則として読み続ける
        }
      } else if (p) {
        out.push({ text: p, index: preludeStart });
      }
      // 宣言ブロックは丸ごと飛ばす（ネストがあれば数える）
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

// セレクタ 1 本（カンマ区切りの 1 つ）が `.name` 単体か。擬似クラス/擬似要素は付いていて
// よい（`.on:hover` も同じグローバル名を握る）。子孫・結合子が入れば bare ではない。
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
  console.log(`# 同名グローバルクラスの重複（${files.length} ファイル / クラス名 ${byName.size} 種）`);
  console.log(`#   ${dups.length} 個が 2 ファイル以上で定義されている（import 順で合成される）`);
  console.log(`#   うち ⚠ ${risky.length} 個は bare（\`.name {}\` 単体）が 2 ファイル以上 = 直接ぶつかる\n`);
  for (const [name, per] of dups) {
    const mark = bareFiles(per) > 1 ? "⚠" : " ";
    const where = [...per].map(([f, r]) => `${f}:${r.lines.join(",")}${r.bare ? "(bare)" : ""}`).join("  ");
    console.log(`${mark} .${name}  (${per.size} files)  ${where}`);
  }
}

// --fail は将来 CI に載せるとき用。既定は列挙のみ（Phase 0 は棚卸しが目的）。
if (process.argv.includes("--fail") && risky.length > 0) process.exit(1);
