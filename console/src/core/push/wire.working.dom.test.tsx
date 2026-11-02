// anySessionWorking is what keeps the idle pause (events.ts) from closing the stream under a
// window someone is watching.
import { beforeAll, describe, expect, it } from "vitest";

let wire: typeof import("./wire.ts");
let store: typeof import("../../features/sessions/store.ts");
beforeAll(async () => {
  wire = await import("./wire.ts");
  store = await import("../../features/sessions/store.ts");
}, 60_000); // wire.ts pulls in every store; the cold import is slow on a busy host

const set = (sessions: object[]) => store.useSessionsStore.setState({ sessions: sessions as never });

describe("anySessionWorking", () => {
  it("is false for an empty or idle list", () => {
    set([]);
    expect(wire.anySessionWorking()).toBe(false);
    set([{ name: "a", state: "idle", alive: true }, { name: "b", state: "question", alive: true }]);
    expect(wire.anySessionWorking()).toBe(false);
  });
  it("is true for a working session or a busy background task", () => {
    set([{ name: "a", state: "working", alive: true }]);
    expect(wire.anySessionWorking()).toBe(true);
    set([{ name: "a", state: "idle", alive: true, backgroundBusy: true }]);
    expect(wire.anySessionWorking()).toBe(true);
  });
  it("ignores a stopped session with a stale working state", () => {
    set([{ name: "a", state: "working", alive: false }]);
    expect(wire.anySessionWorking()).toBe(false);
  });
});
