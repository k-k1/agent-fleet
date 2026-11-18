// The "connect an agent" step is judged by what the credential policy lets the member launch
// (ADR 0138 D10a), and by the login alone while the catalogue has not answered.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let kindsAnswer: unknown;
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  api: async (path: string) => {
    if (path.includes("agents/kinds")) return kindsAnswer;
    if (path.includes("api/connections")) return { claude: { connected: true } };
    return { error: { code: "unrouted" } };
  },
}));

const { useSetupState } = await import("./OnboardingCard.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");

let root: Root;
let host: HTMLDivElement;
let agentOk: boolean | undefined;
function Probe() {
  agentOk = useSetupState().g.agentOk;
  return null;
}
const mount = async () => {
  await act(async () => root.render(<Probe />));
  for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve());
};

beforeEach(() => {
  agentOk = undefined;
  useWorkspaceStore.setState({ state: "running" } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("OnboardingCard agentOk", () => {
  it("is true for a connected kind the catalogue lists", async () => {
    kindsAnswer = { kinds: [{ kind: "claude" }], access: { policyConfirmed: true } };
    await mount();
    expect(agentOk).toBe(true);
  });

  it("is false when the catalogue no longer lists the only connected kind", async () => {
    kindsAnswer = { kinds: [{ kind: "codex" }], access: { policyConfirmed: true } };
    await mount();
    expect(agentOk).toBe(false);
  });

  it("falls back to the login check when the catalogue cannot be vouched for", async () => {
    kindsAnswer = { error: { code: "down" } };
    await mount();
    expect(agentOk).toBe(true);
  });
});
