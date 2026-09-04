// How the File pane presents a `.drawio` file (docs/log/65 §65.4).
//
// Rendering itself cannot be checked in jsdom (scripts inside an iframe never run). What is
// guarded here is which surface appears and what the iframe is allowed to do; the latter is hard
// to see even in a real browser, yet loosening it removes the isolation entirely.
// That a diagram actually draws is checked by scripts/drawio/check.mjs in a real browser.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { revisionOf } from "../editor/buffer.ts";
import { clearDirtyRegistryForTests } from "../editor/dirtyRegistry.ts";

const DIAGRAM = '<mxfile host="app.diagrams.net"><diagram id="a" name="ページ1"></diagram></mxfile>';
const VIEWER_SRC = "/* drawio viewer source */";

let served = { content: DIAGRAM, editable: true, truncated: false };

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path.startsWith("api/fs/linemarks")) return { error: { message: "none" } };
    const { content, editable, truncated } = served;
    return {
      path: "repos/x/design.drawio",
      size: content.length,
      binary: false,
      truncated,
      editable,
      editabilityReason: editable ? null : "read_only_root",
      content,
      ...(editable ? { revision: revisionOf(content) } : {}),
    };
  }),
  downloadURL: (p: string) => `/dl/${p}`,
  isTransientErr: () => false,
  errText: (e: unknown) => String(e),
  // Never make this the identity function: it would stop the test telling whether rel() was
  // applied (a bare relative path yields the same string, so an implementation that breaks under
  // a path-stripping proxy stays green).
  rel: (p: string) => `/agent-fleet/${p}`,
}));

const { FileView } = await import("./FileView.tsx");
const { DrawioView } = await import("./DrawioView.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
let fetched: string[] = [];

async function render(props: { filePath?: string; targetLine?: number; openMode?: "view" | "edit" } = {}) {
  await act(async () => {
    root!.render(<FileView filePath={props.filePath ?? "repos/x/design.drawio"} paneId="pane-1" {...props} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const frame = () => host.querySelector("iframe.drawio-frame") as HTMLIFrameElement | null;
// Whether the diagram surface is visible. Collapsing hides it rather than unmounting it, so
// check `hidden` and not the element's presence (rebuilding would refetch 4 MB).
const diagramVisible = () => {
  const shell = host.querySelector(".file-diagram-shell");
  return !!shell && !shell.hasAttribute("hidden");
};
const groupButtons = (label: string) => {
  const group = host.querySelector(`[aria-label="${label}"]`);
  return group ? [...group.querySelectorAll("button")].map((b) => b.textContent) : null;
};
const clickMode = (label: string) => {
  const button = [...host.querySelectorAll('[aria-label="Diagram display mode"] button')].find(
    (b) => b.textContent === label,
  ) as HTMLButtonElement;
  act(() => button.click());
};
const editorVisible = () => {
  const shell = host.querySelector(".file-editor-shell");
  return !!shell && !shell.hasAttribute("hidden");
};

beforeEach(() => {
  clearDirtyRegistryForTests();
  served = { content: DIAGRAM, editable: true, truncated: false };
  fetched = [];
  vi.stubGlobal("fetch", (url: string) => {
    fetched.push(String(url));
    const viewer = String(url).includes("viewer-static");
    return Promise.resolve({ ok: true, text: async () => (viewer ? VIEWER_SRC : DIAGRAM) } as Response);
  });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.unstubAllGlobals();
  clearDirtyRegistryForTests();
});

describe("the .drawio surface", () => {
  it("opens as a diagram, with neither the Markdown mode group nor the view/edit tabs", async () => {
    await render();
    expect(frame()).not.toBeNull();
    expect(groupButtons("Diagram display mode")).toEqual(["Diagram", "Edit"]);
    expect(groupButtons("Markdown display mode")).toBeNull();
    expect(host.querySelector('[role="tablist"]')).toBeNull();
  });

  it("grants the iframe nothing but allow-scripts", async () => {
    await render();
    // allow-same-origin would give it the Console's own privileges, and allow-popups would let
    // the lightbox's window.open through, handing the drawing to app.diagrams.net.
    expect(frame()!.getAttribute("sandbox")).toBe("allow-scripts");
  });

  it("fetches the diagram from download, not fs/file, to avoid the 2 MiB truncation", async () => {
    await render();
    expect(fetched).toEqual(["/dl/repos/x/design.drawio"]);
  });

  it("still shows the diagram surface when the content was truncated", async () => {
    served = { content: "(file too large to preview)", editable: false, truncated: true };
    await render();
    expect(frame()).not.toBeNull();
    // With no editing surface, the other mode calls itself read-only "Source".
    expect(groupButtons("Diagram display mode")).toEqual(["Diagram", "Source"]);
  });

  it("switching to the source surface collapses the diagram and shows the editor, keeping the frame", async () => {
    await render();
    expect(diagramVisible()).toBe(true);
    expect(editorVisible()).toBe(false);
    clickMode("Edit");
    expect(diagramVisible()).toBe(false);
    expect(editorVisible()).toBe(true);
    // Collapsing never rebuilds it: that would refetch 4 MB and lose the zoom position.
    expect(frame()).not.toBeNull();
  });

  it("a citation naming a line lands on the source surface and does not build the diagram yet", async () => {
    await render({ targetLine: 3 });
    // Nothing is fetched before the diagram has been shown once.
    expect(frame()).toBeNull();
    expect(fetched).toEqual([]);
    expect(editorVisible()).toBe(true);
  });

  it("opens an .xml holding an mxfile as a diagram too", async () => {
    await render({ filePath: "repos/x/diagram.xml" });
    expect(frame()).not.toBeNull();
  });

  it("offers no reader on a diagram, since there is no prose to read out", async () => {
    await render();
    const labels = [...host.querySelectorAll("button")].map((b) => b.textContent);
    expect(labels.some((l) => l?.includes("Read aloud"))).toBe(false);
  });

  it("leaves a plain .xml on the source surface as before", async () => {
    served = { content: "<project><modelVersion>4.0.0</modelVersion></project>", editable: true, truncated: false };
    await render({ filePath: "repos/x/pom.xml" });
    expect(frame()).toBeNull();
    expect(groupButtons("Diagram display mode")).toBeNull();
    expect(host.querySelector('[role="tablist"]')).not.toBeNull();
    // Non-diagram text keeps its reader; this checks the removal condition is not too broad.
    const labels = [...host.querySelectorAll("button")].map((b) => b.textContent);
    expect(labels.some((l) => l?.includes("Read aloud"))).toBe(true);
  });
});

// ── The protocol with the frame (docs/log/65 §65.11-7, §65.11-8) ─────────────────────────────
// jsdom never runs the scripts inside a srcdoc, so the frame's messages are synthesised here.
// What is guarded is what the parent sends and when: a regression guard for two failures that
// only ever appeared in a real browser.
describe("the protocol with the frame", () => {
  const fromFrame = (data: Record<string, unknown>) => {
    const win = frame()!.contentWindow!;
    act(() => {
      window.dispatchEvent(new MessageEvent("message", { data: { af: "af-drawio", ...data }, source: win }));
    });
  };
  const posts = () =>
    (frame()!.contentWindow!.postMessage as unknown as { mock: { calls: unknown[][] } }).mock.calls.map(
      (c) => c[0] as Record<string, unknown>,
    );
  const spyOnFrame = () => {
    vi.spyOn(frame()!.contentWindow!, "postMessage").mockImplementation(() => {});
  };

  it("waits for ready before handing over the viewer, and for booted before asking to render", async () => {
    await render();
    spyOnFrame();
    // Nothing is sent right after creation: measured, a message sent before the srcdoc document
    // exists is delivered to the initial about:blank and lost.
    expect(posts()).toEqual([]);

    fromFrame({ t: "ready" });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    // The parent fetches the viewer: from the frame no Lax cookie is attached and it gets a 401.
    expect(fetched.some((u) => u.includes("viewer-static"))).toBe(true);
    expect(posts().map((m) => m.t)).toEqual(["boot"]);
    expect(posts()[0].src).toBe(VIEWER_SRC);

    fromFrame({ t: "booted" });
    expect(posts().map((m) => m.t)).toEqual(["boot", "render"]);
    expect(posts()[1].xml).toBe(DIAGRAM);
  });

  it("fetches stencils from the CP in the parent on the frame's request and returns the contents", async () => {
    await render();
    spyOnFrame();
    fetched = [];
    // The frame only declares what it needs; the parent does the fetching. From the frame there
    // is no origin, so no Lax cookie is attached and authGate rejects it with a 401.
    fromFrame({ t: "stencils", sets: ["aws4.xml", "rack/general.xml"] });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    // Always go through rel(): a bare relative path resolves against the document URL, so it
    // points somewhere else under a path-stripping proxy or on a deep `/open/...` URL.
    expect(fetched).toEqual([
      "/agent-fleet/api/drawio/stencils/aws4.xml",
      "/agent-fleet/api/drawio/stencils/rack/general.xml",
    ]);
    const back = posts().filter((m) => m.t === "stencils");
    expect(back).toHaveLength(1);
    expect((back[0].xml as string[]).length).toBe(2);
  });

  it("leaves the diagram as it is when stencils cannot be fetched, without raising an error", async () => {
    await render();
    spyOnFrame();
    vi.stubGlobal("fetch", () => Promise.resolve({ ok: false, status: 502, text: async () => "" } as Response));
    fromFrame({ t: "stencils", sets: ["aws4.xml"] });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    // On a closed network only the shape art is empty; frames and colours remain. The diagram
    // opened correctly, so this is not a fault to show the user: nothing appears on screen.
    expect(host.textContent).not.toContain("stencil");
    expect(frame()!.hidden).toBe(false);
    // The frame is still told the fetch failed. Without that, a single upstream blip leaves the
    // icons missing for the whole life of the pane, stuck in the "already requested" state.
    const back = posts().filter((m) => m.t === "stencils");
    expect(back).toHaveLength(1);
    expect(back[0].xml).toEqual([]);
    expect(back[0].missing).toEqual(["aws4.xml"]);
  });

  it("does not claim the diagram is broken when the viewer failed to load", async () => {
    await render();
    spyOnFrame();
    fromFrame({ t: "error", code: "boot" });
    const note = host.querySelector(".drawio-note")!.textContent ?? "";
    expect(note).toContain("Could not load the diagram viewer");
    expect(note).not.toContain("not readable as drawio");
  });

  it("says so plainly when the file is not readable as a diagram", async () => {
    await render();
    spyOnFrame();
    fromFrame({ t: "error", code: "parse" });
    expect(host.querySelector(".drawio-note")!.textContent).toContain("not readable as drawio");
  });
});

// ── Theme switching (docs/log/65 §65.11-12) ──────────────────────────────────────────────────
// drawio does not support switching theme back and forth within one document. Measured: asking
// the same frame to redraw loses the headings and leaves labels as the light theme's white pills
// with black text. The contract is to rebuild the whole frame and carry the viewing position
// over; this checks that wiring.
describe("theme switching", () => {
  const mount = async (dark: boolean) => {
    await act(async () => {
      root!.render(<DrawioView filePath="repos/x/design.drawio" dark={dark} />);
    });
    await act(async () => {
      await Promise.resolve();
    });
  };
  const frameEl = () => host.querySelector("iframe.drawio-frame") as HTMLIFrameElement;
  const postsOf = (el: HTMLIFrameElement) =>
    (el.contentWindow!.postMessage as unknown as { mock: { calls: unknown[][] } }).mock.calls.map(
      (c) => c[0] as Record<string, unknown>,
    );
  const drive = async (el: HTMLIFrameElement) => {
    vi.spyOn(el.contentWindow!, "postMessage").mockImplementation(() => {});
    act(() => {
      window.dispatchEvent(new MessageEvent("message", { data: { af: "af-drawio", t: "ready" }, source: el.contentWindow }));
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    act(() => {
      window.dispatchEvent(new MessageEvent("message", { data: { af: "af-drawio", t: "booted" }, source: el.contentWindow }));
    });
  };

  it("rebuilds the frame on a theme change and carries the page and zoom over", async () => {
    await mount(false);
    const first = frameEl();
    await drive(first);
    expect(postsOf(first).map((m) => m.t)).toEqual(["boot", "render"]);
    // The first render has nothing to carry over.
    expect(postsOf(first)[1].restore).toBeNull();

    // The frame reports that the user has zoomed in and is looking at page 2.
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { af: "af-drawio", t: "rendered", pages: 2, page: 2, scale: 2.5, darkMode: false, pageId: "p2", tx: 12, ty: 34, adjusted: true },
          source: first.contentWindow,
        }),
      );
    });

    // Switch to dark.
    await mount(true);
    const second = frameEl();
    // The element must not be reused; rebuilding it is the whole point.
    expect(second).not.toBe(first);
    await drive(second);
    const render = postsOf(second).find((m) => m.t === "render")!;
    expect(render.dark).toBe(true);
    // The viewing position is passed on unchanged; the page is named by id, not by number.
    expect(render.restore).toEqual({ pageId: "p2", scale: 2.5, tx: 12, ty: 34, adjusted: true });
  });

  it("does not restore a position the user never adjusted, leaving it fitted", async () => {
    await mount(false);
    const first = frameEl();
    await drive(first);
    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { af: "af-drawio", t: "rendered", pages: 1, page: 1, scale: 1, darkMode: false, pageId: "p1", tx: 5, ty: 6, adjusted: false },
          source: first.contentWindow,
        }),
      );
    });
    await mount(true);
    const second = frameEl();
    await drive(second);
    const render = postsOf(second).find((m) => m.t === "render")!;
    // It is still passed, but adjusted=false, so the frame chooses to fit again.
    expect((render.restore as { adjusted: boolean }).adjusted).toBe(false);
  });
});
