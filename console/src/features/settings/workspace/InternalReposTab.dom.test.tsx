// The internal repository list offers rename and delete only on rows the CP marks
// can_manage: the repository's creator or a tenant_admin. A row without the field (a CP that
// does not restrict either) keeps both buttons.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: () => Promise.resolve({}),
  errText: (e: { message?: string }) => e?.message || "",
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(false) }));

import { InternalReposTab } from "./InternalReposTab.tsx";
import { t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("InternalReposTab", () => {
  it("shows rename and delete only where the CP allows them", async () => {
    api.mockResolvedValue({
      repos: [
        { name: "mine", clone_url: "https://x/git/t/mine.git", can_manage: true },
        { name: "theirs", clone_url: "https://x/git/t/theirs.git", can_manage: false },
        { name: "legacy", clone_url: "https://x/git/t/legacy.git" },
      ],
    });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<InternalReposTab />);
    });
    await act(async () => {
      await Promise.resolve();
    });
    const buttons = (name: string) =>
      Array.from(host!.querySelectorAll("li.internal-repo"))
        .find((li) => li.querySelector(".ir-name")?.textContent === name)!
        .querySelectorAll("button");
    const labels = (name: string) => Array.from(buttons(name)).map((b) => b.textContent);
    expect(labels("mine")).toEqual(expect.arrayContaining([t("git.rename"), t("common.delete")]));
    expect(labels("legacy")).toEqual(expect.arrayContaining([t("git.rename"), t("common.delete")]));
    expect(labels("theirs")).not.toContain(t("git.rename"));
    expect(labels("theirs")).not.toContain(t("common.delete"));
  });
});
