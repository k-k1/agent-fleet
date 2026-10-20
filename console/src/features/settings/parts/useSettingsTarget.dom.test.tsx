// A settings target belongs to the section it names. The Agents section's hook scrolls to an
// element and then consumes the request; a memory request (#2407) must pass it by, because
// Settings keeps the old section mounted for a render while it switches to Agent memory.
import { describe, it, expect, afterEach } from "vitest";
import { act, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { useSettingsTarget } from "./useSettingsTarget.ts";
import { useSettingsUI } from "../store.ts";
import { memoryTarget } from "../memory/memorySearch.ts";

function Probe() {
  const ref = useRef<HTMLDivElement>(null);
  useSettingsTarget(ref, true);
  return <div ref={ref} />;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const mount = async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Probe />));
};

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  useSettingsUI.setState({ settingsTarget: null });
});

describe("useSettingsTarget", () => {
  it("consumes a target of its own section", async () => {
    useSettingsUI.setState({ settingsTarget: "codex" });
    await mount();
    expect(useSettingsUI.getState().settingsTarget).toBeNull();
  });

  it("leaves a memory request for Agent memory", async () => {
    const want = memoryTarget({ scope: "user", name: "rules" });
    useSettingsUI.setState({ settingsTarget: want });
    await mount();
    expect(useSettingsUI.getState().settingsTarget).toBe(want);
  });
});
