// The properties toggle on the shared lightbox's bar (ADR 0081 decision 3).
//
// The lightbox takes a URL (`src`), not a path, and a download URL cannot be turned back
// into one. So the toggle appears only when the host passes `path` — the mirror's pasted
// images have none, and offering them a properties panel that can only 404 is the failure
// this guards. The item is found by elimination, the way the folder button's test does it:
// the labels come from the catalogue and an index would break on the next bar button.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// The panel fetches on open. Mocked to a settled promise so the assertions are about the
// TOGGLE, not about the route (which lane A owns).
vi.mock("../imagegen/api.ts", () => ({
  imageProperties: () => Promise.resolve({ source: "sidecar", model: "sdxl-base", seed: 42 }),
}));

const { ImageLightbox } = await import("./ImageLightbox.tsx");

let host: HTMLDivElement;
let root: Root;

const render = async (path?: string): Promise<HTMLButtonElement[]> => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<ImageLightbox src="blob:shot" path={path} onClose={() => {}} />);
  });
  return [...host.querySelectorAll<HTMLButtonElement>(".mirror-lightbox-bar button")];
};

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
});

describe("ライトボックスのプロパティ切替", () => {
  it("path を渡したときだけバーに増える", async () => {
    const plain = (await render()).length;
    await act(async () => root.unmount());
    document.body.innerHTML = "";
    const withPath = (await render("generated/console/image-1.png")).length;
    expect(withPath).toBe(plain + 1);
  });

  it("path が無ければパネルは出しようがない", async () => {
    await render();
    expect(host.querySelector(".mirror-lightbox-props")).toBeNull();
    expect(host.querySelector(".imgprops")).toBeNull();
  });

  it("開いたときだけ読みに行き、もう一度押すと閉じる", async () => {
    await render("generated/console/image-1.png");
    const toggle = host.querySelector<HTMLButtonElement>(".mirror-lightbox-props")!;
    expect(host.querySelector(".imgprops")).toBeNull();
    await act(async () => toggle.click());
    expect(host.querySelector(".imgprops")?.textContent).toContain("sdxl-base");
    await act(async () => toggle.click());
    expect(host.querySelector(".imgprops")).toBeNull();
  });
});
