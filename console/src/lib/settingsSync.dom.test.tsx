// Regression guard for the paths through which server-synced settings (ui-prefs) could
// silently disappear.
//
// Damage: right after a workspace restart (GET returning 502), sending a single reply from a
// surface whose localStorage was empty (just logged out, another browser, a dev Console on a
// different origin) turned the save of the learned chip into a PUT of the whole default set,
// wiping the server's learned suggestions, pins and usage counts at once. Server-first hydrate
// then propagated that to every device.
//
// Three promises are pinned here:
//   1. Do not save while the server copy has never been read (send everything once it lands).
//   2. Never let an empty server value flatten non-empty local accumulated data (push it back
//      instead).
//   3. Ordinary cross-device settings sync still works (the guard did not break syncing).
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

const SETTINGS_KEY = "af-display-settings";
const learned = { "ok": { text: "OK", count: 9, at: 1 }, "commit して": { text: "commit して", count: 4, at: 2 } };

// settings.ts is single global state read from localStorage at import time, so rebuild the
// whole module for every case.
async function freshSettings(local: Record<string, unknown> = {}) {
  localStorage.setItem(SETTINGS_KEY, JSON.stringify(local));
  vi.resetModules();
  return await import("./settings.ts");
}

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset();
  apiJSONMock.mockReset().mockResolvedValue({});
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ui-prefs: no saving until the server copy has been read", () => {
  it("keeps quiet while the server copy is unreadable, then sends once it lands", async () => {
    // This is the shape CP returns while the workspace is starting: api() resolves with
    // {error} instead of throwing.
    apiMock.mockResolvedValueOnce({ error: { code: "http_502", message: "workspace agent unreachable" } });
    const s = await freshSettings({}); // localStorage empty too, so locally this is DEFAULTS

    expect(await s.hydrateUIPrefs()).toBe(false);
    expect(s.uiPrefsLoaded()).toBe(false);

    s.setSetting("iconSet", "seti");
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled(); // defaults must not flatten the server

    // After recovery: once the real server data is readable, send the pending change once.
    apiMock.mockResolvedValueOnce({ quickReplies: learned, chatSize: 16 });
    expect(await s.hydrateUIPrefs()).toBe(true);
    expect(s.uiPrefsLoaded()).toBe(true);
    expect(s.getSettings().quickReplies).toEqual(learned); // the server's learned data arrives
    expect(s.getSettings().chatSize).toBe(16);
    expect(s.getSettings().iconSet).toBe("seti"); // the pending local change survives too

    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
    const [path, method, body] = apiJSONMock.mock.calls[0] as [string, string, Record<string, unknown>];
    expect([path, method]).toEqual(["api/env/ui-prefs", "PUT"]);
    expect(body.quickReplies).toEqual(learned);
    expect(body.iconSet).toBe("seti");
  });

  it("keeps saving normally once the server copy has been read", async () => {
    apiMock.mockResolvedValueOnce({});
    const s = await freshSettings({});
    expect(await s.hydrateUIPrefs()).toBe(true); // an empty {} counts as read: a new user

    s.setSetting("iconSet", "material");
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
  });
});

describe("ui-prefs: an empty value never flattens accumulated data", () => {
  it("never lets an emptied server copy wipe learned replies, and pushes them back", async () => {
    const s = await freshSettings({ quickReplies: learned, quickRepliesPinned: ["OK"], iconSet: "seti" });
    // The server as it looked after the incident (the whole default set). Ordinary keys are
    // adopted, accumulated data is not.
    apiMock.mockResolvedValueOnce({ quickReplies: {}, quickRepliesPinned: [], iconSet: "vscode" });

    expect(await s.hydrateUIPrefs()).toBe(true);
    expect(s.getSettings().quickReplies).toEqual(learned);
    expect(s.getSettings().quickRepliesPinned).toEqual(["OK"]);
    expect(s.getSettings().iconSet).toBe("vscode"); // non-accumulated keys stay server-first

    // Self-heal: push the locally held learned data back to the server.
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
    const body = apiJSONMock.mock.calls[0][2] as Record<string, unknown>;
    expect(body.quickReplies).toEqual(learned);
    expect(body.quickRepliesPinned).toEqual(["OK"]);
  });

  it("still adopts a populated server copy of the same keys", async () => {
    const s = await freshSettings({ quickReplies: { ok: { text: "OK", count: 1, at: 1 } } });
    apiMock.mockResolvedValueOnce({ quickReplies: learned });

    expect(await s.hydrateUIPrefs()).toBe(true);
    expect(s.getSettings().quickReplies).toEqual(learned); // growth from another device arrives
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled(); // nothing to push back once it was adopted
  });

});

// Damage: working sets created under one account stayed in memory after switching to another
// account in the same browser/workspace, and the "never flatten with empty" self-heal above
// wrote them back into the new account's ui-prefs.json. An identity/tenant switch must bypass
// that self-heal first.
describe("ui-prefs: an owner switch never leaks the previous owner's accumulated data", () => {
  it("clears every accumulated key locally instead of showing the previous owner's data", async () => {
    const s = await freshSettings({ workingSets: [{ id: "g1", name: "旧アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }] });
    apiMock.mockResolvedValueOnce({}); // the new owner's server value is still empty, as it should be
    void s.resyncAccumulatedForIdentitySwitch();
    // Local state is already empty before the hydrate response arrives: the previous owner's
    // working sets must never appear on the new owner's screen, not even for a frame.
    expect(s.getSettings().workingSets).toEqual([]);
  });

  it("does not restore the previous owner's data via the empty-server self-heal path", async () => {
    const s = await freshSettings({ workingSets: [{ id: "g1", name: "旧アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }] });
    apiMock.mockResolvedValueOnce({}); // the new owner really is empty
    await s.resyncAccumulatedForIdentitySwitch();
    expect(s.getSettings().workingSets).toEqual([]);
    await vi.advanceTimersByTimeAsync(1_000);
    // If the restore path fired by mistake, the old account's working sets would be pushed here.
    expect(apiJSONMock).not.toHaveBeenCalled();
  });

  it("adopts the new owner's own accumulated data, not the previous owner's", async () => {
    const s = await freshSettings({ workingSets: [{ id: "g1", name: "旧アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }] });
    apiMock.mockResolvedValueOnce({ workingSets: [{ id: "g2", name: "新アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }] });
    await s.resyncAccumulatedForIdentitySwitch();
    expect(s.getSettings().workingSets).toEqual([{ id: "g2", name: "新アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }]);
  });

  it("discards a pending debounced save instead of sending the previous owner's value to the new owner", async () => {
    const s = await freshSettings({});
    apiMock.mockResolvedValueOnce({});
    await s.hydrateUIPrefs();
    s.setSetting("workingSets", [{ id: "g1", name: "旧アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }]); // scheduled to save in 600ms
    apiMock.mockResolvedValueOnce({});
    await s.resyncAccumulatedForIdentitySwitch(); // the pending save must be discarded here
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled();
  });

  it("covers every ACCUMULATED key generically (no per-key wiring needed for new ones)", async () => {
    const s = await freshSettings({
      quickReplies: learned,
      quickRepliesPinned: ["OK"],
      quickRepliesHidden: ["NG"],
      keybindings: { save: "mod+s" },
      hiddenModels: { claude: ["opus"] },
      claudeCustomModels: ["claude-custom"],
      workingSets: [{ id: "g1", name: "旧", repos: [], convs: [], sessions: [], schedules: [] }],
      ttsVoicePool: { zundamon: { use: true } },
      ttsUserDict: "旧アカウントの辞書",
      ssmHostUsage: { host1: 3 },
      ssmHostColors: { host1: "#fff" },
      expandThinking: { claude: true },
    });
    apiMock.mockResolvedValueOnce({});
    await s.resyncAccumulatedForIdentitySwitch();
    const after = s.getSettings();
    for (const key of [
      "quickReplies", "quickRepliesPinned", "quickRepliesHidden", "keybindings",
      "claudeCustomModels", "workingSets", "ttsVoicePool", "ttsUserDict",
      "ssmHostUsage", "ssmHostColors", "expandThinking",
    ] as const) {
      expect(s.isEmptyPref((after as any)[key])).toBe(true);
    }
    // hiddenModels defaults to {claude:["fable"]} - an initial recommendation, not an empty
    // value. Check that it lands on the default rather than on empty.
    expect(after.hiddenModels).toEqual({ claude: ["fable"] });
  });
});

describe("ui-prefs: the ACCUMULATED classification", () => {
  it("classifies which keys are protected and what counts as empty", async () => {
    const s = await freshSettings({});
    expect(s.isAccumulatedSetting("quickReplies")).toBe(true);
    expect(s.isAccumulatedSetting("keybindings")).toBe(true);
    expect(s.isAccumulatedSetting("workingSets")).toBe(true);
    // Toggles and colours cannot become empty, so they are not protected: if one is lost the
    // user can just pick it again.
    expect(s.isAccumulatedSetting("quickRepliesEnabled")).toBe(false);
    expect(s.isAccumulatedSetting("chatSize")).toBe(false);
    // 0 / false are chosen values, not "lost".
    expect([s.isEmptyPref({}), s.isEmptyPref([]), s.isEmptyPref(""), s.isEmptyPref(null)]).toEqual([true, true, true, true]);
    expect([s.isEmptyPref(0), s.isEmptyPref(false), s.isEmptyPref({ a: 1 }), s.isEmptyPref(["a"])]).toEqual([false, false, false, false]);
  });
});

// #1023 item 1: a failed save used to reach console.warn only, while the screen and the Agent
// acted on different settings from then on.
describe("ui-prefs: a save that does not land is shown", () => {
  it("goes failed on an error answer and back to synced once a save lands", async () => {
    apiMock.mockResolvedValueOnce({});
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    expect(s.prefsSyncState()).toBe("synced");
    apiJSONMock.mockResolvedValueOnce({ error: { code: "http_413", message: "too large" } });
    s.setSetting("chatSize", 17);
    expect(s.prefsSyncState()).toBe("pending");
    await vi.advanceTimersByTimeAsync(1_000);
    expect(s.prefsSyncState()).toBe("failed");
    apiMock.mockResolvedValueOnce({}); // the retry reads the server copy first
    s.retryPrefsSync();
    expect(s.prefsSyncState()).toBe("failed"); // stays shown until something lands
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(2);
    expect(s.prefsSyncState()).toBe("synced");
  });

  it("goes failed on a thrown PUT too", async () => {
    apiMock.mockResolvedValueOnce({});
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    apiJSONMock.mockRejectedValueOnce(new Error("network"));
    s.setSetting("chatSize", 17);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(s.prefsSyncState()).toBe("failed");
  });

  it("goes failed when the server copy stays unreadable with a change waiting", async () => {
    apiMock.mockResolvedValue({ error: { code: "http_502" } });
    const s = await freshSettings({});
    expect(await s.hydrateUIPrefs()).toBe(false);
    s.setSetting("chatSize", 17);
    expect(s.prefsSyncState()).toBe("pending");
    await vi.advanceTimersByTimeAsync(200_000); // every automatic hydrate retry used up
    expect(s.prefsSyncState()).toBe("failed");
    apiMock.mockResolvedValue({});
    s.retryPrefsSync(); // reads the server copy, then sends the waiting change
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
    expect(s.prefsSyncState()).toBe("synced");
  });

  // Review of #1023: re-sending a failed save blind (whole-object PUT, last writer wins) wiped
  // every change another device had saved since. A refresh reads first, keeps only this tab's
  // unsaved keys over the server copy, then sends.
  it("keeps a failed change over the server copy on refresh, without wiping another device's", async () => {
    apiMock.mockResolvedValueOnce({ chatSize: 14, iconSet: "default" });
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    apiJSONMock.mockResolvedValueOnce({ error: { code: "http_502" } });
    s.setSetting("chatSize", 17);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(s.prefsSyncState()).toBe("failed");
    // Meanwhile another device saved a different key.
    apiMock.mockResolvedValueOnce({ chatSize: 14, iconSet: "seti" });
    await s.refreshUIPrefs(); // back in the foreground
    expect(s.getSettings().chatSize).toBe(17);
    expect(s.getSettings().iconSet).toBe("seti");
    await vi.advanceTimersByTimeAsync(1_000);
    const body = (apiJSONMock.mock.calls[1] as [string, string, Record<string, unknown>])[2];
    expect([body.chatSize, body.iconSet]).toEqual([17, "seti"]);
    expect(s.prefsSyncState()).toBe("synced");
  });

  it("lets only the newest save speak for the state", async () => {
    apiMock.mockResolvedValueOnce({});
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    let landFirst!: (v: unknown) => void;
    apiJSONMock
      .mockImplementationOnce(() => new Promise((r) => { landFirst = r; })) // slow, lands last
      .mockResolvedValueOnce({ error: { code: "http_413" } });
    s.setSetting("chatSize", 17);
    await vi.advanceTimersByTimeAsync(700);
    s.setSetting("chatSize", 18);
    await vi.advanceTimersByTimeAsync(700);
    expect(s.prefsSyncState()).toBe("failed");
    landFirst({});
    await vi.advanceTimersByTimeAsync(0);
    expect(s.prefsSyncState()).toBe("failed"); // the older save carried 17, not 18
  });

  it("does not go failed when an older save errors after the newest one landed", async () => {
    apiMock.mockResolvedValueOnce({});
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    let errFirst!: (v: unknown) => void;
    apiJSONMock
      .mockImplementationOnce(() => new Promise((r) => { errFirst = r; }))
      .mockResolvedValueOnce({});
    s.setSetting("chatSize", 17);
    await vi.advanceTimersByTimeAsync(700);
    s.setSetting("chatSize", 18);
    await vi.advanceTimersByTimeAsync(700);
    errFirst({ error: { code: "http_502" } });
    await vi.advanceTimersByTimeAsync(0);
    expect(s.prefsSyncState()).toBe("synced"); // 18, the newest, is on the server
  });

  it("ignores the previous owner's save answering after an identity switch", async () => {
    apiMock.mockResolvedValueOnce({});
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    let fail!: (e: unknown) => void;
    apiJSONMock.mockImplementationOnce(() => new Promise((_, rej) => { fail = rej; }));
    s.setSetting("chatSize", 17);
    await vi.advanceTimersByTimeAsync(700);
    apiMock.mockResolvedValueOnce({});
    await s.resyncAccumulatedForIdentitySwitch();
    fail(new Error("network"));
    await vi.advanceTimersByTimeAsync(0);
    expect(s.prefsSyncState()).toBe("synced");
  });
});

// #1023 item 1: the local copy records whose server copy it was merged with. A key the server
// has never held may be pushed back only for that owner — on a first boot the identity-switch
// resync does not run, so without the record a previous account's localStorage could be PUT to
// a new account (#972 tried the push without it and reverted).
describe("ui-prefs: the owner of the local copy", () => {
  const OWNER_KEY = "af-display-settings-owner";
  const hidden = { claude: ["fable", "opus"] };

  it("pushes back a key the server never held, for the recorded owner", async () => {
    const s = await freshSettings({ hiddenModels: hidden });
    localStorage.setItem(OWNER_KEY, "t1|u1");
    s.setPrefsOwnerSource(() => "t1|u1");
    apiMock.mockResolvedValueOnce({ chatSize: 14 }); // no hiddenModels key at all
    await s.hydrateUIPrefs();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
    expect((apiJSONMock.mock.calls[0] as [string, string, Record<string, unknown>])[2].hiddenModels).toEqual(hidden);
  });

  it("drops another owner's accumulated data at the first hydrate and sends nothing of it", async () => {
    const s = await freshSettings({ hiddenModels: hidden, workingSets: [{ id: "g1", name: "旧", repos: [], convs: [], sessions: [], schedules: [] }] });
    localStorage.setItem(OWNER_KEY, "t1|u1");
    s.setPrefsOwnerSource(() => "t1|u2");
    apiMock.mockResolvedValueOnce({ workingSets: [] }); // empty on the server: the old self-heal path
    await s.hydrateUIPrefs();
    expect(s.getSettings().workingSets).toEqual([]);
    expect(s.getSettings().hiddenModels).toEqual({ claude: ["fable"] });
    await vi.advanceTimersByTimeAsync(1_000);
    for (const call of apiJSONMock.mock.calls as [string, string, Record<string, unknown>][]) {
      expect(call[2].workingSets).toEqual([]);
      expect(call[2].hiddenModels).toEqual({ claude: ["fable"] });
    }
    expect(localStorage.getItem(OWNER_KEY)).toBe("t1|u2");
  });

  it("does not push a never-held key while the owner is unknown", async () => {
    const s = await freshSettings({ hiddenModels: hidden });
    localStorage.setItem(OWNER_KEY, "t1|u1");
    s.setPrefsOwnerSource(() => ""); // whoami has not answered yet
    apiMock.mockResolvedValueOnce({});
    await s.hydrateUIPrefs();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled();
    expect(s.getSettings().hiddenModels).toEqual(hidden); // kept locally, just not sent
    expect(localStorage.getItem(OWNER_KEY)).toBe("t1|u1");
  });

  // Review of #1023: adopting an unrecorded copy and recording this owner over it made the
  // SECOND boot push it as this account's.
  it("treats an unrecorded copy as another owner's, so no later boot pushes it", async () => {
    const s = await freshSettings({ hiddenModels: hidden });
    s.setPrefsOwnerSource(() => "t1|u1");
    apiMock.mockResolvedValueOnce({});
    await s.hydrateUIPrefs();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled();
    expect(s.getSettings().hiddenModels).toEqual({ claude: ["fable"] });
    expect(localStorage.getItem(OWNER_KEY)).toBe("t1|u1"); // recorded from now on

    const again = await freshSettings(JSON.parse(localStorage.getItem("af-display-settings")!));
    again.setPrefsOwnerSource(() => "t1|u1");
    apiMock.mockResolvedValueOnce({});
    await again.hydrateUIPrefs();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled();
  });

  // Review of #1023: localStorage is shared by tabs on different tenants; a write must move the
  // record to the writer, or another tenant's tab vouches for it at its next boot.
  it("moves the record to whichever owner last wrote the shared copy", async () => {
    const s = await freshSettings({});
    localStorage.setItem(OWNER_KEY, "t1|u1");
    s.setPrefsOwnerSource(() => "t1|u1");
    apiMock.mockResolvedValueOnce({});
    await s.hydrateUIPrefs();
    localStorage.setItem(OWNER_KEY, "t2|u1"); // another tab, on t2, hydrated since
    s.setSetting("hiddenModels", hidden); // this tab (t1) writes the shared copy
    expect(localStorage.getItem(OWNER_KEY)).toBe("t1|u1");
  });
});
