// The notification table is a real table: every switch is reachable by its row and column header,
// operable as a native checkbox, and writes exactly its own cell.
import { describe, it, expect, afterEach, beforeEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { NotificationTable } from "./NotificationTable.tsx";
import { getSettings, setSettings } from "../../../lib/settings.ts";
import { NOTIFY_ROWS, notifyCell } from "../../notifications/prefs.ts";

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  setSettings({ notifyUnread: {}, notifyDevice: {}, childIdleNotify: true, usageResetNotify: true });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root.render(<NotificationTable />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const switches = () => [...host.querySelectorAll<HTMLInputElement>("input[role=switch]")];

describe("NotificationTable", () => {
  it("has column and row headers and one switch per cell", () => {
    expect(host.querySelectorAll("thead th[scope=col]")).toHaveLength(4);
    expect(host.querySelectorAll("tbody th[scope=row]")).toHaveLength(NOTIFY_ROWS.length);
    expect(switches()).toHaveLength(NOTIFY_ROWS.length * 3);
    expect(switches().every((el) => el.type === "checkbox" && el.getAttribute("aria-label"))).toBe(true);
    expect(host.querySelector("caption")).not.toBeNull();
  });

  it("shows the dot of a row that waits for a person as on and not operable", () => {
    const fixed = switches().filter((el) => el.disabled);
    expect(fixed).toHaveLength(NOTIFY_ROWS.filter((r) => r.unreadFixed).length);
    expect(fixed.every((el) => el.checked)).toBe(true);
  });

  it("writes one cell and leaves the rest", () => {
    const idx = NOTIFY_ROWS.findIndex((r) => r.row === "schedule");
    const os = switches()[idx * 3 + 1];
    act(() => os.click());
    expect(getSettings().notifyDevice).toEqual({ "schedule.os": false });
    expect(os.checked).toBe(false);
    expect(notifyCell(getSettings(), "schedule", "voice")).toBe(true);
    expect(switches().filter((el) => !el.checked)).toHaveLength(1);
  });

  it("reflects the legacy switches it has not overridden", () => {
    act(() => setSettings({ childIdleNotify: false }));
    const idx = NOTIFY_ROWS.findIndex((r) => r.row === "turn-child");
    expect(switches().slice(idx * 3, idx * 3 + 3).map((el) => el.checked)).toEqual([false, false, false]);
  });
});
