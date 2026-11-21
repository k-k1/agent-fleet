import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { wireRailReveals } from "./reveals.ts";
import { useLeftRail } from "../../core/store/leftRail.ts";
import { useFilesStore } from "../../features/files/store.ts";
import { useMemoStore } from "../../features/memo/store.ts";
import { useRepoReveal } from "../../features/repos/store.ts";
import { useSharedReveal } from "../../features/sharing/store.ts";
import { useSchedulesStore } from "../../features/schedules/store.ts";

let unwire: () => void;
beforeEach(() => {
  useLeftRail.setState({ open: false, view: "assistant", swipeOverlay: false });
  unwire = wireRailReveals();
});
afterEach(() => unwire());

const rail = () => ({ open: useLeftRail.getState().open, view: useLeftRail.getState().view });

describe("wireRailReveals", () => {
  it("brings the session tree forward for a repository reveal, opening a folded pane", () => {
    useRepoReveal.getState().reveal("webshop");
    expect(rail()).toEqual({ open: true, view: "sessions" });
  });

  it("brings the schedules view forward for a schedule reveal", () => {
    useSchedulesStore.getState().revealSchedule("s1");
    expect(rail()).toEqual({ open: true, view: "schedules" });
  });

  it("brings the memo queue forward for a category reveal and for a compose request", () => {
    useMemoStore.getState().revealCategory("c1", false);
    expect(rail()).toEqual({ open: true, view: "memo" });
    useLeftRail.setState({ open: false, view: "sessions" });
    useMemoStore.getState().requestCompose();
    expect(rail()).toEqual({ open: true, view: "memo" });
  });

  it("follows a file reveal that takes focus, and ignores one that is a side effect", () => {
    useFilesStore.getState().revealInFiles("repos/webshop");
    expect(rail()).toEqual({ open: false, view: "assistant" });
    useFilesStore.getState().revealInFiles("repos/webshop", { focus: true });
    expect(rail()).toEqual({ open: true, view: "files" });
  });

  it("brings the shared view forward for a shared-session reveal", () => {
    useSharedReveal.getState().reveal("cat1");
    expect(rail()).toEqual({ open: true, view: "shared" });
  });

  it("stops following once unwired", () => {
    unwire();
    useRepoReveal.getState().reveal("webshop");
    expect(rail()).toEqual({ open: false, view: "assistant" });
  });
});
