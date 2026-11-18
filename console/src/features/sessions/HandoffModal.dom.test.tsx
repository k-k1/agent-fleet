// The handoff target picker offers what the launch dialog offers: connected kinds that the live,
// policy-filtered catalogue lists (ADR 0138 D10a). While the catalogue is out it says "checking".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let kindsAnswer: () => Promise<unknown>;
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  api: async (path: string) => (path.includes("agents/kinds") ? kindsAnswer() : { error: { code: "unrouted" } }),
}));

const { HandoffModal } = await import("./HandoffModal.tsx");
const { setCachedConns } = await import("../repos/connsCache.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { t } = await import("../../lib/i18n/index.ts");
type Session = import("../../types/session.ts").Session;

let root: Root;
let host: HTMLDivElement;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve());
};
const render = async () => {
  await act(async () =>
    root.render(
      <HandoffModal
        session={{ name: "s1", kind: "claude", alive: true, state: "idle", title: "t" } as Session}
        actions={{} as never}
        onClose={() => {}}
      />,
    ),
  );
  await flush();
};
const labels = () => [...document.querySelectorAll(".ui-seg.big .seg-btn")].map((b) => b.textContent);

beforeEach(() => {
  useWorkspaceStore.setState({ state: "running" } as never);
  setCachedConns({ claude: { connected: true }, codex: { connected: true }, opencode: { connected: true } } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("HandoffModal target kinds", () => {
  it("leaves out a connected kind the catalogue does not list", async () => {
    kindsAnswer = async () => ({ kinds: [{ kind: "claude" }, { kind: "opencode" }], access: { policyConfirmed: true } });
    await render();
    expect(labels()).toEqual(["Claude Code", "OpenCode"]);
  });

  it("says it is checking while the catalogue is out, instead of offering anything", async () => {
    kindsAnswer = () => new Promise(() => {});
    await render();
    expect(labels()).toEqual([]);
    expect(document.body.textContent).toContain(t("launch.agents_checking"));
  });
});
