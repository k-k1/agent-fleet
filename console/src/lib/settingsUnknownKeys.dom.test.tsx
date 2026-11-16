// The ui-prefs save is a whole-object PUT, so whatever the Console drops from its state, the next
// save deletes on the server. Keys this Console does not know (the member layer's agentEnabled
// when it was unknown, a newer Console's keys) and fields of a launch-default row it does not read
// (the default route, ADR 0138 D11b) have to survive a GET → edit → PUT round trip. An owner
// switch is the exception: the previous owner's unknown keys must not reach the new owner's copy.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

const SETTINGS_KEY = "af-display-settings";

async function freshSettings(local: Record<string, unknown> = {}) {
  localStorage.setItem(SETTINGS_KEY, JSON.stringify(local));
  vi.resetModules();
  return await import("./settings.ts");
}

const ROUTE = { route: "anthropic-api", connection: "ANTHROPIC_API_KEY" };

/** The server copy a member with a default route and a future key would hold. */
const serverCopy = () => ({
  chatSize: 15,
  futureKey: { a: 1 },
  agentLaunchDefaults: {
    claude: { model: "opus", effort: "high", startMode: "normal", skipPermissions: true, route: ROUTE },
    futurekind: { model: "x", route: { route: "r" } },
  },
});

/** The body of the one PUT the edit sent. */
async function savedBody(): Promise<Record<string, any>> {
  await vi.advanceTimersByTimeAsync(1_000);
  expect(apiJSONMock).toHaveBeenCalledTimes(1);
  const [path, method, body] = apiJSONMock.mock.calls[0] as [string, string, Record<string, any>];
  expect([path, method]).toEqual(["api/env/ui-prefs", "PUT"]);
  return body;
}

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset();
  apiJSONMock.mockReset().mockResolvedValue({});
  localStorage.clear();
});
afterEach(() => vi.useRealTimers());

describe("ui-prefs: a save keeps what this Console does not know", () => {
  it("keeps an unknown top-level server key across an unrelated edit", async () => {
    apiMock.mockResolvedValueOnce(serverCopy());
    const s = await freshSettings({});
    expect(await s.hydrateUIPrefs()).toBe(true);
    s.setSetting("iconSet", "seti");
    const body = await savedBody();
    expect(body.futureKey).toEqual({ a: 1 });
    expect(body.iconSet).toBe("seti");
  });

  it("keeps the default route on a launch-default row the member edits", async () => {
    apiMock.mockResolvedValueOnce(serverCopy());
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    const cur = s.getSettings().agentLaunchDefaults;
    s.setSettings({ agentLaunchDefaults: { ...cur, claude: { ...cur.claude, skipPermissions: false } } });
    const body = await savedBody();
    expect(body.agentLaunchDefaults.claude.route).toEqual(ROUTE);
    expect(body.agentLaunchDefaults.claude.skipPermissions).toBe(false);
    // A row for a kind this Console does not know is kept whole.
    expect(body.agentLaunchDefaults.futurekind).toEqual({ model: "x", route: { route: "r" } });
  });

  it("keeps the default route across a reload from the local copy", async () => {
    apiMock.mockResolvedValueOnce(serverCopy());
    let s = await freshSettings({});
    await s.hydrateUIPrefs();
    const local = JSON.parse(localStorage.getItem(SETTINGS_KEY) || "{}");
    apiMock.mockResolvedValueOnce(serverCopy());
    s = await freshSettings(local);
    expect(s.getSettings().agentLaunchDefaults.claude).toMatchObject({ route: ROUTE });
  });

  it("takes an unknown key away when the server no longer holds it", async () => {
    apiMock.mockResolvedValueOnce(serverCopy());
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    apiMock.mockResolvedValueOnce({ chatSize: 15 });
    await s.hydrateUIPrefs();
    s.setSetting("iconSet", "seti");
    const body = await savedBody();
    expect("futureKey" in body).toBe(false);
  });
});

describe("ui-prefs: the same owner follows a deletion on another device", () => {
  it("drops an unknown key the server no longer holds, and does not send it back", async () => {
    apiMock.mockResolvedValueOnce(serverCopy());
    const s = await freshSettings({});
    s.setPrefsOwnerSource(() => "t1|alice");
    await s.hydrateUIPrefs();
    // Another device deleted the key; this one hydrates again as the same recorded owner.
    apiMock.mockResolvedValueOnce({ chatSize: 15, agentLaunchDefaults: serverCopy().agentLaunchDefaults });
    await s.hydrateUIPrefs();
    expect("futureKey" in (s.getSettings() as unknown as Record<string, unknown>)).toBe(false);
    s.setSetting("iconSet", "seti");
    const body = await savedBody();
    expect("futureKey" in body).toBe(false);
    expect(body.agentLaunchDefaults.claude.route).toEqual(ROUTE);
  });
});

describe("ui-prefs: an owner switch never carries unknown keys across", () => {
  it("sends none of the previous owner's route or rows after a switch to an empty server copy", async () => {
    let owner = "t1|alice";
    apiMock.mockResolvedValueOnce(serverCopy());
    const s = await freshSettings({});
    s.setPrefsOwnerSource(() => owner);
    await s.hydrateUIPrefs();
    expect(s.getSettings().agentLaunchDefaults.claude).toMatchObject({ route: ROUTE });
    owner = "t1|bob";
    apiMock.mockResolvedValueOnce({}); // bob's server copy holds nothing yet
    await s.resyncAccumulatedForIdentitySwitch();
    s.setSetting("iconSet", "seti");
    const body = await savedBody();
    expect("futureKey" in body).toBe(false);
    expect(body.agentLaunchDefaults.claude.route).toBeUndefined();
    expect(body.agentLaunchDefaults.futurekind).toBeUndefined();
  });

  it("sends none of them at a start-up under another owner than the one recorded", async () => {
    apiMock.mockResolvedValueOnce(serverCopy());
    let s = await freshSettings({});
    s.setPrefsOwnerSource(() => "t1|alice");
    await s.hydrateUIPrefs();
    const local = JSON.parse(localStorage.getItem(SETTINGS_KEY) || "{}");
    // A reload signed in as bob: the local copy and its owner record are still alice's.
    apiMock.mockResolvedValueOnce({});
    s = await freshSettings(local);
    s.setPrefsOwnerSource(() => "t1|bob");
    await s.hydrateUIPrefs();
    s.setSetting("iconSet", "seti");
    const body = await savedBody();
    expect("futureKey" in body).toBe(false);
    expect(body.agentLaunchDefaults.claude.route).toBeUndefined();
    expect(body.agentLaunchDefaults.futurekind).toBeUndefined();
  });


  it("does not send the previous owner's unknown keys under the new owner", async () => {
    apiMock.mockResolvedValueOnce(serverCopy());
    const s = await freshSettings({});
    await s.hydrateUIPrefs();
    apiMock.mockResolvedValueOnce({ chatSize: 13 }); // the new owner holds no unknown key
    await s.resyncAccumulatedForIdentitySwitch();
    s.setSetting("iconSet", "seti");
    const body = await savedBody();
    expect("futureKey" in body).toBe(false);
  });
});
