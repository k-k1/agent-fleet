// What MarkdownView lets through from attacker-influenced Markdown: no form controls, no
// inline styles, no media fetched from the Console's own API paths; task lists still render.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

const { MarkdownView } = await import("./MarkdownView.tsx");
const { sanitizeMarkdownHtml, isInternalMediaUrl, scrubCssUrls } = await import("../../lib/sanitize.ts");

let host: HTMLDivElement;
let root: Root;

const render = async (source: string, props: { streaming?: boolean } = {}) => {
  await act(async () => {
    root.render(<MarkdownView source={source} basePath="docs/a.md" {...props} />);
  });
};

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("forbidden elements and attributes", () => {
  it("drops form controls and the style element / attribute from raw HTML", async () => {
    await render(
      [
        '<form action="/api/x" method="post"><input name="a"><button>go</button><select><option>1</option></select><textarea>t</textarea></form>',
        "",
        '<style>body{display:none}</style>',
        "",
        '<div style="position:fixed;inset:0">overlay</div>',
        "",
        "after",
      ].join("\n"),
    );
    for (const sel of ["form", "input", "button.x", "select", "textarea", "style", "[style]"]) {
      expect(host.querySelector(sel), sel).toBeNull();
    }
    expect(host.textContent).toContain("overlay");
    expect(host.textContent).toContain("after");
  });

  it("still renders tables, code and plain HTML such as <br> and <details>", async () => {
    await render("| a | b |\n|---|---|\n| 1 | 2 |\n\n```js\nconst x = 1;\n```\n\n<details><summary>s</summary>body</details>");
    expect(host.querySelector("table td")?.textContent).toBe("1");
    expect(host.querySelector("pre code")).not.toBeNull();
    expect(host.querySelector("details summary")?.textContent).toBe("s");
  });
});

describe("task list", () => {
  it("renders checked / unchecked items as inert boxes, not inputs", async () => {
    await render("- [x] done\n- [ ] todo\n");
    expect(host.querySelector("input")).toBeNull();
    const boxes = [...host.querySelectorAll<HTMLElement>(".md-task-box")];
    expect(boxes.map((b) => b.getAttribute("aria-checked"))).toEqual(["true", "false"]);
    expect(boxes.every((b) => b.getAttribute("role") === "checkbox" && b.getAttribute("aria-disabled") === "true")).toBe(true);
    expect(host.querySelectorAll("li")[0]?.textContent).toContain("done");
  });
});

describe("media from the Console's own paths", () => {
  const origin = window.location.origin;

  it("removes src for absolute URLs into /api/, /oauth2/ and /login", async () => {
    await render(
      [
        `![a](${origin}/api/oauth/x/callback?code=1)`,
        `![b](${origin}/oauth2/logout)`,
        `![c](${origin}/login)`,
        `![d](${origin}/%61pi/x)`,
        `![ok](https://example.com/p.png)`,
      ].join("\n\n"),
    );
    const imgs = [...host.querySelectorAll("img")];
    expect(imgs).toHaveLength(5);
    expect(imgs.slice(0, 4).map((i) => i.hasAttribute("src"))).toEqual([false, false, false, false]);
    expect(imgs[4]?.getAttribute("src")).toBe("https://example.com/p.png");
  });

  it("removes raw-HTML media, srcset, poster and background on internal URLs", () => {
    const html = sanitizeMarkdownHtml(
      `<img src="${origin}/api/a"><img srcset="https://e.example/a.png 1x, ${origin}/oauth2/logout 2x" src="https://e.example/a.png">` +
        `<video src="${origin}/api/v" poster="${origin}/login"></video><audio src="${origin}/api/au"></audio>` +
        `<table background="${origin}/api/bg"><tr><td>x</td></tr></table>`,
    );
    expect(html).not.toMatch(/\/api\/|\/oauth2\/|\/login/);
    expect(html).toContain("https://e.example/a.png");
  });

  it("keeps a repo-relative image in a directory named api, rewritten to the file endpoint", async () => {
    await render("![d](api/diagram.png)\n\n![e](/api/diagram2.png)");
    const srcs = [...host.querySelectorAll("img")].map((i) => i.getAttribute("src") ?? "");
    expect(srcs).toHaveLength(2);
    for (const s of srcs) expect(s).toMatch(/api\/fs\/download|download/);
    expect(srcs[0]).toContain(encodeURIComponent("docs/api/diagram.png"));
  });

  it("checks every relative URL when nothing rewrites them (streaming)", async () => {
    await render("![d](/api/x)\n\n![e](oauth2/y)", { streaming: true });
    expect([...host.querySelectorAll("img")].map((i) => i.hasAttribute("src"))).toEqual([false, false]);
  });

  it("isInternalMediaUrl / scrubCssUrls classify by resolved path", () => {
    expect(isInternalMediaUrl("/api/x")).toBe(true);
    expect(isInternalMediaUrl("../api/x")).toBe(true);
    expect(isInternalMediaUrl("/static/x.png")).toBe(false);
    expect(isInternalMediaUrl("https://other.example/api/x")).toBe(false);
    expect(scrubCssUrls("a{background:url('/api/x')} b{background:url(/s/y.png)}")).toBe("a{background:url()} b{background:url(/s/y.png)}");
  });
});
