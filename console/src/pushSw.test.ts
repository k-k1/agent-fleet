// The service worker's Web Push handlers (public/sw.js, #4750): what a push shows, where a tap
// goes, and that adding them did not make the worker intercept ordinary requests.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

type Fn = (e: any) => void;

function loadSw(lang = "en", wins: any[] = []) {
  const src = readFileSync(resolve(__dirname, "../public/sw.js"), "utf8");
  const listeners: Record<string, Fn> = {};
  const shown: { title: string; opts: any }[] = [];
  const opened: string[] = [];
  const self = {
    location: { origin: "https://console.test" },
    navigator: { language: lang },
    registration: { scope: "https://console.test/", showNotification: async (title: string, opts: any) => void shown.push({ title, opts }) },
    skipWaiting() {},
    clients: { claim() {}, matchAll: async () => wins, openWindow: async (u: string) => void opened.push(u) },
    addEventListener: (n: string, f: Fn) => (listeners[n] = f),
  };
  new Function("self", "Response", "caches", "URL", src)(self, class {}, {}, URL);
  return { listeners, shown, opened };
}

const push = async (sw: ReturnType<typeof loadSw>, json: unknown) => {
  let p: Promise<unknown> = Promise.resolve();
  sw.listeners.push({ data: { json: () => json }, waitUntil: (x: Promise<unknown>) => (p = x) });
  await p;
};

describe("push handler", () => {
  it("shows the session name and a kind text rendered locally", async () => {
    const sw = loadSw("ja");
    await push(sw, { v: 1, kind: "question", session: "my-sess", name: "My session" });
    expect(sw.shown[0].title).toBe("My session");
    expect(sw.shown[0].opts.body).toBe("質問があります");
    expect(sw.shown[0].opts.data.session).toBe("my-sess");
  });
  it("uses the payload text when the tenant allows it", async () => {
    const sw = loadSw();
    await push(sw, { v: 1, kind: "question", session: "my-sess", name: "S", text: "Deploy now?" });
    expect(sw.shown[0].opts.body).toBe("Deploy now?");
  });
  it("still shows a notification for an unreadable payload", async () => {
    const sw = loadSw();
    let p: Promise<unknown> = Promise.resolve();
    sw.listeners.push({
      data: {
        json: () => {
          throw new Error("bad");
        },
      },
      waitUntil: (x: Promise<unknown>) => (p = x),
    });
    await p;
    expect(sw.shown).toHaveLength(1);
    expect(sw.shown[0].opts.data.session).toBe("");
  });
  it("drops a session name that is not a valid one", async () => {
    const sw = loadSw();
    await push(sw, { kind: "question", session: "../evil?x=1" });
    expect(sw.shown[0].opts.data.session).toBe("");
  });
});

describe("notificationclick", () => {
  const click = async (sw: ReturnType<typeof loadSw>, session: string) => {
    let p: Promise<unknown> = Promise.resolve();
    sw.listeners.notificationclick({ notification: { close() {}, data: { session } }, waitUntil: (x: Promise<unknown>) => (p = x) });
    await p;
  };
  it("opens the session in a new window when no Console is open", async () => {
    const sw = loadSw();
    await click(sw, "my-sess");
    expect(sw.opened).toEqual(["https://console.test/?session=my-sess"]);
  });
  it("hands the session to an open Console and focuses it", async () => {
    const msgs: unknown[] = [];
    let focused = false;
    const win = { url: "https://console.test/", postMessage: (m: unknown) => msgs.push(m), focus: async () => void (focused = true) };
    const sw = loadSw("en", [win]);
    await click(sw, "my-sess");
    expect(msgs).toEqual([{ type: "af-open-session", session: "my-sess" }]);
    expect(focused).toBe(true);
    expect(sw.opened).toEqual([]);
  });
});

describe("fetch", () => {
  it("still ignores everything but the share POST", () => {
    const sw = loadSw();
    let answered = false;
    sw.listeners.fetch({ request: { method: "GET", url: "https://console.test/api/x" }, respondWith: () => (answered = true) });
    expect(answered).toBe(false);
  });
});
