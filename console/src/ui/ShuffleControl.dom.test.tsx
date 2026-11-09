// The random-colors control: the schedule segment writes appearanceShuffle, and the dice re-picks
// the surface colors at once whatever the schedule.
import { describe, expect, it, vi } from "vitest";

vi.mock("../core/api/client.ts", () => ({
  api: vi.fn().mockResolvedValue({}),
  apiJSON: vi.fn().mockResolvedValue({}),
}));

import { act } from "react";
import { createRoot } from "react-dom/client";
import { ShuffleControl } from "./ShuffleControl.tsx";
import { getSettings, setSettings } from "../lib/settings.ts";
import { setLocale } from "../lib/i18n/index.ts";

describe("ShuffleControl", () => {
  it("sets the schedule and the dice re-picks the colors", async () => {
    setLocale("en");
    setSettings({ appearanceShuffle: "off", topbarColor: "default", theme: "dark" });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => root.render(<ShuffleControl segClass="seg choice-seg" />));
    const btn = (label: string) =>
      [...host.querySelectorAll("button")].find((b) => b.textContent === label) as HTMLButtonElement;

    expect(btn("Off").className).toContain("active");
    const dice = host.querySelector<HTMLButtonElement>(".shuffle-dice")!;
    expect(dice.getAttribute("aria-label")).toBe("Change colors now");

    await act(async () => dice.click());
    expect(getSettings().appearanceShuffle).toBe("off");
    const first = getSettings().topbarColor;
    expect(first).not.toBe("default");

    await act(async () => btn("Daily").click());
    expect(getSettings().appearanceShuffle).toBe("daily");
    expect(btn("Daily").className).toContain("active");
    // Switching on from off picks at once.
    expect(getSettings().topbarColor).not.toBe(first);
    expect(getSettings().theme).toBe("dark");

    await act(async () => root.unmount());
    host.remove();
  });
});
