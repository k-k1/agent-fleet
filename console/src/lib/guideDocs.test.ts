// openGuidePage opens the guide at the root the workspace payload names (native without a
// rootfs), else at the image's fixed path; the ja/en file choice is the same either way.
import { beforeEach, describe, expect, it, vi } from "vitest";

const openTargetInNew = vi.fn();
let storeRoot = "";
let locale = "en";
vi.mock("../layout/store.ts", () => ({ useLayoutStore: { getState: () => ({ openTargetInNew }) } }));
vi.mock("../core/store/workspace.ts", () => ({ useWorkspaceStore: { getState: () => ({ docsRoot: storeRoot }) } }));
vi.mock("./i18n/index.ts", () => ({ getLocale: () => locale }));

import { openGuidePage } from "./guideDocs.ts";

const openedPath = () => openTargetInNew.mock.calls.at(-1)?.[0].content.filePath;

describe("openGuidePage", () => {
  beforeEach(() => {
    openTargetInNew.mockClear();
    storeRoot = "";
    locale = "en";
  });

  it("uses the fixed path when the workspace names no root", () => {
    openGuidePage("README");
    expect(openedPath()).toBe("/usr/local/share/agent-fleet/docs/README.md");
  });

  it("uses the staged root the workspace reports", () => {
    storeRoot = "/data/ws/docs";
    openGuidePage("README");
    expect(openedPath()).toBe("/data/ws/docs/README.md");
    locale = "ja";
    openGuidePage("member/10-integrations");
    expect(openedPath()).toBe("/data/ws/docs/member/10-integrations.ja.md");
  });

  it("does not double the slash after a trailing-slash root", () => {
    storeRoot = "/data/ws/docs/";
    openGuidePage("README");
    expect(openedPath()).toBe("/data/ws/docs/README.md");
  });
});
