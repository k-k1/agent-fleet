// The version zone at the bottom of the account menu (docs/log/35 §35.6.1 /
// version_info.go). Three things are pinned here:
//   1. On a deployment where code ships as an image (ECS), the zone answers not only
//      "which version" but "which image". The CP and the workspace are built from one
//      ImageTag by convention, but either can be rolled back alone, so they get separate
//      rows; `:dev` is mutable, so a digest rides along. That layout must come out as is.
//   2. Elsewhere the CP omits the keys entirely, so the rows must disappear. An empty
//      "image" row would report something that points at nothing.
//   3. Nothing is fetched until the menu is opened. The lazy fetch exists so no start-up
//      request is added for every tab and every user, so calls while closed must be 0.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: () => Promise.resolve({}),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
  clearLocalState: () => {},
  getTenant: () => "",
  setTenant: () => {},
  getUser: () => "",
  setUser: () => {},
  isTransientErr: () => false,
}));
// The notification center subscribes on its own; stub it out so only the version rows
// are under test.
vi.mock("../features/notifications/NotificationCenter.tsx", () => ({
  NotificationCenter: () => null,
}));
// The native self-update row does not exist on ECS (the CP registers no route = null).
vi.mock("../features/settings/hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { TopBar } from "./TopBar.tsx";
import { resetDeploymentVersionCache } from "../features/settings/deploymentVersion.ts";

const ECS_PAYLOAD = {
  version: "9.9.9",
  runtime: "ecs-ec2",
  image: { repo: "af-control-plane", tag: "9.9.9", digest: "sha256:cafe123456789" },
  workspace_image: { repo: "af-workspace", tag: "9.9.9" },
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TopBar toggleNav={() => {}} toggleLeft={() => {}} toggleLeftMode={() => {}} />);
  });
  await settle();
}

async function settle() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function openMenu() {
  const btn = host!.querySelector<HTMLButtonElement>(".acct-btn")!;
  await act(async () => {
    btn.click();
  });
  await settle();
}

const text = () => host?.textContent || "";
const versionRows = () => Array.from(host!.querySelectorAll(".acct-build")).map((n) => n.textContent || "");

beforeEach(() => {
  api.mockReset();
  resetDeploymentVersionCache();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("TopBar version zone", () => {
  it("does not fetch /api/version until the menu is opened", async () => {
    api.mockResolvedValue(ECS_PAYLOAD);
    await mount();
    expect(api).not.toHaveBeenCalled();

    await openMenu();
    expect(api).toHaveBeenCalledWith("api/version");
  });

  it("lists version, CP image, workspace image and build on ECS", async () => {
    api.mockResolvedValue(ECS_PAYLOAD);
    await mount();
    await openMenu();

    const rows = versionRows();
    expect(rows.some((r) => r.includes("9.9.9"))).toBe(true);
    // A tag alone does not identify the artifact, so the digest prefix rides along.
    expect(rows.some((r) => r.includes("af-control-plane:9.9.9") && r.includes("cafe123"))).toBe(true);
    expect(rows.some((r) => r.includes("af-workspace:9.9.9"))).toBe(true);
    // The digest is shown cut to 7 chars; the full value lives in the tooltip.
    expect(text()).not.toContain("cafe123456789");
    expect(host!.querySelector(".acct-build[title='sha256:cafe123456789']")).not.toBeNull();
  });

  it("shows no image rows on a deployment without images", async () => {
    api.mockResolvedValue({ version: "9.9.9", runtime: "local" });
    await mount();
    await openMenu();

    expect(text()).not.toContain("af-workspace");
    expect(text()).not.toContain("af-control-plane");
    // The build stamp (the frontend bundle) is always shown, so it stays.
    expect(versionRows().length).toBeGreaterThan(0);
  });

  it("copies version, images and build as one block", async () => {
    api.mockResolvedValue(ECS_PAYLOAD);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    await mount();
    await openMenu();

    const copy = host!.querySelector<HTMLButtonElement>(".acct-ver-copy")!;
    await act(async () => {
      copy.click();
    });
    await settle();

    const written = writeText.mock.calls[0][0] as string;
    expect(written).toContain("Agent Fleet 9.9.9 (ecs-ec2)");
    expect(written).toContain("control-plane: af-control-plane:9.9.9 (cafe123)");
    expect(written).toContain("workspace: af-workspace:9.9.9");
    expect(written).toContain("console: ");
  });
});
