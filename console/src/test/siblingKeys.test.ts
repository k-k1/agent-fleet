// Static check that siblings rendered at the same time under the same parent never share a key.
//
// React's array reconciliation (reconcileChildrenArray) collects the remaining old fibers into
// a key -> fiber Map as soon as a key changes, and deletes whatever is left in the Map at the
// end. A duplicated key makes the Map keep only the last entry, so the earlier fiber matches no
// new child and is never deleted: it is orphaned, and its DOM stays on screen.
//
// Damage: the mirror's todo strip (TaskChecklist) and changed-files strip (FileChangeStrip)
// both used key={session}, so every time a pane changed session the previous session's todo
// list piled up on top, appearing as "another session's todos stick above every mirror" (a
// reload cleared it once, then it grew back, proving the server was sending nothing). Dev
// builds warn with "Encountered two children with the same key"; production builds are silent,
// so nobody can notice.
//
// Only children that are certainly present at the same time are checked:
//   - a JSX element itself                <A key={s} /> <B key={s} />
//   - the one element of `cond && <X key={s} />`   {ok && <A key={s} />}
// Ternaries (only one branch renders) and the inside of .map() (a separate scope) are out of
// scope, so there are no false positives.
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

/** If the child is a single JSX element that is certainly rendered, the text of its key
 *  attribute expression; null otherwise. */
function keyOfChild(child: ts.JsxChild, sf: ts.SourceFile): string | null {
  let el: ts.JsxElement | ts.JsxSelfClosingElement | null = null;
  if (ts.isJsxElement(child) || ts.isJsxSelfClosingElement(child)) {
    el = child;
  } else if (ts.isJsxExpression(child) && child.expression && ts.isBinaryExpression(child.expression)) {
    // `cond && <X />` — this single element is what would be rendered.
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

describe("JSX sibling keys", () => {
  it("never shares a key between children rendered at once under one parent", () => {
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
