import { beforeEach, describe, expect, it, vi } from "vitest";

// The service module pulls in the layout store and the socket plumbing; only the
// HTTP read is under test here.
const apiMock = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => apiMock(...args),
  apiJSON: vi.fn(),
  getTenant: () => "default",
  rel: (path: string) => `/${path}`,
}));

const { listBrowserAttachments } = await import("./attachmentService.ts");

const future = new Date(Date.now() + 60_000).toISOString();
const past = new Date(Date.now() - 60_000).toISOString();

beforeEach(() => {
  apiMock.mockReset();
});

describe("live Chromium attachment list", () => {
  it("reads the collection route and keeps the server's order", async () => {
    apiMock.mockResolvedValue({
      attachments: [
        { id: "ba_new", state: "attached", title: "新しい方", expiresAt: future },
        { id: "ba_old", state: "viewer-open", title: "古い方", expiresAt: future },
      ],
    });
    const list = await listBrowserAttachments();
    expect(apiMock).toHaveBeenCalledWith("api/browser/attachments");
    expect(list.map((a) => a.id)).toEqual(["ba_new", "ba_old"]);
  });

  // An expired id cannot open a pane, so offering it would be a button that only
  // ever produces an error toast.
  it("drops expired entries and survives a malformed one", async () => {
    apiMock.mockResolvedValue({
      attachments: [
        { id: "ba_expired", state: "attached", expiresAt: past },
        { notAnAttachment: true },
        { id: "ba_live", state: "attached", expiresAt: future },
      ],
    });
    expect((await listBrowserAttachments()).map((a) => a.id)).toEqual(["ba_live"]);
  });

  it("treats a missing or non-array payload as empty", async () => {
    apiMock.mockResolvedValue({});
    expect(await listBrowserAttachments()).toEqual([]);
    apiMock.mockResolvedValue({ attachments: "nope" });
    expect(await listBrowserAttachments()).toEqual([]);
  });

  it("raises the API error instead of hiding it as an empty list", async () => {
    apiMock.mockResolvedValue({ error: { code: "workspace_stopped" } });
    await expect(listBrowserAttachments()).rejects.toMatchObject({ code: "workspace_stopped" });
  });
});
