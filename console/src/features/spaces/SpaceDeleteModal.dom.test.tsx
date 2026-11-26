// What the space delete SENDS: the names the member was shown, so the Agent settles nothing beyond
// them, and what a space_sessions_changed refusal does: the sessions it names join the list, and the
// next press (after a second look) sends them too.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const deleteSpace = vi.fn();
vi.mock("./store.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, deleteSpace: (...a: unknown[]) => deleteSpace(...a) };
});
vi.mock("../../layout/store.ts", () => ({
  useLayoutStore: (sel: (s: unknown) => unknown) => sel({ closeSessionPanes: vi.fn() }),
}));

const { SpaceDeleteModal } = await import("./SpaceDeleteModal.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useSpacesStore } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");
type Session = import("../../types/session.ts").Session;

let root: Root | null = null;
let host: HTMLDivElement;

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  deleteSpace.mockReset();
});

const button = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label);

const click = async (label: string) => {
  const b = button(label);
  if (!b) throw new Error("no button " + label);
  await act(async () => b.click());
};

describe("SpaceDeleteModal", () => {
  it("sends the names it showed and adds the ones the Agent names", async () => {
    const dir = "/home/dev/spaces/c";
    useSpacesStore.setState({ spaces: [{ folder: "c", name: "", dir }] });
    useSessionsStore.setState({
      sessions: [
        { name: "aishow1", kind: "claude", dir } as Session,
        { name: "shhide1", kind: "shell", dir: "/home/dev/alias" } as Session,
      ],
    });
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(
        <ToastProvider>
          <SpaceDeleteModal folder="c" label="c" owner="o" onClose={() => {}} />
        </ToastProvider>,
      );
    });

    // The named shell has no row yet: the list is read again before anything else is sent.
    const refresh = vi.fn(async () => {
      useSessionsStore.setState({
        sessions: [...useSessionsStore.getState().sessions, { name: "shnew01", kind: "shell", dir: "/home/dev/alias2" } as Session],
      });
    });
    useSessionsStore.setState({ refresh });
    deleteSpace.mockReset();
    deleteSpace.mockResolvedValueOnce({
      error: { code: "space_sessions_changed", message: "x", sessions: ["shhide1", "shnew01"] },
    });
    await click(t("pj.space_delete_do"));
    expect(deleteSpace).toHaveBeenLastCalledWith("c", "o", ["aishow1"]);
    expect(refresh).toHaveBeenCalled();

    // The named shell is now part of the space as shown: the button waits for a second look.
    expect(button(t("pj.space_delete_do"))?.disabled).toBe(true);
    await click(t("pj.space_delete_review"));
    deleteSpace.mockResolvedValueOnce(null);
    await click(t("pj.space_delete_do"));
    expect(deleteSpace).toHaveBeenLastCalledWith("c", "o", expect.arrayContaining(["aishow1", "shhide1", "shnew01"]));
  });
});
