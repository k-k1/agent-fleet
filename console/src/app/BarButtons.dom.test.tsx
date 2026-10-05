// The top bar and the WS bar draw every button through ui/Button (#1631). The element rules
// that used to style their raw <button>s (`:where(.topbar|.wsbar) button`) are gone, so a raw
// button left behind would fall back to the browser's own chrome — these tests sweep every
// button each bar renders, popovers open, and pin the role, accessible name and disabled state
// of the ones whose meaning depends on them.
//
// The one deliberate exception is the appearance popover's segmented controls: they are the
// ui-seg primitive (ui.css), the same raw `.seg-btn` markup Settings uses.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// Hoisted: TopBar's import graph reaches api() while the modules load.
const { api } = vi.hoisted(() => ({ api: vi.fn((..._args: unknown[]) => Promise.resolve({})) }));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
}));
vi.mock("./usageResetNotify.ts", () => ({ useUsageResetNotify: () => {} }));
vi.mock("../features/settings/hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { WsBar } from "./WsBar.tsx";
import { TopBar } from "./TopBar.tsx";
import { SwatchGrid } from "../ui/SwatchGrid.tsx";
import { ConfirmProvider } from "../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { useAwsLoginStore } from "../features/awslogin/store.ts";
import { useGcpLoginStore } from "../features/gcplogin/store.ts";
import { SURFACE_COLORS } from "../lib/settings.ts";
import { t } from "../lib/i18n/index.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <ToastProvider>
        <ConfirmProvider>{node}</ConfirmProvider>
      </ToastProvider>,
    ),
  );
}

const click = async (sel: string) => {
  const el = host!.querySelector<HTMLElement>(sel);
  if (!el) throw new Error(`not in the DOM: ${sel}`);
  await act(async () => el.click());
};
const buttonsIn = (sel: string) => Array.from(host!.querySelectorAll<HTMLButtonElement>(`${sel} button`));
const nameOf = (b: HTMLElement) => b.getAttribute("aria-label") || b.getAttribute("title") || b.textContent?.trim() || "";

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockClear();
  useWorkspaceStore.setState({ state: "unknown", stale: false, slotReplace: false });
  useAwsLoginStore.setState({ profiles: null, loggingOut: {} });
  useGcpLoginStore.setState({ profiles: null });
});

describe("WS bar", () => {
  async function mountBar() {
    useWorkspaceStore.setState({ state: "running", stale: true, slotReplace: true, browserUnavailable: "" });
    useAwsLoginStore.setState({
      profiles: [
        { name: "dev", label: "Dev", accountId: "1", roleName: "r", state: "signed_in", expiresAt: "", expiring: false },
        { name: "prod", label: "", accountId: "2", roleName: "r", state: "none", expiresAt: "", expiring: false },
      ],
      loggingOut: { dev: true },
    });
    useGcpLoginStore.setState({ profiles: [{ name: "g", label: "", project: "p", account: "", state: "none" }] });
    await mount(<WsBar />);
  }

  it("draws every button, popovers included, as a ui/Button with a name", async () => {
    await mountBar();
    // Opened one at a time: each popover closes the others on the next outside press.
    for (const open of [".ws-stale:not(.ws-slotmove) .ws-stale-pill", ".ws-slotmove .ws-stale-pill", ".ws-aws:not(.ws-gcp) .ws-aws-btn", ".ws-gcp-btn", ".ws-preview-btn"]) {
      await click(open);
      const buttons = buttonsIn(".wsbar");
      // Positive control: the sweep sees the popover it just opened, not only the bar.
      expect(buttons.length).toBeGreaterThan(8);
      for (const b of buttons) {
        expect(b.classList.contains("ui-btn"), `${open}: ${b.className}`).toBe(true);
        expect(b.type).toBe("button");
        expect(nameOf(b), `${open}: ${b.className}`).not.toBe("");
      }
      await click(open);
    }
  });

  it("keeps the pane buttons' variant, names and disabled states", async () => {
    await mountBar();
    const power = host!.querySelector<HTMLButtonElement>(".ws-power")!;
    expect(power.getAttribute("aria-label")).toBe(t("wsbar.stop_ws"));
    expect(power.disabled).toBe(false);
    const split = buttonsIn(".wsbar").filter((b) => b.classList.contains("ws-split") || b.classList.contains("ws-closeall"));
    expect(split.length).toBeGreaterThanOrEqual(4);
    for (const b of split) expect(b.classList.contains("ui-btn-ghost"), b.className).toBe(true);
    // A single blank pane: nothing to close, so close-all is off — and still there.
    const closeAll = host!.querySelector<HTMLButtonElement>(".ws-closeall")!;
    expect(closeAll.disabled).toBe(true);
    expect(closeAll.title).toContain(t("wsbar.close_all_title"));
    // Positive control for the disabled check: split-down is possible with one pane.
    const down = split.find((b) => b.title.startsWith(t("wsbar.split_down_title")))!;
    expect(down.disabled).toBe(false);
  });

  it("keeps the AWS chip's login/logout as ghost buttons, logout off while it runs", async () => {
    await mountBar();
    await click(".ws-aws:not(.ws-gcp) .ws-aws-btn");
    const chip = host!.querySelector<HTMLButtonElement>(".ws-aws:not(.ws-gcp) .ws-aws-btn")!;
    expect(chip.getAttribute("aria-expanded")).toBe("true");
    const logout = host!.querySelector<HTMLButtonElement>(".ws-aws-logout")!;
    expect(logout.classList.contains("ui-btn-ghost")).toBe(true);
    expect(logout.disabled).toBe(true);
    const login = host!.querySelector<HTMLButtonElement>(".ws-aws-login")!;
    expect(login.classList.contains("ui-btn-ghost")).toBe(true);
    expect(login.disabled).toBe(false);
  });
});

describe("top bar", () => {
  it("draws every button but the segmented controls as a ui/Button with a name", async () => {
    await mount(<TopBar toggleNav={() => {}} toggleLeft={() => {}} toggleLeftMode={() => {}} />);
    for (const open of [null, ".appr-btn", ".acct-btn"]) {
      if (open) await click(open);
      const buttons = buttonsIn(".topbar");
      expect(buttons.length).toBeGreaterThan(4);
      for (const b of buttons) {
        const seg = b.classList.contains("seg-btn");
        expect(b.classList.contains("ui-btn"), `${open}: ${b.className}`).toBe(!seg);
        if (!seg) expect(b.type).toBe("button");
        expect(nameOf(b), `${open}: ${b.className}`).not.toBe("");
      }
      if (open) await click(open);
    }
  });

  it("keeps the account menu's items as menuitems and the copy button labelled", async () => {
    await mount(<TopBar toggleNav={() => {}} toggleLeft={() => {}} toggleLeftMode={() => {}} />);
    await click(".acct-btn");
    const items = buttonsIn(".acct-menu").filter((b) => b.classList.contains("acct-item"));
    expect(items.length).toBeGreaterThanOrEqual(3);
    for (const b of items) expect(b.getAttribute("role")).toBe("menuitem");
    expect(host!.querySelector(".acct-ver-copy")!.getAttribute("aria-label")).toBe(t("topbar.copy_version"));
  });
});

describe("SwatchGrid", () => {
  it("is one named ui/Button per colour, the selected one checked", async () => {
    const onChange = vi.fn();
    await mount(<SwatchGrid theme="dark" value="blue" onChange={onChange} />);
    const swatches = Array.from(host!.querySelectorAll<HTMLButtonElement>(".swatch"));
    expect(swatches.length).toBe(SURFACE_COLORS.length);
    for (const b of swatches) {
      expect(b.classList.contains("ui-btn")).toBe(true);
      expect(b.type).toBe("button");
      expect(b.title).not.toBe("");
    }
    const active = swatches.filter((b) => b.classList.contains("active"));
    expect(active.map((b) => b.textContent)).toEqual(["✓"]);
    await act(async () => swatches[0].click());
    expect(onChange).toHaveBeenCalledWith(SURFACE_COLORS[0].id);
  });
});
