// A rejected propose request (network cut) shows a failure in the form, gives the button back and
// does not report the request as made; a refusal body behaves the same.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setLocale } from "../../../lib/i18n/index.ts";

const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: async () => ({}),
  apiJSON: (...a: unknown[]) => apiJSON(...a),
}));
import { EgressNote } from "./EgressNote.tsx";

let host: HTMLDivElement, root: Root;
const onProposed = vi.fn();
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  setLocale("en");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); setLocale("ja"); });

async function openForm() {
  const check = { configured: true, mode: "enforce", enforce: true, hosts: { "mcp.example.com": { host: "mcp.example.com", allowed: false, proposed: false } } };
  await act(async () => { root.render(<EgressNote url="https://mcp.example.com/mcp" check={check as never} onProposed={onProposed} />); });
  const ask = host.querySelector<HTMLButtonElement>(".egress-mcp-note button")!;
  await act(async () => ask.click());
}
const send = () => host.querySelector<HTMLButtonElement>(".egress-mcp-ask button")!;

describe("EgressNote propose", () => {
  it("shows the failure, re-enables the button and does not call onProposed when the request rejects", async () => {
    apiJSON.mockRejectedValueOnce(new Error("network down"));
    await openForm();
    await act(async () => send().click());
    expect(host.textContent).toContain("network down");
    expect(send().disabled).toBe(false);
    expect(onProposed).not.toHaveBeenCalled();
  });
  it("calls onProposed after a success", async () => {
    apiJSON.mockResolvedValueOnce({});
    await openForm();
    await act(async () => send().click());
    expect(onProposed).toHaveBeenCalledTimes(1);
  });
});
