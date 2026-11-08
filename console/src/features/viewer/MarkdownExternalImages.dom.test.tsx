// Cross-origin images in rendered Markdown wait for a click; same-origin ones load as before.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("mermaid", () => ({ default: { initialize: vi.fn(), render: vi.fn() } }));

const { MarkdownView } = await import("./MarkdownView.tsx");
const { sanitizeMarkdownHtml, deferExternalMedia, stripInternalMedia } = await import("./sanitize.ts");
const { wireExternalImages } = await import("./parts/mdExternalImages.ts");

let host: HTMLDivElement;
let root: Root;
const origin = window.location.origin;

const render = async (source: string, props: { streaming?: boolean } = {}) => {
  await act(async () => {
    root.render(<MarkdownView source={source} basePath="docs/a.md" {...props} />);
  });
};
// Attributes that make an element fetch, anywhere under host.
const fetching = () => host.querySelectorAll("img[src], img[srcset], source[src], source[srcset], video[src], video[poster]");

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("external images", () => {
  it("shows a placeholder with the host and no fetching attribute for a Markdown image", async () => {
    await render("![cat](https://tracker.example/p.png)");
    expect(fetching()).toHaveLength(0);
    const btn = host.querySelector<HTMLButtonElement>("button.md-ext-img");
    expect(btn?.textContent).toContain("tracker.example");
    expect(host.querySelector("img")).toBeNull();
  });

  it("covers raw HTML <img>, protocol-relative URLs, srcset and <picture><source>", async () => {
    await render(
      [
        '<img src="//a.example/1.png">',
        '<img src="/x.png" srcset="https://b.example/2.png 2x">',
        '<picture><source srcset="https://c.example/3.png"><img src="/local.png"></picture>',
        '<video src="https://d.example/v.mp4" poster="https://d.example/p.png"></video>',
      ].join("\n\n"),
    );
    expect(fetching()).toHaveLength(0);
    expect(host.querySelectorAll("button.md-ext-img")).toHaveLength(3);
    expect(host.innerHTML).not.toMatch(/d\.example/);
  });

  it("works while streaming too", async () => {
    await render("![a](https://tracker.example/p.png)", { streaming: true });
    expect(fetching()).toHaveLength(0);
    expect(host.querySelectorAll("button.md-ext-img")).toHaveLength(1);
  });

  it("loads one image on click with referrerpolicy=no-referrer, leaving the others deferred", async () => {
    await render("![a](https://one.example/1.png)\n\n![b](https://two.example/2.png)");
    const btns = host.querySelectorAll<HTMLButtonElement>("button.md-ext-img");
    expect(btns).toHaveLength(2);
    await act(async () => btns[0]!.click());
    const img = host.querySelector("img");
    expect(img?.getAttribute("src")).toBe("https://one.example/1.png");
    expect(img?.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(host.querySelectorAll("button.md-ext-img")).toHaveLength(1);
    expect(host.querySelectorAll("img")).toHaveLength(1);
  });

  it("restores a <picture> with its sources before the <img>", async () => {
    await render('<picture><source srcset="https://c.example/3.png"><img src="https://c.example/4.png"></picture>');
    await act(async () => host.querySelector<HTMLButtonElement>("button.md-ext-img")!.click());
    expect(host.querySelector("source")?.getAttribute("srcset")).toBe("https://c.example/3.png");
    expect(host.querySelector("img")?.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(host.innerHTML).not.toContain("data-md-ext");
  });
});

describe("restoring on click goes through the normal image rules", () => {
  const clickAll = async () => {
    for (const b of [...host.querySelectorAll<HTMLButtonElement>("button.md-ext-img")]) await act(async () => b.click());
  };
  const loaded = () => [...host.querySelectorAll("img, source")].flatMap((e) => ["src", "srcset"].map((a) => e.getAttribute(a) ?? ""));

  it("never GETs a Console path after the click (mixed src + external srcset, picture fallback)", async () => {
    await render(
      [
        '<img src="/api/probe-mixed" srcset="https://tracker.example/a.png 0x">',
        '<picture><source media="(width: 0px)" srcset="https://tracker.example/a.png"><img src="/api/probe-picture"></picture>',
        '<img src="/oauth2/x" srcset="https://tracker.example/b.png 2x">',
      ].join("\n\n"),
    );
    await clickAll();
    // Every loaded URL is external or the workspace file endpoint; never the raw Console path.
    const urls = loaded().filter(Boolean);
    expect(urls.length).toBeGreaterThan(0);
    for (const u of urls) expect(u.startsWith("https://tracker.example/") || u.includes("api/fs/download?path=")).toBe(true);
    expect(host.innerHTML).not.toMatch(/\/oauth2\/|\/login/);
  });

  it("rewrites a repo-relative fallback <img> to the file endpoint and sets no-referrer on it", async () => {
    await render('<picture><source srcset="https://tracker.example/p.png"><img src="docs/local.png"></picture>');
    await clickAll();
    const img = host.querySelector("img")!;
    expect(img.getAttribute("src")).toContain("api/fs/download");
    expect(img.getAttribute("src")).toContain("local.png");
    expect(img.getAttribute("referrerpolicy")).toBe("no-referrer");
  });

  it("keeps same-origin srcset candidates so the intended source is still selected", async () => {
    await render(
      [
        '<picture><source media="(width: 0px)" srcset="https://tracker.example/unused.png"><source srcset="/local-source.png"><img src="/local-fallback.png"></picture>',
        '<img src="https://tracker.example/a.png" srcset="/local-1x.png 1x, /local-2x.png 2x">',
      ].join("\n\n"),
    );
    await clickAll();
    const sources = [...host.querySelectorAll("source")].map((e) => e.getAttribute("srcset"));
    expect(sources).toEqual(["https://tracker.example/unused.png", "/local-source.png"]);
    expect(host.querySelectorAll("img")[1]?.getAttribute("srcset")).toBe("/local-1x.png 1x, /local-2x.png 2x");
  });

  it("drops the whole srcset when one candidate names a Console path", async () => {
    await render('<img src="https://tracker.example/a.png" srcset="https://tracker.example/b.png 1x, /api/x 2x">');
    await clickAll();
    expect(host.innerHTML).not.toContain("/api/x");
  });

  it("drops a relative src while streaming (nothing rewrites it)", async () => {
    await render('<img src="docs/x.png" srcset="https://tracker.example/a.png 1x">', { streaming: true });
    await clickAll();
    expect(host.querySelector("img")?.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(loaded().join(" ")).not.toContain("api/fs/download");
  });
});

describe("SVG <feImage>", () => {
  it("drops an external or internal feImage href", () => {
    const html = sanitizeMarkdownHtml(
      '<svg><filter><feImage href="https://tracker.example/p.png"/><feImage xlink:href="/api/x"/></filter></svg>',
    );
    expect(html).not.toMatch(/tracker\.example|\/api\//);
    expect(html.toLowerCase()).toContain("feimage"); // positive control: the element itself survives
  });

  it("stripInternalMedia drops an internal feImage href", () => {
    const r = document.createElement("div");
    r.innerHTML = '<svg><filter><feImage href="/api/x"/></filter></svg>';
    stripInternalMedia(r);
    expect(r.innerHTML).not.toContain("/api/x");
  });
});

describe("SVG presentation attributes", () => {
  const attrs = ["fill", "stroke", "clip-path", "mask", "marker-start", "marker-mid", "marker-end", "filter", "cursor"];
  const svg = (u: string) => `<svg><rect ${attrs.map((a) => `${a}="url(${u}#x)"`).join(" ")}/></svg>`;

  it("scrubs external and Console url() in each attribute itself, keeping #fragment references", () => {
    const html = sanitizeMarkdownHtml(svg("https://tracker.example/f.svg") + svg("/api/x") + '<svg><rect fill="url(#grad)" clip-path="url(#c)"/></svg>');
    expect(html).not.toMatch(/tracker\.example|\/api\//);
    const box = document.createElement("div");
    box.innerHTML = html;
    const rects = box.querySelectorAll("rect");
    expect(rects).toHaveLength(3);
    for (const a of attrs) for (const r of [rects[0]!, rects[1]!]) expect(r.getAttribute(a) ?? "", a).not.toMatch(/https?:|\/api\//);
    expect(rects[2]!.getAttribute("fill")).toBe("url(#grad)");
    expect(rects[2]!.getAttribute("clip-path")).toBe("url(#c)");
  });

  it("stripInternalMedia scrubs a Console url() in those attributes", () => {
    const r = document.createElement("div");
    r.innerHTML = svg("/api/x");
    stripInternalMedia(r);
    expect(r.innerHTML).not.toContain("/api/x");
  });
});

describe("what keeps loading or stays blocked", () => {
  it("leaves same-origin and repo-relative images loading, and data: images", async () => {
    await render(`![a](${origin}/files/a.png)\n\n![b](img/b.png)\n\n![c](data:image/png;base64,AAAA)`);
    expect(host.querySelector("button.md-ext-img")).toBeNull();
    expect(host.querySelectorAll("img[src]")).toHaveLength(3);
  });

  it("does not turn a Console /api/ image into click-to-load", async () => {
    await render(`![a](${origin}/api/oauth/x?c=1)\n\n![b](${origin}/oauth2/logout)\n\n<img srcset="/login 1x, /api/y 2x">`);
    expect(host.querySelector("button.md-ext-img")).toBeNull();
    expect(host.innerHTML).not.toMatch(/\/api\/|oauth2|\/login|data-md-ext/);
  });

  it("an author-written data-md-ext-* attribute cannot smuggle a URL to the click handler", async () => {
    await render('<img data-md-ext-src="/api/logout" alt="x">\n\n<img src="https://e.example/a.png" data-md-ext-src="/api/logout">');
    expect(host.innerHTML).not.toContain("/api/logout");
    await act(async () => host.querySelector<HTMLButtonElement>("button.md-ext-img")!.click());
    expect(host.querySelector("img[src]")?.getAttribute("src")).toBe("https://e.example/a.png");
  });

  it("drops an external SVG <image> href", () => {
    const html = sanitizeMarkdownHtml('<svg><image href="https://e.example/i.png" xlink:href="https://e.example/j.png"></image></svg>');
    expect(html).not.toContain("e.example");
  });
});

describe("renderers outside the Markdown sanitizer (Marp)", () => {
  it("defers external images and scrubs external inline CSS urls", () => {
    const root = document.createElement("div");
    root.innerHTML =
      '<section><img src="https://m.example/a.png"><figure style="background-image:url(https://m.example/bg.png)"></figure><img src="/ok.png"></section>';
    root.querySelectorAll("*").forEach(deferExternalMedia);
    wireExternalImages(root);
    expect(root.querySelectorAll("button.md-ext-img")).toHaveLength(1);
    expect(root.querySelector("img")?.getAttribute("src")).toBe("/ok.png");
    expect(root.innerHTML).not.toContain("m.example/bg");
  });
});
