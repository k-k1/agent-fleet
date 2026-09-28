// StartHost remounts the launch dialog for each target (working copy, work item). The dialog
// keeps which fields the person edited, so reusing one instance across targets would launch a
// new working copy with the last one's name and base (ADR 0103 decision 8).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

const mounts: { repo: string; instance: number }[] = [];
let nextInstance = 0;
vi.mock("./LaunchModal.tsx", () => ({
  LaunchModal: ({ repo }: { repo: string }) => {
    const [instance] = useState(() => ++nextInstance);
    mounts.push({ repo, instance });
    return null;
  },
}));
vi.mock("./useRepoRail.ts", () => ({ useRepoRailContext: () => ({ launchKinds: ["claude"], connsSettling: false }) }));
vi.mock("./useStartWork.ts", () => ({ useStartWork: () => vi.fn() }));

const { StartHost } = await import("./StartHost.tsx");
const { useLaunchSeed, useLaunchTarget } = await import("./store.ts");
type LaunchWorkItem = import("./store.ts").LaunchWorkItem;

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(async () => {
  mounts.length = 0;
  useLaunchTarget.getState().clear();
  useLaunchSeed.getState().clear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<StartHost />));
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

const open = async (name: string) =>
  act(async () => useLaunchTarget.getState().open({ name, path: `/home/dev/repos/${name}` } as never));
const lastInstance = (repo: string) => mounts.filter((m) => m.repo === repo).at(-1)?.instance;

describe("StartHost", () => {
  it("gives each working copy and each work item its own launch dialog", async () => {
    await open("web");
    const first = lastInstance("web");
    await open("api");
    expect(lastInstance("api")).not.toBe(first);

    const item: LaunchWorkItem = { provider: "github", key: "acme/api#1", branch: "feature/issue-1", title: "t", type: "", labels: [] };
    const before = lastInstance("api");
    await act(async () => useLaunchSeed.getState().set("p", "t", "", "", "", item));
    expect(lastInstance("api")).not.toBe(before);
  });
});
