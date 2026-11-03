import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { answerConfirm, confirmDialog } from "../../../test/confirmDialog.ts";
import { BackgroundShellsPanel } from "./BackgroundShellsPanel.tsx";
import { t } from "../../../lib/i18n/index.ts";
import type { BackgroundShell } from "../../../core/api/client.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  vi.useRealTimers();
  host?.remove();
  root = null;
  host = null;
  vi.restoreAllMocks();
});

const now = Date.parse("2026-11-03T15:00:00+09:00");
const hung: BackgroundShell = {
  pid: 200,
  start: 5000,
  command: "cat > /tmp/x_test.go",
  startedAt: "2026-11-03T13:00:00+09:00",
  running: false,
  cpuSec: 0,
};
const build: BackgroundShell = { pid: 210, start: 6000, command: "go test ./...", startedAt: "2026-11-03T14:55:00+09:00", running: true, cpuSec: 120 };

// liveClock: leave the panel on its own clock instead of pinning `now`.
function render(shells: BackgroundShell[], stopping: ReadonlySet<number> = new Set(), liveClock = false) {
  const clock = liveClock ? undefined : now;
  const onStop = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <ConfirmProvider>
        <BackgroundShellsPanel shells={shells} stopping={stopping} onStop={onStop} now={clock} />
      </ConfirmProvider>,
    ),
  );
  return onStop;
}

const rows = () => Array.from(document.querySelectorAll<HTMLLIElement>(".mbs-item"));

describe("BackgroundShellsPanel", () => {
  it("renders nothing without shells", () => {
    render([]);
    expect(document.querySelector(".mirror-bg-shells")).toBeNull();
  });

  it("lists each shell with its command and age, and flags the stuck one", () => {
    render([hung, build]);
    expect(rows()).toHaveLength(2);
    expect(rows()[0].textContent).toContain("cat > /tmp/x_test.go");
    expect(rows()[0].textContent).toContain("2h");
    expect(rows()[0].classList.contains("stuck")).toBe(true);
    expect(rows()[1].textContent).toContain("5m");
    expect(rows()[1].classList.contains("stuck")).toBe(false);
  });

  it("stops only after the member confirms", async () => {
    const onStop = render([hung]);
    const btn = document.querySelector<HTMLButtonElement>(".mbs-stop")!;
    await act(async () => btn.click());
    const dlg = confirmDialog()!;
    expect(dlg.querySelector(".ui-confirm-title")?.textContent).toBe(t("mirror.bg_shell_stop_confirm_title"));
    expect(dlg.querySelector("pre.ui-confirm-code")?.textContent).toBe(hung.command);
    expect(dlg.textContent).toContain(t("mirror.bg_shell_stop_confirm_body"));
    expect(dlg.querySelector(".ui-btn-danger, .btn-danger")?.textContent).toBe(t("mirror.bg_shell_stop"));
    await answerConfirm(false);
    expect(confirmDialog()).toBeNull();
    expect(onStop).not.toHaveBeenCalled();
    await act(async () => btn.click());
    await answerConfirm(true);
    expect(onStop).toHaveBeenCalledWith(hung);
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("disables the button while that shell is being stopped", () => {
    render([hung], new Set([200]));
    expect(document.querySelector<HTMLButtonElement>(".mbs-stop")!.disabled).toBe(true);
  });
});

describe("BackgroundShellsPanel clock", () => {
  it("crosses the 30-minute line with no new data from the poll", () => {
    vi.useFakeTimers();
    vi.setSystemTime(Date.parse("2026-11-03T13:29:00+09:00"));
    const young = { ...hung, startedAt: "2026-11-03T13:00:00+09:00" };
    render([young], new Set(), true);
    expect(rows()[0].classList.contains("stuck")).toBe(false);
    expect(rows()[0].textContent).toContain("29m");
    act(() => vi.advanceTimersByTime(90_000));
    expect(rows()[0].classList.contains("stuck")).toBe(true);
    expect(rows()[0].textContent).toContain("30m");
  });
});
