// A quick launch that needs a choice only the dialog offers hands over to the launch dialog on
// that agent kind (useLaunchTarget.initialKind); StartHost passes it on, and a plain open or a
// close forgets it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("./useRepoRail.ts", () => ({ useRepoRailContext: () => ({ access: { kinds: null, noRoute: false, allOff: false, refresh: () => {} }, launchKinds: ["claude", "codex"], connsSettling: false }) }));
vi.mock("./StartModal.tsx", () => ({ StartModal: () => null }));
const seen: { initialKind?: string }[] = [];
vi.mock("./LaunchModal.tsx", () => ({
  LaunchModal: (p: { initialKind?: string }) => {
    seen.push({ initialKind: p.initialKind });
    return <div data-testid="dialog" />;
  },
}));

const { StartHost } = await import("./StartHost.tsx");
const { useLaunchTarget } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

const repo = { name: "app", path: "/home/dev/repos/app", branch: "main" } as never;
let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(async () => {
  seen.length = 0;
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ offers: [] }), { status: 200 })));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <StartHost />
      </ToastProvider>,
    );
  });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  useLaunchTarget.getState().clear();
  vi.unstubAllGlobals();
});

describe("StartHost launch dialog kind", () => {
  it("opens the dialog on the kind a quick launch handed over", async () => {
    await act(async () => useLaunchTarget.getState().open(repo, "", false, "codex"));
    expect(host.querySelector("[data-testid=dialog]")).not.toBeNull();
    expect(seen.at(-1)?.initialKind).toBe("codex");
  });

  it("leaves the kind to the dialog for an ordinary open, and forgets it on close", async () => {
    await act(async () => useLaunchTarget.getState().open(repo, "", false, "codex"));
    await act(async () => useLaunchTarget.getState().clear());
    expect(host.querySelector("[data-testid=dialog]")).toBeNull();
    await act(async () => useLaunchTarget.getState().open(repo));
    expect(seen.at(-1)?.initialKind).toBeUndefined();
  });
});
