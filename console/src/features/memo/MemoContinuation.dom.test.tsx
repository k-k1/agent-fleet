// A follow-up that was held (ADR 0137, addendum 2026-11-20): the approval card says what an
// approval of a note with a work item also covers, and says on the held row why it waits.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo } from "../../types/memo.ts";

vi.mock("./api.ts", () => ({
  memoPasteImage: async () => ({ status: 500 }),
}));

const { MemoReleaseCard } = await import("./MemoReleaseCard.tsx");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;
beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

const held: Memo = {
  id: "h1",
  repo: "",
  category: "impl",
  kind: "text",
  body: "part 6",
  refPath: "",
  position: 0,
  createdAt: "2026-11-20T00:00:00Z",
  sentAt: "",
  state: "held",
  provenance: "workspace",
  producer: "run-1",
  reference: { provider: "github", key: "acme/app#5", url: "https://example.test/acme/app#5" },
  heldReason: { after: "p5", reason: "continuation_limit" },
} as Memo;

async function renderCard(continuationMax: number | undefined) {
  await act(async () => {
    root!.render(
      <MemoReleaseCard
        card={{ key: "k", provenance: "workspace", producer: "run-1", items: [held] } as never}
        continuationMax={continuationMax}
        busy={false}
        changedIds={new Set()}
        edited={new Set()}
        awaiting={new Set()}
        onRelease={() => {}}
        onConfirm={() => {}}
      />,
    );
  });
}

describe("the approval card of a held follow-up", () => {
  it("names the reason on the item and what an approval covers", async () => {
    await renderCard(4);
    expect(host.textContent).toContain(t("memo.continuation_held", { why: t("memo.cont_r_limit") }));
    expect(host.textContent).toContain(t("memo.release_continuation_hint", { max: 4 }));
  });
  it("says nothing about follow-ups when the category allows none", async () => {
    await renderCard(0);
    expect(host.textContent).not.toContain(t("memo.release_continuation_hint", { max: 0 }));
  });
});
