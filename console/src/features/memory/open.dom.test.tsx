// openMemoryView (#3720): the one way into the Memory pane. Pinned: it opens a pane of kind
// "memory" on the asked view, a second open focuses and retargets that pane rather than adding
// one, the settings modal is closed (it would hide the pane), a memory to
// pick travels as the one-shot target, and a stored layout of this kind survives a reload.
import { beforeEach, describe, expect, it } from "vitest";
import { openMemoryView } from "./open.ts";
import { openMemoryEntry } from "./openEntry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews, freshLayout } from "../../layout/ops.ts";
import { useSettingsUI } from "../settings/store.ts";
import { paneTitle } from "../panes/paneTitle.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import { validateStoredContent } from "../../layout/migrate.ts";

const panes = () => allViews(useLayoutStore.getState().layout).filter((v) => v.content.kind === "memory");

beforeEach(() => {
  useSettingsUI.getState().closeSettings();
  useLayoutStore.setState({ layout: freshLayout() });
  setLocale("en");
});

describe("openMemoryEntry", () => {
  const entries = () => allViews(useLayoutStore.getState().layout).filter((v) => v.content.kind === "memoryEntry");

  it("opens one pane per memory, and retargets it when the memory was forgotten since", () => {
    const m = { scope: "project", project: { id: "app-0123456789ab", display: "app" }, name: "build-cmd" };
    openMemoryEntry(m);
    openMemoryEntry(m, { gone: true });
    expect(entries().map((p) => p.content)).toEqual([
      { kind: "memoryEntry", scope: "project", project: "app-0123456789ab", projectName: "app", name: "build-cmd", gone: true },
    ]);
  });

  it("closes the settings modal, which would cover the pane", () => {
    useSettingsUI.getState().openSettings("memory");
    openMemoryEntry({ scope: "user", name: "rules" });
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
  });
});

describe("openMemoryView", () => {
  it("opens a Memory pane on the proposals by default", () => {
    openMemoryView();
    expect(panes().map((p) => p.content)).toEqual([{ kind: "memory", memoryView: "proposals" }]);
  });

  it("retargets the pane that is open instead of adding a second", () => {
    openMemoryView("changes");
    openMemoryView("proposals");
    expect(panes().map((p) => p.content)).toEqual([{ kind: "memory", memoryView: "proposals" }]);
  });

  it("closes the settings modal, which would cover the pane", () => {
    useSettingsUI.getState().openSettings("memory");
    openMemoryView("changes");
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
  });

  it("opens one proposal alone, and drops it when the pane goes back to a tab (ADR 0141)", () => {
    openMemoryView("proposals", { proposalId: "0123456789abcdef0123" });
    expect(panes().map((p) => p.content)).toEqual([{ kind: "memory", memoryView: "proposals", proposalId: "0123456789abcdef0123" }]);
    openMemoryView("changes", { proposalId: "0123456789abcdef0123" });
    expect(panes().map((p) => p.content)).toEqual([{ kind: "memory", memoryView: "changes" }]);
  });

  it("titles the tab Memory", () => {
    openMemoryView();
    expect(paneTitle(panes()[0], null)).toBe("Memory");
  });

  it("keeps a stored Memory pane and drops an unknown view name", () => {
    expect(validateStoredContent({ kind: "memory", memoryView: "proposals" })).toEqual({ kind: "memory", memoryView: "proposals" });
    expect(validateStoredContent({ kind: "memory", memoryView: "../etc" })).toEqual({ kind: "memory" });
    const id = "0123456789abcdef0123";
    expect(validateStoredContent({ kind: "memory", memoryView: "proposals", proposalId: id })).toEqual({ kind: "memory", memoryView: "proposals", proposalId: id });
    // The id reaches an API query: anything not shaped like the Agent's is dropped, and so is one
    // on a view that does not show a single proposal.
    expect(validateStoredContent({ kind: "memory", memoryView: "proposals", proposalId: "../x" })).toEqual({ kind: "memory", memoryView: "proposals" });
    expect(validateStoredContent({ kind: "memory", memoryView: "changes", proposalId: id })).toEqual({ kind: "memory", memoryView: "changes" });
  });

  it("keeps a stored memory pane and drops one whose names the Agent would refuse (ADR 0141)", () => {
    const ok = { kind: "memoryEntry", scope: "project", project: "app-0123456789ab", projectName: "app", name: "build-cmd" };
    expect(validateStoredContent(ok)).toEqual(ok);
    expect(validateStoredContent({ ...ok, gone: true })).toEqual({ ...ok, gone: true });
    expect(validateStoredContent({ kind: "memoryEntry", scope: "user", project: "", name: "" })).toEqual({ kind: "memoryEntry", scope: "user", project: "", name: "" });
    const blank = { kind: "terminal", chat: false };
    expect(validateStoredContent({ ...ok, name: "../etc" })).toEqual(blank);
    expect(validateStoredContent({ ...ok, name: "Upper" })).toEqual(blank);
    expect(validateStoredContent({ ...ok, project: "a/b" })).toEqual(blank);
    expect(validateStoredContent({ ...ok, scope: "user" })).toEqual(blank);
    expect(validateStoredContent({ ...ok, scope: "team" })).toEqual(blank);
    // A new memory has nothing to have forgotten.
    expect(validateStoredContent({ kind: "memoryEntry", scope: "user", project: "", name: "", gone: true })).toEqual({ kind: "memoryEntry", scope: "user", project: "", name: "" });
  });
});
