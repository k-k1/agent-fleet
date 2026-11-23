// The count chip of a repository or space row (ADR 0142 decision 10): shown only when the Terminal
// view lists shells for that place, and a click asks the view for it without folding the row it sits on.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";
import { TerminalChip } from "./TerminalChip.tsx";
import { useTerminalReveal } from "./store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useReposStore } from "../repos/store.ts";
import { setSettings } from "../../lib/settings.ts";

let root: Root | null = null;
let host: HTMLDivElement;
const shell = (name: string, extra: Partial<Session> = {}): Session => ({ name, kind: "shell", alive: true, ...extra });

beforeEach(() => {
  localStorage.clear();
  setSettings({ workingSets: [], workingSetActive: "" });
  useTerminalReveal.setState({ key: null, n: 0 });
  useReposStore.setState({ repos: [{ name: "app" }] });
  useSessionsStore.setState({ sessions: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

describe("TerminalChip", () => {
  it("is absent for a place with no shell", () => {
    act(() => root!.render(<TerminalChip groupKey="shell:repo:app" />));
    expect(host.querySelector(".term-chip")).toBeNull();
  });

  it("counts the place's shells, not its SSM logins, and asks for the group without folding the row", () => {
    useSessionsStore.setState({
      sessions: [
        shell("a", { repo: "app", dir: "/home/dev/repos/app" }),
        shell("b", { repo: "app", dir: "/home/dev/repos/app" }),
        { name: "m", kind: "ssm", alive: true, repo: "app", dir: "/home/dev/repos/app" },
        shell("h", { repo: "dev", dir: "/home/dev" }),
      ],
    });
    const rowClick = vi.fn();
    act(() =>
      root!.render(
        <div onClick={rowClick}>
          <TerminalChip groupKey="shell:repo:app" />
        </div>,
      ),
    );
    const chip = host.querySelector<HTMLButtonElement>(".term-chip")!;
    expect(chip.textContent).toBe("2");
    act(() => chip.click());
    expect(useTerminalReveal.getState()).toMatchObject({ key: "shell:repo:app", n: 1 });
    expect(rowClick).not.toHaveBeenCalled();
  });

  // The view lists a worktree's shells by the parent copy the tree files it under; a chip that
  // counted by folder name would promise a group the view then does not show.
  it("counts under a working set by the same rule as the view", () => {
    useReposStore.setState({ repos: [{ name: "app" }, { name: "scratch", worktree: true, parent: "app" }] });
    useSessionsStore.setState({ sessions: [shell("w", { repo: "scratch", dir: "/home/dev/repos/scratch" })] });
    const set = { id: "w1", name: "A", repos: ["app"], convs: [], sessions: [], schedules: [] };
    setSettings({ workingSets: [set], workingSetActive: "w1" });
    act(() => root!.render(<TerminalChip groupKey="shell:repo:scratch" />));
    expect(host.querySelector(".term-chip")?.textContent).toBe("1");
    act(() => setSettings({ workingSets: [{ ...set, repos: ["payments"] }], workingSetActive: "w1" }));
    expect(host.querySelector(".term-chip")).toBeNull();
  });
});
