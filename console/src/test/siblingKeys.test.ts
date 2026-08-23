// 同じ親の中で「同時に描かれる」兄弟に同じ key を付けてはいけない、という静的検査。
//
// React の配列リコンサイル（reconcileChildrenArray）は、key が変わった時点で残りの旧 fiber を
// key → fiber の Map に集め、最後に Map に残ったものを削除する。key が重複していると Map は
// 後勝ちで上書きされるので、前のほうの fiber は「新しい子と一致もしない・削除もされない」
// 迷子になり、その DOM が画面に取り残される。
//
// 実害: ミラーの ToDo 帯（TaskChecklist）と 変更ファイル 帯（FileChangeStrip）が両方 key={session}
// だったため、ペインのセッションを持ち替えるたびに前のセッションの ToDo が 1 枚ずつ積み上がり、
// 「他セッションの ToDo が全部のミラーの上に固着する」という形で表面化した（リロードで一度
// 消えてもすぐ増え直す＝サーバは何も返していない）。dev ビルドは "Encountered two children with
// the same key" を警告するが、**本番ビルドは無言**なので人間が気づけない。
//
// 検査するのは「確実に同時に居る」子だけ:
//   - JSX 要素そのもの                      <A key={s} /> <B key={s} />
//   - `cond && <X key={s} />` の 1 個       {ok && <A key={s} />}
// 三項（どちらか一方しか描かれない）と .map() の中（別スコープ）は対象外＝誤検知しない。
import { describe, it, expect } from "vitest";
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

/** その子が「必ず描かれる 1 個の JSX」なら、その key 属性の式テキスト（無ければ null）。 */
function keyOfChild(child: ts.JsxChild, sf: ts.SourceFile): string | null {
  let el: ts.JsxElement | ts.JsxSelfClosingElement | null = null;
  if (ts.isJsxElement(child) || ts.isJsxSelfClosingElement(child)) {
    el = child;
  } else if (ts.isJsxExpression(child) && child.expression && ts.isBinaryExpression(child.expression)) {
    // `cond && <X />` — 描かれるとしたらこの 1 個。
    const b = child.expression;
    if (b.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken) {
      const r = b.right;
      if (ts.isJsxElement(r) || ts.isJsxSelfClosingElement(r)) el = r;
    }
  }
  if (!el) return null;
  const attrs = ts.isJsxElement(el) ? el.openingElement.attributes : el.attributes;
  for (const a of attrs.properties) {
    if (!ts.isJsxAttribute(a) || a.name.getText(sf) !== "key") continue;
    const init = a.initializer;
    if (init && ts.isJsxExpression(init) && init.expression) return init.expression.getText(sf).replace(/\s+/g, " ");
    if (init && ts.isStringLiteral(init)) return JSON.stringify(init.text);
  }
  return null;
}

describe("JSX の兄弟 key", () => {
  it("同じ親で同時に描かれる子が同じ key を共有しない", () => {
    const offenders: string[] = [];
    for (const file of tsxFiles(SRC)) {
      const sf = ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
      const visit = (node: ts.Node) => {
        if (ts.isJsxElement(node) || ts.isJsxFragment(node)) {
          const seen = new Map<string, number>();
          for (const c of node.children) {
            const k = keyOfChild(c, sf);
            if (k === null) continue;
            const line = sf.getLineAndCharacterOfPosition(c.getStart(sf)).line + 1;
            const first = seen.get(k);
            if (first !== undefined) {
              offenders.push(`${path.relative(SRC, file)}:${first},${line}  key={${k}}`);
            } else {
              seen.set(k, line);
            }
          }
        }
        ts.forEachChild(node, visit);
      };
      visit(sf);
    }
    expect(offenders).toEqual([]);
  });
});
