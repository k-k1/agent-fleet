// What MarkdownView lets through from attacker-influenced Markdown: no form controls, no
// inline styles, no media fetched from the Console's own API paths; task lists still render.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
const mermaidRender = vi.fn(async () => ({ svg: '<svg data-ok="1"></svg>' }));
vi.mock("mermaid", () => ({ default: { initialize: vi.fn(), render: mermaidRender } }));

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
    for (const sel of ["form", "input", "button", "select", "textarea", "style", "[style]"]) {
      expect(host.querySelector(sel), sel).toBeNull();
    }
    expect(host.textContent).toContain("overlay");
    expect(host.textContent).toContain("after");
  });

  it("keeps the forbidden-tag check meaningful: a stock DOMPurify pass would keep the button", () => {
    const stock = document.createElement("div");
    stock.innerHTML = "<button>go</button>";
    expect(stock.querySelector("button")).not.toBeNull(); // positive control for the assertion above
    expect(sanitizeMarkdownHtml("<button>go</button><p>x</p>")).not.toContain("button");
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

  it("in normal display, relative / root-relative non-<img src> URLs are judged as written", async () => {
    await render(
      [
        '<img srcset="/api/a 1x">',
        '<picture><source srcset="/oauth2/logout"><img src="https://e.example/x.png"></picture>',
        '<video poster="/login" src="/api/v"></video>',
        '<audio src="api/au"></audio>',
      ].join("\n\n"),
    );
    expect(host.innerHTML).not.toMatch(/\/api\/|\/oauth2\/|\/login|api\/au/);
    expect(host.querySelector("img")?.getAttribute("srcset")).toBeNull();
  });

  it("removes an SVG <image> href that points into the Console", () => {
    const html = sanitizeMarkdownHtml('<svg><image href="/api/i" xlink:href="/api/j"></image></svg>', { repoRelative: true });
    expect(html).not.toMatch(/\/api\//);
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

  it("does not render mermaid source that can name a resource, and still renders plain diagrams", async () => {
    mermaidRender.mockClear();
    await render("```mermaid\nflowchart LR\n A@{ img: \"/api/x\", label: \"a\" }\n```");
    await render("```mermaid\nflowchart LR\n A[<img src='/oauth2/logout'>]\n```");
    await new Promise((r) => setTimeout(r, 50));
    expect(mermaidRender).not.toHaveBeenCalled();
    expect(host.querySelector("pre code")).not.toBeNull();
    await render("```mermaid\nflowchart LR\n A-->B\n```");
    await vi.waitFor(() => expect(mermaidRender).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(host.querySelector(".mermaid-diagram svg")).not.toBeNull());
  });

  it("scrubCssUrls sees through escapes, quoted parentheses and image-set / @import strings", () => {
    const bad = [
      "a{background:url(/\\61 pi/x)}",
      "a{background:url(\"/api/x?)(\")}",
      "a{background:U\\52L(/api/x)}",
      "a{background:image-set(\"/oauth2/logout\" 1x)}",
      "@import \"/login\";",
      "a{background:url( '/api/x' )}",
      "a{background:/* c */url(/api/x)}",
    ];
    for (const css of bad) {
      expect(scrubCssUrls(css), css).not.toMatch(/api|oauth2|login|\\61|U\\52L/i);
    }
    const ok = "a{background:url(/static/x.png)} .b\\:c{color:red;content:\"url(/api/x)\"}";
    expect(scrubCssUrls(ok)).toBe(ok);
  });

  it("isInternalMediaUrl / scrubCssUrls classify by resolved path", () => {
    expect(isInternalMediaUrl("/api/x")).toBe(true);
    expect(isInternalMediaUrl("../api/x")).toBe(true);
    expect(isInternalMediaUrl("/static/x.png")).toBe(false);
    expect(isInternalMediaUrl("https://other.example/api/x")).toBe(false);
    expect(scrubCssUrls("a{background:url('/api/x')} b{background:url(/s/y.png)}")).toBe("a{background:url()} b{background:url(/s/y.png)}");
  });
});
