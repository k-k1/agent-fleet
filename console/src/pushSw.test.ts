// The service worker's Web Push handlers (public/sw.js, #4750): what a push shows, where a tap
// goes, and that adding them did not make the worker intercept ordinary requests.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";

type Fn = (e: any) => void;

function loadSw(lang = "en", wins: any[] = [], scope = "https://console.test/") {
  const src = readFileSync(resolve(__dirname, "../public/sw.js"), "utf8");
  const listeners: Record<string, Fn> = {};
  const shown: { title: string; opts: any }[] = [];
  const opened: string[] = [];
  const self = {
    location: { origin: "https://console.test" },
    navigator: { language: lang },
    registration: { scope, showNotification: async (title: string, opts: any) => void shown.push({ title, opts }) },
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
  it("words a failed auto-run turn in both languages, not as the generic fallback", async () => {
    const ja = loadSw("ja");
    await push(ja, { v: 1, kind: "turn-failed", session: "my-sess", name: "My session" });
    expect(ja.shown[0].opts.body).toBe("自動実行のターンが失敗しました");
    const en = loadSw("en");
    await push(en, { v: 1, kind: "turn-failed", session: "my-sess", name: "My session" });
    expect(en.shown[0].opts.body).toBe("An auto-run turn failed");
  });
  it("prefixes the title with a localized origin label, none for own", async () => {
    const want: Record<string, [string, string]> = {
      own: ["S", "S"],
      "own-child": ["Child · S", "子 · S"],
      autorun: ["Auto-run · S", "自動実行 · S"],
      "autorun-child": ["Auto-run · Child · S", "自動実行 · 子 · S"],
      schedule: ["Scheduled · S", "スケジュール · S"],
      "schedule-child": ["Scheduled · Child · S", "スケジュール · 子 · S"],
    };
    for (const [origin, [en, ja]] of Object.entries(want)) {
      for (const [lang, title] of [["en", en], ["ja", ja]]) {
        const sw = loadSw(lang);
        await push(sw, { v: 1, kind: "question", session: "s1", name: "S", origin });
        expect(sw.shown[0].title).toBe(title);
        // The tag does not depend on the origin.
        expect(sw.shown[0].opts.tag).toBe("af-s1-question");
      }
    }
  });
  it("treats a missing or unknown origin as own", async () => {
    for (const origin of [undefined, "constructor", "bogus", 7]) {
      const sw = loadSw();
      await push(sw, { v: 1, kind: "question", session: "s1", name: "S", origin });
      expect(sw.shown[0].title).toBe("S");
    }
  });
  it("sets the brand icon and the monochrome badge under the scope", async () => {
    const sw = loadSw();
    await push(sw, { v: 1, kind: "question", session: "s1", name: "S" });
    expect(sw.shown[0].opts.icon).toBe("https://console.test/brand/icon-192.png");
    expect(sw.shown[0].opts.badge).toBe("https://console.test/brand/badge-96.png");
  });
  it("resolves icon and badge under a path-prefixed scope", async () => {
    const sw = loadSw("en", [], "https://console.test/prefix/");
    await push(sw, { v: 1, kind: "question", session: "s1", name: "S" });
    expect(sw.shown[0].opts.icon).toBe("https://console.test/prefix/brand/icon-192.png");
    expect(sw.shown[0].opts.badge).toBe("https://console.test/prefix/brand/badge-96.png");
  });
  it("ships the badge as a 96 px PNG with an alpha channel", () => {
    const png = readFileSync(resolve(__dirname, "../public/brand/badge-96.png"));
    expect(png.subarray(1, 4).toString()).toBe("PNG");
    expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([96, 96]);
    expect(png[25]).toBe(6); // colour type RGBA
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

describe("tenant in the payload", () => {
  const clickWith = async (sw: ReturnType<typeof loadSw>, data: unknown) => {
    let p: Promise<unknown> = Promise.resolve();
    sw.listeners.notificationclick({ notification: { close() {}, data }, waitUntil: (x: Promise<unknown>) => (p = x) });
    await p;
  };
  it("keeps the tenant on the notification and in its tag", async () => {
    const sw = loadSw();
    await push(sw, { v: 1, kind: "question", session: "s", name: "S", tenant: "acme" });
    expect(sw.shown[0].opts.data).toEqual({ session: "s", tenant: "acme" });
    expect(sw.shown[0].opts.tag).toBe("af-acme/s-question");
  });
  it("does not let the same session name in two tenants replace each other's notification", async () => {
    const sw = loadSw();
    await push(sw, { kind: "question", session: "s", tenant: "a" });
    await push(sw, { kind: "question", session: "s", tenant: "b" });
    expect(sw.shown[0].opts.tag).not.toBe(sw.shown[1].opts.tag);
  });
  it("drops a tenant that is not a slug, and one that arrives without a session", async () => {
    const sw = loadSw();
    await push(sw, { kind: "question", session: "s", tenant: "a&session=x" });
    await push(sw, { kind: "question", tenant: "acme" });
    expect(sw.shown[0].opts.data.tenant).toBe("");
    expect(sw.shown[1].opts.data).toEqual({ session: "", tenant: "" });
  });
  it("names the tenant when it opens a new window", async () => {
    const sw = loadSw();
    await clickWith(sw, { session: "my-sess", tenant: "acme" });
    expect(sw.opened).toEqual(["https://console.test/?session=my-sess&tenant=acme"]);
  });
  it("hands the tenant to an open Console", async () => {
    const msgs: unknown[] = [];
    const win = {
      url: "https://console.test/",
      postMessage: (m: unknown, ports: MessagePort[]) => {
        msgs.push(m);
        ports[0].postMessage("ok");
      },
      focus: async () => {},
    };
    const sw = loadSw("en", [win]);
    await clickWith(sw, { session: "my-sess", tenant: "acme" });
    expect(msgs).toEqual([{ type: "af-open-tenant-session", session: "my-sess", tenant: "acme" }]);
  });
});

describe("an open Console from before the tenant was in the push", () => {
  it("does not get the tenant hand-off, so the tap opens a window that names the tenant", async () => {
    // That Console answers only "af-open-session" and would open the name in its own tenant.
    const win = {
      url: "https://console.test/",
      postMessage: (m: any, ports: MessagePort[]) => {
        if (m.type === "af-open-session") ports[0].postMessage("ok");
      },
      focus: async () => {},
    };
    const sw = loadSw("en", [win]);
    let p: Promise<unknown> = Promise.resolve();
    sw.listeners.notificationclick({ notification: { close() {}, data: { session: "s", tenant: "acme" } }, waitUntil: (x: Promise<unknown>) => (p = x) });
    await p;
    expect(sw.opened).toEqual(["https://console.test/?session=s&tenant=acme"]);
  }, 5000);
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
    const win = {
      url: "https://console.test/",
      postMessage: (m: unknown, ports: MessagePort[]) => {
        msgs.push(m);
        ports[0].postMessage("ok");
      },
      focus: async () => void (focused = true),
    };
    const sw = loadSw("en", [win]);
    await click(sw, "my-sess");
    expect(msgs).toEqual([{ type: "af-open-session", session: "my-sess" }]);
    expect(focused).toBe(true);
    expect(sw.opened).toEqual([]);
  });
});

describe("notificationclick hand-off", () => {
  const click = async (sw: ReturnType<typeof loadSw>, session: string) => {
    let p: Promise<unknown> = Promise.resolve();
    sw.listeners.notificationclick({ notification: { close() {}, data: { session } }, waitUntil: (x: Promise<unknown>) => (p = x) });
    await p;
  };
  it("opens a new window when the only page under the scope never answers (a /login page)", async () => {
    let focused = false;
    const login = { url: "https://console.test/login", postMessage() {}, focus: async () => void (focused = true) };
    const sw = loadSw("en", [login]);
    await click(sw, "my-sess");
    expect(sw.opened).toEqual(["https://console.test/?session=my-sess"]);
    expect(focused).toBe(false);
  }, 5000);
  it("takes the page that answers when another page does not", async () => {
    const dead = { url: "https://console.test/login", postMessage() {}, focus: async () => {} };
    const got: unknown[] = [];
    const live = {
      url: "https://console.test/",
      postMessage: (m: unknown, ports: MessagePort[]) => {
        got.push(m);
        ports[0].postMessage("ok");
      },
      focus: async () => {},
    };
    const sw = loadSw("en", [dead, live]);
    await click(sw, "A_1");
    expect(got).toEqual([{ type: "af-open-session", session: "A_1" }]);
    expect(sw.opened).toEqual([]);
  }, 5000);
});

describe("candidate order", () => {
  it("asks the app's own page before crowding previews under the same scope", async () => {
    const mk = (url: string, answers: boolean, log: string[]) => ({
      url,
      postMessage: (_m: unknown, ports: MessagePort[]) => {
        log.push(url);
        if (answers) ports[0].postMessage("ok");
      },
      focus: async () => {},
    });
    const log: string[] = [];
    const previews = [1, 2, 3, 4].map((n) => mk(`https://console.test/preview/${n}`, false, log));
    const app = mk("https://console.test/", true, log);
    const sw = loadSw("en", [...previews, app]);
    let p: Promise<unknown> = Promise.resolve();
    sw.listeners.notificationclick({ notification: { close() {}, data: { session: "abc" } }, waitUntil: (x: Promise<unknown>) => (p = x) });
    await p;
    expect(log[0]).toBe("https://console.test/");
    expect(sw.opened).toEqual([]);
  }, 8000);
});

describe("hung focus", () => {
  it("falls back to opening the session when an acknowledging page never settles focus()", async () => {
    vi.useFakeTimers();
    try {
      const win = {
        url: "https://console.test/",
        postMessage: (_m: unknown, ports: MessagePort[]) => ports[0].postMessage("ok"),
        focus: () => new Promise(() => {}),
      };
      const sw = loadSw("en", [win]);
      let p: Promise<unknown> = Promise.resolve();
      sw.listeners.notificationclick({ notification: { close() {}, data: { session: "abc" } }, waitUntil: (x: Promise<unknown>) => (p = x) });
      await vi.advanceTimersByTimeAsync(3100);
      await p;
      expect(sw.opened).toEqual(["https://console.test/?session=abc"]);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("search after the deadline", () => {
  it("does not hand the session to a second Console once the fallback window is opening", async () => {
    vi.useFakeTimers();
    try {
      let failFirst!: () => void;
      const second: unknown[] = [];
      const first = {
        url: "https://console.test/",
        postMessage: (_m: unknown, ports: MessagePort[]) => ports[0].postMessage("ok"),
        focus: () => new Promise((_, rej) => (failFirst = () => rej(new Error("gone")))),
      };
      const other = {
        url: "https://console.test/index.html",
        postMessage: (m: unknown, ports: MessagePort[]) => {
          second.push(m);
          ports[0].postMessage("ok");
        },
        focus: async () => {},
      };
      const sw = loadSw("en", [first, other]);
      let p: Promise<unknown> = Promise.resolve();
      sw.listeners.notificationclick({ notification: { close() {}, data: { session: "abc" } }, waitUntil: (x: Promise<unknown>) => (p = x) });
      await vi.advanceTimersByTimeAsync(3000);
      await p;
      expect(sw.opened).toEqual(["https://console.test/?session=abc"]);
      failFirst();
      await vi.advanceTimersByTimeAsync(200);
      expect(second).toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("focus failure", () => {
  it("falls back to opening the session when the answering page cannot be focused", async () => {
    const win = {
      url: "https://console.test/",
      postMessage: (_m: unknown, ports: MessagePort[]) => ports[0].postMessage("ok"),
      focus: async () => {
        throw new Error("gone");
      },
    };
    const sw = loadSw("en", [win]);
    let p: Promise<unknown> = Promise.resolve();
    sw.listeners.notificationclick({ notification: { close() {}, data: { session: "abc" } }, waitUntil: (x: Promise<unknown>) => (p = x) });
    await p;
    expect(sw.opened).toEqual(["https://console.test/?session=abc"]);
  });
});

describe("payload shapes", () => {
  it("keeps a capitalised / underscored session name the Agent allows", async () => {
    const sw = loadSw();
    await push(sw, { kind: "question", session: "A_1" });
    expect(sw.shown[0].opts.data.session).toBe("A_1");
  });
  it("shows a generic text for an inherited property name used as kind", async () => {
    const sw = loadSw();
    await push(sw, { kind: "constructor", session: "abc" });
    expect(sw.shown[0].opts.body).toBe("A session needs you");
  });
  it("survives a kind that is not a string", async () => {
    const sw = loadSw();
    await push(sw, { kind: { toString: null }, session: "abc" });
    expect(sw.shown).toHaveLength(1);
    expect(sw.shown[0].opts.tag).toBe("af-abc-other");
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
