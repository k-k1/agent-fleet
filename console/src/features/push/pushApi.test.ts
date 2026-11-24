import { beforeEach, describe, expect, it, vi } from "vitest";

const sent: unknown[] = [];
vi.mock("../../core/api/client.ts", () => ({
  raw: vi.fn(),
  apiJSON: async () => ({}),
  rawJSON: vi.fn((_p: string, _m: string, body: unknown) => {
    sent.push(body);
    return Promise.resolve({ ok: true, status: 200 });
  }),
}));

import { putPushPrefs } from "./pushApi.ts";

describe("putPushPrefs wire", () => {
  beforeEach(() => void (sent.length = 0));
  it("leaves offOrigins out of a kinds-only save so the CP keeps the saved origins", async () => {
    await putPushPrefs({ off: ["question"] });
    expect(sent[0]).toEqual({ off: ["question"] });
    expect(Object.keys(sent[0] as object)).not.toContain("offOrigins");
  });
  it("sends both lists for an origin save", async () => {
    await putPushPrefs({ off: [], offOrigins: ["autorun"] });
    expect(sent[0]).toEqual({ off: [], offOrigins: ["autorun"] });
  });
});
