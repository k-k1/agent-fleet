import { beforeEach, describe, expect, it, vi } from "vitest";

const aws = vi.fn<() => Promise<unknown[] | null>>();
const gcp = vi.fn<() => Promise<boolean>>();
let wsState = "running";

vi.mock("../features/awslogin/store.ts", () => ({ useAwsLoginStore: { getState: () => ({ refreshExpiry: aws }) } }));
vi.mock("../features/gcplogin/store.ts", () => ({ useGcpLoginStore: { getState: () => ({ refreshProfiles: gcp }) } }));
vi.mock("../core/store/workspace.ts", () => ({ useWorkspaceStore: { getState: () => ({ state: wsState }) } }));

const { reloadCloudProfiles } = await import("./cloudProfilesReload.ts");

describe("reloadCloudProfiles", () => {
  beforeEach(() => {
    aws.mockReset();
    gcp.mockReset();
    wsState = "running";
  });

  it("asks again after a first answer lost to a 502 while the workspace keeps running", async () => {
    aws.mockResolvedValueOnce(null).mockResolvedValueOnce([]);
    gcp.mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    expect(await reloadCloudProfiles()).toBe(false);
    expect(await reloadCloudProfiles()).toBe(true);
  });

  it("asks again when only one of the two answered", async () => {
    aws.mockResolvedValue([]);
    gcp.mockResolvedValue(false);
    expect(await reloadCloudProfiles()).toBe(false);
  });

  it("stops asking while the workspace is stopped", async () => {
    aws.mockResolvedValue(null);
    gcp.mockResolvedValue(false);
    wsState = "stopped";
    expect(await reloadCloudProfiles()).toBe(true);
  });
});
