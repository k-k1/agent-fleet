// Prompts are written for a model (ADR 0100 revision 9): with none chosen, no agent is attached
// and a bound agent's composer is held shut with the reason in its place.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";

const mirrorProps: { composerBlock?: ReactNode }[] = [];
vi.mock("../mirror/MirrorView.tsx", () => ({
  MirrorView: (p: { composerBlock?: ReactNode }) => {
    mirrorProps.push(p);
    return <div className="mirror-stub">{p.composerBlock}</div>;
  },
}));

import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { StudioAgent } from "./parts/StudioAgent.tsx";
import type { Session } from "../../types/session.ts";

let host: HTMLDivElement;
let root: Root;

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  mirrorProps.length = 0;
});

async function mount(session: string, needsModel: boolean) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () =>
    root.render(
      <ToastProvider>
        <StudioAgent
          paneId="p0"
          studioId="st1"
          session={session}
          active
          log={[]}
          onRewind={() => undefined}
          needsModel={needsModel}
          onAttach={() => undefined}
          onReplace={() => undefined}
        />
      </ToastProvider>,
    ),
  );
}

describe("the studio's agent column without a model", () => {
  it("does not offer to attach an agent", async () => {
    await mount("", true);
    const btn = [...host.querySelectorAll("button")].find((b) => /付ける|Attach/.test(b.textContent || ""));
    expect(btn?.disabled).toBe(true);
    await act(async () => root.unmount());
    host.remove();
    await mount("", false);
    const on = [...host.querySelectorAll("button")].find((b) => /付ける|Attach/.test(b.textContent || ""));
    expect(on?.disabled).toBe(false);
  });

  it("holds a bound agent's composer shut, and lets it go once a model is chosen", async () => {
    useSessionsStore.setState({ loaded: true, sessions: [{ name: "s1", kind: "claude", alive: true } as Session] });
    await mount("s1", true);
    expect(host.querySelector(".igen-needs-model")).not.toBeNull();
    await act(async () => root.unmount());
    host.remove();
    await mount("s1", false);
    expect(host.querySelector(".igen-needs-model")).toBeNull();
    expect(mirrorProps.at(-1)?.composerBlock).toBeUndefined();
  });
});
