import { describe, it, expect } from "vitest";
import { readReviewStatus } from "./memoryReviewQueue.ts";

// A status the Agent did not really give is "unknown", never "0 waiting": a nav that said 0 for an
// old Agent or a failed read would hide proposals that are there.
describe("readReviewStatus", () => {
  it("reads the Agent's status", () => {
    expect(readReviewStatus({ mode: "propose", enabled: true, pending: 3, releasePending: 1 })).toEqual({
      mode: "propose",
      enabled: true,
      pending: 3,
      releasePending: 1,
    });
  });
  it.each([[null], [undefined], [{}], [{ error: { code: "x" } }], [{ pending: "3" }], ["pending"]])(
    "reads %j as unknown",
    (v) => {
      expect(readReviewStatus(v)).toBeNull();
    },
  );
});
