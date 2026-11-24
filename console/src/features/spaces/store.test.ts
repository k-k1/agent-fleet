import { beforeEach, describe, expect, it, vi } from "vitest";

let tenant = "t1";
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => tenant,
  getUser: () => "u",
  api: vi.fn(),
  apiJSON: async () => ({}),
  isTransientErr: () => false,
}));
const { useSpacesStore } = await import("./store.ts");

const list = (...folders: string[]) => ({ spaces: folders.map((folder) => ({ folder, name: "", dir: "/s/" + folder })) });
const folders = () => useSpacesStore.getState().spaces.map((s) => s.folder);

beforeEach(() => {
  tenant = "t1";
  useSpacesStore.getState().clear();
});

describe("adopting a request somebody else made", () => {
  it("takes the answer of the latest request for this workspace", () => {
    const t = useSpacesStore.getState().begin();
    useSpacesStore.getState().adopt(t, list("a"));
    expect(folders()).toEqual(["a"]);
    expect(useSpacesStore.getState().owner).toBe("t1\nu");
  });

  it("ignores an answer for a workspace the member has left since asking", () => {
    const t = useSpacesStore.getState().begin();
    tenant = "t2";
    useSpacesStore.getState().clear();
    useSpacesStore.getState().adopt(t, list("old-tenant-space"));
    expect(folders()).toEqual([]);
  });

  it("does not let an older answer overwrite a newer one, whatever the order they arrive in", () => {
    const first = useSpacesStore.getState().begin();
    const second = useSpacesStore.getState().begin();
    useSpacesStore.getState().adopt(second, list("new"));
    useSpacesStore.getState().adopt(first, list("old"));
    expect(folders()).toEqual(["new"]);
  });

  it("ignores anything that is not a list", () => {
    const t = useSpacesStore.getState().begin();
    useSpacesStore.getState().adopt(t, { error: { code: "http_500" } });
    useSpacesStore.getState().adopt(useSpacesStore.getState().begin(), null);
    expect(folders()).toEqual([]);
    expect(useSpacesStore.getState().owner).toBe("");
  });
});
