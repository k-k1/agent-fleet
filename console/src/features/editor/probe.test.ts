import { describe, expect, it, vi } from "vitest";

// probe.ts pulls in the api client (localStorage at module scope) and the
// workspace store through its hook half; the pure gate under test needs neither.
vi.mock("./api.ts", () => ({ probeFileMeta: vi.fn() }));
vi.mock("../../core/store/workspace.ts", () => ({
  useWorkspaceStore: { getState: () => ({ state: "running" }) },
  wsRunning: (state: string) => state === "running",
}));

const { shouldProbe } = await import("./probe.ts");
type ExternalProbeGates = import("./probe.ts").ExternalProbeGates;

const open = (): ExternalProbeGates => ({
  path: "repos/a.txt",
  documentVisible: true,
  paneVisible: true,
  workspaceRunning: true,
  saving: false,
});

describe("external probe gates (docs/log/44 §7.2)", () => {
  it("probes only when every condition holds", () => {
    expect(shouldProbe(open())).toBe(true);
  });

  it("stays quiet when any single gate closes", () => {
    expect(shouldProbe({ ...open(), path: null })).toBe(false);
    expect(shouldProbe({ ...open(), documentVisible: false })).toBe(false);
    expect(shouldProbe({ ...open(), paneVisible: false })).toBe(false);
    expect(shouldProbe({ ...open(), workspaceRunning: false })).toBe(false);
    expect(shouldProbe({ ...open(), saving: true })).toBe(false);
  });
});
