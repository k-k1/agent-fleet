// Rendering harness for the `.drawio` pane (docs/log/65).
//
// What it protects:
//  1. The diagram renders with the bundled viewer alone (zero external network requests). The
//     viewer carries viewer.diagrams.net as a default in several places, so a single outgoing
//     request makes "works offline" a lie. One missed dead-value substitution in drawioFrame.ts
//     is enough to turn this red (measured: it caught a missed DRAW_MATH_URL).
//  2. A compressed `<diagram>` (deflate+base64) opens too.
//  3. Multiple pages are counted and handed to the parent (the material for the header's
//     "n / m" display).
//  4. Both light and dark render.
//  5. A requested dark render really is dark. `"dark-mode"` does not take a boolean (isDarkMode()
//     compares against the strings "dark" / "auto"); passing true silently keeps the light
//     rendering on a dark background, and default-coloured labels come out black on black. The
//     verdict comes from asking the viewer for isDarkMode() — counting pixels is unreliable here,
//     since the non-dark rendering has MORE bright pixels thanks to the shapes' light fills
//     (measured 40778 vs 2387: a naive threshold answers exactly backwards).
//  6. Gestures work: Ctrl+wheel / two-finger pinch / double tap. GraphViewer wires none of them
//     (init sets pinchEnabled=false and setPanning(false)), so whether our own implementation is
//     alive can only be told by pressing in a real browser.
//  7. Switching theme does not break the diagram. drawio does not expect a theme round trip
//     inside one document: asking the same frame to redraw drops container headings and leaves
//     edge labels as the light theme's white pill with black text (measured). DrawioView rebuilds
//     the frame and restores the viewport (page, scale, position). Here we follow that procedure
//     and check that it ends up dark with the scale preserved.
//  8. The background is painted in the requested theme. The colour comes from two places: the
//     srcdoc stylesheet and the inline style set at render time. Overriding only html inline
//     leaves body's rule painting over it, so the theme used at build time survives (measured:
//     built dark + requested light stayed #1e1e1e, and the reverse stayed white; both symptoms
//     users reported were this). Here the build and the request are deliberately mismatched and
//     the verdict is by pixel — a flat background colour maps straight onto the question, unlike
//     contrast.
//  9. It renders even when the assets sit behind an auth gate. A sandboxed iframe has no origin,
//     so its requests count as cross-site and carry no SameSite=Lax session cookie. A design
//     where the frame fetches its own `<script src>` is rejected with 401 by CP's authGate and
//     breaks only on a real deployment. Here the server answers cookie-less requests with 401 and
//     the diagram must still render — a plain local static server can never surface this class of
//     defect, so without it we fall into the same hole again and again.
//
// No bundle, no CP, no Agent: the frame is exactly the HTML drawioFrame.ts builds, read into
// headless Chromium over raw CDP (Node's global WebSocket). Same style as ../pane-heads/check.mjs.
//
//   npm --prefix console run drawio:check
//   node console/scripts/drawio/check.mjs --screenshot /tmp/drawio.png
//
// Run this in real time. `--virtual-time-budget` does not advance the sandboxed iframe (a
// separate process): time stops after ready and it looks like nothing renders — that is a harness
// trap, not a product defect.
//
// The exit status is the verdict: 0 = every case rendered, zero external requests.
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import zlib from "node:zlib";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.join(HERE, "../../..");
const VIEWER = path.join(REPO, "console/vendor/drawio/viewer-static.min.js");
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const CHROME = arg("chrome", process.env.CHROMIUM || "/usr/bin/chromium");
const SHOT = arg("screenshot", "");
const PORT = Number(arg("port", 0)) || 8000 + (process.pid % 1000);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------------------------------------------------------------- fixtures
const PLAIN = `<mxfile host="app.diagrams.net">
  <diagram name="ページ1" id="p1"><mxGraphModel dx="800" dy="600" page="1"><root>
    <mxCell id="0"/><mxCell id="1" parent="0"/>
    <mxCell id="n1" value="開始" style="ellipse;whiteSpace=wrap;html=1;fillColor=#d5e8d4;strokeColor=#82b366;" vertex="1" parent="1"><mxGeometry x="80" y="40" width="120" height="60" as="geometry"/></mxCell>
    <mxCell id="n2" value="処理する" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;" vertex="1" parent="1"><mxGeometry x="80" y="160" width="120" height="60" as="geometry"/></mxCell>
    <mxCell id="n3" value="判定?" style="rhombus;whiteSpace=wrap;html=1;fillColor=#fff2cc;strokeColor=#d6b656;" vertex="1" parent="1"><mxGeometry x="60" y="280" width="160" height="80" as="geometry"/></mxCell>
    <mxCell id="e1" style="edgeStyle=orthogonalEdgeStyle;html=1;" edge="1" parent="1" source="n1" target="n2"><mxGeometry relative="1" as="geometry"/></mxCell>
    <mxCell id="e2" value="はい" style="edgeStyle=orthogonalEdgeStyle;html=1;dashed=1;" edge="1" parent="1" source="n2" target="n3"><mxGeometry relative="1" as="geometry"/></mxCell>
  </root></mxGraphModel></diagram>
  <diagram name="ページ2" id="p2"><mxGraphModel page="1"><root>
    <mxCell id="0"/><mxCell id="1" parent="0"/>
    <mxCell id="m1" value="2枚目" style="whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="40" y="40" width="160" height="60" as="geometry"/></mxCell>
  </root></mxGraphModel></diagram>
</mxfile>`;

// drawio's compressed form: encodeURIComponent -> raw deflate -> base64.
function compressed(source) {
  const inner = source.match(/<mxGraphModel[\s\S]*?<\/mxGraphModel>/)[0];
  const raw = zlib.deflateRawSync(Buffer.from(encodeURIComponent(inner), "utf8"), { level: 9 });
  return `<mxfile host="app.diagrams.net"><diagram name="圧縮" id="c1">${raw.toString("base64")}</diagram></mxfile>`;
}

// A diagram clearly larger than the pane. With fit working the scale ends up < 1.
const BIG = `<mxfile><diagram name="大" id="b1"><mxGraphModel page="1"><root>
  <mxCell id="0"/><mxCell id="1" parent="0"/>
  ${Array.from({ length: 24 }, (_, i) => `<mxCell id="b${i}" value="ノード${i}" style="rounded=1;whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="${(i % 6) * 420}" y="${Math.floor(i / 6) * 320}" width="360" height="220" as="geometry"/></mxCell>`).join("")}
</root></mxGraphModel></diagram></mxfile>`;

// ── Stencils (docs/log/65 §65.5) ──────────────────────────────────────────
// The real aws4.xml is 6.2 MB, so the harness serves a minimal set of the same shape (byte-level
// correctness is owned by CP's sha256 check and the manifest test). Whether the artwork actually
// landed is judged by the number of paths: without a stencil the shape is a bare rectangle.
function stencilSet(setName, shapeName) {
  return `<shapes name="mxgraph.${setName}">
  <shape aspect="fixed" h="44" name="${shapeName}" strokewidth="inherit" w="44">
    <connections/>
    <foreground>
      <path><move x="4" y="4"/><line x="40" y="4"/><line x="40" y="40"/><line x="4" y="40"/><close/></path>
      <fillstroke/>
      <path><move x="12" y="12"/><line x="32" y="32"/></path>
      <stroke/>
    </foreground>
  </shape>
</shapes>`;
}

// Manifest key (= the file name requested from CP) -> contents.
const STENCIL_FILES = {
  // The straightforward case: basename "aws4" -> "aws4.xml".
  "aws4.xml": stencilSet("aws4", "a1 instance"),
  // A case where the basename and the file name differ: the viewer's mxStencilRegistry.libraries
  // rewrites rackGeneral -> rack/general.xml. An implementation that just concatenates
  // basename + ".xml" gets a 404 here, and this one case is what catches it.
  "rack/general.xml": stencilSet("rackGeneral", "rack unit"),
};

function stencilDiagram(shapeStyle, name) {
  return `<mxfile><diagram name="${name}" id="s1"><mxGraphModel page="1"><root>
    <mxCell id="0"/><mxCell id="1" parent="0"/>
    <mxCell id="v1" value="X" style="html=1;whiteSpace=wrap;shape=${shapeStyle};" vertex="1" parent="1"><mxGeometry x="40" y="40" width="80" height="80" as="geometry"/></mxCell>
  </root></mxGraphModel></diagram></mxfile>`;
}
const AWS4 = stencilDiagram("mxgraph.aws4.a1_instance", "aws");
const RACK = stencilDiagram("mxgraph.rackGeneral.rack_unit", "rack");

const CASES = [
  { name: "plain-light", xml: PLAIN, dark: false, pages: 2, scale: 1, gestures: true },
  // Vendor icons: the frame declares which sets it needs and the PARENT fetches and hands them in.
  { name: "stencil-aws4", xml: AWS4, dark: false, pages: 1, stencils: ["aws4.xml"] },
  // A compressed diagram must reach the same set (the raw XML is still deflated, so the lookup
  // has to run against the inflated model).
  { name: "stencil-compressed", xml: compressed(AWS4), dark: false, pages: 1, stencils: ["aws4.xml"] },
  // The libraries rewrite (rackGeneral -> rack/general.xml).
  { name: "stencil-remap", xml: RACK, dark: false, pages: 1, stencils: ["rack/general.xml"] },
  // Air-gapped. A failed fetch must leave the diagram open and raise no error (it degrades to
  // outlines and colour only).
  { name: "stencil-offline", xml: AWS4, dark: false, pages: 1, stencils: ["aws4.xml"], stencilFail: true },
  // After a blip the set must be requested again. Marking a set that could not be fetched as
  // "already asked for" makes one upstream reset cost that pane its icons for its whole lifetime.
  { name: "stencil-retry", xml: AWS4, dark: false, pages: 1, stencils: ["aws4.xml"], stencilFail: true, retryAfterFail: true },
  // The dark theme is checked down to "are default-coloured labels readable" (a boolean here
  // renders black on black).
  { name: "plain-dark", xml: PLAIN, dark: true, pages: 2, scale: 1 },
  { name: "compressed", xml: compressed(PLAIN), dark: false, pages: 1, scale: 1 },
  { name: "big", xml: BIG, dark: false, pages: 1, maxScale: 0.5 },
  // XML that is not a readable diagram. Rather than silently showing the viewer's own English
  // message, it must come back as an event the parent can handle.
  { name: "not-a-diagram", xml: "<foo><bar/></foo>", dark: false, expectError: true },
  // Open in light, zoom, then go through a theme switch (= frame rebuild).
  { name: "theme-switch", xml: PLAIN, dark: false, pages: 2, themeSwitch: true },
  // Mismatch build and request on purpose. The background must follow the REQUEST.
  { name: "bg-follows-request-dark", xml: PLAIN, dark: true, builtDark: false, pages: 2, bg: "#1e1e1e" },
  { name: "bg-follows-request-light", xml: PLAIN, dark: false, builtDark: true, pages: 2, bg: "#ffffff" },
];

// ---------------------------------------------------------------- frame html
// Use drawioFrame.ts as-is: if the harness wrote its own HTML, what it protects would no longer
// be the product code. esbuild is present in node_modules as a dependency of vite.
async function loadFrameBuilder() {
  const out = path.join(os.tmpdir(), `af-drawio-frame-${process.pid}.cjs`);
  const esbuild = path.join(REPO, "console/node_modules/.bin/esbuild");
  await new Promise((res, rej) => {
    const p = spawn(esbuild, [path.join(REPO, "console/src/features/viewer/drawioFrame.ts"), "--format=cjs", `--outfile=${out}`], { stdio: "inherit" });
    p.on("exit", (c) => (c === 0 ? res() : rej(new Error("esbuild failed"))));
  });
  const mod = await import(`file://${out}`);
  return mod.default ?? mod;
}

// ---------------------------------------------------------------- static server
// A server that imitates CP: `/assets/*` answers 401 without a session cookie, the same shape as
// authGate. The page hands out a Lax cookie, so it rides along on the parent's fetch but not on a
// request from the origin-less frame — exactly what happened on a real deployment.
function serve(dir, port) {
  return new Promise((resolve) => {
    import("node:http").then(({ createServer }) => {
      const srv = createServer((req, res) => {
        const url = req.url.split("?")[0];
        // As on CP, stencils sit inside the auth gate too. The parent (which has the cookie) gets
        // through and a request from the origin-less frame gets 401 — that is precisely why the
        // design of letting the frame fetch them itself was rejected, so reproduce it here.
        const gated = url.startsWith("/assets/") || url.startsWith("/stencils/");
        if (gated && !(req.headers.cookie || "").includes("af_session=")) {
          unauthorized.push(url);
          res.writeHead(401, { "Content-Type": "application/json" }).end('{"error":"unauthenticated"}');
          return;
        }
        if (url.startsWith("/stencils/")) {
          const name = decodeURIComponent(url.slice("/stencils/".length));
          stencilAsks.push(name);
          const body = stencilFail ? null : STENCIL_FILES[name];
          if (!body) {
            // A name absent from the manifest is 404 on CP, 502 when air-gapped. Either way it
            // cannot be fetched.
            res.writeHead(STENCIL_FILES[name] ? 502 : 404).end("no");
            return;
          }
          res.writeHead(200, {
            "Content-Type": "text/xml; charset=utf-8",
            "Set-Cookie": "af_session=harness; Path=/; HttpOnly; SameSite=Lax",
          }).end(body);
          return;
        }
        const file = path.join(dir, decodeURIComponent(url.startsWith("/assets/") ? url.slice("/assets/".length) : url));
        fs.readFile(file, (err, buf) => {
          if (err) {
            res.writeHead(404).end("no");
            return;
          }
          res.writeHead(200, {
            "Content-Type": (file.endsWith(".js") ? "text/javascript" : "text/html") + "; charset=utf-8",
            // SameSite=Lax, as on CP. Without it the defect cannot be reproduced.
            "Set-Cookie": "af_session=harness; Path=/; HttpOnly; SameSite=Lax",
          }).end(buf);
        });
      });
      srv.listen(port, "127.0.0.1", () => resolve(srv));
    });
  });
}

/** Requests rejected for having no cookie (= evidence the frame fetched something itself). */
const unauthorized = [];
/** Stencil names the parent requested from CP. */
const stencilAsks = [];
/** While true, no stencil is served (reproducing the air-gapped case). */
let stencilFail = false;
/** Screenshots of the stencil cases. With and without the stencil must not look identical. */
const stencilShots = {};

// ---------------------------------------------------------------- CDP
async function browser() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "af-drawio-"));
  const proc = spawn(
    CHROME,
    [
      "--headless",
      "--disable-gpu",
      "--no-sandbox",
      "--hide-scrollbars",
      "--remote-debugging-address=127.0.0.1",
      // A fixed port risks grabbing another session's Chromium in the same container. Take 0 and
      // read the port actually bound from DevToolsActivePort.
      "--remote-debugging-port=0",
      `--user-data-dir=${dir}`,
      "about:blank",
    ],
    { stdio: "ignore" },
  );
  let port = "";
  for (let i = 0; i < 100 && !port; i++) {
    await sleep(100);
    try {
      port = fs.readFileSync(path.join(dir, "DevToolsActivePort"), "utf8").split("\n")[0];
    } catch {}
  }
  if (!port) throw new Error("chromium did not report a debugging port");
  const target = await (await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" })).json();
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const pending = new Map();
  const events = [];
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      pending.get(m.id)(m.result);
      pending.delete(m.id);
    } else if (m.method) events.push(m);
  };
  const call = (method, params = {}) =>
    new Promise((res) => {
      const i = ++id;
      pending.set(i, res);
      ws.send(JSON.stringify({ id: i, method, params }));
    });
  return { call, events, close: () => (ws.close(), proc.kill(), fs.rmSync(dir, { recursive: true, force: true })) };
}

// ---------------------------------------------------------------- run
const fail = [];
const { drawioFrameSrcdoc } = await loadFrameBuilder();
const work = fs.mkdtempSync(path.join(os.tmpdir(), "af-drawio-www-"));
fs.copyFileSync(VIEWER, path.join(work, "viewer.js"));
const srv = await serve(work, PORT);
const base = `http://127.0.0.1:${PORT}`;
const b = await browser();
await b.call("Page.enable");
// Screenshots are cut off at the default window width (780px). Make it wider than the frame.
await b.call("Emulation.setDeviceMetricsOverride", { width: 900, height: 600, deviceScaleFactor: 1, mobile: false });
await b.call("Network.enable");

for (const c of CASES) {
  // Only cases that set builtDark deliberately diverge the build-time theme from the request.
  const srcdoc = drawioFrameSrcdoc({ dark: c.builtDark === undefined ? c.dark : c.builtDark });
  // The parent's procedure mirrors DrawioView.tsx (diverge from the real thing here and what is
  // protected stops being the product code): wait for ready -> hand the viewer body we fetched
  // ourselves through boot -> wait for booted, then render. The frame issues no request at all.
  const page = `<!doctype html><html><head><meta charset="utf-8"></head><body style="margin:0;background:${c.dark ? "#1e1e1e" : "#fff"}">
<iframe id="f" sandbox="allow-scripts" width="860" height="520" style="border:0;display:block" srcdoc="${srcdoc.replace(/&/g, "&amp;").replace(/"/g, "&quot;")}"></iframe>
<script>
window.__ev=[];
function post(m){ m.af="af-drawio"; document.getElementById("f").contentWindow.postMessage(m,"*"); }
window.__dark=${c.dark}; window.__restore=null;
window.addEventListener("message",function(e){ var d=e.data; if(!d||d.af!=="af-drawio")return; window.__ev.push(d);
  if(d.t==="ready") fetch("/assets/viewer.js",{credentials:"same-origin"}).then(function(r){return r.text()}).then(function(src){ post({t:"boot",src:src}); });
  if(d.t==="booted") post({t:"render",xml:${JSON.stringify(c.xml)},dark:window.__dark,restore:window.__restore?JSON.parse(window.__restore):null});
  // Same as DrawioView.tsx: the frame declares what it needs, the PARENT fetches it and hands the
  // contents back.
  if(d.t==="stencils"){ window.__asked=(window.__asked||[]).concat(d.sets);
    Promise.all(d.sets.map(function(n){
      return fetch("/stencils/"+n.split("/").map(encodeURIComponent).join("/"),{credentials:"same-origin"})
        .then(function(r){ return r.ok?r.text():null }).catch(function(){ return null });
    })).then(function(xs){
      var got=xs.filter(Boolean);
      var missing=d.sets.filter(function(_,i){ return !xs[i] });
      if(got.length||missing.length) post({t:"stencils",xml:got,missing:missing});
    });
  }
});
</script></body></html>`;
  fs.writeFileSync(path.join(work, `${c.name}.html`), page);

  b.events.length = 0;
  unauthorized.length = 0;
  stencilAsks.length = 0;
  stencilFail = !!c.stencilFail;
  await b.call("Page.navigate", { url: `${base}/${c.name}.html` });
  // Wait in real time (see the header). Ample for loading the 4MB viewer plus rendering.
  await sleep(3500);

  const got = await b.call("Runtime.evaluate", { expression: "JSON.stringify(window.__ev)", returnByValue: true });
  const evs = JSON.parse(got.result?.value || "[]");
  const rendered = evs.filter((e) => e.t === "rendered").pop();
  const errors = evs.filter((e) => e.t === "error");

  if (c.expectError) {
    if (!errors.length) fail.push(`${c.name}: did not come back as an error (events=${JSON.stringify(evs)})`);
  } else {
    if (!rendered) fail.push(`${c.name}: did not render (events=${JSON.stringify(evs)})`);
    else if (rendered.pages !== c.pages) fail.push(`${c.name}: ${rendered.pages} pages (expected ${c.pages})`);
    else if (c.scale && rendered.scale !== c.scale) fail.push(`${c.name}: scale ${rendered.scale} (expected ${c.scale})`);
    else if (c.maxScale && !(rendered.scale < c.maxScale)) fail.push(`${c.name}: not scaled down (scale=${rendered.scale})`);
    if (errors.length) fail.push(`${c.name}: error ${JSON.stringify(errors)}`);
  }

  // External traffic. Not one request may go anywhere but our own static server.
  const external = b.events
    .filter((e) => e.method === "Network.requestWillBeSent")
    .map((e) => e.params.request.url)
    .filter((u) => !u.startsWith(base) && !u.startsWith("data:") && !u.startsWith("about:") && !u.startsWith("blob:"));
  if (external.length) fail.push(`${c.name}: external request ${JSON.stringify([...new Set(external)])}`);
  // A request without the cookie = a request the origin-less frame issued for itself.
  if (unauthorized.length) {
    fail.push(`${c.name}: the frame fetched without credentials ${JSON.stringify([...new Set(unauthorized)])}`);
  }

  // ── Stencils (docs/log/65 §65.5) ────────────────────────────────────────
  if (c.stencils) {
    const want = JSON.stringify(c.stencils);
    const asked = JSON.stringify([...new Set(stencilAsks)]);
    if (asked !== want) {
      fail.push(`${c.name}: requested sets ${asked} (expected ${want}) - is the basename-to-file-name rewrite still in place?`);
    }
    // Did the artwork really land? The frame is an opaque origin, so the parent cannot count what
    // is inside it: judge by whether a redraw happened (a second rendered after the injection) and
    // whether the picture changed (pixels). A check that only watches the request go out stays
    // green even when the received XML is thrown away.
    const renders = evs.filter((e) => e.t === "rendered").length;
    if (c.stencilFail) {
      if (renders !== 1) fail.push(`${c.name}: redrew even though nothing was fetched (rendered ${renders} times)`);
      // Air-gapped means the diagram stays up and no error is raised (degrading to outlines and
      // colour is the correct outcome).
      if (errors.length) fail.push(`${c.name}: turned a failed fetch into an error`);
    } else if (renders < 2) {
      fail.push(`${c.name}: stencils were handed in but no redraw happened (rendered ${renders} times)`);
    }
    // Scale and position must not move on injection (it is a refresh(), not a re-render).
    const first = evs.find((e) => e.t === "rendered");
    const last = evs.filter((e) => e.t === "rendered").pop();
    if (first && last && (first.scale !== last.scale || first.tx !== last.tx || first.ty !== last.ty)) {
      fail.push(`${c.name}: the viewport moved on injection (${first.scale}@${first.tx},${first.ty} -> ${last.scale}@${last.tx},${last.ty})`);
    }
    if (c.retryAfterFail) {
      // Restore connectivity and render again. The same set must be requested a second time.
      stencilFail = false;
      stencilAsks.length = 0;
      await b.call("Runtime.evaluate", {
        expression: `post({t:"render",xml:${JSON.stringify(c.xml)},dark:false,restore:null})`,
        returnByValue: true,
      });
      await sleep(2500);
      const again = [...new Set(stencilAsks)];
      if (JSON.stringify(again) !== JSON.stringify(c.stencils)) {
        fail.push(`${c.name}: a set that failed to fetch is never requested again (second round ${JSON.stringify(again)})`);
      } else {
        console.log(`   stencils: re-requested after failure ${JSON.stringify(again)}`);
      }
      const evs2 = JSON.parse(
        (await b.call("Runtime.evaluate", { expression: "JSON.stringify(window.__ev)", returnByValue: true })).result?.value || "[]",
      );
      // The second attempt gets through, so it must reach the redraw from the injection.
      if (evs2.filter((e) => e.t === "rendered").length < 3) {
        fail.push(`${c.name}: re-requested but the artwork never landed (rendered ${evs2.filter((e) => e.t === "rendered").length} times)`);
      }
    }

    // The picture itself. Same diagram, same dimensions, so only the stencils can differ.
    const shot = await b.call("Page.captureScreenshot", {});
    stencilShots[c.name] = shot.data;
    console.log(`   stencils: asked=${asked} rendered=${renders} times scale=${last?.scale}`);
  }

  // ── Background (docs/log/65 §65.11-13) ────────────────────────────────────
  if (c.bg && rendered) {
    // Sample a corner with no shape on it (the frame is 860x520 and the diagram fits centred).
    const shot = await b.call("Page.captureScreenshot", { clip: { x: 840, y: 500, width: 8, height: 8, scale: 1 } });
    const px = await b.call("Runtime.evaluate", {
      expression: `(async () => {
        const img = new Image();
        img.src = "data:image/png;base64," + ${JSON.stringify(shot.data)};
        await img.decode();
        const cv = document.createElement("canvas");
        cv.width = img.width; cv.height = img.height;
        cv.getContext("2d").drawImage(img, 0, 0);
        const d = cv.getContext("2d").getImageData(2, 2, 1, 1).data;
        return "#" + [d[0], d[1], d[2]].map((v) => v.toString(16).padStart(2, "0")).join("");
      })()`,
      awaitPromise: true,
      returnByValue: true,
    });
    const got = px.result?.value;
    if (got !== c.bg) {
      fail.push(`${c.name}: background is ${got} (requested ${c.bg}) - the build-time theme is still there`);
    } else {
      console.log(`   background: ${got}`);
    }
  }

  // ── Theme switch (docs/log/65 §65.11-12) ──────────────────────────────────
  // The same procedure as DrawioView: zoom in -> rebuild the frame (a new srcdoc) -> boot again
  // from ready -> hand the previous viewport back through restore.
  if (c.themeSwitch && rendered) {
    // First imitate the user zooming in, to create something that has to carry over.
    await b.call("Input.dispatchMouseEvent", { type: "mouseWheel", x: 430, y: 260, deltaX: 0, deltaY: -240, modifiers: 2 });
    await sleep(400);
    const before = await b.call("Runtime.evaluate", {
      expression: "JSON.stringify(window.__ev.filter(function(e){return e.t==='rendered'}).pop())",
      returnByValue: true,
    });
    const kept = JSON.parse(before.result?.value || "null");

    const darkDoc = drawioFrameSrcdoc({ dark: true }).replace(/&/g, "&amp;").replace(/"/g, "&quot;");
    await b.call("Runtime.evaluate", {
      expression: `(() => {
        const old = document.getElementById("f");
        const next = document.createElement("iframe");
        next.id = "f"; next.width = "860"; next.height = "520";
        next.style.cssText = "border:0;display:block";
        next.setAttribute("sandbox", "allow-scripts");
        window.__dark = true;
        window.__restore = ${JSON.stringify(JSON.stringify(kept))};
        next.srcdoc = ${JSON.stringify(darkDoc.replace(/&amp;/g, "&").replace(/&quot;/g, '"'))};
        old.replaceWith(next);
        window.__ev.length = 0;
        return true;
      })()`,
      returnByValue: true,
    });
    await sleep(4000);

    const after = await b.call("Runtime.evaluate", {
      expression: "JSON.stringify(window.__ev.filter(function(e){return e.t==='rendered'}).pop())",
      returnByValue: true,
    });
    const now = JSON.parse(after.result?.value || "null");
    if (!now) fail.push(`${c.name}: the rebuilt frame did not render`);
    else {
      if (!now.darkMode) fail.push(`${c.name}: not dark after the rebuild`);
      if (Math.abs(now.scale - kept.scale) > 0.01) {
        fail.push(`${c.name}: scale was not carried over (${kept.scale} -> ${now.scale})`);
      }
      if (now.pageId !== kept.pageId) fail.push(`${c.name}: page was not carried over (${kept.pageId} -> ${now.pageId})`);
      console.log(`   theme-switch: scale ${kept.scale} -> ${now.scale} / page ${now.pageId} / darkMode=${now.darkMode}`);
    }
  }

  // ── Gestures (docs/log/65 §65.12) ─────────────────────────────────────────
  // The frame emits rendered on every scale change, so the parent can observe what a press did.
  if (c.gestures && rendered) {
    const scaleNow = async () => {
      const r = await b.call("Runtime.evaluate", {
        expression: "JSON.stringify(window.__ev.filter(function(e){return e.t==='rendered'}).pop()||null)",
        returnByValue: true,
      });
      return JSON.parse(r.result?.value || "null")?.scale ?? null;
    };
    const base0 = await scaleNow();

    // Ctrl+wheel (modifiers: 2 = Ctrl). A trackpad pinch takes the same path.
    await b.call("Input.dispatchMouseEvent", {
      type: "mouseWheel", x: 430, y: 260, deltaX: 0, deltaY: -240, modifiers: 2,
    });
    await sleep(400);
    const afterWheel = await scaleNow();
    if (!(afterWheel > base0)) fail.push(`${c.name}: Ctrl+wheel does not zoom in (${base0} -> ${afterWheel})`);

    // A plain wheel must not change the scale (pan only).
    await b.call("Input.dispatchMouseEvent", { type: "mouseWheel", x: 430, y: 260, deltaX: 0, deltaY: -240 });
    await sleep(300);
    const afterPan = await scaleNow();
    if (afterPan !== afterWheel) fail.push(`${c.name}: a plain wheel moved the scale (${afterWheel} -> ${afterPan})`);

    // Two-finger pinch (spreading = zoom in). Enable touch before dispatching.
    await b.call("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 5 });
    await b.call("Input.dispatchTouchEvent", {
      type: "touchStart", touchPoints: [ { x: 380, y: 260, id: 1 }, { x: 480, y: 260, id: 2 } ],
    });
    for (const spread of [40, 80, 120]) {
      await b.call("Input.dispatchTouchEvent", {
        type: "touchMove",
        touchPoints: [ { x: 430 - spread, y: 260, id: 1 }, { x: 430 + spread, y: 260, id: 2 } ],
      });
      await sleep(60);
    }
    await b.call("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await sleep(400);
    const afterPinch = await scaleNow();
    if (!(afterPinch > afterPan)) fail.push(`${c.name}: pinch does not zoom in (${afterPan} -> ${afterPinch})`);

    // Double tap = back to fit (we are zoomed in, so it must drop to fit).
    for (let i = 0; i < 2; i++) {
      await b.call("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [{ x: 430, y: 260, id: 3 }] });
      await b.call("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
      await sleep(60);
    }
    await sleep(500);
    const afterTap = await scaleNow();
    if (!(afterTap < afterPinch)) fail.push(`${c.name}: double tap does not refit (${afterPinch} -> ${afterTap})`);
    await b.call("Emulation.setTouchEmulationEnabled", { enabled: false });
    console.log(`   gestures: fit=${base0} -> ctrl+wheel=${afterWheel} -> pinch=${afterPinch} -> dbltap=${afterTap}`);
  }

  // ── Contrast (docs/log/65 §65.12) ────────────────────────────────────────
  // Whether default-coloured text dissolves into the background under the dark theme.
  if (rendered && rendered.darkMode !== c.dark) {
    fail.push(
      `${c.name}: requested dark=${c.dark} but the viewer reports darkMode=${rendered.darkMode}` +
        ` (passing a boolean to "dark-mode" silently falls back to the light rendering)`,
    );
  }

  if (SHOT) {
    const shot = await b.call("Page.captureScreenshot", {});
    const file = SHOT.replace(/\.png$/, "") + `-${c.name}.png`;
    fs.writeFileSync(file, Buffer.from(shot.data, "base64"));
    console.log(`  screenshot: ${file}`);
  }
  console.log(`${fail.length ? "…" : "ok"} ${c.name}: ${JSON.stringify(rendered || null)}`);
}

// The picture with the stencil applied against the one that fell back. Same diagram, so if they
// do not differ the XML we handed in is not being used (watching only for the request misses it).
if (stencilShots["stencil-aws4"] && stencilShots["stencil-offline"]) {
  if (stencilShots["stencil-aws4"] === stencilShots["stencil-offline"]) {
    fail.push("stencil-aws4 and stencil-offline render identically - the stencils handed in have no effect on the drawing");
  } else {
    console.log("ok stencil-pixels: the picture differs with and without the stencil");
  }
}

b.close();
srv.close();
fs.rmSync(work, { recursive: true, force: true });

if (fail.length) {
  console.error("\nFAIL:\n - " + fail.join("\n - "));
  process.exit(1);
}
console.log("\nall good: rendered with the bundled viewer alone, zero external requests");
