// Equivalence proof for settings.ts (ADR 0124 Phase 2, #2867): a golden of what the module does
// per key and per sync scenario, so that moving it onto one descriptor per preference provably
// changes nothing. The ui-prefs sync has lost user data before; a refactor of it is only safe
// when every key's default, normalisation, scope and every PUT body are pinned byte for byte.
//
// Regenerate (only when a behaviour change is intended, and say so in the commit):
//   UPDATE_SETTINGS_GOLDEN=1 npx vitest run src/lib/settingsEquivalence.dom.test.tsx
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

const SETTINGS_KEY = "af-display-settings";
const OWNER_KEY = "af-display-settings-owner";
const GOLDEN = path.resolve(__dirname, "settings", "equivalence.golden.json");
const UPDATE = process.env.UPDATE_SETTINGS_GOLDEN === "1";

type Mod = typeof import("./settings.ts");
type Obj = Record<string, unknown>;

async function fresh(local: Obj | null, storedOwner = "", owner = ""): Promise<Mod> {
  // The previous instance's debounce and retry timers would otherwise fire inside this scenario
  // and land in its trace (the shared afterEach only runs between tests).
  ((globalThis as any)[Symbol.for("af.prefsTimerHooks")] as Set<() => unknown> | undefined)?.forEach((c) => c());
  apiMock.mockClear();
  apiJSONMock.mockClear();
  localStorage.clear();
  if (local) localStorage.setItem(SETTINGS_KEY, JSON.stringify(local));
  if (storedOwner) localStorage.setItem(OWNER_KEY, storedOwner);
  vi.resetModules();
  const s = await import("./settings.ts");
  s.setPrefsOwnerSource(() => owner);
  return s;
}

// A valid value other than the default, per key. Enumerations get a real other member; the rest
// are derived from the default's type.
const VALID_OVERRIDES: Obj = {
  theme: "light",
  paneLayout: "split",
  mirrorTheme: "dark",
  sharedTheme: "light",
  assistantTheme: "dark",
  topbarColor: "blue",
  leftpaneColor: "green",
  viewerColor: "slate",
  chatColor: "rose",
  sharedColor: "teal",
  assistantColor: "mono",
  mirrorSend: "enter",
  defaultModel: "opus",
  cjkFont: "Meiryo",
  readerFont: "ゴシック",
  opencodeCatalog: "go",
  externalMemory: "read",
  memoryReview: "propose",
  outputLanguage: "ja",
  ttsProvider: "polly",
  ttsLang: "en",
  ttsBackgroundPlayback: "mute",
  ttsWorkRead: "whisper",
  claudeCustomModels: ["claude-opus-4-9", "claude-sonnet-x"],
  imageProviderOrder: ["agy", "codex", "comfy", "openai-compat"],
  assistantAgentOrder: ["muse", "agy", "cursor", "opencode", "codex", "claude"],
  aiAssistOrder: ["codex", "claude"],
  hiddenModels: { claude: ["opus"], codex: ["gpt-x"] },
  agentLaunchDefaults: { claude: { model: "opus", effort: "high", startMode: "plan", skipPermissions: false } },
  notifyDevice: { "turn-child.os": false, "turn-child.voice": true },
  notifyUnread: { "turn-child": false },
  streamReplies: { claude: "lines", codex: false },
  expandThinking: { claude: true },
  ssmHostUsage: { h1: { count: 3, at: 5 } },
  quickReplies: { ok: { text: "OK", count: 2, at: 9 } },
  workingSets: [{ id: "w1", name: "one", repos: ["r"], chats: [], sessions: [] }],
  launchTemplates: { v: 1, items: [{ id: "t", text: "hi" }] },
  launchHistory: { v: 1, repos: { r: ["a"] } },
  ttsVoicePool: { ずんだもん: { use: true, speed: 1.5 } },
};

function validFor(k: string, d: unknown): unknown {
  if (k in VALID_OVERRIDES) return VALID_OVERRIDES[k];
  if (k === "locale") return d === "en" ? "ja" : "en";
  if (typeof d === "boolean") return !d;
  if (typeof d === "number") return d + 1;
  if (typeof d === "string") return d ? `${d}-x` : "x";
  if (Array.isArray(d)) return ["x"];
  if (d && typeof d === "object") return { x: "y" };
  return "x";
}

function wrongTypeFor(d: unknown): unknown {
  if (typeof d === "boolean") return "yes";
  if (typeof d === "number") return "7";
  if (typeof d === "string") return 7;
  if (Array.isArray(d)) return "x";
  return ["x"];
}

function emptyFor(d: unknown): unknown {
  if (typeof d === "string") return "";
  if (Array.isArray(d)) return [];
  if (d && typeof d === "object") return {};
  return d;
}

// A hydrate that throws is behaviour too, so a throw is recorded rather than failing the run.
async function hydrate(s: Mod): Promise<unknown> {
  try {
    return await s.hydrateUIPrefs();
  } catch (e) {
    return { threw: String(e) };
  }
}

const mapKeys = (keys: string[], f: (k: string) => unknown): Obj => Object.fromEntries(keys.map((k) => [k, f(k)]));

// A settings-shaped object as its difference from the defaults, losslessly: the key order (when
// it is not the defaults' own), every value that differs, and every default key it lacks. Keeps
// the golden readable; a whole object per scenario made it a megabyte.
function delta(o: unknown): unknown {
  if (!o || typeof o !== "object" || Array.isArray(o)) return o;
  const obj = o as Obj;
  const order = Object.keys(obj);
  const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
  return {
    order: same(order, KEYS) ? "defaults" : order,
    set: Object.fromEntries(order.filter((k) => !(k in DEFAULTS) || !same(obj[k], DEFAULTS[k])).map((k) => [k, obj[k]])),
    absent: KEYS.filter((k) => !(k in obj)),
  };
}

// What one scenario leaves behind: every call it made, the state, what localStorage holds.
function trace(s: Mod, extra: Obj = {}): Obj {
  const st = s.getSettings() as unknown as Obj;
  const raw = localStorage.getItem(SETTINGS_KEY);
  const stored = raw === null ? null : JSON.parse(raw);
  return {
    ...extra,
    gets: apiMock.mock.calls.map((c) => c[0]),
    puts: apiJSONMock.mock.calls.map((c) => ({ path: c[0], method: c[1], body: delta(c[2]) })),
    syncState: s.prefsSyncState(),
    loaded: s.uiPrefsLoaded(),
    state: delta(st),
    stored: JSON.stringify(stored) === JSON.stringify(st) ? "same as state" : delta(stored),
    storedOwner: localStorage.getItem(OWNER_KEY),
  };
}

const golden: Obj = UPDATE || !existsSync(GOLDEN) ? {} : JSON.parse(readFileSync(GOLDEN, "utf8"));
const produced: Obj = {};

function check(name: string, actual: unknown) {
  const v = JSON.parse(JSON.stringify(actual));
  produced[name] = v;
  if (UPDATE) return;
  expect(name in golden, `golden has no section ${name}; regenerate it on the old code`).toBe(true);
  expect(v).toEqual(golden[name]);
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-01T00:00:00Z"));
  apiMock.mockReset().mockResolvedValue({});
  apiJSONMock.mockReset().mockResolvedValue({});
});

afterEach(() => {
  vi.useRealTimers();
  if (UPDATE) writeFileSync(GOLDEN, JSON.stringify(produced, null, 1) + "\n");
});

let KEYS: string[] = [];
let DEFAULTS: Obj = {};
let VALID: Obj = {};

describe("settings.ts equivalence golden", () => {
  it("exports the same surface", async () => {
    const s = await fresh(null);
    const surface: Obj = {};
    for (const [name, v] of Object.entries(s).sort(([a], [b]) => a.localeCompare(b))) {
      surface[name] = typeof v === "function" ? `function/${(v as Function).length}` : v;
    }
    check("exports", surface);
  });

  it("has the same defaults, in the same key order", async () => {
    const s = await fresh(null);
    DEFAULTS = s.settingsDefaults() as unknown as Obj;
    KEYS = Object.keys(DEFAULTS);
    VALID = mapKeys(KEYS, (k) => validFor(k, DEFAULTS[k]));
    check("keyOrder", KEYS);
    check("defaults", DEFAULTS);
    check("defaultSetting", mapKeys(KEYS, (k) => s.defaultSetting(k as never)));
    check("loadedFromNothing", trace(s));
    check("ttsReset", s.TTS_RESET);
  });

  it("puts every key in the same scope", async () => {
    const s = await fresh(null);
    check("scope", mapKeys(KEYS, (k) => ({
      deviceLocal: s.isDeviceLocalSetting(k as never),
      accumulated: s.isAccumulatedSetting(k as never),
    })));
  });

  it("normalises a stored copy the same way on load", async () => {
    const inputs: Record<string, Obj> = {
      valid: VALID,
      wrongType: mapKeys(KEYS, (k) => wrongTypeFor(DEFAULTS[k])),
      nulls: mapKeys(KEYS, () => null),
      empty: mapKeys(KEYS, (k) => emptyFor(DEFAULTS[k])),
    };
    for (const [name, local] of Object.entries(inputs)) {
      const s = await fresh(local);
      check(`load.${name}`, delta(s.getSettings()));
    }
    // A stored copy that is not JSON at all.
    localStorage.clear();
    localStorage.setItem(SETTINGS_KEY, "{not json");
    vi.resetModules();
    const s = await import("./settings.ts");
    check("load.corrupt", delta(s.getSettings()));
  });

  it("merges a server copy the same way, per key", async () => {
    const servers: Record<string, Obj> = {
      valid: VALID,
      wrongType: mapKeys(KEYS, (k) => wrongTypeFor(DEFAULTS[k])),
      nulls: mapKeys(KEYS, () => null),
      empty: mapKeys(KEYS, (k) => emptyFor(DEFAULTS[k])),
    };
    for (const [name, srv] of Object.entries(servers)) {
      for (const owner of ["same", "unknown"]) {
        const s = owner === "same" ? await fresh({}, "t|u", "t|u") : await fresh({});
        apiMock.mockResolvedValueOnce(structuredClone(srv));
        const ok = await hydrate(s);
        await vi.advanceTimersByTimeAsync(1_000);
        check(`hydrate.${name}.${owner}`, trace(s, { ok }));
      }
    }
  });

  it("restores, keeps or drops this device's values the same way against an empty server", async () => {
    // owner: recorded and matching / recorded as someone else / no record / not resolved yet.
    const cases: [string, string, string][] = [
      ["sameOwner", "t|a", "t|a"],
      ["otherOwner", "t|a", "t|b"],
      ["noRecord", "", "t|a"],
      ["ownerUnknown", "t|a", ""],
    ];
    for (const [name, stored, owner] of cases) {
      for (const [srvName, srv] of [
        ["absent", {}],
        ["emptyValues", mapKeys(KEYS, (k) => emptyFor(DEFAULTS[k]))],
        ["defaults", DEFAULTS],
      ] as [string, Obj][]) {
        const s = await fresh(VALID, stored, owner);
        apiMock.mockResolvedValueOnce(structuredClone(srv));
        const ok = await hydrate(s);
        await vi.advanceTimersByTimeAsync(1_000);
        check(`localVsServer.${name}.${srvName}`, trace(s, { ok }));
      }
    }
  });

  it("sends the same body for a local change, with the same debounce", async () => {
    const s = await fresh({ zzLegacyUnknown: 1 }, "t|u", "t|u");
    expect(await s.hydrateUIPrefs()).toBe(true);
    s.setSettings(structuredClone(VALID) as never);
    await vi.advanceTimersByTimeAsync(599);
    const before = apiJSONMock.mock.calls.length;
    await vi.advanceTimersByTimeAsync(2);
    check("localChange.all", trace(s, { putsAt599: before }));

    // One key at a time: each lands in a PUT or never leaves the device.
    const perKey: Obj = {};
    for (const k of KEYS) {
      apiJSONMock.mockClear();
      s.setSetting(k as never, structuredClone(DEFAULTS[k]) as never);
      await vi.advanceTimersByTimeAsync(700);
      perKey[k] = apiJSONMock.mock.calls.length;
    }
    check("localChange.putsPerKey", perKey);

    // Coalescing: two changes inside the window are one PUT.
    apiJSONMock.mockClear();
    s.setSetting("iconSet", "seti");
    await vi.advanceTimersByTimeAsync(400);
    s.setSetting("tabSize", 2);
    await vi.advanceTimersByTimeAsync(400);
    const mid = apiJSONMock.mock.calls.length;
    await vi.advanceTimersByTimeAsync(400);
    check("localChange.coalesced", trace(s, { mid }));
  });

  it("keeps an unsaved change over the server, and retries a failed read on the same schedule", async () => {
    const s = await fresh({}, "t|u", "t|u");
    apiMock.mockResolvedValue({ error: { code: "http_502", message: "down" } });
    expect(await s.hydrateUIPrefs()).toBe(false);
    s.setSettings({ iconSet: "seti", theme: "light", quickReplies: { a: { text: "A", count: 1, at: 1 } } } as never);
    const t0 = Date.now();
    const at: number[] = [];
    apiMock.mockImplementation(() => {
      at.push(Date.now() - t0);
      return Promise.resolve({ error: { code: "http_502", message: "down" } });
    });
    await vi.advanceTimersByTimeAsync(200_000);
    check("retry.schedule", trace(s, { at }));

    apiMock.mockReset().mockResolvedValueOnce({ iconSet: "material", chatSize: 18, quickReplies: {} });
    s.retryPrefsSync();
    await vi.advanceTimersByTimeAsync(1_000);
    check("retry.recovered", trace(s));
  });

  it("reports a refused PUT and recovers the same way", async () => {
    const s = await fresh({}, "t|u", "t|u");
    expect(await s.hydrateUIPrefs()).toBe(true);
    apiJSONMock.mockResolvedValueOnce({ error: { code: "http_413", message: "too big" } });
    s.setSetting("iconSet", "seti");
    await vi.advanceTimersByTimeAsync(700);
    const failed = s.prefsSyncState();
    // refresh while nothing is in flight reads the server; the unsaved key stays and goes out.
    apiMock.mockResolvedValueOnce({ iconSet: "devicon", tabSize: 8 });
    await s.refreshUIPrefs();
    await vi.advanceTimersByTimeAsync(700);
    check("putRefused", trace(s, { failed }));

    // refresh while a save is pending does not read.
    apiMock.mockClear();
    s.setSetting("tabSize", 3);
    await s.refreshUIPrefs();
    check("refreshWhilePending", { gets: apiMock.mock.calls.length });
  });

  it("migrates a legacy server copy the same way", async () => {
    const legacy = {
      ttsQuietWhenHidden: false,
      assistantAgent: "codex",
      autoTitleSuggest: false,
      assistantUtilityModels: { claude: "haiku" },
      outputLanguage: "en",
      replySuggestEnabled: false,
      opencodeCatalog: "hide-zen",
      defaultModel: "opus",
      agentMemory: "true",
      museContributorMode: 1,
      claudeCustomModels: ["claude-a", "opus", "CLAUDE-A", 3, "bad id"],
      zzServerUnknown: true,
    };
    for (const owner of ["same", "unknown"]) {
      const s = owner === "same" ? await fresh({}, "t|u", "t|u") : await fresh({});
      apiMock.mockResolvedValueOnce(structuredClone(legacy));
      const ok = await hydrate(s);
      await vi.advanceTimersByTimeAsync(1_000);
      s.setSetting("tabSize", 2);
      await vi.advanceTimersByTimeAsync(1_000);
      check(`legacyServer.${owner}`, trace(s, { ok }));
    }
    // The server's own agentLaunchDefaults wins over a legacy defaultModel.
    const s = await fresh({ defaultModel: "haiku" }, "t|u", "t|u");
    apiMock.mockResolvedValueOnce({ defaultModel: "opus", agentLaunchDefaults: { codex: { model: "gpt", effort: "low" } } });
    await s.hydrateUIPrefs();
    await vi.advanceTimersByTimeAsync(1_000);
    check("legacyServer.launchMap", trace(s));
  });

  it("migrates a legacy local copy the same way and keeps its unknown keys", async () => {
    const s = await fresh({
      ttsQuietWhenHidden: true,
      autoTitleSuggest: false,
      assistantAgentOrder: ["codex"],
      assistantUtilityModels: { codex: "mini" },
      outputLanguage: "ja",
      replySuggestEnabled: false,
      opencodeCatalog: "go-first",
      defaultModel: "haiku",
      aiShortModels: { claude: "haiku" },
      zzLocalUnknown: [1],
    }, "t|u", "t|u");
    const loaded = trace(s);
    await s.hydrateUIPrefs();
    s.setSetting("tabSize", 2);
    await vi.advanceTimersByTimeAsync(1_000);
    check("legacyLocal", { loaded, after: trace(s) });
  });

  it("switches owner the same way", async () => {
    const s = await fresh({}, "t|a", "t|a");
    let owner = "t|a";
    s.setPrefsOwnerSource(() => owner);
    apiMock.mockResolvedValueOnce({ quickReplies: { a: { text: "A", count: 1, at: 1 } }, workingSets: [{ id: "x" }], chatSize: 20 });
    await s.hydrateUIPrefs();
    s.setSettings({ keybindings: { "app.palette": "Ctrl+P" }, iconSet: "seti", theme: "light" } as never);
    await vi.advanceTimersByTimeAsync(100); // the debounced save is still pending
    owner = "t|b";
    apiMock.mockResolvedValueOnce({ chatSize: 11, hiddenModels: { claude: [] } });
    const p = s.resyncAccumulatedForIdentitySwitch();
    const during = trace(s);
    const ok = await p;
    await vi.advanceTimersByTimeAsync(1_000);
    check("ownerSwitch", { during, after: trace(s, { ok }) });

    // An answer asked as the previous owner is not merged after the switch.
    const s2 = await fresh({}, "t|a", "t|a");
    let owner2 = "t|a";
    s2.setPrefsOwnerSource(() => owner2);
    let finish!: (v: unknown) => void;
    apiMock.mockReturnValueOnce(new Promise((r) => (finish = r)));
    const slow = s2.hydrateUIPrefs();
    owner2 = "t|b";
    finish({ chatSize: 30, quickReplies: { z: { text: "Z", count: 1, at: 1 } } });
    const slowOk = await slow;
    await vi.advanceTimersByTimeAsync(1_000);
    check("ownerSwitch.staleAnswer", trace(s2, { slowOk }));
  });

  it("keeps a per-tenant appearance the same way", async () => {
    const s = await fresh({}, "t|a", "t|a");
    let owner = "t|a";
    s.setPrefsOwnerSource(() => owner);
    await s.hydrateUIPrefs();
    s.setSettings({ appearancePerTenant: true } as never);
    s.setSettings({ theme: "light", topbarColor: "blue" } as never);
    owner = "u|a";
    s.applyTenantAppearance();
    const first = trace(s);
    s.setSetting("theme", "dark");
    owner = "t|a";
    s.applyTenantAppearance();
    await vi.advanceTimersByTimeAsync(1_000);
    const snaps = Object.fromEntries(
      Object.keys(localStorage).filter((k) => k.startsWith("af-appearance-tenant:")).sort().map((k) => [k, JSON.parse(localStorage.getItem(k)!)]),
    );
    check("tenantAppearance", { first, after: trace(s), snaps });
  });

  it("applies theme, fonts and locale to the page the same way", async () => {
    const dom = () => ({
      theme: document.documentElement.dataset.theme,
      lang: document.documentElement.lang,
      style: document.documentElement.getAttribute("style"),
      cjk: document.getElementById("af-cjk-font")?.textContent ?? null,
    });
    const s = await fresh(null);
    const out: Obj = { boot: dom() };
    s.setSettings({ theme: "light", topbarColor: "blue", leftpaneColor: "warm", viewerColor: "pink", cjkFont: "Meiryo", locale: "ja" } as never);
    out.light = dom();
    s.setSettings({ theme: "dark", leftpaneColor: "default", cjkFont: "欧文優先", locale: "en" } as never);
    out.dark = dom();
    const st = s.getSettings();
    out.helpers = {
      fontStack: ["", "システム等幅", "Fira Code"].map(s.fontStack),
      termFontStack: ["", "Fira Code"].map(s.termFontStack),
      chatFontStack: ["", "システム", "セリフ", "Fira Code"].map(s.chatFontStack),
      readerFontStack: ["明朝", "ゴシック"].map(s.readerFontStack),
      surfaceBg: ["default", "blue", "mono", "nope"].flatMap((c) => ["dark", "light"].map((t) => s.surfaceBg(c, t))),
      surfaceAccent: ["default", "warm", "nope"].map(s.surfaceAccent),
      effectiveTheme: [["inherit", "light"], ["dark", "light"], ["x", "dark"]].map(([a, b]) => s.effectiveTheme(a, b)),
      agentLaunchDefault: ["claude", "nope"].map((k) => s.agentLaunchDefault(st, k)),
    };
    // Leave the page as the next test file expects it.
    document.documentElement.removeAttribute("style");
    document.getElementById("af-cjk-font")?.remove();
    check("dom", out);
  });
});
