// The auto-started boot shows as a Home row and not as the modal; a manual start keeps the modal.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { AutoStartRow } from "./AutoStartRow.tsx";
import { WsStartingDialog } from "../../app/WsStartingDialog.tsx";
import { useSigninStartStore } from "./signinStart.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";

let root: Root;
let host: HTMLElement;
const mount = () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root.render(<><AutoStartRow /><WsStartingDialog /></>));
};

beforeEach(() => {
  useSigninStartStore.setState({ auto: false, blocked: null });
  useWorkspaceStore.setState({ state: "starting", bootPhase: "boot-install (pinned): claude-code@1" });
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("AutoStartRow", () => {
  it("shows the boot phase on Home and keeps the modal away for an auto start", () => {
    useSigninStartStore.setState({ auto: true });
    mount();
    expect(host.querySelector(".home-autostart")).not.toBeNull();
    expect(host.textContent).toContain("boot-install (pinned)");
    expect(document.querySelector(".ws-starting")).toBeNull();
  });

  it("leaves a start the member began to the modal", () => {
    mount();
    expect(host.querySelector(".home-autostart")).toBeNull();
    expect(document.querySelector(".ws-starting")).not.toBeNull();
  });

  it("explains a reached workspace limit while the workspace stays stopped", () => {
    useWorkspaceStore.setState({ state: "stopped", bootPhase: "" });
    useSigninStartStore.setState({ blocked: { reason: "quota_workspaces" } });
    mount();
    expect(host.querySelector(".home-autostart-blocked")).not.toBeNull();
  });
});
