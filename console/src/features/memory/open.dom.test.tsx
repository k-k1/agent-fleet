// openMemoryView (#3720): the one way into the Memory pane. Pinned: it opens a pane of kind
// "memory" on the asked view, a second open focuses and retargets that pane rather than adding
// one, the settings modal is closed (it would hide the pane), a memory to
// pick travels as the one-shot target, and a stored layout of this kind survives a reload.
import { beforeEach, describe, expect, it } from "vitest";
import { openMemoryView } from "./open.ts";
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

describe("openMemoryView", () => {
  it("opens a Memory pane on the viewer by default", () => {
    openMemoryView();
    expect(panes().map((p) => p.content)).toEqual([{ kind: "memory", memoryView: "memories" }]);
  });

  it("retargets the pane that is open instead of adding a second", () => {
    openMemoryView("memories");
    openMemoryView("proposals");
    expect(panes().map((p) => p.content)).toEqual([{ kind: "memory", memoryView: "proposals" }]);
  });

  it("closes the settings modal, which would cover the pane", () => {
    useSettingsUI.getState().openSettings("memory");
    openMemoryView("changes");
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
  });

  it("hands a memory to pick over as the one-shot target", () => {
    useSettingsUI.getState().openSettings("memory");
    openMemoryView("memories", { target: "user::notes" });
    expect(useSettingsUI.getState().settingsTarget).toBe("user::notes");
  });

  it("titles the tab Memory", () => {
    openMemoryView();
    expect(paneTitle(panes()[0], null)).toBe("Memory");
  });

  it("keeps a stored Memory pane and drops an unknown view name", () => {
    expect(validateStoredContent({ kind: "memory", memoryView: "proposals" })).toEqual({ kind: "memory", memoryView: "proposals" });
    expect(validateStoredContent({ kind: "memory", memoryView: "../etc" })).toEqual({ kind: "memory" });
  });
});
