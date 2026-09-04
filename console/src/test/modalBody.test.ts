// Static check that everything inside `<Modal>` sits on ui-modal-head / ui-modal-body /
// ui-modal-foot.
//
// The ui/Modal shell carries no padding by design: head / body / foot each own their own
// spacing. Content placed directly under `<Modal>` therefore gets zero horizontal padding, so
// the heading sits 12px from the frame while its text and inputs stick to the border.
//
// Damage: five modals had this shape at once (measured: heading 13px, body 1px). Every one of
// them was green, because the tests only checked that something rendered. A visual break like
// this is catchable statically once it is expressed as structure.
//
// Only intrinsic elements directly under `<Modal>` are inspected (div / p / footer ...). A
// child starting with a capital is a component, and nesting another dialog inside `<Modal>` is
// legitimate (ShareListModal -> ShareCreateModal); its contents are checked when that file is
// scanned. Shapes where "this is what would be rendered" is written in JSX
// (`{cond && <div className="ui-modal-body">}`, a ternary) are unwrapped and looked into.
// Shapes built from a function call or a variable are given up on and passed, so false
// positives do not make the test a nuisance.
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

/** The string-literal part of className; for an expression like `"a " + b`, as much as can
 *  be read. */
function classNameOf(el: El): string {
  const attrs = ts.isJsxElement(el) ? el.openingElement.attributes : el.attributes;
  for (const a of attrs.properties) {
    if (!ts.isJsxAttribute(a) || a.name.getText() !== "className" || !a.initializer) continue;
    if (ts.isStringLiteral(a.initializer)) return a.initializer.text;
    return a.initializer.getText(); // {"ui-modal-body " + x} and the like are read as raw text
  }
  return "";
}

/** Collect the JSX elements this child could render. Shapes that cannot be unwrapped (a
 *  function call, say) are ignored rather than yielding null. */
function renderedElements(node: ts.Node): El[] {
  if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node)) return [node];
  if (ts.isJsxFragment(node)) return node.children.flatMap(renderedElements);
  if (ts.isJsxExpression(node)) return node.expression ? renderedElements(node.expression) : [];
  if (ts.isParenthesizedExpression(node)) return renderedElements(node.expression);
  // cond && <X /> — this is what would be rendered
  if (ts.isBinaryExpression(node)) return renderedElements(node.right);
  // cond ? <A /> : <B /> — either could be rendered
  if (ts.isConditionalExpression(node)) {
    return [...renderedElements(node.whenTrue), ...renderedElements(node.whenFalse)];
  }
  return [];
}

describe("modal contents sit on the shared shell", () => {
  it("puts nothing but ui-modal-* directly under <Modal> (anything else loses its padding)", () => {
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
              if (!/^[a-z]/.test(tag)) continue; // components (nested dialogs etc.) are checked separately
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
