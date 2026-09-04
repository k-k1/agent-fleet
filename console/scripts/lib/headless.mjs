// Foundation for real-browser checks (used by scripts/pdf and scripts/doc).
//
// For checks that need neither CP nor Agent, it holds only three things: serve a temp directory
// over plain http, start headless Chromium, and drive CDP over a raw WebSocket. No puppeteer or
// playwright - the chromium baked into the image is used as it comes.
//
// Always take port 0: sessions share one container, and a fixed port collides with another
// session's server. On collision chromium silently falls back to the IPv6 side, so the failure
// shows up as "we were attached to a different browser" and is never noticed.
import { spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const TYPES = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".css": "text/css",
  ".pdf": "application/pdf",
  ".wasm": "application/wasm", // drop this and wasm falls out of instantiateStreaming
  ".json": "application/json",
};

/** Serve a directory on a free port of 127.0.0.1.
 *
 *  The returned `requests` is the record of what was served (an array of `{ path, status }`).
 *  It is needed because what the page actually fetched never shows up in the DOM: measured, with
 *  the copy of the bundled assets (pdf.js cMaps / the standard 14 fonts) removed entirely,
 *  pdf:check still passed with all 11 checks OK. The only way to see it is the URLs requested and
 *  whether they could be served. */
export function serveDir(dir) {
  const requests = [];
  const server = http.createServer((req, res) => {
    const urlPath = req.url.split("?")[0];
    const file = path.join(dir, decodeURIComponent(urlPath));
    if (!file.startsWith(dir) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      requests.push({ path: urlPath, status: 404 });
      res.writeHead(404).end("not found");
      return;
    }
    requests.push({ path: urlPath, status: 200 });
    res.writeHead(200, { "Content-Type": TYPES[path.extname(file)] || "application/octet-stream" });
    fs.createReadStream(file).pipe(res);
  });
  return new Promise((r) =>
    server.listen(0, "127.0.0.1", () => r({ server, requests, port: server.address().port })),
  );
}

// Where the real browser lives. It differs per environment and any of them may be absent:
//   - this Workspace container - /usr/bin/chromium (baked into the image)
//   - GitHub ubuntu-latest     - both /usr/bin/chromium and /usr/bin/google-chrome exist
//     (measured with a throwaway probe in ci.yml: Chromium 151.0.7922.0 /
//      Google Chrome 151.0.7922.173, CHROME_BIN=/usr/bin/google-chrome)
// Holding the default as a single path string is correct while that path exists and, on the day it
// stops existing, fails as "the debugging port never opens" without naming the cause (spawn's
// ENOENT goes nowhere because stdio is discarded). List the candidates and name the one chosen.
//
// The runner has no Japanese font at all (measured: `fc-list :lang=ja` returns 0; this container
// has Noto CJK baked in). Today's pdf:check / doc:check only look at whether something was drawn
// and whether the assets could be served, so the verdicts still hold - the Japanese sample stays
// green while it is drawn as tofu. But adding glyph, advance-width or screenshot comparison on top
// of this foundation would break in CI only, and that would need a font-install step.
const CHROMIUM_CANDIDATES = ["/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable"];

const usable = (p) => {
  try {
    fs.accessSync(p, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
};

/** Decide which browser binary to use. An explicitly named one (argument / CHROMIUM / CHROME_BIN)
 *  is never silently swapped for another - measuring with something other than what was asked for
 *  is the hardest failure to see. */
export function resolveChromium(explicit = "") {
  const asked = explicit || process.env.CHROMIUM || "";
  if (asked) {
    if (usable(asked)) return asked;
    throw new Error(`the specified browser is not executable: ${asked} (given via CHROMIUM or an argument)`);
  }
  const tried = [...CHROMIUM_CANDIDATES, process.env.CHROME_BIN || ""].filter(Boolean);
  const found = tried.find(usable);
  if (found) return found;
  throw new Error(
    "no real browser found. Point CHROMIUM=<path> at one, or install chromium. Looked in: " + tried.join(" "),
  );
}

/** Start headless Chromium and return a handle with CDP connected. */
export async function startBrowser({ chromium = "", size = "1000,760" } = {}) {
  const bin = resolveChromium(chromium);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "af-headless-"));
  const proc = spawn(bin, [
    "--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
    "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
    `--user-data-dir=${dir}`, `--window-size=${size}`,
    // Headless answers "no hover, no pointer" by default. This is required when checking the
    // desktop appearance (workspace-notes, "Headless browser").
    "--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4",
    "about:blank",
  ], { stdio: ["ignore", "ignore", "ignore"] });

  // A spawn failure (ENOENT / EACCES) is invisible by default: stdio is discarded, so it turns
  // into "the debugging port never opens" after a 12 second wait. Fail carrying the real reason.
  let spawnErr = null;
  proc.on("error", (e) => (spawnErr = e));

  let port = 0;
  for (let i = 0; i < 120 && !port; i++) {
    await sleep(100);
    if (spawnErr) throw new Error(`cannot start the browser: ${bin}: ${spawnErr.message}`);
    try {
      port = Number(fs.readFileSync(path.join(dir, "DevToolsActivePort"), "utf8").split("\n")[0]) || 0;
    } catch {}
  }
  if (!port) throw new Error(`chromium did not open a debugging port (${bin})`);

  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const ws = new WebSocket(targets.find((t) => t.type === "page").webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0;
  const pending = new Map();
  const logs = [];
  ws.onmessage = (e) => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) {
      pending.get(m.id)(m);
      pending.delete(m.id);
    }
    if (m.method === "Runtime.exceptionThrown") {
      logs.push("exception: " + (m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text));
    }
  };
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const i = ++id;
      pending.set(i, (m) => (m.error ? rej(new Error(`${method}: ${m.error.message}`)) : res(m.result)));
      ws.send(JSON.stringify({ id: i, method, params }));
    });
  const evaluate = async (expression) => {
    const r = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || "evaluate failed");
    return r.result.value;
  };
  await send("Page.enable");
  await send("Runtime.enable");
  return {
    send,
    evaluate,
    logs,
    goto: (url) => send("Page.navigate", { url }),
    screenshot: async (out) => {
      const shot = await send("Page.captureScreenshot", { format: "png" });
      fs.writeFileSync(out, Buffer.from(shot.data, "base64"));
    },
    close: () => {
      ws.close();
      proc.kill();
      try {
        fs.rmSync(dir, { recursive: true, force: true });
      } catch {}
    },
  };
}

/** Poll until the condition holds and return the last value read (returned even if it never holds). */
export async function until(evaluate, expression, want, tries = 100, waitMs = 100) {
  let last;
  for (let i = 0; i < tries; i++) {
    last = await evaluate(expression);
    if (want(last)) return last;
    await sleep(waitMs);
  }
  return last;
}

/** Result tally. Add with check(condition, label, detail); report() prints and decides the exit code. */
export function checker() {
  const ok = [];
  const bad = [];
  return {
    check: (cond, label, detail = "") => (cond ? ok : bad).push(label + (detail ? ` — ${detail}` : "")),
    report: () => {
      for (const line of ok) console.log("  OK   " + line);
      for (const line of bad) console.log("  NG   " + line);
      console.log(bad.length ? `\n${bad.length} NG` : `\nall ${ok.length} OK`);
      return bad.length ? 1 : 0;
    },
  };
}
