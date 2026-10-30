// A remote-only row must send the remote it was listed from: with the same branch on
// two remotes, a bare name makes git refuse the checkout (#2268).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
  errText: (e: unknown) => String(e),
}));

const { BranchModal } = await import("./BranchModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  apiMock.mockResolvedValue({
    current: "work",
    branches: [
      { name: "work", unix: 3, current: true },
      { name: "main", unix: 2, remote: true, remote_name: "origin" },
      { name: "local-only", unix: 1 },
    ],
  });
  apiJSONMock.mockResolvedValue({});
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.clearAllMocks();
});

async function openAndClick(label: string): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <BranchModal repoName="app" onChecked={() => {}} />
      </ToastProvider>,
    );
  });
  const row = [...document.querySelectorAll<HTMLButtonElement>(".branch-item")].find((b) => b.textContent?.includes(label))!;
  await act(async () => {
    row.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
}

describe("BranchModal checkout payload", () => {
  it("sends the remote of a remote-only row", async () => {
    await openAndClick("main");
    expect(apiJSONMock).toHaveBeenCalledWith("api/repos/app/checkout", "POST", { branch: "main", remote: "origin" });
  });

  it("sends only the branch for a local row", async () => {
    await openAndClick("local-only");
    expect(apiJSONMock).toHaveBeenCalledWith("api/repos/app/checkout", "POST", { branch: "local-only" });
  });
});
