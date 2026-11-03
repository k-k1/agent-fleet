// The idle compaction row mirrors Claude Code's own default (absent = on), so it must not be drawn
// from a settings answer that lacks the field: an older Agent would make it read "off" while the
// CLI keeps compacting.
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

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(claude: Record<string, unknown>, updateClaude: (patch: unknown) => void = () => {}) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ClaudeCard running st={{ connected: true }} reload={() => {}} claude={claude} updateClaude={updateClaude} />,
    );
  });
  await act(async () => {
    host!.querySelector<HTMLButtonElement>("button.ps-disclosure")!.click();
  });
}

// The OnOff buttons of the row whose label is the idle compaction label, or null.
function idleRow(): HTMLButtonElement[] | null {
  const label = tr("agents.idle_compaction");
  const rows = Array.from(host!.querySelectorAll<HTMLElement>(".ps-body *")).filter(
    (el) => el.children.length > 0 && el.textContent?.startsWith(label) && el.querySelector(".choice-seg"),
  );
  if (rows.length === 0) return null;
  const row = rows[rows.length - 1];
  return Array.from(row.querySelectorAll<HTMLButtonElement>(".choice-seg button"));
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const base = { remoteControlAtStartup: false, agentPushNotifEnabled: false, rtk_enabled: false, rtk_available: true };

describe("ClaudeCard idle compaction", () => {
  it("shows the current state and sends the toggle", async () => {
    const update = vi.fn();
    await mount({ ...base, idleCompaction: true }, update);
    const btns = idleRow();
    expect(btns).not.toBeNull();
    const [on, off] = btns!;
    expect(on.classList.contains("active")).toBe(true);
    expect(off.classList.contains("active")).toBe(false);
    await act(async () => off.click());
    expect(update).toHaveBeenCalledWith({ idleCompaction: false });
  });

  it("reflects off", async () => {
    await mount({ ...base, idleCompaction: false });
    const [on, off] = idleRow()!;
    expect(on.classList.contains("active")).toBe(false);
    expect(off.classList.contains("active")).toBe(true);
  });

  it("hides the row when the Agent does not report the field", async () => {
    await mount(base);
    expect(idleRow()).toBeNull();
    expect(host!.textContent).not.toContain(tr("agents.note_claude_idle_compaction"));
  });
});
