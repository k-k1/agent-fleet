// Does an outstanding handoff proposal still let you SEE the conversation?
//
// The regression this guards (2026-08-04): the proposal card rendered after every group,
// as the transcript scroller's last child. A card that never goes away then owns the
// mirror's landing position forever — auto-follow scrolls to the bottom, lands on the
// card, and the message you just sent plus the reply streaming in are pushed off-screen.
// It reads as "I send something and nothing appears" even though the send, the transcript and /messages are
// all fine. The fix places the card at the moment it was proposed, so it is last only
// until the next turn arrives.
//
// Drives the real Console bundle (served by ../mirror-scroll/stub.mjs) with headless
// Chromium over raw CDP — no Playwright, no CP, no agent. jsdom cannot answer this: the
// question is what is inside the viewport after the mirror lands.
//
//   npm --prefix console run build      # console/dist must exist (the real bundle)
//   node console/scripts/mirror-handoff/check.mjs
//
// Exit status is the check: 0 = the card sits at its own moment and the newest turn is
// on screen.
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const STUB = path.join(HERE, "../mirror-scroll/stub.mjs");
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8793));
const CDP_PORT = Number(arg("cdp-port", 9253));
const TURNS = Number(arg("turns", 24));
const BASE = `http://127.0.0.1:${PORT}/`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      const p = this.pending.get(msg.id);
      if (!p) return;
      this.pending.delete(msg.id);
      msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
    });
  }
  static async connect(url) {
    const ws = new WebSocket(url);
    await new Promise((res, rej) => {
      ws.addEventListener("open", res, { once: true });
      ws.addEventListener("error", rej, { once: true });
    });
    return new CDP(ws);
  }
  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }
  async ev(expression) {
    const r = await this.send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || "eval failed");
    return r.result.value;
  }
}

const fetchJSON = async (url, init) => {
  for (let i = 0; i < 60; i++) {
    try {
      const r = await fetch(url, init);
      if (r.ok) return await r.json();
    } catch {
      /* not up yet */
    }
    await sleep(250);
  }
  throw new Error(`unreachable: ${url}`);
};

const OPEN_SESSION = `(() => {
  const b = [...document.querySelectorAll(".sess-row .sess-btn")]
    .find((e) => (e.textContent || "").includes("チェックアウトの入力検証"));
  if (!b) return "no-row";
  b.click();
  return "ok";
})()`;

// The same card on a shared session (the receiving side). The shared view could not render a
// proposal that lives in a different store from the owner's transcript, so all that remained in
// the conversation were tool rows and the boilerplate completion text - the reader could only tell
// that a handoff had apparently happened. Here we check that the body is rendered, and that no
// controls the reader cannot use are shown.
const OPEN_SHARED = `(() => {
  const b = document.querySelector(".shared-rail-row");
  if (!b) return "no-row";
  b.click();
  return "ok";
})()`;

// What the reader actually gets: is the card present, is anything rendered BELOW it, and
// is the newest turn inside the viewport once the view has landed? `scroller` is a JS
// expression that finds the surface's scroll container (the mirror's and the shared
// view's are different elements).
const probeIn = (scroller) => `(() => {
  const body = ${scroller};
  if (!body) return { err: "no scroller" };
  const card = body.querySelector(".mirror-handoff");
  const turns = [...body.querySelectorAll(".mirror-turn")];
  const last = turns[turns.length - 1];
  const gap = Math.round(body.scrollHeight - body.clientHeight - body.scrollTop);
  if (!card || !last) return { err: !card ? "no handoff card" : "no turns", turns: turns.length, gap };
  // Document order decides "below" — compared node-to-node, since turns and the card are
  // not necessarily siblings of the scroller itself.
  const follows = (a, b) => !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
  const turnsAfterCard = turns.filter((t) => follows(card, t)).length;
  const view = body.getBoundingClientRect();
  const lastBox = last.getBoundingClientRect();
  return {
    turns: turns.length,
    turnsAfterCard,
    cardIsLast: turnsAfterCard === 0,
    turnsBelowCard: follows(card, last),
    // Visible = the newest turn overlaps the scroller's visible rect at all.
    newestVisible: lastBox.bottom > view.top + 4 && lastBox.top < view.bottom - 4,
    launchedBadge: !!card.querySelector(".mirror-handoff-done"),
    // Is the body actually readable (do not pass a card that shows only a title and no content).
    prompt: (card.querySelector(".mirror-handoff-prompt")?.textContent || "").trim().slice(0, 12),
    // No controls on a surface that lacks the capability (the shared side cannot edit, discard or launch).
    controls: card.querySelectorAll("button, textarea, input").length,
    gap,
  };
})()`;

const PROBE = probeIn(`document.querySelector(".mirror-body") || document.querySelector(".mirror-scroll")`);
const PROBE_SHARED = probeIn(`document.querySelector(".shared-view-body")`);

const pane = { id: "p0", session: null, content: { kind: "terminal", chat: true }, wrap: null };
const layout = { cols: [{ id: "c0", rowRatio: 0.5, panes: [pane] }], colRatios: [1], activeId: "p0" };

async function run(mode, { shared = false } = {}) {
  const stub = spawn(
    process.execPath,
    [STUB, "--port", String(PORT), "--turns", String(TURNS), "--images", "0", "--mermaid", "0", "--handoff", mode,
     "--shared", shared ? "1" : "0"],
    { stdio: ["ignore", "ignore", "inherit"] },
  );
  try {
    await fetchJSON(`${BASE}api/whoami`);
    const target = await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" });
    const cdp = await CDP.connect(target.webSocketDebuggerUrl);
    await cdp.send("Page.enable");
    // A phone-sized viewport is where this bug bites hardest: the card alone is taller
    // than the screen, so anything below it is invisible.
    await cdp.send("Emulation.setDeviceMetricsOverride", { width: 420, height: 780, deviceScaleFactor: 1, mobile: false });
    await cdp.send("Page.addScriptToEvaluateOnNewDocument", {
      source: `try {
        localStorage.setItem("af-display-settings", '{"locale":"ja","theme":"dark"}');
        localStorage.setItem("af-tenant", "demo");
        localStorage.setItem("af.layout2.demo@example.com.demo", ${JSON.stringify(JSON.stringify(layout))});
      } catch (e) {}`,
    });
    await cdp.send("Page.navigate", { url: BASE });
    await sleep(5000);
    const opened = await cdp.ev(shared ? OPEN_SHARED : OPEN_SESSION);
    if (opened !== "ok") throw new Error("could not find the session row in the left pane");
    await sleep(6000); // transcript render + the card's own poll
    const p = await cdp.ev(shared ? PROBE_SHARED : PROBE);
    cdp.ws.close();
    await fetch(`http://127.0.0.1:${CDP_PORT}/json/close/${target.id}`).catch(() => {});
    return p;
  } finally {
    stub.kill();
    await sleep(300);
  }
}

const chrome = spawn(
  "/usr/bin/chromium",
  ["--headless=new", "--no-sandbox", "--disable-gpu", "--hide-scrollbars",
   `--remote-debugging-port=${CDP_PORT}`, "--remote-allow-origins=*", "--lang=ja-JP", "about:blank"],
  { stdio: ["ignore", "ignore", "ignore"] },
);
const cleanup = () => {
  try {
    chrome.kill();
  } catch {
    /* already gone */
  }
};
process.on("exit", cleanup);
process.on("SIGINT", () => {
  cleanup();
  process.exit(1);
});

let failed = 0;
try {
  await fetchJSON(`http://127.0.0.1:${CDP_PORT}/json/version`);

  // 1) Proposed a few turns back: turns must render BELOW the card, and the newest one
  //    must be what you land on. This is the exact shape of the reported failure.
  const mid = await run("mid");
  const midOK = !mid.err && mid.turnsBelowCard && !mid.cardIsLast && mid.newestVisible;
  console.log(`[mirror-handoff] mid      ${midOK ? "OK " : "NG "} ${JSON.stringify(mid)}`);
  if (!midOK) failed++;

  // 2) Just proposed (nothing newer): the card SHOULD be last — placement is driven by
  //    time, not hard-coded to either end.
  const fresh = await run("new");
  const freshOK = !fresh.err && fresh.cardIsLast && !fresh.turnsBelowCard;
  console.log(`[mirror-handoff] new      ${freshOK ? "OK " : "NG "} ${JSON.stringify(fresh)}`);
  if (!freshOK) failed++;

  // 3) Already launched from: the proposal is KEPT and badged (discarding is the user's call).
  const done = await run("launched");
  const doneOK = !done.err && done.launchedBadge;
  console.log(`[mirror-handoff] launched ${doneOK ? "OK " : "NG "} ${JSON.stringify(done)}`);
  if (!doneOK) failed++;

  // 4) The receiving side of a share reads the same content. The transcript alone only says that
  //    a handoff happened, so the card must appear with a readable body - and with no controls
  //    the reader cannot use.
  const shared = await run("mid", { shared: true });
  const sharedOK = !shared.err && shared.prompt.length > 0 && shared.controls === 0 && shared.turnsBelowCard;
  console.log(`[mirror-handoff] shared   ${sharedOK ? "OK " : "NG "} ${JSON.stringify(shared)}`);
  if (!sharedOK) failed++;
} finally {
  cleanup();
}
console.log(failed ? `\n[mirror-handoff] FAILED: ${failed} check(s)` : "\n[mirror-handoff] OK");
process.exit(failed ? 1 : 0);
