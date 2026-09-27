// Coming back to a studio tab. A tabbed cell mounts only its selected view, so the studio pane
// remounts from nothing on every tab switch. Its first frame must be the studio as this window
// last saw it — the bound agent, the engine state, the picker — not "no agent" and "engine
// unavailable" for the moment before the reads come back.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { StudioWire } from "./wire.ts";

// The Agent never answers during these tests: what is on screen can only be the cache.
vi.mock("./api.ts", async (orig) => {
  const never = () => new Promise<never>(() => {});
  return {
    ...(await orig<typeof import("./api.ts")>()),
    imagegenStatus: never,
    imagegenJobs: never,
    listStudios: never,
    imagegenHistory: never,
    getStudio: never,
    studioDraftLog: never,
  };
});
vi.mock("../mirror/MirrorView.tsx", () => ({ MirrorView: () => <div className="mirror-stub" /> }));
vi.mock("../repos/useRepoRail.ts", () => ({
  useRepoRailContext: () => ({ launchKinds: ["claude"], connsSettling: false }),
}));

import { ImagegenView } from "./ImagegenView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useStudioCache } from "./studioCache.ts";

const studio = {
  id: "s1",
  title: "港の夕暮れ",
  draft: { prompt: "harbour at dusk", provider: "comfy", model: "sdxl-base" },
  locks: [],
  session: "painter",
  agent_trial: true,
  created_at: "2026-09-27T10:00:00Z",
  updated_at: "7",
  recent_log: [],
} as unknown as StudioWire;
const status = {
  enabled: true,
  ready: true,
  providers: [{ id: "comfy", kind: "comfy", fleet: true, ready: true, models: [{ id: "sdxl-base", label: "SDXL" }] }],
};

let host: HTMLDivElement;
let root: Root;

const mount = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () =>
    root.render(
      <ToastProvider>
        <ConfirmProvider>
          <ImagegenView paneId="p1" studioId="s1" />
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );
};

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  useStudioCache.setState({ list: [], byId: {}, status: null });
});

describe("a remounted studio pane", () => {
  it("paints the studio this window last read, not an empty one", async () => {
    useWorkspaceStore.setState({ state: "running" });
    useSessionsStore.setState({ sessions: [{ name: "painter", kind: "claude", driver: "tui", alive: true, studio: "s1" }], loaded: true });
    useStudioCache.setState({ byId: { s1: studio }, status: status as never, list: [{ id: "s1", title: "港の夕暮れ", updated_at: "7" }] });
    await mount();
    expect(host.querySelector(".igen-agent-none")).toBeNull();
    expect(host.querySelector(".mirror-stub")).not.toBeNull();
    expect(host.querySelector(".igen-engine-unavailable")).toBeNull();
    expect(host.querySelector<HTMLTextAreaElement>("textarea")?.value).toBe("harbour at dusk");
    expect(host.querySelector(".igen-studio-pick")?.textContent).toContain("港の夕暮れ");
  });
});
