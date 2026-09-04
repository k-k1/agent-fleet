// The contents of the iframe that contains the drawio viewer (docs/log/65 §65.3, ADR 0046
// decision 2).
//
// Why an iframe (measured; docs/log/65 §65.2.1):
//   - viewer-static.min.js creates 932 globals on window, including generic names such as
//     `lang` / `dash` / `Base64` / `MathJax` and even an overwrite of window.DOMPurify. Once
//     they are on the app's window there is no way to undo it.
//   - The toolbar's lightbox window.opens DRAWIO_LIGHTBOX_URL (app.diagrams.net by default) and
//     hands the drawing to it. showLightbox's condition is
//     `"open" == lightbox || window.self !== window.top`, so inside an iframe it falls on the
//     side that tries to open the external site regardless of configuration. It is blocked
//     three ways here: (a) keep it off the toolbar, (b) blank the URL, (c) the parent does not
//     grant allow-popups in the sandbox.
//
// Because it is a srcdoc, the iframe has no origin (the parent does not grant
// allow-same-origin). The frame fetches nothing at all by itself: both the diagram XML and the
// 4 MB viewer source are fetched by the parent and passed in by postMessage, so the CSP stays
// at default-src 'none' and script-src needs no external origin.
//
// Never load the viewer with `<script src>`: a request from an origin-less frame counts as
// cross-site, so the SameSite=Lax session cookie is not attached. The CP's authGate does not
// pass `/assets/*` through, the request 401s, and render then runs with GraphViewer undefined
// and misreports the file as not interpretable as a diagram (measured). Fetching is the
// parent's job, since it holds the credentials; the frame only receives.

/** Messages parent → frame and frame → parent. `af` keeps them apart from other postMessages. */
export const DRAWIO_MSG = "af-drawio" as const;

/** Parent → frame. `boot` is sent once (it carries the 4 MB source); `render` any number of times. */
/** The viewing position, passed straight back to restore it when a theme change rebuilds the frame. */
export interface DrawioViewState {
  /** The page on screen, as the diagram's id rather than its number: numbers shift as pages are added or removed. */
  pageId: string | null;
  scale: number;
  tx: number;
  ty: number;
  /** Whether the user zoomed or panned themselves. If not, refit instead of restoring. */
  adjusted: boolean;
}

export type DrawioFrameRequest =
  | { af: typeof DRAWIO_MSG; t: "boot"; src: string }
  | { af: typeof DRAWIO_MSG; t: "render"; xml: string; dark: boolean; restore?: DrawioViewState | null }
  /** The contents of the stencils the frame declared, plus the names of those that could not be
   *  fetched. On a closed network `xml` is empty and `missing` holds them all; that is not an
   *  error, it degrades quietly to a picture with frames and colours only. `missing` is returned
   *  so the frame can drop those from "already requested" and ask again on the next render
   *  (docs/log/65 §65.5.4). */
  | { af: typeof DRAWIO_MSG; t: "stencils"; xml: string[]; missing?: string[] };

export type DrawioFrameEvent =
  | { af: typeof DRAWIO_MSG; t: "ready" }
  | { af: typeof DRAWIO_MSG; t: "booted" }
  | {
      af: typeof DRAWIO_MSG;
      t: "rendered";
      pages: number;
      page: number;
      scale: number;
      /** Whether the viewer really rendered dark, so the parent or the harness can verify it
       *  matches what was asked. Counting pixels is not a reliable test: a drawing that stayed
       *  light on a dark background ends up with MORE light pixels because of the shapes' bright
       *  fills (measured, 40778 against 2387). */
      darkMode: boolean;
      /** The current position, to carry over to a rebuilt frame. */
      pageId: string | null;
      tx: number;
      ty: number;
      adjusted: boolean;
    }
  /** File names of the stencil sets this diagram needs (`aws4.xml`, `rack/f5.xml`).
   *  The frame never fetches them itself: a request from an origin-less frame counts as
   *  cross-site, carries no SameSite=Lax cookie and is rejected by the CP's authGate with a 401
   *  (measured; the same hole as §65.11-7). Fetching is the parent's job, as it holds the
   *  credentials. */
  | { af: typeof DRAWIO_MSG; t: "stencils"; sets: string[] }
  /** `boot` means the viewer could not be evaluated, `parse` that the file was not readable as
   *  a diagram. Never conflate the two: reporting a load failure as "the diagram is broken"
   *  makes the cause look like the file and sends the whole investigation the wrong way. */
  | { af: typeof DRAWIO_MSG; t: "error"; code: "boot" | "parse" | "empty" };

const FRAME_EVENTS = ["ready", "booted", "rendered", "stencils", "error"];

/** Whether the event really came from the frame; anyone can send a postMessage. */
export function isDrawioFrameEvent(data: unknown): data is DrawioFrameEvent {
  if (!data || typeof data !== "object") return false;
  const m = data as { af?: unknown; t?: unknown };
  return m.af === DRAWIO_MSG && typeof m.t === "string" && FRAME_EVENTS.includes(m.t);
}

// srcdoc is an attribute value, so every embedded string must be escaped, double quotes included.
function attrEscape(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

export interface DrawioFrameOptions {
  /** The initial theme. It is passed with every render request too, so this only decides the colour of the first frame. */
  dark: boolean;
}

/**
 * Builds the HTML placed inside the iframe.
 *
 * The CSP starts at default-src 'none' and opens only what is needed. connect-src stays 'none':
 * the frame fetches nothing by itself, stencils included. Opening it would restore a path by
 * which the frame can reach external hosts (docs/log/65 §65.5.4).
 */
export function drawioFrameSrcdoc({ dark }: DrawioFrameOptions): string {
  // The parent passes the viewer source in by postMessage and it is evaluated as an inline
  // script, so no external origin belongs in script-src; adding one would restore the path by
  // which the frame fetches for itself.
  const csp = [
    "default-src 'none'",
    "script-src 'unsafe-inline'",
    "style-src 'unsafe-inline'",
    "img-src data: blob:",
    "font-src data:",
    "connect-src 'none'",
  ].join("; ");

  // The in-frame script. Only plain DOM is available here, not the Console's modules.
  // i18n-exempt-start: this is script text and its comments; no string here reaches the screen.
  const boot = `
(function () {
  // Kill every default that points outside (docs/log/65 §65.2.1-3). In P1 only STENCIL_PATH is
  // pointed at the same-origin CP proxy.
  //
  // The empty string does not kill them: the viewer fills defaults in as
  // window.X = window.X || "https://…", so "" is falsy and the external default survives.
  // Measured: with DRAW_MATH_URL left at "" it went for viewer.diagrams.net/math4/es5/startup.js
  // and the CSP stopped it. Use a dead value that never reaches the network.
  var DEAD = "about:blank";
  window.PROXY_URL = DEAD;
  window.STYLE_PATH = DEAD;
  window.SHAPES_PATH = DEAD;
  window.STENCIL_PATH = DEAD;
  window.DRAW_MATH_URL = DEAD;
  window.GRAPH_IMAGE_PATH = DEAD;
  window.CSS_PATH = DEAD;
  window.DRAWIO_BASE_URL = DEAD;
  window.DRAWIO_SERVER_URL = DEAD;
  window.DRAWIO_LIGHTBOX_URL = DEAD;
  window.DRAWIO_LOG_URL = DEAD;

  var MSG = ${JSON.stringify(DRAWIO_MSG)};
  var viewer = null;
  var booted = false;
  var dark = ${dark ? "true" : "false"};
  // The fit scale, and whether the user moved the view themselves. Once they have, a change in
  // the pane's size must not refit on its own: being reset while reading zoomed in is the worst
  // thing that can happen.
  var fitScale = 1;
  var adjusted = false;

  function post(m) {
    m.af = MSG;
    parent.postMessage(m, "*");
  }

  // The viewer can fail asynchronously, and swallowing that leaves the pane empty for no
  // apparent reason, so always report one line to the parent, which turns it into a message.
  //
  // window.onerror cannot catch it: the viewer overwrites window.onerror with its own logger, so
  // an assigned handler is silently dropped (measured). Listen via addEventListener, which it
  // cannot overwrite.
  window.addEventListener("error", function (e) {
    post({
      t: "error",
      code: booted ? "parse" : "boot",
      detail: String((e && e.message) || e).slice(0, 200),
    });
  });

  // The viewer tries to size the container itself (graph.resizeContainer /
  // updateContainerHeight). To keep it filling the pane, give it an inline width and height and
  // turn resizing off: addSizeHandler branches on the presence of an inline style.height, so a
  // CSS class alone has no effect (measured: in an 860x520 frame the container shrank to
  // 181x341 and the drawing stuck to the top left).
  function host() {
    var old = document.getElementById("c");
    var el = document.createElement("div");
    el.id = "c";
    sizeToViewport(el);
    old.parentNode.replaceChild(el, old);
    return el;
  }

  // Set the background inline on BOTH html and body. The stylesheet colours both with
  // html,body{background:…}, so overriding html alone never takes effect: body's rule paints
  // over it. Measured: built dark but rendered light, the shapes were light while the background
  // stayed #1e1e1e, and the reverse stayed white. Both symptoms users saw were this.
  // docs/log/65 §65.11-13.
  function setBackground(isDark) {
    var color = isDark ? "#1e1e1e" : "#ffffff";
    document.documentElement.style.background = color;
    if (document.body) document.body.style.background = color;
  }

  // The toolbar sits outside the container, above it, and the container gets a marginTop.
  // Without subtracting that, the bottom edge overflows the frame.
  function sizeToViewport(el) {
    var chrome = parseInt(el.style.marginTop || "0", 10) || 0;
    el.style.width = document.documentElement.clientWidth + "px";
    el.style.height = Math.max(0, document.documentElement.clientHeight - chrome) + "px";
  }

  // Leave refitting to the viewer's own fitGraph. allowZoomIn defaults to false, so
  // maxFitScale = 1: a large diagram is scaled down and a small one stays at natural size, both
  // centred. Calling graph.fit directly disagrees with the viewer's initialViewState and shifts
  // the reference the zoom buttons work from.
  function fit(v) {
    if (!v || typeof v.fitGraph !== "function") return;
    sizeToViewport(v.graph.container);
    v.fitGraph();
    fitScale = v.graph.view.scale;
    adjusted = false;
  }

  function render(xml, isDark, restore) {
    dark = !!isDark;
    setBackground(dark);
    var el = host();
    if (!xml || !xml.trim()) {
      post({ t: "error", code: "empty" });
      return;
    }
    el.setAttribute(
      "data-mxgraph",
      JSON.stringify({
        xml: xml,
        // No lightbox: that is the path that takes the drawing outside. Pages, zoom, layers only.
        toolbar: "pages zoom layers",
        "toolbar-nohide": true,
        lightbox: 0,
        nav: true,
        resize: 0,
        center: true,
        // A boolean does not work: isDarkMode() compares against the strings "dark" / "auto",
        // so passing true silently means light. The default text colour stays black on the dark
        // background and default-coloured labels vanish, black on black (measured: luminance 0
        // for the text against 30 for the background, a contrast ratio of 1.3:1).
        // docs/log/65 §65.11-10.
        "dark-mode": dark ? "dark" : "light",
        highlight: "#3572b0",
        // The page to restore, named by id rather than number (graphConfig.pageId): numbers
        // shift as pages are added or removed, and what is wanted is the page last viewed.
        pageId: restore && restore.pageId ? restore.pageId : undefined,
      })
    );
    if (!booted || typeof GraphViewer === "undefined") {
      // Reaching here means only that the viewer could not be evaluated. Do not blame the
      // diagram: the wrong message sends the investigation towards the file.
      post({ t: "error", code: "boot" });
      return;
    }
    try {
      GraphViewer.createViewerForElement(el, function (v) {
        viewer = v;
        // Report the state once fitted; scale drives the header's zoom label and the harness check.
        requestAnimationFrame(function () {
          // Fit first, to settle fitScale and initialViewState, where a double tap returns to.
          fit(v);
          // Then return to where the user had moved the view. If they never did, leave it
          // fitted: for someone who did nothing, that is the correct state.
          if (restore && restore.adjusted) {
            v.graph.view.scaleAndTranslate(restore.scale, restore.tx, restore.ty);
            adjusted = true;
          }
          postState(v);
          // The diagram is already up; missing icons are filled in afterwards, never blocking it.
          askForStencils(v);
        });
        // Page and layer changes report in the same shape, for the header's "n / m".
        if (v.addListener) {
          v.addListener("graphChanged", function () {
            postState(v);
          });
        }
      });
    } catch (e) {
      post({ t: "error", code: "parse" });
    }
  }

  // ── Stencils (docs/log/65 §65.5) ─────────────────────────────────────────
  // The shape art for vendor icons (\`shape=mxgraph.aws4.*\` and friends) is not in the viewer;
  // at 40.8 MB in total it is not bundled. The frame does not fetch it: it declares the file
  // names it needs to the parent, which fetches them from the CP and returns them by postMessage.
  //
  // Setting dynamicLoading back to true and letting the frame fetch was rejected on measurement:
  //   - with no origin, the CP's authGate rejects it with a 401 (the same hole as §65.11-7)
  //   - the CSP's connect-src would have to be opened (restoring the external fetch path)
  //   - a set that failed once is never fetched again: loadStencilSet swallows the failure and
  //     then sets \`packages[basename] = 1\` (measured: a redraw issued no request at all and the
  //     icons stayed empty)
  var stencilAsked = {};

  // Mirrors the viewer's own resolution rule. basename + ".xml" is not enough: sets listed in
  // \`mxStencilRegistry.libraries\` have different file names (ios7icons → ios7/icons.xml,
  // rackGeneral → rack/general.xml, ibmcloud → ibm_cloud.xml, …). Get this wrong and the name is
  // not in the registry and 404s.
  function stencilFilesFor(basename) {
    var reg = window.mxStencilRegistry;
    var prefix = String(window.STENCIL_PATH) + "/";
    var lib = reg && reg.libraries && reg.libraries[basename];
    if (lib) {
      var out = [];
      for (var i = 0; i < lib.length; i++) {
        var f = String(lib[i]);
        // The same table also lists SHAPES_PATH \`.js\` entries, but those shapes are already
        // baked into viewer-static and the viewer itself sets
        // \`mxStencilRegistry.allowEval = false\` at the end (measured). Do not fetch them.
        if (f.indexOf(prefix) === 0 && f.slice(-4) === ".xml") out.push(f.slice(prefix.length));
      }
      return out;
    }
    return [basename.replace("_-_", "_") + ".xml"];
  }

  // Works out which sets are still missing from the model after rendering. Read the model, not
  // the raw XML: in a compressed \`<diagram>\` the raw XML is still deflated and nothing is found
  // (measured). The model is already expanded, so compression makes no difference.
  function neededStencils(v) {
    var reg = window.mxStencilRegistry;
    if (!reg || !v || !v.graph || !v.graph.model) return [];
    var need = {};
    var cells = v.graph.model.cells || {};
    for (var id in cells) {
      var style = cells[id].style;
      if (!style || style.indexOf("mxgraph.") < 0) continue;
      var re = /mxgraph\\.[A-Za-z0-9_]+(?:\\.[A-Za-z0-9_]+)+/g;
      var m;
      while ((m = re.exec(style))) {
        var full = m[0];
        if (mxCellRenderer.defaultShapes[full]) continue;                  // JS shapes are baked in
        if (reg.stencils[full] || reg.stencils[full.toLowerCase()]) continue; // already have it
        var parts = full.split(".").slice(1);
        if (parts.length < 2) continue;
        var files = stencilFilesFor(parts.slice(0, -1).join("/"));
        for (var i = 0; i < files.length; i++) {
          if (!stencilAsked[files[i]]) need[files[i]] = true;
        }
      }
    }
    return Object.keys(need);
  }

  // Declare each set only once; no re-request even when the parent returns nothing.
  function askForStencils(v) {
    var want = neededStencils(v);
    if (!want.length) return;
    for (var i = 0; i < want.length; i++) stencilAsked[want[i]] = true;
    post({ t: "stencils", sets: want });
  }

  // Registers the stencils the parent sent and redraws. Do not re-run render: \`graph.refresh()\`
  // swaps in the shape art alone and keeps the zoom and position the user was looking at
  // (measured: the scale stayed at 1.8221 while paths went from 1 to 3).
  function addStencils(xmls, missing) {
    // Drop what could not be fetched from "already requested". Otherwise a single upstream blip
    // leaves the icons missing for the whole life of the frame, recreating on this side exactly
    // the dead end that got the viewer's own lazy fetching rejected (\`packages[basename]=1\` in
    // §65.5.4-3). A first fetch on real hardware did hit a connection reset from
    // raw.githubusercontent.
    if (missing) {
      for (var k = 0; k < missing.length; k++) delete stencilAsked[missing[k]];
    }
    if (!viewer || !xmls || !xmls.length || !window.mxStencilRegistry) return;
    try {
      mxStencilRegistry.parseStencilSets(xmls);
      viewer.graph.refresh();
      postState(viewer);
    } catch (e) {
      // The diagram is already up; only the icons are missing, so this is not an error.
    }
  }

  // ── Gestures (zoom / pan) ────────────────────────────────────────────────
  // GraphViewer wires up nothing: init sets pinchEnabled=false and setPanning(false) and never
  // subscribes to the wheel. Only the toolbar buttons exist, so these are added here:
  //   Ctrl/Cmd + wheel   … zoom about the pointer (a trackpad pinch arrives the same way)
  //   plain wheel        … pan in all directions
  //   two-finger pinch   … zoom about the midpoint
  //   one-finger drag    … pan
  //   double click / tap … fit ↔ natural size (2x when the fit is already natural size)
  var MIN_SCALE = 0.05;
  var MAX_SCALE = 16;

  function graph() {
    return viewer && viewer.graph;
  }

  // Let gestures on the toolbar through, so the buttons keep working.
  function onToolbar(target) {
    return !!(viewer && viewer.toolbar && target && viewer.toolbar.contains(target));
  }

  // Coordinates with the container's top left as origin; used as the zoom anchor.
  function localPoint(clientX, clientY) {
    var g = graph();
    if (!g) return { x: 0, y: 0 };
    var box = g.container.getBoundingClientRect();
    return { x: clientX - box.left, y: clientY - box.top };
  }

  // Changes the scale while holding one point on screen fixed. In mxGraph
  // screen = (graph + translate) * scale, so the translate that keeps that point still is
  // translate + p * (1/s' - 1/s).
  function zoomAt(nextScale, px, py) {
    var g = graph();
    if (!g) return;
    var s = g.view.scale;
    var s2 = Math.max(MIN_SCALE, Math.min(MAX_SCALE, nextScale));
    if (!isFinite(s2) || s2 === s) return;
    var t = g.view.translate;
    adjusted = true;
    g.view.scaleAndTranslate(s2, t.x + px * (1 / s2 - 1 / s), t.y + py * (1 / s2 - 1 / s));
    postState(viewer);
  }

  function panBy(dx, dy) {
    var g = graph();
    if (!g || (!dx && !dy)) return;
    var s = g.view.scale;
    var t = g.view.translate;
    adjusted = true;
    g.view.scaleAndTranslate(s, t.x + dx / s, t.y + dy / s);
  }

  // Returns to the state first shown. fitGraph cannot be used: it does nothing when the
  // container width is unchanged (an early return on N == t), so a double tap, which changes no
  // dimension, has no effect (measured: it stayed at 4.37x). Restoring the initialViewState the
  // viewer keeps is faster and does not disagree with the zoom buttons' reference.
  function resetView(v) {
    var g = v && v.graph;
    if (!g) return;
    var st = g.initialViewState;
    if (st && st.translate) {
      g.view.scaleAndTranslate(st.scale, st.translate.x, st.translate.y);
      fitScale = st.scale;
    } else {
      fit(v);
    }
    adjusted = false;
    postState(v);
  }

  // Double click / double tap: when fitted, go to natural size; otherwise refit. Only when the
  // fit is already natural size does it go to 2x, so the gesture is never a no-op.
  function toggleZoom(px, py) {
    var g = graph();
    if (!g) return;
    var atFit = Math.abs(g.view.scale - fitScale) < 0.01;
    if (!atFit) {
      resetView(viewer);
      return;
    }
    zoomAt(fitScale >= 0.99 ? 2 : 1, px, py);
  }

  function installGestures() {
    var el = document.documentElement;

    // A wheel listener registered passive cannot preventDefault, so opt out explicitly.
    el.addEventListener(
      "wheel",
      function (e) {
        if (!graph() || onToolbar(e.target)) return;
        // deltaMode: 0=px, 1=line, 2=page. Keep the same feel on line/page browsers.
        var unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1;
        e.preventDefault();
        var p = localPoint(e.clientX, e.clientY);
        // ctrlKey is also set by a trackpad pinch, a convention shared across browsers.
        if (e.ctrlKey || e.metaKey) {
          zoomAt(graph().view.scale * Math.exp((-e.deltaY * unit) / 400), p.x, p.y);
        } else {
          panBy(-e.deltaX * unit, -e.deltaY * unit);
        }
      },
      { passive: false }
    );

    var points = {};   // pointerId -> {x,y}
    var pinch = null;  // {dist, scale}
    var lastTap = 0;
    var lastTapPos = null;

    el.addEventListener("pointerdown", function (e) {
      if (!graph() || onToolbar(e.target)) return;
      points[e.pointerId] = { x: e.clientX, y: e.clientY };
      if (el.setPointerCapture) {
        try {
          el.setPointerCapture(e.pointerId);
        } catch (err) {
          /* another element already has capture, etc.; panning still works, so carry on */
        }
      }
      var ids = Object.keys(points);
      if (ids.length === 2) {
        var a = points[ids[0]];
        var b = points[ids[1]];
        pinch = { dist: Math.hypot(a.x - b.x, a.y - b.y), scale: graph().view.scale };
      }
    });

    el.addEventListener("pointermove", function (e) {
      var prev = points[e.pointerId];
      if (!prev || !graph()) return;
      var cur = { x: e.clientX, y: e.clientY };
      points[e.pointerId] = cur;
      var ids = Object.keys(points);
      if (ids.length >= 2 && pinch) {
        var a = points[ids[0]];
        var b = points[ids[1]];
        var dist = Math.hypot(a.x - b.x, a.y - b.y);
        if (pinch.dist > 0) {
          var mid = localPoint((a.x + b.x) / 2, (a.y + b.y) / 2);
          zoomAt((pinch.scale * dist) / pinch.dist, mid.x, mid.y);
        }
        return;
      }
      panBy(cur.x - prev.x, cur.y - prev.y);
    });

    function endPointer(e) {
      if (!points[e.pointerId]) return;
      delete points[e.pointerId];
      if (Object.keys(points).length < 2) pinch = null;
      if (!graph()) return;
      // Double-tap detection: dblclick varies too much across touch environments to rely on.
      var now = e.timeStamp || 0;
      var pos = { x: e.clientX, y: e.clientY };
      if (lastTapPos && now - lastTap < 350 && Math.hypot(pos.x - lastTapPos.x, pos.y - lastTapPos.y) < 30) {
        var p = localPoint(pos.x, pos.y);
        lastTap = 0;
        lastTapPos = null;
        toggleZoom(p.x, p.y);
        return;
      }
      lastTap = now;
      lastTapPos = pos;
    }
    el.addEventListener("pointerup", endPointer);
    el.addEventListener("pointercancel", endPointer);

    el.addEventListener("dblclick", function (e) {
      if (!graph() || onToolbar(e.target)) return;
      e.preventDefault();
      var p = localPoint(e.clientX, e.clientY);
      toggleZoom(p.x, p.y);
    });
  }

  function postState(v) {
    var page = v.diagrams && v.diagrams[v.currentPage || 0];
    post({
      t: "rendered",
      pages: v.diagrams ? v.diagrams.length : 1,
      page: (v.currentPage || 0) + 1,
      scale: Math.round(v.graph.view.scale * 100) / 100,
      darkMode: !!(v.isDarkMode && v.isDarkMode()),
      pageId: page && page.getAttribute ? page.getAttribute("id") : null,
      tx: v.graph.view.translate.x,
      ty: v.graph.view.translate.y,
      adjusted: adjusted,
    });
  }

  // Receives the 4 MB viewer source from the parent and evaluates it as an inline script.
  // Execution is synchronous, so GraphViewer exists as soon as append returns; if not, boot failed.
  function boot(src) {
    if (booted) return;
    var el = document.createElement("script");
    el.textContent = src;
    document.body.appendChild(el);
    if (typeof GraphViewer === "undefined") {
      post({ t: "error", code: "boot" });
      return;
    }
    booted = true;
    installGestures();
    // Never let the viewer fetch stencils itself (docs/log/65 §65.5.4). neededStencils() works
    // out what is required and the parent fetches it from the CP. Setting this back to true
    // brings back all three at once: requests that cannot authenticate, a hole in the CSP, and
    // the dead end where a failed set is never retried (all measured).
    if (window.mxStencilRegistry) mxStencilRegistry.dynamicLoading = false;
    post({ t: "booted" });
    if (pending) {
      var m = pending;
      pending = null;
      render(m.xml, m.dark, m.restore);
    }
  }

  // A render request can arrive before boot, so exactly one is held and replayed after boot.
  var pending = null;

  window.addEventListener("message", function (e) {
    var m = e.data;
    if (!m || m.af !== MSG) return;
    if (m.t === "boot") {
      boot(m.src);
      return;
    }
    if (m.t === "stencils") {
      addStencils(m.xml, m.missing);
      return;
    }
    if (m.t !== "render") return;
    if (!booted) {
      pending = m;
      return;
    }
    render(m.xml, m.dark, m.restore);
  });

  // Tells the parent this document can now receive messages; the parent waits for it before
  // sending. Sent right after the iframe is created, there is no srcdoc document yet and the
  // message is delivered to the initial about:blank and lost (measured: 0/10/50 ms were lost,
  // 200 ms got through).
  post({ t: "ready" });

  // When the pane's size changes, refit rather than redraw.
  if (window.ResizeObserver) {
    new ResizeObserver(function () {
      if (!viewer) return;
      if (adjusted) {
        // Keep the scale and position; only follow the container's size.
        sizeToViewport(viewer.graph.container);
        return;
      }
      fit(viewer);
    }).observe(document.body);
  }
})();`;
  // i18n-exempt-end

  // i18n-exempt-start: the HTML skeleton; it contains no display text.
  return `<!doctype html>
<html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${attrEscape(csp)}">
<style>
/* Without touch-action:none, a pinch on a phone is taken as page zoom and never reaches the
   diagram. overscroll-behavior stops the parent pane being dragged when panning past an edge. */
html,body{margin:0;padding:0;height:100%;overflow:hidden;background:${dark ? "#1e1e1e" : "#ffffff"};
  touch-action:none;overscroll-behavior:contain;-webkit-user-select:none;user-select:none;}
#c{position:absolute;inset:0;}
</style></head>
<body><div id="c"></div>
<script>${boot}</script>
</body></html>`;
  // i18n-exempt-end
}
