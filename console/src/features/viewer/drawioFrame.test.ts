// Assembly of the frame HTML (docs/log/65 §65.3). All that is guarded here is that the
// isolation, and the absence of paths out to external hosts, really are in the string.
// Rendering itself can only be checked in a real browser, which scripts/drawio/check.mjs does.
import { describe, expect, it } from "vitest";
import { drawioFrameSrcdoc, isDrawioFrameEvent, frameChordOf, DRAWIO_MSG, type FrameKey } from "./drawioFrame.ts";
import { eventChordString } from "../../lib/keys/chords.ts";

const html = (dark = false) => drawioFrameSrcdoc({ dark });

describe("drawioFrameSrcdoc", () => {
  it("kills the external default URLs with a dead value rather than an empty string", () => {
    const s = html();
    // "" is falsy and loses to `window.X = window.X || "https://…"` (measured: DRAW_MATH_URL
    // went for viewer.diagrams.net).
    expect(s).not.toMatch(/window\.DRAW_MATH_URL = "";/);
    for (const name of [
      "PROXY_URL",
      "STYLE_PATH",
      "SHAPES_PATH",
      "STENCIL_PATH",
      "DRAW_MATH_URL",
      "GRAPH_IMAGE_PATH",
      "CSS_PATH",
      "DRAWIO_BASE_URL",
      "DRAWIO_SERVER_URL",
      "DRAWIO_LIGHTBOX_URL",
      "DRAWIO_LOG_URL",
    ]) {
      expect(s).toContain(`window.${name} = DEAD;`);
    }
    expect(s).toContain('var DEAD = "about:blank";');
  });

  it("has the frame fetch nothing at all by itself", () => {
    const s = html();
    expect(s).toContain("default-src 'none'");
    expect(s).toContain("connect-src 'none'");
    // Never restore `<script src>`: a request from an origin-less frame counts as cross-site,
    // carries no SameSite=Lax session cookie and is rejected by the CP's authGate with a 401.
    // The viewer's source is passed in by the parent through postMessage.
    expect(s).not.toMatch(/<script[^>]+src=/);
    // No external origin belongs in script-src either; adding one restores the fetch path.
    expect(s).toContain("script-src 'unsafe-inline';");
  });

  it("does not let the frame fetch stencils either (docs/log/65 §65.5.4)", () => {
    const s = html();
    // Keep the viewer's own lazy fetching off. Setting it back to true brings all three back at
    // once: requests that cannot authenticate, a hole in the CSP, and the dead end where a
    // failed set is never retried (all measured).
    expect(s).toContain("mxStencilRegistry.dynamicLoading = false");
    // STENCIL_PATH and SHAPES_PATH therefore stay dead values. Pointing them at the CP would be
    // a step back to the frame fetching for itself.
    expect(s).toMatch(/window\.STENCIL_PATH = DEAD;/);
    expect(s).toMatch(/window\.SHAPES_PATH = DEAD;/);
    // With fetching being the parent's job, there is no reason to open connect-src (also above).
    expect(s).toContain("connect-src 'none'");
  });

  it("resolves the required sets through libraries rather than the bare basename", () => {
    const s = html();
    // Hard-coding `basename + ".xml"` loses mappings such as rackGeneral → rack/general.xml and
    // 404s. Only the path that consults libraries works on real hardware.
    expect(s).toContain("mxStencilRegistry.libraries");
    // The detection reads the expanded model: scanning the raw XML finds nothing in a
    // compressed <diagram> (measured, rawSeen=0).
    expect(s).toContain("v.graph.model");
    // Injection is a refresh, not a re-render, so the zoom and position on screen survive.
    expect(s).toContain("parseStencilSets");
    expect(s).toContain("viewer.graph.refresh()");
  });

  it("keeps the lightbox out of both the toolbar and the configuration", () => {
    const s = html();
    // The one button that takes the drawing to app.diagrams.net; if it appears in the toolbar string, fail.
    expect(s).toContain('toolbar: "pages zoom layers"');
    expect(s).toContain("lightbox: 0");
  });

  it("changes the initial background with the theme", () => {
    expect(html(false)).toContain("background:#ffffff");
    expect(html(true)).toContain("background:#1e1e1e");
  });

  it("passes dark-mode as a string, not a boolean", () => {
    // isDarkMode() compares against "dark" / "auto", so true silently means light and the
    // default black text lands on a dark background, unreadable (docs/log/65 §65.11-10).
    // The value is decided inside the frame at render time, so all that is visible here is the
    // expression. Whether it really rendered dark is verified by scripts/drawio/check.mjs
    // through isDarkMode().
    expect(html()).toContain('"dark-mode": dark ? "dark" : "light"');
    expect(html()).not.toMatch(/"dark-mode": dark,/);
  });

  it("wires up the gestures itself, since GraphViewer wires up none", () => {
    const s = html();
    // A wheel listener defaults to passive, where preventDefault does nothing, so say so.
    expect(s).toContain('"wheel"');
    expect(s).toContain("{ passive: false }");
    expect(s).toContain('"pointerdown"');
    expect(s).toContain('"dblclick"');
    // Turning touch-action off is the only way a phone pinch reaches the diagram.
    expect(s).toContain("touch-action:none");
    // Refitting uses initialViewState, not fitGraph, which does nothing when the width is unchanged.
    expect(s).toContain("initialViewState");
  });

  it("ignores messages from any window but its parent, and accepts boot once", () => {
    const s = html();
    // Order matters: the source check comes before anything reads the message.
    const handler = s.slice(s.indexOf('window.addEventListener("message"'));
    expect(handler.indexOf("e.source !== window.parent")).toBeGreaterThan(0);
    expect(handler.indexOf("e.source !== window.parent")).toBeLessThan(handler.indexOf("e.data"));
    expect(handler).toMatch(/if \(bootSeen\) return;\s*bootSeen = true;\s*boot\(m\.src\)/);
  });

  it("catches failures with addEventListener rather than window.onerror", () => {
    // The viewer overwrites window.onerror (measured).
    const s = html();
    expect(s).toContain('window.addEventListener("error"');
    expect(s).not.toContain("window.onerror =");
  });
});

describe("isDrawioFrameEvent", () => {
  it("accepts only the shape of its own frame's events", () => {
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "ready" })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "booted" })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "rendered", pages: 1, page: 1, scale: 1 })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "error", code: "parse" })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "key", code: "KeyP", ctrlKey: true })).toBe(true);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "keys", chords: [] })).toBe(false);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "render", xml: "" })).toBe(false);
    expect(isDrawioFrameEvent({ af: DRAWIO_MSG, t: "boot", src: "" })).toBe(false);
    expect(isDrawioFrameEvent({ t: "ready" })).toBe(false);
    expect(isDrawioFrameEvent("ready")).toBe(false);
    expect(isDrawioFrameEvent(null)).toBe(false);
  });
});

describe("gateway chords pressed inside the frame", () => {
  // Keys typed in an iframe never reach the Console's window: without this, Ctrl/⌘+P printed
  // and Ctrl/⌘+K went to the browser once the user had clicked into the diagram.
  it("cancels only the chords the parent sent, before handing them over", () => {
    const s = html();
    const listener = s.slice(s.indexOf('"keydown"'));
    // preventDefault comes before the composition check: the browser prints even mid-composition.
    expect(listener).toMatch(/appKeys\.indexOf\(c\) < 0\) return;[^]*?e\.preventDefault\(\);[^]*?if \(e\.isComposing\) return;\s*post\(\{\s*t: "key"/);
    // The frame's `.key` is never sent: the parent must not be handed a field it could misread.
    expect(listener).not.toContain("e.key");
    // Capture phase: the viewer's own handlers must not get to stop it first.
    expect(listener).toMatch(/\},\s*true,\s*\);/);
    expect(s).toContain('if (m.t === "keys")');
  });

  // The function is embedded by its source text; anything it referenced outside its body would
  // be undefined in the frame. Evaluating the text on its own proves it is self-contained.
  const embedded = new Function("return " + frameChordOf.toString())() as (e: FrameKey) => string | null;

  it("computes the same chord as the Console's dispatcher", () => {
    const codes = [
      "KeyP", "KeyK", "KeyA", "KeyZ", "Digit0", "Digit9", "Numpad5", "F1", "F6", "F12", "F24",
      "ArrowUp", "ArrowLeft", "Backslash", "BracketLeft", "BracketRight", "Comma", "Period",
      "Semicolon", "Quote", "Backquote", "Minus", "Equal", "Slash", "Space", "Enter", "NumpadEnter",
      "Tab", "Escape", "Backspace", "Delete", "PageDown", "Home", "NumpadAdd", "IntlRo",
      "ShiftLeft", "ControlRight", "AltLeft", "MetaRight",
    ];
    let compared = 0;
    for (const code of codes) {
      for (let bits = 0; bits < 16; bits++) {
        const init = { code, ctrlKey: !!(bits & 1), metaKey: !!(bits & 2), altKey: !!(bits & 4), shiftKey: !!(bits & 8) };
        const want = eventChordString(init as unknown as KeyboardEvent);
        expect(frameChordOf(init), `${code} ${bits}`).toBe(want);
        expect(embedded(init), `${code} ${bits} (embedded)`).toBe(want);
        compared++;
      }
    }
    expect(compared).toBe(codes.length * 16);
    expect(embedded({ code: "KeyP", ctrlKey: true, metaKey: false, altKey: false, shiftKey: false })).toBe("mod+p");
  });
});
