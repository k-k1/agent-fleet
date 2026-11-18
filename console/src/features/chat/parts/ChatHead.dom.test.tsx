// The assistant chat's agent menu offers the backends the live catalogue offers (ADR 0138 D10a);
// the conversation's current backend stays listed even when it is no longer offered.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ChatHead } from "./ChatHead.tsx";
import { agentOf } from "../../../agents/registry.ts";

let root: Root | null = null;
let host: HTMLDivElement;

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

const items = async (offeredKinds: Set<string> | null, current: string) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  const ref = { current: null };
  await act(async () =>
    root!.render(
      <ChatHead
        title="t"
        draftAsst={null}
        conv={{ id: "c1", agent: current } as never}
        conversationId="c1"
        agent={agentOf("claude")}
        agentKind="claude"
        agentTagRef={ref}
        agentMenuRef={ref}
        agentPickerOpen
        onToggleAgentPicker={() => {}}
        onSwitchAgent={() => {}}
        chatConns={null}
        offeredKinds={offeredKinds}
        switching={false}
        showStreaming={false}
        compacting={false}
        wsRunning
        stateChip={{ cls: "", icon: "x", spin: false, text: "" }}
        planOpen={false}
        onTogglePlan={() => {}}
      />,
    ),
  );
  return [...document.querySelectorAll<HTMLElement>(".chat-agent-menu [role=menuitemradio]")].map((b) => ({
    text: b.textContent,
    checked: b.getAttribute("aria-checked"),
  }));
};

describe("ChatHead agent menu", () => {
  it("lists only the offered backends, plus the current one", async () => {
    const list = await items(new Set(["claude", "codex"]), "cursor");
    expect(list.map((i) => i.text)).toEqual([
      agentOf("claude").assistantName,
      agentOf("codex").assistantName,
      agentOf("cursor").assistantName,
    ]);
    expect(list.find((i) => i.text === agentOf("cursor").assistantName)!.checked).toBe("true");
  });

  it("lists only the current backend while there is no catalogue answer", async () => {
    const list = await items(null, "claude");
    expect(list.map((i) => i.text)).toEqual([agentOf("claude").assistantName]);
  });
});
