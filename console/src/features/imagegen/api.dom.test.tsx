// Every successful studio create announces itself on the bus, whichever path made it (the pane's
// "+ New studio", "Start an image studio" on a repo row via attach.ts): the other panes' pickers
// re-read their lists from that.
import { describe, expect, it, vi } from "vitest";

let answer: unknown = { id: "st1" };
vi.mock("../../core/api/client.ts", () => ({
  api: async () => ({}),
  raw: async () => new Response("{}"),
  apiJSON: async () => answer,
}));

const { createStudio } = await import("./api.ts");
const { onStudiosChanged } = await import("./studioBus.ts");

describe("createStudio", () => {
  it("作れたら studiosChanged を 1 回、断られたら鳴らさない", async () => {
    let n = 0;
    const off = onStudiosChanged(() => n++);
    await createStudio({ draft: {} });
    expect(n).toBe(1);
    answer = { error: { code: "unavailable" } };
    await createStudio({ draft: {} });
    expect(n).toBe(1);
    off();
  });
});
