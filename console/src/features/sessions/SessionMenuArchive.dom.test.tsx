import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { raw } from "../../core/api/client.ts";
import { allViews } from "../../layout/ops.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { setLocale, t } from "../../lib/i18n/index.ts";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { SessionRow } from "./SessionRow.tsx";
import { useSessionsStore } from "./store.ts";
import { useSessionActions } from "./useSessionActions.tsx";
import type { Session } from "../../types/session.ts";

vi.mock("../../core/api/client.ts", async (original) => ({
  ...(await original<typeof import("../../core/api/client.ts")>()),
  raw: vi.fn(),
}));

const session: Session = {
  name: "sk7f3q9",
  title: "Update the guide",
  kind: "claude",
  alive: true,
  state: "idle",
  links: [{ provider: "github", key: "o/r#1", role: "working", state: "merged" }],
};
const refresh = vi.fn(async () => {});
const originalRefresh = useSessionsStore.getState().refresh;
let root: Root;
let host: HTMLDivElement;

function Row() {
  const actions = useSessionActions();
  return <SessionRow s={session} actions={actions} running selected={false} opens={[]} multi={false} />;
}

const click = async (button: HTMLElement) => {
  await act(async () => button.click());
};
const dialog = () => document.querySelector('[role="dialog"]');
const buttonNamed = (within: ParentNode, label: string) =>
  [...within.querySelectorAll("button")].find((button) => button.textContent?.trim() === label)!;
const paneIsOpen = () =>
  allViews(useLayoutStore.getState().layout).some((view) => view.session === session.name);

async function openArchive() {
  await click(host.querySelector<HTMLButtonElement>(".sess-menu-btn")!);
  await click(buttonNamed(document.querySelector(".sess-menu")!, t("srow.archive")));
  expect(document.querySelector(".sess-menu")).toBeNull();
  expect(dialog()?.textContent).toContain(session.title);
  expect(dialog()?.textContent).toContain("stops the session if it is running");
  expect(dialog()?.textContent).toContain("Open archive (restore)” in the Repositories heading");
  expect(raw).not.toHaveBeenCalled();
}

beforeEach(async () => {
  setLocale("en");
  vi.mocked(raw).mockReset();
  refresh.mockClear();
  useSessionsStore.setState({ refresh });
  useLayoutStore.getState().initSinglePane({ kind: "terminal", chat: true }, session.name, null, "tabs");
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<ToastProvider><ConfirmProvider><ul><Row /></ul></ConfirmProvider></ToastProvider>);
  });
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  useSessionsStore.setState({ refresh: originalRefresh });
  setLocale("ja");
});

describe("individual session archive confirmation", () => {
  it.each(["Cancel", "Escape"])("keeps the session and sends no request on %s", async (dismiss) => {
    await openArchive();
    if (dismiss === "Cancel") {
      await click(buttonNamed(dialog()!, t("ui.cancel")));
    } else {
      await act(async () => {
        document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      });
    }
    expect(dialog()).toBeNull();
    expect(raw).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
    expect(paneIsOpen()).toBe(true);
  });

  it("sends one request and closes the session pane after success", async () => {
    let respond!: (response: Response) => void;
    vi.mocked(raw).mockReturnValue(new Promise((resolve) => { respond = resolve; }));
    await openArchive();
    const confirm = buttonNamed(dialog()!, t("srow.archive_confirm"));
    await act(async () => {
      confirm.click();
      confirm.click();
    });
    expect(raw).toHaveBeenCalledExactlyOnceWith("api/sessions/sk7f3q9/archive", { method: "POST" });
    expect(paneIsOpen()).toBe(true);
    expect(refresh).not.toHaveBeenCalled();
    await act(async () => respond(new Response(null, { status: 204 })));
    expect(paneIsOpen()).toBe(false);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it.each(["HTTP error", "network error"])("keeps the pane and shows an error on %s", async (failure) => {
    if (failure === "HTTP error") {
      vi.mocked(raw).mockResolvedValue(new Response(null, { status: 500 }));
    } else {
      vi.mocked(raw).mockRejectedValue(new TypeError("Failed to fetch"));
    }
    await openArchive();
    await click(buttonNamed(dialog()!, t("srow.archive_confirm")));
    expect(raw).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain(t("sess.archive_failed"));
    expect(host.textContent).toContain(session.title);
    expect(paneIsOpen()).toBe(true);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("archives from the merged-PR shortcut after one confirmation", async () => {
    vi.mocked(raw).mockResolvedValue(new Response(null, { status: 204 }));
    await click(host.querySelector<HTMLButtonElement>(".sess-tidy")!);
    expect(dialog()?.textContent).toContain(t("srow.prs_merged_reason"));
    expect(raw).not.toHaveBeenCalled();
    await click(buttonNamed(dialog()!, t("srow.archive_confirm")));
    expect(dialog()).toBeNull();
    expect(raw).toHaveBeenCalledExactlyOnceWith("api/sessions/sk7f3q9/archive", { method: "POST" });
    expect(paneIsOpen()).toBe(false);
  });
});
