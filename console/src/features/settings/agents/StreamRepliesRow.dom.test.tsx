// Settings > Agents > Claude > "Stream replies in the chat view" (#1250). It is on for a kind
// nobody has touched, and switching it writes an explicit per-kind value that the mirror reads
// (settings.streamReplies) without touching any other kind.
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
  it("shows on for an untouched kind", () => {
    render();
    expect(button(t("common.on")).classList.contains("active")).toBe(true);
    expect(streamReplies(getSettings(), "claude")).toBe(true);
  });

  it("switches the kind off and on again, leaving other kinds alone", () => {
    render();
    act(() => button(t("common.off")).click());
    expect(getSettings().streamReplies).toEqual({ codex: false, claude: false });
    expect(streamReplies(getSettings(), "claude")).toBe(false);
    act(() => button(t("common.on")).click());
    expect(getSettings().streamReplies).toEqual({ codex: false, claude: true });
  });
});
