import { describe, expect, it } from "vitest";
import type { FleetNotification } from "./store.ts";
import { unseenSessionEventIDs } from "./read.ts";

const event = (id: string, targetID: string, seen = false, type = "session"): FleetNotification => ({
  seq: Number(id.replace(/\D/g, "")) || 1,
  id,
  kind: "answer-ready",
  target: { type, id: targetID },
  displayName: targetID,
  payload: {},
  createdAt: "2026-07-13T00:00:00Z",
  seen,
});

describe("unseenSessionEventIDs", () => {
  it("returns only unread events belonging to the active session", () => {
    const items = [
      event("e1", "active"),
      event("e2", "active", true),
      event("e3", "other"),
      event("e4", "active", false, "usage"),
    ];
    expect(unseenSessionEventIDs(items, "active")).toEqual(["e1"]);
    expect(unseenSessionEventIDs(items, "")).toEqual([]);
  });
});
