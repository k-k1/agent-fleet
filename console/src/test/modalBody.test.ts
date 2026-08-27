// `<Modal>` の中身は必ず ui-modal-head / ui-modal-body / ui-modal-foot に載せる、という静的検査。
//
// ui/Modal のシェルは padding を持たない。余白は head / body / foot がそれぞれ自分で持つ
// 設計で、`<Modal>` の直下に本文を置くと**その本文だけ左右の余白がゼロ**になり、見出しは
// 枠から 12px なのに文字と入力欄は枠線に貼りつく。
//
// 実害: 作業項目の 3 モーダル・WS 起動中ダイアログ・掃除モーダルの 5 つが同時にこの形に
// なっていた（実測で見出し 13px / 本文 1px）。どれもテストは緑で、緑なのは「描けている」
// ことしか確かめていなかったから。**見た目の崩れは、構造で書けば静的に捕まる**。
//
// 見るのは `<Modal>` の直下の**組み込み要素だけ**（div / p / footer …）。大文字始まりの子は
// コンポーネントで、`<Modal>` の中に別のダイアログを重ねる正当な形がある
// （ShareListModal → ShareCreateModal）。その中身は、そのファイルを見るときに検査される。
// `{cond && <div className="ui-modal-body">}` や三項のように「描かれるとしたらこれ」が JSX で
// 書かれている形はほどいて中を見る。関数呼び出しや変数で組み立てている形は判定を諦めて通す
// （誤検知でテストを鬱陶しくしない）。
import { describe, expect, it } from "vitest";
import ts from "typescript";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";

const SRC = path.resolve(__dirname, "..");

function tsxFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) tsxFiles(p, out);
    else if (e.name.endsWith(".tsx")) out.push(p);
  }
  return out;
}

type El = ts.JsxElement | ts.JsxSelfClosingElement;

const tagName = (el: El) =>
  (ts.isJsxElement(el) ? el.openingElement.tagName : el.tagName).getText();

/** className の文字列リテラル部分（`"a " + b` のような式は読める分だけ）。 */
function classNameOf(el: El): string {
  const attrs = ts.isJsxElement(el) ? el.openingElement.attributes : el.attributes;
  for (const a of attrs.properties) {
    if (!ts.isJsxAttribute(a) || a.name.getText() !== "className" || !a.initializer) continue;
    if (ts.isStringLiteral(a.initializer)) return a.initializer.text;
    return a.initializer.getText(); // {"ui-modal-body " + x} などは素のテキストで見る
  }
  return "";
}

/** その子が描きうる JSX 要素を集める。ほどけない形（関数呼び出し等）は null を混ぜず無視。 */
function renderedElements(node: ts.Node): El[] {
  if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) return [node];
  if (ts.isJsxFragment(node)) return node.children.flatMap(renderedElements);
  if (ts.isJsxExpression(node)) return node.expression ? renderedElements(node.expression) : [];
  if (ts.isParenthesizedExpression(node)) return renderedElements(node.expression);
  // cond && <X /> — 描かれるとしたらこれ
  if (ts.isBinaryExpression(node)) return renderedElements(node.right);
  // cond ? <A /> : <B /> — どちらも描かれうる
  if (ts.isConditionalExpression(node)) {
    return [...renderedElements(node.whenTrue), ...renderedElements(node.whenFalse)];
  }
  return [];
}

describe("Modal の中身は共有のシェルに載る", () => {
  it("★ <Modal> の直下に ui-modal-* 以外の要素を置かない（置くとその本文だけ余白ゼロになる）", () => {
    const offenders: string[] = [];
    for (const file of tsxFiles(SRC)) {
      const text = readFileSync(file, "utf8");
      if (!text.includes("<Modal")) continue;
      const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
      const walk = (node: ts.Node) => {
        if (ts.isJsxElement(node) && node.openingElement.tagName.getText() === "Modal") {
          for (const child of node.children) {
            if (ts.isJsxText(child) && !child.text.trim()) continue;
            for (const el of renderedElements(child)) {
              const tag = tagName(el);
              if (!/^[a-z]/.test(tag)) continue; // コンポーネント（入れ子のダイアログ等）は別途検査される
              const cls = classNameOf(el);
              if (/\bui-modal-(head|body|foot)\b/.test(cls)) continue;
              const { line } = sf.getLineAndCharacterOfPosition(el.getStart(sf));
              offenders.push(`${path.relative(SRC, file)}:${line + 1} <${tag} className=${cls || "(none)"}>`);
            }
          }
        }
        ts.forEachChild(node, walk);
      };
      walk(sf);
    }
    expect(offenders).toEqual([]);
  });
});
