// Settings > Agents > Claude > "Stream replies in the chat view" (#1250, #1274). It is typewriter
// for a kind nobody has touched, a stored boolean from the on/off days reads as off / typewriter,
// and choosing writes an explicit per-kind mode that the mirror reads (settings.streamReplies)
// without touching any other kind.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../../lib/i18n/index.ts";

vi.mock("../../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: vi.fn(async () => ({})),
  apiJSON: vi.fn(),
  raw: vi.fn(async () => new Response("")),
  isTransientErr: () => false,
}));

const { StreamRepliesRow } = await import("./AgentCardParts.tsx");
const { getSettings, setSetting, streamReplies } = await import("../../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement;

function render() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<StreamRepliesRow kind="claude" />));
}

const button = (label: string) =>
  [...host.querySelectorAll<HTMLButtonElement>(".seg-btn")].find((b) => b.textContent === label)!;

beforeEach(() => {
  localStorage.clear();
  setSetting("streamReplies", { codex: false });
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

describe("StreamRepliesRow", () => {
  it("shows typewriter for an untouched kind", () => {
    render();
    expect(button(t("agents.stream_replies_typewriter")).classList.contains("active")).toBe(true);
    expect(button(t("common.off")).classList.contains("active")).toBe(false);
    expect(streamReplies(getSettings(), "claude")).toBe("typewriter");
  });

  it("shows a stored boolean as off / typewriter", () => {
    setSetting("streamReplies", { claude: false });
    render();
    expect(button(t("common.off")).classList.contains("active")).toBe(true);
    act(() => root?.unmount());
    setSetting("streamReplies", { claude: true });
    render();
    expect(button(t("agents.stream_replies_typewriter")).classList.contains("active")).toBe(true);
  });

  it("writes the chosen mode for the kind alone", () => {
    render();
    act(() => button(t("agents.stream_replies_lines")).click());
    expect(getSettings().streamReplies).toEqual({ codex: false, claude: "lines" });
    expect(streamReplies(getSettings(), "claude")).toBe("lines");
    expect(button(t("agents.stream_replies_lines")).classList.contains("active")).toBe(true);
    act(() => button(t("common.off")).click());
    expect(getSettings().streamReplies).toEqual({ codex: false, claude: "off" });
    expect(streamReplies(getSettings(), "claude")).toBe("off");
    act(() => button(t("agents.stream_replies_typewriter")).click());
    expect(getSettings().streamReplies).toEqual({ codex: false, claude: "typewriter" });
  });
});
