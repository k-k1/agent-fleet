// The enable/disable control: the Agent accepts it for user and tenant rows and refuses it
// for a built-in (mcpreg.SetEnabled), so a built-in row must not offer a button that can only fail.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { ServerRow } from "./mcpServerRow.tsx";
import type { McpServer } from "./mcpWire.ts";
import { setLocale } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const server = (over: Partial<McpServer>): McpServer => ({
  id: "x", name: "x", origin: "user", transport: "stdio", command: "x",
  enabled: true, targets: { assistant: true, session: true }, editable: false, ready: true, ...over,
});

async function mount(s: McpServer) {
  act(() => root?.unmount());
  host?.remove();
  setLocale("en");
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <ServerRow
        s={s} egress={null} onProposed={() => {}} onEdit={() => {}} onTest={() => {}}
        onToggle={() => {}} onDelete={() => {}} onEnterSecrets={() => {}}
      />,
    ),
  );
}
const labels = () => Array.from(host!.querySelectorAll("button")).map((b) => (b.textContent || "").trim());

describe("enable/disable control by origin", () => {
  it("a built-in row has no Disable or Enable button", async () => {
    await mount(server({ id: "pagerduty", name: "pagerduty", origin: "builtin" }));
    expect(labels()).not.toContain("Disable");
    await mount(server({ id: "pagerduty", name: "pagerduty", origin: "builtin", enabled: false }));
    expect(labels()).not.toContain("Enable");
  });

  it("tenant and user rows keep it", async () => {
    await mount(server({ origin: "tenant" }));
    expect(labels()).toContain("Disable");
    await mount(server({ origin: "user", editable: true, enabled: false }));
    expect(labels()).toContain("Enable");
  });
});
