// The region format and time zone selectors (#2185): UTC must be pickable even though
// Intl.supportedValuesOf("timeZone") omits it, and both selects need an accessible name.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../../lib/i18n/index.ts";
import { DisplayTab } from "./DisplayTab.tsx";

let root: Root | null = null;
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.innerHTML = "";
});

function render(): HTMLDivElement {
  const host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<DisplayTab />));
  return host;
}

describe("DisplayTab time zone and region format", () => {
  it("offers UTC and names both selects", () => {
    const host = render();
    const zone = host.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings.time_zone")}"]`);
    const region = host.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings.region_format")}"]`);
    expect(zone).not.toBeNull();
    expect(region).not.toBeNull();
    const values = Array.from(zone!.options).map((o) => o.value);
    expect(values).toContain("UTC");
    expect(values).toContain("auto");
    expect(values).toContain("Asia/Tokyo");
  });
});
