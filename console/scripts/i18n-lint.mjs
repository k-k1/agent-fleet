#!/usr/bin/env node
// i18n 回帰防止チェック（docs/log/28-i18n.md P5）。JSX テキスト・文字列/テンプレートリテラルの中に
// 生の日本語（CJK）が残っていないかを TypeScript の AST で検出する。コメントは AST ノードでは
// ないので自動的に対象外＝「表示に出る文言」だけを拾える（正規表現よりも誤検出が少ない）。
//
// 使い方:
//   node scripts/i18n-lint.mjs           … 違反があれば非ゼロ終了（CI ゲート）
//   node scripts/i18n-lint.mjs --list     … ファイル別の棚卸しサマリ（pending 含む）
//   node scripts/i18n-lint.mjs --pending  … 現状の全違反ファイルで pending リストを再生成
//
// 段階移行（ratchet）: 二次画面はまだ裸和文が残る。`i18n-lint-pending.json` に列挙した
// ファイルの違反は「移行待ち（backlog）」として非致命で報告し、**それ以外**のファイルの違反
// だけを CI 落とし条件にする。移行してゼロになったファイルは pending から外す（＝二度と戻れない）。
//
// 除外の仕組み（翻訳しないと決めた文言＝docs/log/28 §4/§6.4 の LLM プロンプト・TTS 読み辞書・
// 回答判定の正規表現・固有名詞など。pending と違い「恒久的に翻訳しない」意思表示）:
//   * 行末 or 直前コメント行の  // i18n-exempt[: 理由]          … その 1 ノード
//   * // i18n-exempt-start 〜 // i18n-exempt-end で囲む            … 範囲（LLM プロンプト塊など）
//   * ファイル内に // i18n-exempt-file[: 理由]                    … そのファイル全体
//   * ALLOW_FILES（下記）… カタログ本体や読み辞書など丸ごと対象外

import ts from "typescript";
import { readFileSync, readdirSync, statSync, existsSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = fileURLToPath(new URL(".", import.meta.url));
const ROOT = join(HERE, "..");
const SRC = join(ROOT, "src");
const PENDING_FILE = join(HERE, "i18n-lint-pending.json");

// CJK 文字（ひらがな・カタカナ・漢字）。ASCII のみの文字列は素通し。
const HAS_LETTER = /[぀-ヿ㐀-鿿]/;

// 丸ごと対象外にするファイル（src/ 起点）。翻訳ではなくロジック/データのもの。
const ALLOW_FILES = new Set([
  "lib/i18n/locales/ja.ts", // カタログ正本（CJK が中身）
  "lib/i18n/locales/en.ts", // 英カタログ（型網羅対象）
  "features/chat/ttsText.ts", // TTS 読み変換の内部（発音辞書ロジック・docs/log/28 §4）
  "features/chat/ttsDict.ts", // 読み辞書データ
  "features/chat/tts.ts", // VOICEVOX 話者名・感情スタイル名（§6.4 未翻訳）＋読み上げロジック
  "features/viewer/readerText.ts", // なろうルビ解析ロジック（《》｜ は構文文字）
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

// 各ファイルの「除外行」集合を作る（1-indexed）。i18n-exempt 行、その直後の 1 ノード用に
// 直前コメント行、そして start/end で囲まれた範囲。
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
      set.add(i + 1); // この行のノード
      // 直前が「コメントだけの i18n-exempt 行」なら、次の実コード行を狙ったものとみなす
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
  if (isTest(rel) || ALLOW_FILES.has(rel)) continue;

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

// --pending: 現状の違反ファイル一覧を pending リストとして出力（初期生成・棚卸し用）。
if (process.argv.includes("--pending")) {
  console.log(JSON.stringify([...offenders].sort(), null, 2));
  process.exit(0);
}

const blocking = findings.filter((f) => !pending.has(f.rel));
const backlog = findings.filter((f) => pending.has(f.rel));

// --all: pending 状態に関係なく全違反を line:col で出力（移行作業のブリーフィング用）。
if (process.argv.includes("--all")) {
  for (const f of findings) console.log(`${f.rel}:${f.line}:${f.col}  [${f.kind}]  ${f.snippet}`);
  process.exit(0);
}

if (process.argv.includes("--list")) {
  const byFile = new Map();
  for (const f of findings) byFile.set(f.rel, (byFile.get(f.rel) || 0) + 1);
  const rows = [...byFile.entries()].sort((a, b) => b[1] - a[1]);
  console.log(`# i18n 裸和文リテラル棚卸し（計 ${findings.length} 件 / ${rows.length} ファイル）`);
  console.log(`#   backlog(pending): ${backlog.length} 件 / blocking(新規): ${blocking.length} 件\n`);
  for (const [file, n] of rows) {
    const tag = pending.has(file) ? "backlog" : "BLOCK  ";
    console.log(`${tag} ${String(n).padStart(4)}  ${file}`);
  }
  process.exit(0);
}

// backlog は情報として要約表示（非致命）。
if (backlog.length > 0) {
  const files = new Set(backlog.map((f) => f.rel)).size;
  console.log(`ℹ 移行待ち(backlog): ${backlog.length} 件 / ${files} ファイル（i18n-lint-pending.json 記載）。`);
}

// pending に載っているが今はゼロのファイル＝移行完了。リストから外すよう促す（非致命）。
const cleaned = [...pending].filter((p) => !offenders.has(p));
if (cleaned.length > 0) {
  console.log(`✎ 移行完了につき i18n-lint-pending.json から削除してください: ${cleaned.join(", ")}`);
}

if (blocking.length > 0) {
  for (const f of blocking) console.error(`${f.rel}:${f.line}:${f.col}  [${f.kind}]  ${f.snippet}`);
  console.error(`\n✖ ${blocking.length} 件の裸和文リテラル（pending 外）が見つかりました。`);
  console.error(`  t()/useT() でカタログ化するか、翻訳対象外なら // i18n-exempt を付けてください。`);
  console.error(`  棚卸し全体は: node scripts/i18n-lint.mjs --list`);
  process.exit(1);
}
console.log("✓ pending 外に裸和文リテラルはありません。");
