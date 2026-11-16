// SoloSection: a section that is a whole left-pane view is always open, whatever fold was
// stored for it, and its header is a plain title. A section nested in its body folds as usual.
import { describe, it, expect, afterEach, beforeEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Section, SoloSection } from "./Section.tsx";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => localStorage.clear());
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
});
async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
}

describe("SoloSection", () => {
  it("shows the body of a section whose stored fold is closed, without a toggle", async () => {
    localStorage.setItem("af-section-solo-a", "0");
    await mount(
      <SoloSection>
        <Section id="solo-a" title="Outer">
          <p className="body-a">body</p>
        </Section>
      </SoloSection>,
    );
    expect(host!.querySelector(".body-a")).not.toBeNull();
    expect(host!.querySelector("button.ui-section-toggle")).toBeNull();
    expect(host!.querySelector(".ui-section-toggle.solo")?.textContent).toContain("Outer");
    // The stored choice is left alone for whoever reads the key.
    expect(localStorage.getItem("af-section-solo-a")).toBe("0");
  });

  it("overrides a controlled section that says closed", async () => {
    await mount(
      <SoloSection>
        <Section title="Files" open={false} onToggle={() => {}}>
          <p className="body-b">body</p>
        </Section>
      </SoloSection>,
    );
    expect(host!.querySelector(".body-b")).not.toBeNull();
  });

  it("leaves a nested section foldable", async () => {
    await mount(
      <SoloSection>
        <Section title="Outer">
          <Section title="Inner" defaultOpen={false}>
            <p className="body-c">body</p>
          </Section>
        </Section>
      </SoloSection>,
    );
    expect(host!.querySelector(".body-c")).toBeNull();
    const toggle = host!.querySelector<HTMLButtonElement>("button.ui-section-toggle")!;
    expect(toggle.textContent).toContain("Inner");
    await act(async () => toggle.click());
    expect(host!.querySelector(".body-c")).not.toBeNull();
  });
});
