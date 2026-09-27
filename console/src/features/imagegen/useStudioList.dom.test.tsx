// A studio created, renamed or deleted in one pane reaches every other pane's picker.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let list: { id: string; title: string; updated_at: string }[] = [];
// When set, listStudios answers through these instead of at once (the race test).
let held: ((v: unknown) => void)[] | null = null;
vi.mock("./api.ts", () => ({
  listStudios: () => {
    const snapshot = { studios: list };
    if (held) return new Promise((r) => held!.push(() => r(snapshot)));
    return Promise.resolve(snapshot);
  },
  // The real wrapper announces a create (pinned in api.dom.test.tsx); the mock keeps that.
  createStudio: async () => {
    const s = { id: `st${list.length + 1}`, title: "", updated_at: "" };
    list = [s, ...list];
    (await import("./studioBus.ts")).studiosChanged();
    return s;
  },
}));

const { useStudioList } = await import("./useStudioList.ts");
const { newStudio } = await import("./open.ts");
const { emptyDraft } = await import("./draft.ts");
const { studiosChanged } = await import("./studioBus.ts");

const seen: Record<string, string[]> = {};
function Picker({ name }: { name: string }) {
  const [studios, read] = useStudioList();
  if (!seen[name]) void read();
  seen[name] = studios.map((s) => s.id);
  return null;
}

let root: Root;
afterEach(async () => {
  await act(async () => root.unmount());
});

describe("スタジオ一覧の共有", () => {
  it("別のペインで作ったスタジオが、もう一方のペインの一覧にも出る", async () => {
    list = [{ id: "st0", title: "a", updated_at: "" }];
    root = createRoot(document.createElement("div"));
    await act(async () =>
      root.render(
        <>
          <Picker name="A" />
          <Picker name="B" />
        </>,
      ),
    );
    await act(async () => new Promise((r) => setTimeout(r, 10)));
    expect(seen.B).toEqual(["st0"]);
    await act(async () => {
      await newStudio(emptyDraft());
      await new Promise((r) => setTimeout(r, 10));
    });
    expect(seen.A).toEqual(["st2", "st0"]);
    expect(seen.B).toEqual(["st2", "st0"]);
    // A rename or delete elsewhere announces itself the same way.
    list = [{ id: "st0", title: "renamed", updated_at: "" }];
    await act(async () => {
      studiosChanged();
      await new Promise((r) => setTimeout(r, 10));
    });
    expect(seen.B).toEqual(["st0"]);
  });
});

describe("一覧の取得競合", () => {
  it("遅れて返った古い一覧は、新しい一覧を上書きしない", async () => {
    list = [{ id: "old", title: "", updated_at: "" }];
    held = [];
    root = createRoot(document.createElement("div"));
    await act(async () => root.render(<Picker name="R" />));
    list = [{ id: "new", title: "", updated_at: "" }];
    await act(async () => studiosChanged());
    const [first, second] = held;
    await act(async () => {
      second(undefined);
      await new Promise((r) => setTimeout(r, 5));
    });
    expect(seen.R).toEqual(["new"]);
    await act(async () => {
      first(undefined);
      await new Promise((r) => setTimeout(r, 5));
    });
    expect(seen.R).toEqual(["new"]);
    held = null;
  });
});
