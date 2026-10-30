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

function styleRules(): CSSStyleRule[] {
  return [...document.styleSheets].flatMap((sheet) =>
    [...sheet.cssRules].filter((rule): rule is CSSStyleRule => rule instanceof CSSStyleRule),
  );
}

/** Rules under one theme class, keyed by the selector with that class cut out. */
function rulesOf(themeClass: string): Map<string, string> {
  const scope = `.${themeClass}`;
  const rules = new Map<string, string>();
  for (const rule of styleRules()) {
    if (rule.selectorText.startsWith(scope)) {
      rules.set(rule.selectorText.split(scope).join("&"), rule.style.cssText);
    }
  }
  return rules;
}

describe("editorTheme", () => {
  it("overrides every light base-theme colour the editor would show in the dark theme", () => {
    const plain = mount(basicSetup).themeClasses.split(" ");
    const themed = mount([basicSetup, editorTheme]).themeClasses.split(" ");
    const ours = themed.filter((c) => !plain.includes(c));
    expect(ours).toHaveLength(1);
    // The light base theme's scope class is the one a plain dark-unflagged editor carries
    // and that also scopes the light focused-selection fill.
    const light = plain.find((c) => rulesOf(c).has("&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground"));
    expect(light).toBeDefined();

    const theme = rulesOf(ours[0]);
    const mustOverride = [
      "& .cm-selectionBackground",
      "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground",
      "& .cm-panels",
      "& .cm-panels-top",
      "& .cm-panels-bottom",
      "& .cm-textfield",
      "& .cm-button",
    ];
    for (const selector of mustOverride) {
      expect(rulesOf(light!).has(selector), `base theme still has ${selector}`).toBe(true);
      expect(theme.get(selector), selector).toMatch(/var\(--/);
    }
    // The base caret is black outside the dark theme.
    expect(theme.get("& .cm-cursor, & .cm-dropCursor")).toContain("var(--fg)");
  });
});
