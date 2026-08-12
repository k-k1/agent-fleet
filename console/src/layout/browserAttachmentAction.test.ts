import { describe, expect, it, vi } from "vitest";
import { activeView, allPanes, closePane, freshLayout, openActive, openInNew, selectView } from "./ops.ts";
import type { Layout, OpenTarget } from "./types.ts";
import {
  browserAttachmentIdFromLink,
  browserAttachmentIdFromPath,
  canonicalWorkspaceURL,
  planBrowserAttachmentOpen,
  replaceActiveWithBrowserAttachment,
  runBrowserAttachmentAction,
} from "./browserAttachmentAction.ts";

const file = (filePath: string): OpenTarget => ({ content: { kind: "file", filePath } });

function fullDesktop(): Layout {
  let layout = openActive(freshLayout(), file("source.md"));
  for (let i = 1; i < 8; i++) layout = openInNew(layout, file(`${i}.md`), { force: true });
  return layout;
}

describe("Chromium attachment action route", () => {
  it("accepts only a bounded opaque id at the action path", () => {
    expect(browserAttachmentIdFromPath("/open/browser-attachment/ba_abc-123")).toBe("ba_abc-123");
    expect(browserAttachmentIdFromPath("/prefix/open/browser-attachment/ba_x/")).toBe("ba_x");
    expect(browserAttachmentIdFromPath("/open/browser-attachment/%2Fetc")).toBeNull();
    expect(browserAttachmentIdFromPath("/open/browser-attachment/")).toBeNull();
    expect(canonicalWorkspaceURL("https://fleet.invalid/agent-fleet/")).toBe("/agent-fleet/");
  });

  // The same link arrives as a Markdown href in the mirror, where anything not
  // recognised here is resolved as a repository file path instead.
  it("recognises the action link relative or absolute, and only on our origin", () => {
    const base = "https://fleet.invalid/agent-fleet/";
    expect(browserAttachmentIdFromLink("/open/browser-attachment/ba_x", base)).toBe("ba_x");
    expect(browserAttachmentIdFromLink("open/browser-attachment/ba_x", base)).toBe("ba_x");
    expect(browserAttachmentIdFromLink("https://fleet.invalid/open/browser-attachment/ba_x", base)).toBe("ba_x");
    expect(browserAttachmentIdFromLink("https://evil.invalid/open/browser-attachment/ba_x", base)).toBeNull();
    expect(browserAttachmentIdFromLink("javascript:alert(1)//open/browser-attachment/ba_x", base)).toBeNull();
    expect(browserAttachmentIdFromLink("/docs/53.md", base)).toBeNull();
    expect(browserAttachmentIdFromLink("", base)).toBeNull();
  });

  it("focuses a duplicate, then reuses a blank without growing", () => {
    let layout = openActive(freshLayout(), file("source.md"));
    layout = openInNew(layout, { content: { kind: "browserAttach", attachmentId: "ba_same" } });
    layout = selectView(layout, allPanes(layout)[0].id);
    const duplicate = planBrowserAttachmentOpen(layout, "ba_same", false);
    expect(duplicate.kind).toBe("commit");
    if (duplicate.kind === "commit") expect(activeView(duplicate.layout)?.id).toBe(allPanes(layout)[1].id);

    layout = closePane(layout, allPanes(layout)[1].id);
    const blank = planBrowserAttachmentOpen(layout, "ba_new", false);
    expect(blank.kind).toBe("commit");
    if (blank.kind === "commit") {
      expect(allPanes(blank.layout)).toHaveLength(2);
      expect(allPanes(blank.layout)[1].content).toEqual({ kind: "browserAttach", attachmentId: "ba_new" });
    }
  });

  it("uses right columns then down splits, preserving openInNew ordering", () => {
    let layout = openActive(freshLayout(), file("source.md"));
    for (let i = 1; i <= 7; i++) {
      const plan = planBrowserAttachmentOpen(layout, `ba_${i}`, false);
      expect(plan.kind).toBe("commit");
      if (plan.kind === "commit") layout = plan.layout;
    }
    expect(layout.cols).toHaveLength(4);
    expect(layout.cols.every((column) => column.cells.length === 2)).toBe(true);
  });

  it("asks at the desktop/mobile cap and replaces only the active pane after consent", () => {
    const desktop = fullDesktop();
    expect(planBrowserAttachmentOpen(desktop, "ba_overflow", false)).toEqual({ kind: "confirm-replace" });
    const activeId = activeView(desktop)!.id;
    const activeCellId = desktop.activeCellId;
    const replaced = replaceActiveWithBrowserAttachment(desktop, "ba_overflow");
    expect(replaced.activeCellId).toBe(activeCellId);
    expect(allPanes(replaced).find((pane) => pane.id === activeId)?.content).toEqual({
      kind: "browserAttach",
      attachmentId: "ba_overflow",
    });

    let mobile = openActive(freshLayout(), file("source.md"));
    mobile = openInNew(mobile, file("other.md"), { mobile: true });
    expect(planBrowserAttachmentOpen(mobile, "ba_mobile", true)).toEqual({ kind: "confirm-replace" });
  });

  it("verifies status before one commit and replaces the action URL only after success", async () => {
    let layout = freshLayout();
    const order: string[] = [];
    const opened = await runBrowserAttachmentAction({
      attachmentId: "ba_action",
      mobile: false,
      async getStatus() { order.push("get"); return { id: "ba_action", expiresAt: "2099-01-01T00:00:00Z" }; },
      getLayout: () => layout,
      async commit(next) { order.push("commit"); layout = next; return true; },
      async confirmReplace() { order.push("confirm"); return true; },
      replaceURL() { order.push("replace"); },
    });
    expect(opened).toBe(true);
    expect(order).toEqual(["get", "commit", "replace"]);
    expect(allPanes(layout)[0].content).toEqual({ kind: "browserAttach", attachmentId: "ba_action" });
  });

  it("does not commit or replace history when the cap dialog is cancelled", async () => {
    const layout = fullDesktop();
    const commit = vi.fn(async () => true);
    const replaceURL = vi.fn();
    expect(await runBrowserAttachmentAction({
      attachmentId: "ba_cancel",
      mobile: false,
      async getStatus() { return { id: "ba_cancel", expiresAt: "2099-01-01T00:00:00Z" }; },
      getLayout: () => layout,
      commit,
      async confirmReplace() { return false; },
      replaceURL,
    })).toBe(false);
    expect(commit).not.toHaveBeenCalled();
    expect(replaceURL).not.toHaveBeenCalled();
  });
});
