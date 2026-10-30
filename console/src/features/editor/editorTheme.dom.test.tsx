// CodeEditor never flags its view dark, so CodeMirror's LIGHT base theme applies in every
// app theme. Each light-theme fill that sits behind or over the text has to be overridden
// by editorTheme with the same selector (so it wins on order), or the dark theme paints
// the light fill under light --fg text (#d7d4f0 under #ddd for a focused selection).
import { afterEach, describe, expect, it } from "vitest";
import { EditorState, type Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { editorTheme } from "./CodeEditor.tsx";

const views: EditorView[] = [];

afterEach(() => {
  for (const view of views.splice(0)) view.destroy();
  document.body.innerHTML = "";
});

function mount(extensions: Extension): EditorView {
  const view = new EditorView({
    state: EditorState.create({ doc: "abc", extensions }),
    parent: document.body,
  });
  views.push(view);
  return view;
}

interface Rule {
  /** Position in the cascade: later wins between equal selectors. */
  order: number;
  style: CSSStyleDeclaration;
}

/** Rules under one theme class, keyed by the selector with that class written as `&`. */
function rulesOf(themeClass: string): Map<string, Rule> {
  const scope = `.${themeClass}`;
  const rules = new Map<string, Rule>();
  let order = 0;
  for (const sheet of document.styleSheets) {
    for (const rule of sheet.cssRules) {
      order++;
      if (rule instanceof CSSStyleRule && rule.selectorText.startsWith(scope)) {
        rules.set(rule.selectorText.split(scope).join("&"), { order, style: rule.style });
      }
    }
  }
  return rules;
}

/** The value `style` gives `longhand`, directly or through a shorthand. jsdom keeps a
 *  shorthand holding var() unexpanded, so `border: … var(--border)` has no longhands. */
function valueFor(style: CSSStyleDeclaration, longhand: string): string | undefined {
  for (const property of style) {
    if (longhand === property || longhand.startsWith(`${property}-`)) {
      return style.getPropertyValue(property);
    }
  }
  return undefined;
}

const MUST_OVERRIDE = [
  "& .cm-selectionBackground",
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground",
  "& .cm-panels",
  "& .cm-panels-top",
  "& .cm-panels-bottom",
  "& .cm-tooltip",
  "& .cm-tooltip-section:not(:first-child)",
  "& .cm-textfield",
  "& .cm-button",
  "& .cm-button:active",
];

describe("editorTheme", () => {
  it("replaces every colour the light base theme paints behind or over the text with a token", () => {
    const plain = mount(basicSetup).themeClasses.split(" ");
    const themed = mount([basicSetup, editorTheme]).themeClasses.split(" ");
    const ours = themed.filter((c) => !plain.includes(c));
    expect(ours).toHaveLength(1);
    const light = plain.find((c) =>
      rulesOf(c).has("&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground"),
    );
    expect(light).toBeDefined();
    const base = rulesOf(light!);
    const theme = rulesOf(ours[0]);

    for (const selector of MUST_OVERRIDE) {
      const baseRule = base.get(selector);
      const themeRule = theme.get(selector);
      expect(baseRule, `base theme no longer has ${selector}`).toBeDefined();
      expect(themeRule, selector).toBeDefined();
      expect(themeRule!.order, `${selector} loses the cascade`).toBeGreaterThan(baseRule!.order);
      const painted = [...baseRule!.style].filter((p) => /color|background|border-image/.test(p));
      expect(painted.length, selector).toBeGreaterThan(0);
      for (const property of painted) {
        expect(valueFor(themeRule!.style, property), `${selector} ${property}`).toMatch(
          /var\(--|^none$/,
        );
      }
    }
  });

  it("draws the caret in --fg instead of the base theme's black", () => {
    const view = mount([basicSetup, editorTheme]);
    const theme = rulesOf(view.themeClasses.split(" ").at(-1)!);
    expect(theme.get("& .cm-cursor, & .cm-dropCursor")?.style.getPropertyValue("border-left-color")).toBe(
      "var(--fg)",
    );
    expect(theme.get("& .cm-content")?.style.getPropertyValue("caret-color")).toBe("var(--fg)");
  });
});
