// basicSetup installs defaultHighlightStyle as a fallback highlighter, a light-only
// palette (a Markdown URL is #219 on the dark #1e1e1e, 1.28:1). CodeEditor has to
// install editorHighlightStyle as a regular highlighter so the fallback stays unused.
// These tests mount the real CodeEditor: a style that is merely exported but not
// installed there must fail.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../core/api/client.ts", () => ({
  rel: (p: string) => p,
}));

const { CodeEditor } = await import("./CodeEditor.tsx");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.innerHTML = "";
});

async function mountEditor(path: string, content: string): Promise<HTMLElement> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <CodeEditor
        path={path}
        content={content}
        wrap={false}
        onChange={() => {}}
        onSave={() => {}}
        onValidationError={() => {}}
      />,
    );
  });
  // The language package loads through a dynamic import.
  await vi.waitFor(() => {
    expect(host.querySelector(".cm-line span[class]")).not.toBeNull();
  });
  return host;
}

/** The declarations of every stylesheet rule whose selector is exactly `.cls`. */
function declarationsOf(cls: string): CSSStyleDeclaration[] {
  const found: CSSStyleDeclaration[] = [];
  for (const sheet of document.styleSheets) {
    for (const rule of sheet.cssRules) {
      if (rule instanceof CSSStyleRule && rule.selectorText === `.${cls}`) found.push(rule.style);
    }
  }
  return found;
}

/** The colour the highlight rules give the token spanning `text`. */
function colourOf(host: HTMLElement, text: string): string {
  const span = [...host.querySelectorAll<HTMLElement>(".cm-line span")].find(
    (s) => s.textContent?.trim() === text && s.className,
  );
  expect(span, `no highlighted span for ${text}`).toBeDefined();
  const colours = span!.className
    .split(/\s+/)
    .flatMap(declarationsOf)
    .map((style) => style.getPropertyValue("color"))
    .filter(Boolean);
  expect(colours, `${text} is not coloured`).not.toHaveLength(0);
  return colours.at(-1)!;
}

describe("CodeEditor syntax colours", () => {
  it("colours a Markdown link URL and heading from --hl-* tokens", async () => {
    const host = await mountEditor("note.md", "# Title\n\n[doc](https://example.com/x)\n");
    expect(colourOf(host, "https://example.com/x")).toBe("var(--hl-attr)");
    expect(colourOf(host, "Title")).toBe("var(--hl-attr)");
  });

  it("colours TypeScript keywords and strings from --hl-* tokens", async () => {
    const host = await mountEditor("a.ts", 'const greeting = "hi";\n');
    expect(colourOf(host, "const")).toBe("var(--hl-keyword)");
    expect(colourOf(host, '"hi"')).toBe("var(--hl-string)");
  });

  it("never emits a literal colour from the fallback palette", async () => {
    const host = await mountEditor("note.md", "[doc](https://example.com/x)\n");
    const classes = new Set<string>();
    host.querySelectorAll(".cm-line span[class]").forEach((s) => s.classList.forEach((c) => classes.add(c)));
    for (const cls of classes) {
      for (const style of declarationsOf(cls)) {
        const colour = style.getPropertyValue("color");
        if (colour) expect(colour, `.${cls}`).toMatch(/^var\(--hl-/);
      }
    }
  });
});
