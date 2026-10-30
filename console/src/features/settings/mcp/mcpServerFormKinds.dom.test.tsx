// The "applies to" checkboxes of the MCP server form read the kind catalog. Without one they are
// the static list; a catalog that lands while the form is open re-renders it.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ServerForm } from "./mcpServerForm.tsx";
import { MCP_KINDS, emptyForm } from "./mcpWire.ts";
import { agentOf } from "../../../agents/registry.ts";
import { parseKindCatalog, setKindCatalog } from "../../../agents/kindCatalog.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  setKindCatalog(null);
});

function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <ServerForm
        form={emptyForm()}
        setForm={() => {}}
        onSave={async () => true}
        onTest={async () => {}}
        egress={null}
        onProposed={() => {}}
        submitLabel="save"
      />,
    ),
  );
}

// The form has other checkboxes (targets, enabled); the kinds are the ones labelled with an agent.
const kindBoxes = () =>
  [...host!.querySelectorAll(".mcp-check")].map((l) => l.textContent).filter((t) => MCP_KINDS.some((k) => agentOf(k).label === t));

describe("ServerForm kind checkboxes", () => {
  it("offers the static list while no catalog has arrived", () => {
    mount();
    expect(kindBoxes()).toEqual(MCP_KINDS.map((k) => agentOf(k).label));
  });

  it("re-renders when a catalog arrives and drops a kind that no longer takes MCP servers", () => {
    mount();
    act(() => setKindCatalog(parseKindCatalog({ kinds: [{ kind: "codex", managedOnly: false, managedDefault: true, mcpClient: "no" }] })));
    expect(kindBoxes()).toEqual(MCP_KINDS.filter((k) => k !== "codex").map((k) => agentOf(k).label));
  });
});
