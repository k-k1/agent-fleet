#!/usr/bin/env node

// Reproducible P0 probe for docs/log/53 Chromium Attach View. This is deliberately
// outside the normal E2E suite: it exercises an externally owned Chromium and
// two independent CDP clients, not a running Agent Fleet deployment.
import { chromium } from "@playwright/test";
import http from "node:http";
import net from "node:net";

const chromiumPath = process.env.AF_CHROMIUM_BIN || "/usr/bin/chromium";
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function freePort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return server.address().port;
}

async function waitFor(fn, label, timeoutMs = 5000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    try {
      const value = await fn();
      if (value) return value;
    } catch (error) {
      last = error;
    }
    await pause(50);
  }
  throw new Error(`${label} timed out${last ? `: ${last.message}` : ""}`);
}

function fixture(pathname) {
  const pageName = pathname === "/next" ? "next" : "start";
  return `<!doctype html>
<meta charset="utf-8">
<title>attach-p0-${pageName}</title>
<style>body{margin:0;font:16px sans-serif}main{padding:24px;height:2200px}input{width:360px;height:32px}button{height:36px}</style>
<main>
  <input id="field" aria-label="field"><button id="button">click</button>
  <output id="clicks">0</output><div id="page">${pageName}</div>
</main>
<script>
  button.onclick = () => clicks.textContent = String(Number(clicks.textContent) + 1);
</script>`;
}

function beginCast(session) {
  const state = { frames: 0, lastSize: 0, ackErrors: [] };
  const onFrame = ({ data, sessionId }) => {
    state.frames += 1;
    state.lastSize = Buffer.byteLength(data, "base64");
    void session.send("Page.screencastFrameAck", { sessionId }).catch((error) => {
      state.ackErrors.push(error.message);
    });
  };
  session.on("Page.screencastFrame", onFrame);
  return { state, onFrame };
}

async function main() {
  const fixtureServer = http.createServer((req, res) => {
    res.writeHead(200, { "content-type": "text/html; charset=utf-8", "cache-control": "no-store" });
    res.end(fixture(new URL(req.url, "http://fixture").pathname));
  });
  const fixturePort = await listen(fixtureServer);
  const cdpPort = await freePort();
  const endpoint = `http://127.0.0.1:${cdpPort}`;
  let owner;
  let clientA;
  let clientB;

  try {
    owner = await chromium.launch({
      executablePath: chromiumPath,
      headless: true,
      args: [
        "--remote-debugging-address=127.0.0.1",
        `--remote-debugging-port=${cdpPort}`,
      ],
    });
    const context = await owner.newContext({ viewport: { width: 800, height: 600 } });
    const page = await context.newPage();
    await page.goto(`http://127.0.0.1:${fixturePort}/start`);

    const version = await waitFor(async () => {
      const response = await fetch(`${endpoint}/json/version`, { redirect: "manual" });
      return response.ok ? response.json() : null;
    }, "CDP discovery");

    // Playwright is the owner. These are separate browser WebSocket clients,
    // matching two independent AF-like observers of the same endpoint.
    [clientA, clientB] = await Promise.all([
      chromium.connectOverCDP(endpoint),
      chromium.connectOverCDP(endpoint),
    ]);
    const pageA = clientA.contexts()[0].pages().find((candidate) => candidate.url() === page.url());
    const pageB = clientB.contexts()[0].pages().find((candidate) => candidate.url() === page.url());
    if (!pageA || !pageB) throw new Error("both CDP clients did not discover the owner page");
    const [sessionA, sessionB] = await Promise.all([
      pageA.context().newCDPSession(pageA),
      pageB.context().newCDPSession(pageB),
    ]);
    const targetBefore = await sessionA.send("Target.getTargetInfo");

    await Promise.all([sessionA.send("Page.enable"), sessionB.send("Page.enable")]);
    const castA = beginCast(sessionA);
    const castB = beginCast(sessionB);
    await Promise.all([
      sessionA.send("Page.startScreencast", { format: "jpeg", quality: 70, maxWidth: 800, maxHeight: 600 }),
      sessionB.send("Page.startScreencast", { format: "jpeg", quality: 70, maxWidth: 800, maxHeight: 600 }),
    ]);
    await page.evaluate(() => { document.querySelector("#page").textContent = "cast-both"; });
    await waitFor(() => castA.state.frames > 0 && castB.state.frames > 0, "both screencasts");
    const simultaneousFrames = { a: castA.state.frames, b: castB.state.frames };

    await sessionA.send("Page.stopScreencast");
    const framesAfterAStop = castB.state.frames;
    await page.evaluate(() => { document.querySelector("#page").textContent = "cast-b-only"; });
    await waitFor(() => castB.state.frames > framesAfterAStop, "client B after client A stopped screencast");

    const box = await page.locator("#button").boundingBox();
    if (!box) throw new Error("button has no bounding box");
    const x = box.x + box.width / 2;
    const y = box.y + box.height / 2;
    await sessionB.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", buttons: 1, clickCount: 1 });
    await sessionB.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", buttons: 0, clickCount: 1 });
    await waitFor(async () => await page.locator("#clicks").textContent() === "1", "mouse click");
    const clickCount = Number(await page.locator("#clicks").textContent());

    await page.locator("#field").focus();
    await sessionB.send("Input.dispatchKeyEvent", { type: "keyDown", key: "A", code: "KeyA", text: "A", unmodifiedText: "A" });
    await sessionB.send("Input.dispatchKeyEvent", { type: "keyUp", key: "A", code: "KeyA" });
    await sessionB.send("Input.insertText", { text: "日本語" });
    const textAfterInput = await page.locator("#field").inputValue();

    const scrollBefore = await page.evaluate(() => scrollY);
    await sessionB.send("Input.dispatchMouseEvent", { type: "mouseWheel", x: 400, y: 300, deltaX: 0, deltaY: 500 });
    const scrollAfter = await waitFor(async () => {
      const value = await page.evaluate(() => scrollY);
      return value > scrollBefore ? value : 0;
    }, "mouse wheel");

    await page.goto(`http://127.0.0.1:${fixturePort}/next`);
    await waitFor(() => pageB.url().endsWith("/next"), "navigation visible to second client");
    const targetAfter = await sessionB.send("Target.getTargetInfo");

    // There is no transaction or ownership arbitration between CDP clients.
    // Race owner fill against AF-like insertText repeatedly and retain the
    // observed outcomes rather than asserting one scheduling order.
    const raceOutcomes = {};
    for (let i = 0; i < 20; i += 1) {
      await page.locator("#field").fill("");
      await page.locator("#field").focus();
      await Promise.all([
        page.locator("#field").fill("OWNER"),
        sessionB.send("Input.insertText", { text: "USER" }),
      ]);
      const value = await page.locator("#field").inputValue();
      raceOutcomes[value] = (raceOutcomes[value] || 0) + 1;
    }
    await page.locator("#field").fill("");
    await page.locator("#field").focus();
    await sessionB.send("Input.insertText", { text: "USER-PAUSED" });
    await pause(100);
    const pausedOwnerValue = await page.locator("#field").inputValue();

    // Detaching one observer must neither close the target nor stop the owner.
    const framesBeforeDetach = castB.state.frames;
    await sessionA.detach();
    await page.evaluate(() => { document.querySelector("#page").textContent = "detached-a"; });
    await waitFor(() => castB.state.frames > framesBeforeDetach, "client B after client A detached");
    const ownerAliveAfterDetach = owner.isConnected() && !page.isClosed();

    let closedSessionError = "";
    await page.close();
    await waitFor(() => pageB.isClosed(), "target close propagation");
    try {
      await sessionB.send("Runtime.evaluate", { expression: "1" });
    } catch (error) {
      closedSessionError = error.message;
    }

    const result = {
      probe: "chromium-attach-p0",
      chromium: version.Browser,
      playwright: (await import("@playwright/test/package.json", { with: { type: "json" } })).default.version,
      clients: { owner: "playwright-pipe", attach: 2, endpointHost: "127.0.0.1" },
      target: {
        sameAcrossNavigation: targetBefore.targetInfo.targetId === targetAfter.targetInfo.targetId,
        closePropagated: pageB.isClosed(),
        commandAfterCloseFailed: closedSessionError.length > 0,
      },
      screencast: {
        simultaneousFrames,
        secondContinuedAfterFirstStop: castB.state.frames > framesAfterAStop,
        secondContinuedAfterFirstDetach: castB.state.frames > framesBeforeDetach,
        lastJPEGBytes: { a: castA.state.lastSize, b: castB.state.lastSize },
        ackErrors: { a: castA.state.ackErrors, b: castB.state.ackErrors },
      },
      input: {
        clickCount,
        textAfterInput,
        scrollBefore,
        scrollAfter,
      },
      concurrency: { activeOwnerRaceOutcomes: raceOutcomes, pausedOwnerValue },
      lifecycle: { ownerAliveAfterObserverDetach: ownerAliveAfterDetach },
    };
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);

    const failures = [
      !result.target.sameAcrossNavigation && "target id changed across navigation",
      !result.target.closePropagated && "target close did not propagate",
      !result.target.commandAfterCloseFailed && "closed target still accepted commands",
      !result.screencast.secondContinuedAfterFirstStop && "one client's stop stopped the other client",
      !result.screencast.secondContinuedAfterFirstDetach && "one client's detach stopped the other client",
      result.input.textAfterInput !== "A日本語" && "keyboard/IME input mismatch",
      result.input.scrollAfter <= result.input.scrollBefore && "wheel input did not scroll",
      result.concurrency.pausedOwnerValue !== "USER-PAUSED" && "paused-owner input did not persist",
      !result.lifecycle.ownerAliveAfterObserverDetach && "observer detach closed owner target",
    ].filter(Boolean);
    if (failures.length) throw new Error(failures.join("; "));
  } finally {
    await clientA?.close().catch(() => {});
    await clientB?.close().catch(() => {});
    await owner?.close().catch(() => {});
    await new Promise((resolve) => fixtureServer.close(resolve));
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
