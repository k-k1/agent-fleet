import { describe, expect, it } from "vitest";
import { RecoveryCoordinator } from "./recovery.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("RecoveryCoordinator", () => {
  it("shares a same-key recovery request", async () => {
    const gate = new RecoveryCoordinator();
    const pending = deferred<string>();
    let requests = 0;
    const applied: string[] = [];
    const run = () => gate.run("unknown:p1", () => {
      requests++;
      return pending.promise;
    }, (value) => applied.push(value));

    const first = run();
    const second = run();
    expect(first).toBe(second);
    expect(requests).toBe(1);
    pending.resolve("remote");
    await first;
    expect(applied).toEqual(["remote"]);
  });

  it("ignores an older response after a newer recovery starts", async () => {
    const gate = new RecoveryCoordinator();
    const older = deferred<string>();
    const newer = deferred<string>();
    const applied: string[] = [];

    const first = gate.run("unknown:p1", () => older.promise, (value) => applied.push(value));
    const second = gate.run("conflict:p1", () => newer.promise, (value) => applied.push(value));
    newer.resolve("newer");
    await second;
    older.resolve("older");
    await first;
    expect(applied).toEqual(["newer"]);
  });

  it("ignores a response from an invalidated file epoch", async () => {
    const gate = new RecoveryCoordinator();
    const pending = deferred<string>();
    const applied: string[] = [];
    const request = gate.run("unknown:p1", () => pending.promise, (value) => applied.push(value));

    gate.invalidate();
    pending.resolve("stale");
    await request;
    expect(applied).toEqual([]);
  });
});
