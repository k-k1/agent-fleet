// The assistant form's backend picker offers the backends the live catalogue offers (ADR 0138
// D10a): a new assistant defaults to the first offered one, and an edited one keeps its own.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let kinds = [
  { kind: "claude", chat: false },
  { kind: "codex", chat: true },
  { kind: "opencode", chat: true },
];
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  api: async (path: string) =>
    path.includes("agents/kinds") ? { kinds, access: { policyConfirmed: true } } : { error: { code: "unrouted" } },
}));

const { AssistantModal } = await import("./AssistantModal.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { agentOf } = await import("../../agents/registry.ts");

let root: Root;
let host: HTMLDivElement;
const render = async (initial?: { agent: string }) => {
  await act(async () => root.render(<AssistantModal initial={initial as never} onClose={() => {}} onSave={async () => {}} />));
  for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve());
};
const picks = () =>
  [...document.querySelectorAll<HTMLElement>(".ui-seg .seg-btn")]
    .filter((b) => Object.values(["claude", "codex", "opencode"]).some((k) => b.textContent === agentOf(k as never).assistantName))
    .map((b) => ({ text: b.textContent, active: b.classList.contains("seg-btn--active") }));

const saveBtn = () => document.querySelector<HTMLButtonElement>("button[type=submit]")!;
const typeName = async () => {
  const input = document.querySelector<HTMLInputElement>("input")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "A");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

beforeEach(() => {
  kinds = [
    { kind: "claude", chat: false },
    { kind: "codex", chat: true },
    { kind: "opencode", chat: true },
  ];
  useWorkspaceStore.setState({ state: "running" } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("AssistantModal backend picker", () => {
  it("offers only the offered backends and defaults a new assistant to the first of them", async () => {
    await render();
    expect(picks()).toEqual([
      { text: agentOf("codex").assistantName, active: true },
      { text: agentOf("opencode").assistantName, active: false },
    ]);
  });

  it("keeps an edited assistant's own backend listed and selected even when it is no longer offered", async () => {
    await render({ agent: "claude" });
    expect(picks()).toEqual([
      { text: agentOf("claude").assistantName, active: true },
      { text: agentOf("codex").assistantName, active: false },
      { text: agentOf("opencode").assistantName, active: false },
    ]);
  });

  it("saves a new assistant on an offered backend", async () => {
    await render();
    await typeName();
    expect(saveBtn().disabled).toBe(false);
  });

  it("does not save a new assistant when no backend is offered", async () => {
    kinds = [{ kind: "claude", chat: false }, { kind: "codex", chat: false }, { kind: "opencode", chat: false }];
    await render();
    await typeName();
    expect(saveBtn().disabled).toBe(true);
  });

  it("still saves an edited assistant on its own, no longer offered, backend", async () => {
    await render({ agent: "claude", name: "A" } as never);
    expect(saveBtn().disabled).toBe(false);
  });
});
