// The Behavior row "Tool permissions" asks "ask for approval before each tool run?", so On must mean
// ask (skipPermissions=false). The stored field keeps its old meaning (skipPermissions=true = skip).
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: vi.fn(() => Promise.resolve({})),
  apiJSON: vi.fn(() => Promise.resolve({})),
  raw: vi.fn(() => Promise.resolve(new Response("{}"))),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => vi.fn() }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({
  useConfirm: () => () => Promise.resolve(true),
}));

const { ClaudeCard } = await import("./ClaudeCard.tsx");
const { t: tr } = await import("../../../lib/i18n/index.ts");
const { getSettings, setSettings } = await import("../../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(skipPermissions: boolean) {
  const cur = getSettings().agentLaunchDefaults;
  setSettings({ agentLaunchDefaults: { ...cur, claude: { ...cur.claude, skipPermissions } } });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ClaudeCard running st={{ connected: true }} reload={() => {}} claude={{ rtk_available: true }} updateClaude={() => {}} />,
    );
  });
  await act(async () => {
    host!.querySelector<HTMLButtonElement>("button.ps-disclosure")!.click();
  });
}

function toggle(): HTMLButtonElement[] {
  const label = tr("agents.skip_permissions");
  const rows = Array.from(host!.querySelectorAll<HTMLElement>(".ps-body *")).filter(
    (el) => el.children.length > 0 && el.textContent?.startsWith(label) && el.querySelector(".choice-seg"),
  );
  return Array.from(rows[rows.length - 1].querySelectorAll<HTMLButtonElement>(".choice-seg button"));
}

const note = () => host!.textContent?.includes(tr("agents.skip_permissions_off_note"));

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("Tool permissions toggle", () => {
  it("shows Off and no approvals note while prompts are skipped (skipPermissions=true)", async () => {
    await mount(true);
    const [on, off] = toggle();
    expect(on.classList.contains("active")).toBe(false);
    expect(off.classList.contains("active")).toBe(true);
    expect(note()).toBe(false);
  });

  it("shows On with the approvals note when asking (skipPermissions=false)", async () => {
    await mount(false);
    const [on, off] = toggle();
    expect(on.classList.contains("active")).toBe(true);
    expect(off.classList.contains("active")).toBe(false);
    expect(note()).toBe(true);
  });

  it("turning On stores skipPermissions=false, turning Off stores true", async () => {
    await mount(true);
    await act(async () => toggle()[0].click());
    expect(getSettings().agentLaunchDefaults.claude.skipPermissions).toBe(false);
    await act(async () => toggle()[1].click());
    expect(getSettings().agentLaunchDefaults.claude.skipPermissions).toBe(true);
  });
});
