import { afterEach, describe, expect, it, vi } from "vitest";
import { addPushDevice, postViewing, probePushKey, releasePushEndpoint, snapshotReleaseBinding } from "./pushApi.ts";

const reply = (status: number, body: unknown) => vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(body), { status }));
afterEach(() => vi.restoreAllMocks());

describe("probePushKey", () => {
  it("returns the key", async () => {
    reply(200, { publicKey: "k" });
    expect(await probePushKey()).toBe("k");
  });
  it("reads the CP's own push_unavailable as not configured", async () => {
    reply(503, { error: { code: "push_unavailable", message: "x" } });
    expect(await probePushKey()).toBeNull();
  });
  it("treats a bare gateway 503 as transient, not as not configured", async () => {
    reply(503, "Service unavailable");
    await expect(probePushKey()).rejects.toThrow();
  });
  it("treats a network failure as transient", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("offline"));
    await expect(probePushKey()).rejects.toThrow();
  });
});

describe("release token", () => {
  const sub = { endpoint: "https://push.example/e", keys: { p256dh: "p", auth: "a" } } as PushSubscriptionJSON;
  const bodyOf = (spy: ReturnType<typeof vi.spyOn>, call: number) => JSON.parse(String((spy.mock.calls[call][1] as RequestInit).body));
  afterEach(() => localStorage.clear());
  it("sends the token the add answered with, and nothing before one exists", async () => {
    const spy = reply(200, {});
    await releasePushEndpoint(sub);
    expect(bodyOf(spy, 0).releaseToken).toBeUndefined();
    spy.mockResolvedValueOnce(new Response(JSON.stringify({ id: "d", releaseToken: "tok1" }), { status: 201 }));
    expect(await addPushDevice(sub)).toBe("d");
    await releasePushEndpoint(sub);
    expect(bodyOf(spy, 2).releaseToken).toBe("tok1");
  });
  it("drops the old binding when the new one cannot be stored", async () => {
    const spy = reply(200, { id: "d", releaseToken: "tokA" });
    await addPushDevice(sub);
    const real = Storage.prototype.setItem;
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    spy.mockResolvedValue(new Response(JSON.stringify({ id: "d", releaseToken: "tokB" }), { status: 201 }));
    await addPushDevice({ ...sub, endpoint: "https://push.example/new" });
    expect(snapshotReleaseBinding()).toBeNull();
    Storage.prototype.setItem = real;
  });
  it("does not send a token issued for another subscription, and uses a binding taken earlier", async () => {
    const spy = reply(200, { id: "d", releaseToken: "tokA" });
    await addPushDevice(sub);
    await releasePushEndpoint({ ...sub, endpoint: "https://push.example/other" });
    expect(bodyOf(spy, 1).releaseToken).toBeUndefined();
    // Another registration replaced the stored token after the release was decided on.
    const taken = snapshotReleaseBinding();
    spy.mockResolvedValue(new Response(JSON.stringify({ id: "d", releaseToken: "tokB" }), { status: 201 }));
    await addPushDevice(sub);
    spy.mockResolvedValue(new Response("{}", { status: 200 }));
    await releasePushEndpoint(sub, undefined, taken);
    expect(bodyOf(spy, 3).releaseToken).toBe("tokA");
  });
});

describe("postViewing", () => {
  it("sends seq only when given", async () => {
    const spy = reply(200, {});
    await postViewing("d", "s");
    await postViewing("d", "s", "t", 7);
    expect(JSON.parse(String((spy.mock.calls[0][1] as RequestInit).body)).seq).toBeUndefined();
    expect(JSON.parse(String((spy.mock.calls[1][1] as RequestInit).body)).seq).toBe(7);
  });
});
