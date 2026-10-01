// The mirror's shared-file lightbox shows `preview` (ADR 0080 decision 12), the same
// screen-sized copy the gallery's does. The URL is chosen in MirrorView's wiring, so the whole
// view is mounted: the card alone (UserFileBlock.dom.test.tsx) only proves it forwards
// whatever URL it is handed.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

import { MirrorView } from "./MirrorView.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import type { Session } from "../../types/session.ts";

const SESSION = "lightbox-session";
const TURNS = [{ role: "assistant", idx: 1, parts: [{ kind: "userfile", files: ["out/shot.png"], caption: "done" }] }];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function settle(rounds = 5) {
  for (let i = 0; i < rounds; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
}

async function until(cond: () => boolean, what: string) {
  for (let i = 0; i < 50; i++) {
    if (cond()) return;
    await settle(1);
  }
  throw new Error("timed out waiting for " + what);
}

beforeEach(() => {
  localStorage.clear();
  useWorkspaceStore.setState({ state: "running" });
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const url = String(input);
    const body = url.includes(`/sessions/${SESSION}/messages`) ? { cursor: 1, alive: true, status: "idle", messages: TURNS } : {};
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
});

describe("the mirror's shared-file lightbox", () => {
  it("enlarges the screen-sized copy, not the original", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const meta = { name: SESSION, kind: "codex", driver: "managed", alive: true } as unknown as Session;
    await act(async () => {
      root!.render(
        <ToastProvider>
          <MirrorView paneId="p1" session={SESSION} sessionMeta={meta} active mirror onToggleMirror={() => {}} />
        </ToastProvider>,
      );
    });
    await until(() => !!document.querySelector(".mt-file-zoom"), "the shared image card");

    // ImageView decodes the big copy off-screen in a `new Image()` and swaps it in when it has
    // loaded; jsdom never fires that load, so this stand-in is the only place the URL shows.
    const probes: string[] = [];
    class ProbeImage {
      decoding = "";
      complete = false;
      set src(v: string) {
        probes.push(v);
      }
      addEventListener() {}
      removeEventListener() {}
    }
    vi.stubGlobal("Image", ProbeImage);
    await act(async () => document.querySelector<HTMLElement>(".mt-file-zoom")!.click());
    await settle(2);

    // On screen meanwhile: the card's own thumbnail, which the tab already has.
    expect(document.querySelector<HTMLImageElement>(".mirror-lightbox img")!.src).toContain("thumb=512");
    const big = probes.map((s) => new URL(s)).filter((u) => u.searchParams.get("path") === "out/shot.png");
    expect(big, `fetched: ${probes.join(" ")}`).toHaveLength(1);
    expect(big[0].searchParams.has("thumb")).toBe(false);
    // One of the steps the Agent warms (previewSteps in workspace/agent/fs_thumb.go).
    expect(["1024", "1536", "2048"]).toContain(big[0].searchParams.get("preview"));
  });
});
