// The mirror header's idle chip, driven through the whole MirrorView against a stubbed poll:
// the precedence between watches and a pending handoff lives in MirrorView's wiring (which
// fields it hands stateInfo), so the pure stateInfo tests alone cannot catch a dropped field.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { MirrorView } from "./MirrorView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import type { Session } from "../../types/session.ts";
import { t, tCount } from "../../lib/i18n/index.ts";

const SESSION = "chip-session";
const WATCHES = [
  { id: "w1", source: "github", ref: "o/r#1", types: ["ci"] },
  { id: "w2", source: "github", ref: "o/r#2", types: ["ci"] },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(meta: Partial<Session>, watches: unknown[]) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const body = String(input).includes("/messages")
      ? { cursor: 1, alive: true, status: "idle", messages: [], watches }
      : {};
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const sessionMeta = { name: SESSION, kind: "claude", driver: "tui", alive: true, ...meta } as unknown as Session;
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <MirrorView paneId="p1" session={SESSION} sessionMeta={sessionMeta} active mirror onToggleMirror={() => {}} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const chip = () => document.querySelector(".session-state")?.textContent ?? "";

beforeEach(() => {
  localStorage.clear();
  useWorkspaceStore.setState({ state: "running" });
});
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
});

describe("mirror header idle chip with watches", () => {
  it("counts the polled watches", async () => {
    await mount({}, WATCHES);
    expect(chip()).toContain(tCount("state.idle_watches", 2));
  });

  it("is the plain idle chip with no watches", async () => {
    await mount({}, []);
    expect(chip()).toContain(t("state.idle"));
    expect(chip()).not.toContain(tCount("state.idle_watches", 2));
  });

  it("a pending handoff keeps its chip over the watches", async () => {
    await mount({ handoffPending: true }, WATCHES);
    expect(chip()).toContain(t("state.idle_handoff"));
  });
});
